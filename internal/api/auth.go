package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"
	"golang.org/x/crypto/bcrypt"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/store"
)

// dummyHash is compared against on every login for an unknown email, so a
// bad password and an unknown email take the same amount of time.
var dummyHash = func() string {
	h, err := bcrypt.GenerateFromPassword([]byte("upsera-timing-safety-placeholder"), bcrypt.DefaultCost)
	if err != nil {
		panic(err)
	}
	return string(h)
}()

func validatePassword(pw string) error {
	if utf8.RuneCountInString(pw) < 10 {
		return errors.New("password must be at least 10 characters")
	}
	if len(pw) > 72 {
		return errors.New("password must be at most 72 bytes")
	}
	return nil
}

func hashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

func sessionCookie(value string, secure bool, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
	}
}

func clearSessionCookie(secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	}
}

// startSession creates a session for userID and returns its cookie and CSRF
// token.
func startSession(ctx context.Context, d Deps, userID int64) (*http.Cookie, string, error) {
	secret, value, err := newSecret()
	if err != nil {
		return nil, "", err
	}
	_, csrf, err := newSecret()
	if err != nil {
		return nil, "", err
	}
	expires := time.Now().Add(sessionTTL)
	if err := d.Store.CreateSession(ctx, userID, hashSecret(secret), csrf, expires); err != nil {
		return nil, "", err
	}
	return sessionCookie(value, d.Config.SecureCookies(), expires), csrf, nil
}

type meUser struct {
	ID      int64  `json:"id"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	IsAdmin bool   `json:"is_admin"`
}

type meTeam struct {
	ID   int64      `json:"id"`
	Name string     `json:"name"`
	Role model.Role `json:"role"`
}

type meBody struct {
	User      meUser   `json:"user"`
	Teams     []meTeam `json:"teams"`
	CSRFToken string   `json:"csrf_token"`
}

func buildMeBody(ctx context.Context, d Deps, u model.User, csrf string) (meBody, error) {
	memberships, err := d.Store.ListTeamsForUser(ctx, u.ID)
	if err != nil {
		return meBody{}, err
	}
	teams := make([]meTeam, len(memberships))
	for i, m := range memberships {
		teams[i] = meTeam{ID: m.Team.ID, Name: m.Team.Name, Role: m.Role}
	}
	return meBody{
		User:      meUser{ID: u.ID, Email: u.Email, Name: u.Name, IsAdmin: u.IsAdmin},
		Teams:     teams,
		CSRFToken: csrf,
	}, nil
}

// authOutput is the response of every endpoint that starts a session.
type authOutput struct {
	SetCookie string `header:"Set-Cookie"`
	Body      meBody
}

const (
	loginMaxFailures = 10
	loginWindow      = 15 * time.Minute
)

// loginLimiter tracks recent failed logins per (client IP, email) so brute
// forcing a password cannot run unbounded. Entries whose failures have all
// aged out of loginWindow are pruned so the map cannot grow without bound
// from one-off attempts that are never retried.
type loginLimiter struct {
	mu        sync.Mutex
	failures  map[string][]time.Time
	lastSweep time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{failures: make(map[string][]time.Time)}
}

func loginLimiterKey(ip, email string) string {
	return ip + "|" + strings.ToLower(email)
}

// sweep drops every failure older than loginWindow across all keys, and
// deletes keys left with no failures. It runs at most once per loginWindow
// (callers hold l.mu already) so a normal login request never pays for a
// full-map scan.
func (l *loginLimiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < loginWindow {
		return
	}
	l.lastSweep = now
	cutoff := now.Add(-loginWindow)
	for k, ts := range l.failures {
		kept := ts[:0]
		for _, t := range ts {
			if t.After(cutoff) {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			delete(l.failures, k)
		} else {
			l.failures[k] = kept
		}
	}
}

// allow reports whether an attempt from ip/email may proceed: fewer than
// loginMaxFailures failures were recorded in the last loginWindow.
func (l *loginLimiter) allow(ip, email string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.sweep(now)
	k := loginLimiterKey(ip, email)
	cutoff := now.Add(-loginWindow)
	kept := l.failures[k][:0]
	for _, t := range l.failures[k] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, k)
	} else {
		l.failures[k] = kept
	}
	return len(kept) < loginMaxFailures
}

func (l *loginLimiter) recordFailure(ip, email string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(time.Now())
	k := loginLimiterKey(ip, email)
	l.failures[k] = append(l.failures[k], time.Now())
}

func (l *loginLimiter) reset(ip, email string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, loginLimiterKey(ip, email))
}

type setupStatusOutput struct {
	Body struct {
		Needed bool `json:"needed"`
	}
}

type setupInput struct {
	Body struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
		TeamName string `json:"team_name"`
	}
}

type loginInput struct {
	Body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
}

type logoutInput struct {
	SessionCookie string `cookie:"upsera_session"`
}

type logoutOutput struct {
	SetCookie string `header:"Set-Cookie"`
}

type meOutput struct {
	Body meBody
}

func registerAuthRoutes(api huma.API, d Deps, limiter *loginLimiter) {
	huma.Get(api, "/api/setup", func(ctx context.Context, _ *noInput) (*setupStatusOutput, error) {
		n, err := d.Store.CountUsers(ctx)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &setupStatusOutput{}
		out.Body.Needed = n == 0
		return out, nil
	})

	huma.Post(api, "/api/setup", func(ctx context.Context, in *setupInput) (*authOutput, error) {
		email := strings.TrimSpace(in.Body.Email)
		if email == "" {
			return nil, huma.Error422UnprocessableEntity("email is required")
		}
		if err := validatePassword(in.Body.Password); err != nil {
			return nil, huma.Error422UnprocessableEntity(err.Error())
		}
		hash, err := hashPassword(in.Body.Password)
		if err != nil {
			d.Logger.Error("hash password", "error", err)
			return nil, huma.Error500InternalServerError("internal error")
		}
		user, team, err := d.Store.Setup(ctx, email, in.Body.Name, hash, in.Body.TeamName)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		cookie, csrf, err := startSession(ctx, d, user.ID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		writeAudit(ctx, d, &team.ID, actor{userID: &user.ID}, "setup", "team", &team.ID, nil)
		body, err := buildMeBody(ctx, d, user, csrf)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		return &authOutput{SetCookie: cookie.String(), Body: body}, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated })

	huma.Post(api, "/api/auth/login", func(ctx context.Context, in *loginInput) (*authOutput, error) {
		ip := clientIP(ctx)
		email := strings.TrimSpace(in.Body.Email)
		if !limiter.allow(ip, email) {
			return nil, huma.Error429TooManyRequests("too many login attempts, try again later")
		}
		hash := dummyHash
		user, err := d.Store.GetUserByEmail(ctx, email)
		switch {
		case err == nil:
			hash = user.PasswordHash
		case errors.Is(err, store.ErrNotFound):
			// keep the dummy hash so the compare below still runs
		default:
			return nil, mapStoreErr(d, err)
		}
		match := bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Body.Password)) == nil
		if err != nil || !match {
			limiter.recordFailure(ip, email)
			return nil, huma.Error401Unauthorized("invalid email or password")
		}
		limiter.reset(ip, email)
		cookie, csrf, err := startSession(ctx, d, user.ID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		body, err := buildMeBody(ctx, d, user, csrf)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		return &authOutput{SetCookie: cookie.String(), Body: body}, nil
	})

	huma.Post(api, "/api/auth/logout", func(ctx context.Context, in *logoutInput) (*logoutOutput, error) {
		if _, err := requireSessionUser(ctx); err != nil {
			return nil, err
		}
		if raw, err := base64.RawURLEncoding.DecodeString(in.SessionCookie); err == nil {
			if err := d.Store.DeleteSession(ctx, hashSecret(raw)); err != nil {
				d.Logger.Error("delete session failed", "error", err)
			}
		}
		return &logoutOutput{SetCookie: clearSessionCookie(d.Config.SecureCookies()).String()}, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusNoContent })

	huma.Get(api, "/api/auth/me", func(ctx context.Context, _ *noInput) (*meOutput, error) {
		p := principalFrom(ctx)
		u, err := requireSessionUser(ctx)
		if err != nil {
			return nil, err
		}
		body, err := buildMeBody(ctx, d, u, p.csrf)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		return &meOutput{Body: body}, nil
	})
}
