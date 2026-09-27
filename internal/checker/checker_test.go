package checker

import (
	"encoding/json"
	"testing"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/netpolicy"
)

// newTestChecker returns a Checker whose policy allows private/loopback
// targets (as httptest servers use) but still refuses always-blocked
// ranges such as link-local metadata addresses.
func newTestChecker() *Checker {
	return New(netpolicy.New(""))
}

// mustMonitor builds a model.Monitor of the given type from cfg, running it
// through ValidateConfig so defaults are filled exactly as the API would.
func mustMonitor(t *testing.T, typ model.MonitorType, cfg any, timeoutS int) model.Monitor {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	normalized, err := ValidateConfig(typ, raw)
	if err != nil {
		t.Fatalf("ValidateConfig(%s, %s): %v", typ, raw, err)
	}
	if timeoutS <= 0 {
		timeoutS = model.DefaultTimeoutS
	}
	return model.Monitor{Type: typ, Config: normalized, TimeoutS: timeoutS}
}
