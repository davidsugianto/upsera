package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/davidsugianto/upsera/internal/events"
	"github.com/davidsugianto/upsera/internal/model"
)

const (
	// ssePingEvery keeps idle streams alive through proxies.
	ssePingEvery = 25 * time.Second
	// sseMaxAge bounds how long a stream outlives a revoked session: the
	// browser's EventSource reconnects and re-authenticates.
	sseMaxAge = 15 * time.Minute
)

// liveMonitorBody is the "monitor" event: a monitor changed state.
type liveMonitorBody struct {
	MonitorID int64        `json:"monitor_id"`
	From      model.Status `json:"from"`
	To        model.Status `json:"to"`
	At        time.Time    `json:"at"`
	Since     time.Time    `json:"since"`
	Message   string       `json:"message"`
	Flapping  bool         `json:"flapping"`
}

// monitorsChangedBody is the "monitors_changed" event: a monitor was
// created, updated or deleted.
type monitorsChangedBody struct {
	MonitorID int64 `json:"monitor_id"`
	Deleted   bool  `json:"deleted"`
}

func registerEventRoutes(api huma.API, d Deps) {
	// Not huma/sse.Register: it commits the 200 before the handler runs, so
	// auth failures could not be reported as 401/403/404.
	huma.Register(api, huma.Operation{
		OperationID: "stream-team-events",
		Method:      http.MethodGet,
		Path:        "/api/teams/{teamID}/events",
		Summary:     "Stream team events",
		Description: "Server-Sent Events: `monitor` (state transitions), `alert` (alert changes) and " +
			"`monitors_changed` (monitor created, updated or deleted). Steady-state checks are not streamed.",
		Responses: map[string]*huma.Response{
			"200": {
				Description: "text/event-stream of monitor, alert and monitors_changed events",
				Content:     map[string]*huma.MediaType{"text/event-stream": {Schema: &huma.Schema{Type: "string"}}},
			},
		},
	}, func(ctx context.Context, in *teamPathInput) (*huma.StreamResponse, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		sub := d.Events.Subscribe(in.TeamID)
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			defer sub.Close()
			hctx.SetHeader("Content-Type", "text/event-stream")
			hctx.SetHeader("Cache-Control", "no-cache")
			hctx.SetHeader("X-Accel-Buffering", "no")
			hctx.SetStatus(http.StatusOK)
			w := hctx.BodyWriter()
			flush := func() error { return nil }
			if rw, ok := w.(http.ResponseWriter); ok {
				rc := http.NewResponseController(rw)
				flush = rc.Flush
			}
			if _, err := io.WriteString(w, "retry: 3000\n\n"); err != nil || flush() != nil {
				return
			}

			ping := time.NewTicker(ssePingEvery)
			defer ping.Stop()
			maxAge := time.NewTimer(sseMaxAge)
			defer maxAge.Stop()
			for {
				var err error
				select {
				case <-hctx.Context().Done():
					return
				case <-maxAge.C:
					return
				case <-ping.C:
					_, err = io.WriteString(w, ": ping\n\n")
				case v, ok := <-sub.C:
					if !ok {
						return
					}
					err = writeEvent(w, v)
				}
				if err == nil {
					err = flush()
				}
				if err != nil {
					return
				}
			}
		}}, nil
	})
}

// writeEvent writes v as one SSE frame; values of unknown types are
// skipped.
func writeEvent(w io.Writer, v any) error {
	var name string
	var body any
	switch e := v.(type) {
	case model.Transition:
		name = "monitor"
		body = liveMonitorBody{
			MonitorID: e.MonitorID, From: e.From, To: e.To, At: e.At, Since: e.Since,
			Message: e.Message, Flapping: e.Flapping,
		}
	case model.Alert:
		name, body = "alert", toAlertBody(e)
	case events.MonitorsChanged:
		name, body = "monitors_changed", monitorsChangedBody{MonitorID: e.MonitorID, Deleted: e.Deleted}
	default:
		return nil
	}
	data, err := json.Marshal(body) // single line: json.Marshal never emits newlines
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data)
	return err
}
