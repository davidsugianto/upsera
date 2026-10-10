package checker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/davidsugianto/upsera/internal/dockerapi"
	"github.com/davidsugianto/upsera/internal/model"
)

// checkDocker reports UP for a running container that has no healthcheck or
// a healthy one. Everything else is a failed check with a message that names
// the reason ("exited (137)", "unhealthy", ...); a container whose health is
// still "starting" fails too, so the monitor shows PENDING until the
// retries are used up.
func (c *Checker) checkDocker(ctx context.Context, m model.Monitor, timeoutS int) Result {
	var cfg DockerConfig
	if err := json.Unmarshal(m.Config, &cfg); err != nil {
		return Result{Status: model.StatusDown, Message: "invalid monitor config"}
	}
	if c.docker == nil {
		return Result{Status: model.StatusDown, Message: "docker monitoring is not configured"}
	}
	client, err := c.docker.Client(m.TeamID, cfg.DockerHostID)
	if err != nil {
		return Result{Status: model.StatusDown, Message: err.Error()}
	}
	start := time.Now()
	st, err := client.Inspect(ctx, cfg.Container)
	latency := time.Since(start)
	if err != nil {
		msg := "docker host unreachable: " + describeErr(err, timeoutS)
		var se *dockerapi.StatusError
		switch {
		case errors.Is(err, dockerapi.ErrNotFound):
			msg = "container not found"
		case errors.As(err, &se):
			msg = se.Error()
		case errors.Is(err, context.DeadlineExceeded):
			msg = describeErr(err, timeoutS)
		}
		return Result{Status: model.StatusDown, Latency: latency, Message: msg}
	}
	if msg, ok := dockerProblem(st); !ok {
		return Result{Status: model.StatusDown, Latency: latency, Message: msg}
	}
	return Result{Status: model.StatusUp, Latency: latency}
}

// dockerProblem returns ok=true when the container is healthy, or the
// reason it is not.
func dockerProblem(st dockerapi.ContainerState) (string, bool) {
	suffix := ""
	if st.OOMKilled {
		suffix = ", OOM killed"
	}
	switch st.Status {
	case "running":
		switch st.Health {
		case "", "healthy":
			return "", true
		case "starting":
			return "health check starting", false
		case "unhealthy":
			return "unhealthy", false
		}
		return "health: " + st.Health, false
	case "exited":
		return fmt.Sprintf("exited (%d%s)", st.ExitCode, suffix), false
	case "restarting":
		return fmt.Sprintf("restarting (last exit %d%s)", st.ExitCode, suffix), false
	case "":
		return "unknown container state", false
	}
	// created, paused, removing, dead
	return st.Status, false
}
