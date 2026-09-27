package checker

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/davidsugianto/upsera/internal/model"
)

func TestValidateConfigHTTPDefaults(t *testing.T) {
	t.Parallel()
	raw, err := ValidateConfig(model.TypeHTTP, json.RawMessage(`{"url":"https://example.com"}`))
	if err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	var cfg HTTPConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Method != "GET" {
		t.Errorf("Method = %q, want GET", cfg.Method)
	}
	if len(cfg.AcceptedStatus) != 1 || cfg.AcceptedStatus[0] != "200-299" {
		t.Errorf("AcceptedStatus = %v, want [200-299]", cfg.AcceptedStatus)
	}
	if cfg.MaxRedirects == nil || *cfg.MaxRedirects != DefaultMaxRedirects {
		t.Errorf("MaxRedirects = %v, want default %d", cfg.MaxRedirects, DefaultMaxRedirects)
	}

	// An explicit 0 (do not follow) must survive normalization, not be
	// replaced by the default.
	raw, err = ValidateConfig(model.TypeHTTP, json.RawMessage(`{"url":"https://example.com","max_redirects":0}`))
	if err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	cfg = HTTPConfig{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.MaxRedirects == nil || *cfg.MaxRedirects != 0 {
		t.Errorf("explicit max_redirects 0 became %v", cfg.MaxRedirects)
	}
}

func TestValidateConfigDNSResolverNormalization(t *testing.T) {
	t.Parallel()
	raw, err := ValidateConfig(model.TypeDNS, json.RawMessage(`{"hostname":"example.com","resolver":"1.1.1.1"}`))
	if err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	var cfg DNSConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Resolver != "1.1.1.1:53" {
		t.Errorf("Resolver = %q, want 1.1.1.1:53", cfg.Resolver)
	}
	if cfg.RecordType != "A" {
		t.Errorf("RecordType = %q, want default A", cfg.RecordType)
	}
}

func TestValidateConfigRejections(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		typ  model.MonitorType
		raw  string
		want string // substring expected in the error
	}{
		{
			name: "unknown field",
			typ:  model.TypeHTTP,
			raw:  `{"url":"https://example.com","bogus":1}`,
			want: "invalid config",
		},
		{
			name: "bad url",
			typ:  model.TypeHTTP,
			raw:  `{"url":"not-a-url"}`,
			want: "absolute http",
		},
		{
			name: "bad status range",
			typ:  model.TypeHTTP,
			raw:  `{"url":"https://example.com","accepted_status":["oops"]}`,
			want: "invalid accepted_status",
		},
		{
			name: "keyword required for keyword type",
			typ:  model.TypeKeyword,
			raw:  `{"url":"https://example.com"}`,
			want: "keyword is required",
		},
		{
			name: "keyword fields not allowed on http",
			typ:  model.TypeHTTP,
			raw:  `{"url":"https://example.com","keyword":"ok"}`,
			want: "only allowed on keyword monitors",
		},
		{
			name: "bad port too low",
			typ:  model.TypeTCP,
			raw:  `{"host":"example.com","port":0}`,
			want: "port must be between",
		},
		{
			name: "bad port too high",
			typ:  model.TypeTCP,
			raw:  `{"host":"example.com","port":70000}`,
			want: "port must be between",
		},
		{
			name: "bad dns record type",
			typ:  model.TypeDNS,
			raw:  `{"hostname":"example.com","record_type":"SRV"}`,
			want: "record_type must be one of",
		},
		{
			name: "bad method",
			typ:  model.TypeHTTP,
			raw:  `{"url":"https://example.com","method":"TRACE"}`,
			want: "method must be one of",
		},
		{
			name: "bad header name",
			typ:  model.TypeHTTP,
			raw:  `{"url":"https://example.com","headers":{"bad header":"v"}}`,
			want: "invalid header name",
		},
		{
			name: "bad max redirects",
			typ:  model.TypeHTTP,
			raw:  `{"url":"https://example.com","max_redirects":21}`,
			want: "max_redirects must be between",
		},
		{
			name: "empty tcp host",
			typ:  model.TypeTCP,
			raw:  `{"host":"","port":80}`,
			want: "host is required",
		},
		{
			name: "unknown resolver port",
			typ:  model.TypeDNS,
			raw:  `{"hostname":"example.com","resolver":"1.1.1.1:notaport"}`,
			want: "resolver port must be between",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ValidateConfig(tc.typ, json.RawMessage(tc.raw))
			if err == nil {
				t.Fatalf("ValidateConfig(%s) = nil error, want error containing %q", tc.raw, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ValidateConfig(%s) error = %q, want it to contain %q", tc.raw, err.Error(), tc.want)
			}
		})
	}
}

func TestValidateConfigUnknownType(t *testing.T) {
	t.Parallel()
	_, err := ValidateConfig(model.MonitorType("carrier-pigeon"), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "unknown monitor type") {
		t.Fatalf("err = %v, want unknown monitor type error", err)
	}
}
