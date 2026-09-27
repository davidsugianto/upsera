package checker

import (
	"context"
	"fmt"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/netpolicy"
)

// Checker runs checks for every monitor type, applying the outbound target
// policy to every network operation it performs.
type Checker struct {
	policy *netpolicy.Policy
}

// New builds a Checker that dials through p.
func New(p *netpolicy.Policy) *Checker {
	return &Checker{policy: p}
}

// Check runs one check of m and never panics. It applies m.TimeoutS as a
// context deadline itself, so callers should not additionally time it out.
func (c *Checker) Check(ctx context.Context, m model.Monitor) (result Result) {
	defer func() {
		if r := recover(); r != nil {
			result = Result{
				Status:  model.StatusDown,
				Message: model.TruncateMessage(fmt.Sprintf("check panicked: %v", r)),
			}
		}
	}()

	timeoutS := m.TimeoutS
	if timeoutS <= 0 {
		timeoutS = model.DefaultTimeoutS
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutS)*time.Second)
	defer cancel()

	switch m.Type {
	case model.TypeHTTP, model.TypeKeyword:
		result = c.checkHTTP(ctx, m, timeoutS)
	case model.TypeTCP:
		result = c.checkTCP(ctx, m, timeoutS)
	case model.TypePing:
		result = c.checkPing(ctx, m, timeoutS)
	case model.TypeDNS:
		result = c.checkDNS(ctx, m, timeoutS)
	case model.TypePush:
		result = Result{Status: model.StatusDown, Message: "push monitors are not actively checked"}
	default:
		result = Result{Status: model.StatusDown, Message: fmt.Sprintf("unknown monitor type %q", m.Type)}
	}
	result.Message = model.TruncateMessage(result.Message)
	return result
}
