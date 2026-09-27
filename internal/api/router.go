// Package api implements the Upsera REST API: chi for routing, huma v2 for
// request/response validation and OpenAPI generation. It authenticates
// browser sessions (cookie + CSRF) and team API tokens (bearer), enforces
// per-team roles, and drives the scheduler through the Runner interface.
package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/davidsugianto/upsera/internal/config"
	"github.com/davidsugianto/upsera/internal/events"
	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/netpolicy"
	"github.com/davidsugianto/upsera/internal/scheduler"
	"github.com/davidsugianto/upsera/internal/store"
)

// Runner is the subset of the scheduler the API needs: applying monitor
// changes, accepting push heartbeats and reporting live state and health.
type Runner interface {
	Upsert(model.Monitor)
	Remove(int64)
	Push(token string, status model.Status, msg string, latencyMs int32) error
	State(id int64) (model.MonitorState, bool)
	Health() scheduler.Health
}

// Alerting is the subset of the alerting engine the API needs: keeping its
// in-memory caches of monitors, channels and escalation policies in sync
// with the store, acknowledging alerts and sending test notifications.
type Alerting interface {
	UpsertMonitor(model.Monitor)
	RemoveMonitor(int64)
	UpsertChannel(model.Channel)
	RemoveChannel(int64)
	UpsertPolicy(model.EscalationPolicy)
	RemovePolicy(int64)
	Acknowledge(teamID int64, alertID string, by model.AckBy) (model.Alert, error)
	TestSend(ctx context.Context, ch model.Channel) error
}

// MaintenanceRegistry is the subset of the maintenance window registry the
// API needs to keep in sync with the store.
type MaintenanceRegistry interface {
	Upsert(model.MaintenanceWindow)
	Remove(int64)
}

// Deps are the router's dependencies.
type Deps struct {
	Store       *store.Store
	Runner      Runner
	Alerting    Alerting
	Maintenance MaintenanceRegistry
	Policy      *netpolicy.Policy
	Config      config.Config
	Logger      *slog.Logger
	Version     string
	// Events receives monitor-list changes and feeds the dashboard's live
	// stream (required).
	Events *events.Hub
	// UI serves the dashboard for every path the API does not route (nil =
	// no dashboard).
	UI http.Handler
}

const (
	sessionCookieName = "upsera_session"
	csrfHeaderName    = "X-CSRF-Token"
	bearerPrefix      = "Bearer "
	tokenPrefix       = "ups_"
	sessionTTL        = 30 * 24 * time.Hour
)

// noInput is used for operations that take no path, query or body
// parameters.
type noInput struct{}

// emptyOutput is used for operations that return no body (204 No Content).
type emptyOutput struct{}

// NewRouter builds the HTTP handler for the whole API, plus the
// unauthenticated /healthz endpoint used by container health checks and,
// when d.UI is set, the dashboard for every other path.
func NewRouter(d Deps) http.Handler {
	r, _ := newAPI(d)
	if d.UI != nil {
		r.NotFound(d.UI.ServeHTTP)
	}
	return r
}

// OpenAPISpec returns the generated OpenAPI document (for web type
// generation).
func OpenAPISpec(version string) ([]byte, error) {
	_, a := newAPI(Deps{Version: version, Logger: slog.New(slog.DiscardHandler)})
	return json.MarshalIndent(a.OpenAPI(), "", "  ")
}

// newAPI builds the chi router with /healthz and every huma operation
// registered.
func newAPI(d Deps) (*chi.Mux, huma.API) {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Recoverer, accessLogMiddleware(d.Logger))
	r.Get("/healthz", healthzHandler(d))

	cfg := huma.DefaultConfig("Upsera API", d.Version)
	cfg.OpenAPIPath = "/api/openapi"
	cfg.DocsPath = "/api/docs"
	cfg.SchemasPath = "/api/schemas"
	cfg.Info.Description = "Authenticate with either the `" + sessionCookieName +
		"` session cookie (set by /api/auth/login) plus an `" + csrfHeaderName +
		"` header on non-GET requests, or a team `Authorization: Bearer " + tokenPrefix + "...` API token."
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"cookieAuth": {
			Type: "apiKey", In: "cookie", Name: sessionCookieName,
			Description: "Browser session cookie; non-GET requests must also send " + csrfHeaderName + ".",
		},
		"csrfHeader": {Type: "apiKey", In: "header", Name: csrfHeaderName},
		"bearerAuth": {
			Type: "http", Scheme: "bearer",
			Description: "Team API token: `Authorization: Bearer " + tokenPrefix + "...`.",
		},
	}

	api := humachi.New(r, cfg)
	limiter := newLoginLimiter()

	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		gctx := context.WithValue(ctx.Context(), remoteAddrCtxKey, ctx.RemoteAddr())
		p := &principal{}

		if auth := ctx.Header("Authorization"); strings.HasPrefix(auth, bearerPrefix) {
			secret := strings.TrimPrefix(strings.TrimPrefix(auth, bearerPrefix), tokenPrefix)
			if raw, err := base64.RawURLEncoding.DecodeString(secret); err == nil {
				tok, err := d.Store.UseAPIToken(gctx, hashSecret(raw))
				switch {
				case err == nil:
					p.token = &tok
				case errors.Is(err, store.ErrNotFound):
					// unknown, expired or revoked token: unauthenticated.
				default:
					d.Logger.Error("auth: token lookup failed", "error", err)
					huma.WriteErr(api, ctx, http.StatusServiceUnavailable, "temporarily unavailable")
					return
				}
			}
		} else {
			for _, c := range huma.ReadCookies(ctx) {
				if c.Name != sessionCookieName {
					continue
				}
				raw, err := base64.RawURLEncoding.DecodeString(c.Value)
				if err != nil {
					break
				}
				sess, user, err := d.Store.GetSession(gctx, hashSecret(raw))
				if err != nil {
					if !errors.Is(err, store.ErrNotFound) {
						d.Logger.Error("auth: session lookup failed", "error", err)
						huma.WriteErr(api, ctx, http.StatusServiceUnavailable, "temporarily unavailable")
						return
					}
					break
				}
				p.user = &user
				p.csrf = sess.CSRFToken
				break
			}
			if p.user != nil && !safeMethod(ctx.Method()) {
				if subtle.ConstantTimeCompare([]byte(ctx.Header(csrfHeaderName)), []byte(p.csrf)) != 1 {
					huma.WriteErr(api, ctx, http.StatusForbidden, "missing or invalid CSRF token")
					return
				}
			}
		}

		gctx = context.WithValue(gctx, principalCtxKey, p)
		next(huma.WithContext(ctx, gctx))
	})

	registerAuthRoutes(api, d, limiter)
	registerAdminRoutes(api, d)
	registerTeamRoutes(api, d)
	registerMonitorRoutes(api, d)
	registerChannelRoutes(api, d)
	registerPolicyRoutes(api, d)
	registerAlertRoutes(api, d)
	registerMaintenanceRoutes(api, d)
	registerAuditRoutes(api, d)
	registerPushRoutes(api, d)
	registerDashboardRoutes(api, d)
	registerEventRoutes(api, d)

	return r, api
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func accessLogMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			logger.Info("http_request",
				"method", r.Method, "path", r.URL.Path,
				"status", ww.Status(), "duration_ms", time.Since(start).Milliseconds())
		})
	}
}

func healthzHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := d.Runner.Health()
		db := "down"
		if h.DBReachable {
			db = "up"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":              "ok",
			"db":                  db,
			"buffered_heartbeats": h.BufferedHeartbeats,
			"dropped_heartbeats":  h.DroppedHeartbeats,
			"version":             d.Version,
		})
	}
}

// principal is the resolved caller of one request: either a session user or
// a bearer-token team, never both.
type principal struct {
	user  *model.User
	csrf  string
	token *model.APIToken
}

type ctxKey int

const (
	principalCtxKey ctxKey = iota
	remoteAddrCtxKey
)

func principalFrom(ctx context.Context) *principal {
	p, _ := ctx.Value(principalCtxKey).(*principal)
	return p
}

// clientIP returns the connecting client's address without its port. It
// intentionally ignores X-Forwarded-For: phase 1 has no trusted proxy list.
func clientIP(ctx context.Context) string {
	addr, _ := ctx.Value(remoteAddrCtxKey).(string)
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// actor identifies who performed a change, for audit logging. Exactly one
// field is set.
type actor struct {
	userID  *int64
	tokenID *int64
}

// requireSessionUser returns the authenticated session user, or an error:
// 403 if the caller authenticated with an API token instead (tokens can
// never use session-only endpoints), 401 if unauthenticated.
func requireSessionUser(ctx context.Context) (model.User, error) {
	p := principalFrom(ctx)
	if p != nil && p.token != nil {
		return model.User{}, huma.Error403Forbidden("API tokens cannot use this endpoint")
	}
	if p == nil || p.user == nil {
		return model.User{}, huma.Error401Unauthorized("authentication required")
	}
	return *p.user, nil
}

// requireAdmin is requireSessionUser plus an instance-admin check.
func requireAdmin(ctx context.Context) (model.User, error) {
	u, err := requireSessionUser(ctx)
	if err != nil {
		return u, err
	}
	if !u.IsAdmin {
		return u, huma.Error403Forbidden("instance admin required")
	}
	return u, nil
}

// authorizeTeam resolves the caller's access to teamID at min role or
// better, accepting either a session user or a bearer token bound to that
// team. A token's scope maps to a role ceiling: read -> viewer, write ->
// editor. Non-members and tokens for a different team get 404, insufficient
// role gets 403, no credentials gets 401.
func authorizeTeam(ctx context.Context, d Deps, teamID int64, min model.Role) (actor, error) {
	p := principalFrom(ctx)
	if p == nil {
		return actor{}, huma.Error401Unauthorized("authentication required")
	}
	if p.token != nil {
		if p.token.TeamID != teamID {
			return actor{}, huma.Error404NotFound("team not found")
		}
		role := model.RoleViewer
		if p.token.Scope == model.ScopeWrite {
			role = model.RoleEditor
		}
		if !role.AtLeast(min) {
			return actor{}, huma.Error403Forbidden("token scope insufficient")
		}
		return actor{tokenID: &p.token.ID}, nil
	}
	if p.user == nil {
		return actor{}, huma.Error401Unauthorized("authentication required")
	}
	return authorizeTeamSession(ctx, d, teamID, min)
}

// authorizeTeamSession is authorizeTeam restricted to session users; bearer
// tokens always get 403. Used for member and token management, which tokens
// may never perform.
func authorizeTeamSession(ctx context.Context, d Deps, teamID int64, min model.Role) (actor, error) {
	u, err := requireSessionUser(ctx)
	if err != nil {
		return actor{}, err
	}
	role, err := d.Store.GetRole(ctx, teamID, u.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return actor{}, huma.Error404NotFound("team not found")
		}
		return actor{}, mapStoreErr(d, err)
	}
	if !role.AtLeast(min) {
		return actor{}, huma.Error403Forbidden("insufficient role")
	}
	return actor{userID: &u.ID}, nil
}

// mapStoreErr converts a store error into the right HTTP status. Anything
// unrecognized is logged and reported as a generic 500 so internals (SQL,
// hashes) never leak to clients.
func mapStoreErr(d Deps, err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, store.ErrConflict):
		return huma.Error409Conflict("conflict")
	case errors.Is(err, store.ErrLastOwner):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, store.ErrSetupDone):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, store.ErrInUse):
		return huma.Error409Conflict("in use")
	default:
		d.Logger.Error("internal error", "error", err)
		return huma.Error500InternalServerError("internal error")
	}
}

// writeAudit records an audit entry. Failure is logged, not fatal: audit
// logging must never block the change it describes.
func writeAudit(ctx context.Context, d Deps, teamID *int64, act actor, action, targetType string, targetID *int64, details any) {
	var raw json.RawMessage
	if details != nil {
		if b, err := json.Marshal(details); err == nil {
			raw = b
		}
	}
	entry := model.AuditEntry{
		TeamID: teamID, ActorUserID: act.userID, ActorTokenID: act.tokenID,
		Action: action, TargetType: targetType, TargetID: targetID, Details: raw,
	}
	if err := d.Store.InsertAudit(ctx, entry); err != nil {
		d.Logger.Error("audit write failed", "action", action, "error", err)
	}
}

// newSecret generates a random 32-byte value, returning both the raw bytes
// (to hash for storage) and its base64url encoding (to hand to the client).
func newSecret() (raw []byte, encoded string, err error) {
	raw = make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return nil, "", err
	}
	return raw, base64.RawURLEncoding.EncodeToString(raw), nil
}

// newPushToken generates a push monitor's public token: 24 random bytes,
// base64url-encoded.
func newPushToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashSecret(raw []byte) []byte {
	h := sha256.Sum256(raw)
	return h[:]
}
