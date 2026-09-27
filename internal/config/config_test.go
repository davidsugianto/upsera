package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func valid() map[string]string {
	return map[string]string{
		"DATABASE_URL": "postgres://u:p@localhost:5432/upsera",
		"APP_SECRET":   strings.Repeat("x", 32),
	}
}

func TestLoadDefaults(t *testing.T) {
	c, err := load(env(valid()))
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 3080 || c.DBMaxConns != 10 || c.HeartbeatBufferSize != 100000 || c.RetentionDays != 14 ||
		c.TimeZone != "UTC" || c.FlushInterval != time.Second || c.BaseURL != "http://localhost:3080" ||
		c.TelegramAPIURL != "https://api.telegram.org" || c.SlackAPIURL != "https://slack.com/api" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if c.SecureCookies() {
		t.Fatal("http base URL must not use secure cookies")
	}
}

func TestLoadRejects(t *testing.T) {
	cases := map[string]struct {
		set  map[string]string
		want string
	}{
		"missing database":     {map[string]string{"DATABASE_URL": ""}, "DATABASE_URL is required"},
		"short secret":         {map[string]string{"APP_SECRET": "short"}, "APP_SECRET"},
		"retention below 3":    {map[string]string{"HEARTBEAT_RETENTION_DAYS": "2"}, "HEARTBEAT_RETENTION_DAYS"},
		"pool too small":       {map[string]string{"DB_MAX_CONNS": "1"}, "DB_MAX_CONNS"},
		"bad time zone":        {map[string]string{"TZ": "Mars/Olympus"}, "TZ must be"},
		"relative base url":    {map[string]string{"BASE_URL": "status.example.com"}, "BASE_URL"},
		"port out of range":    {map[string]string{"PORT": "70000"}, "PORT"},
		"bad flush interval":   {map[string]string{"HEARTBEAT_FLUSH_INTERVAL": "1ms"}, "HEARTBEAT_FLUSH_INTERVAL"},
		"bad telegram api url": {map[string]string{"TELEGRAM_API_URL": "telegram.example.com"}, "TELEGRAM_API_URL"},
		"bad slack api url":    {map[string]string{"SLACK_API_URL": "slack.example.com"}, "SLACK_API_URL"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := valid()
			for k, v := range tc.set {
				m[k] = v
			}
			_, err := load(env(m))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error mentioning %q, got %v", tc.want, err)
			}
		})
	}
}

func TestSecureCookiesFollowBaseURL(t *testing.T) {
	m := valid()
	m["BASE_URL"] = "https://status.example.com/"
	c, err := load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if !c.SecureCookies() || c.BaseURL != "https://status.example.com" {
		t.Fatalf("got %q secure=%v", c.BaseURL, c.SecureCookies())
	}
}

func TestTelegramAndSlackAPIURLTrimTrailingSlash(t *testing.T) {
	m := valid()
	m["TELEGRAM_API_URL"] = "https://telegram.example.com/"
	m["SLACK_API_URL"] = "https://slack.example.com/"
	c, err := load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if c.TelegramAPIURL != "https://telegram.example.com" || c.SlackAPIURL != "https://slack.example.com" {
		t.Fatalf("got telegram=%q slack=%q", c.TelegramAPIURL, c.SlackAPIURL)
	}
}
