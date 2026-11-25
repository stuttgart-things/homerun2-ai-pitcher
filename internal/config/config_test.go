package config

import (
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("SCOUT_URL", "http://scout/")
	t.Setenv("SCOUT_TOKEN", "t")
	t.Setenv("PITCHER_URL", "http://omni")
	t.Setenv("PITCHER_TOKEN", "p")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.ScoutURL != "http://scout" {
		t.Errorf("trailing slash not trimmed: %q", c.ScoutURL)
	}
	if c.Model != "openrouter/free" || c.System != "ai-digest" || c.DigestSchedule != "daily" || c.Language != "German" {
		t.Errorf("defaults = %+v", c)
	}
}

func TestLoadMissing(t *testing.T) {
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "SCOUT_URL") || !strings.Contains(err.Error(), "PITCHER_TOKEN") {
		t.Errorf("err = %v", err)
	}
}

func TestLoadDryRunNeedsNoPitcher(t *testing.T) {
	t.Setenv("SCOUT_URL", "http://scout")
	t.Setenv("SCOUT_TOKEN", "t")
	t.Setenv("DRY_RUN", "true")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadBadSchedule(t *testing.T) {
	t.Setenv("SCOUT_URL", "http://scout")
	t.Setenv("SCOUT_TOKEN", "t")
	t.Setenv("DRY_RUN", "true")
	t.Setenv("DIGEST_SCHEDULE", "monthly")
	if _, err := Load(); err == nil {
		t.Fatal("want an error for monthly")
	}
	t.Setenv("DIGEST_SCHEDULE", "weekly")
	if _, err := Load(); err != nil {
		t.Fatalf("weekly: %v", err)
	}
}

func TestLoadMessages(t *testing.T) {
	t.Setenv("SCOUT_URL", "http://scout")
	t.Setenv("SCOUT_TOKEN", "t")
	t.Setenv("DRY_RUN", "true")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.MessagesEnabled || !c.OutagesEnabled || c.MaxTitles != 5 ||
		strings.Join(c.MessagesSystems, ",") != "homerun2-git-pitcher" ||
		strings.Join(c.OutageExcludeSystems, ",") != "homerun2-git-pitcher,homerun2-schedule-pitcher" {
		t.Errorf("defaults = %+v", c)
	}

	t.Setenv("MESSAGES_SYSTEMS", " a, ,b ")
	t.Setenv("OUTAGES_ENABLED", "false")
	t.Setenv("MAX_TITLES", "0")
	if c, err = Load(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.MessagesSystems, ",") != "a,b" || c.OutagesEnabled || c.MaxTitles != 0 {
		t.Errorf("overrides = %+v", c)
	}
}

func TestLoadModes(t *testing.T) {
	t.Setenv("SCOUT_URL", "http://scout")
	t.Setenv("SCOUT_TOKEN", "t")
	t.Setenv("DRY_RUN", "true")

	cases := []struct {
		mode, schedule, system string
		ticker, activity       bool
	}{
		{"", "daily", "ai-digest", true, true}, // morning, as before modes
		{"morning", "daily", "ai-digest", true, true},
		{"evening", "daily", "ai-evening", false, true},
		{"week-ahead", "daily", "ai-week-ahead", false, false},
		{"weekly", "weekly", "ai-weekly", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			t.Setenv("MODE", tc.mode)
			c, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if c.DigestSchedule != tc.schedule || c.System != tc.system || c.TickerEnabled != tc.ticker ||
				c.MessagesEnabled != tc.activity || c.OutagesEnabled != tc.activity {
				t.Errorf("mode %q: schedule %q system %q ticker %v messages %v outages %v",
					c.Mode.Name, c.DigestSchedule, c.System, c.TickerEnabled, c.MessagesEnabled, c.OutagesEnabled)
			}
		})
	}

	// An env var that is set wins over the mode's default.
	t.Setenv("MODE", "weekly")
	t.Setenv("PITCH_SYSTEM", "ai-digest")
	t.Setenv("TICKER_ENABLED", "false")
	t.Setenv("DIGEST_SCHEDULE", "daily")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode.Name != "weekly" || c.System != "ai-digest" || c.TickerEnabled || c.DigestSchedule != "daily" {
		t.Errorf("overrides = %+v", c)
	}

	t.Setenv("MODE", "nightly")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "evening, incident, morning, week-ahead, weekly") {
		t.Errorf("unknown mode: err = %v", err)
	}
}

func TestLoadLLMTimeoutAndNetwork(t *testing.T) {
	t.Setenv("SCOUT_URL", "http://scout")
	t.Setenv("SCOUT_TOKEN", "t")
	t.Setenv("DRY_RUN", "true")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.LLMTimeout.String() != "30s" || c.LLMModelsPerRequest != 1 || c.LLMDialNetwork != "tcp" {
		t.Errorf("timeout=%v perRequest=%d network=%q", c.LLMTimeout, c.LLMModelsPerRequest, c.LLMDialNetwork)
	}
	for env, bad := range map[string]string{"LLM_MODELS_PER_REQUEST": "4", "LLM_DIAL_NETWORK": "udp", "LLM_TIMEOUT": "0s"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, bad)
			if _, err := Load(); err == nil {
				t.Errorf("%s=%s: want an error", env, bad)
			}
		})
	}
}

func TestLoadIncident(t *testing.T) {
	t.Setenv("MODE", "incident")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "REDIS_ADDR") {
		t.Errorf("without redis: %v", err)
	}
	t.Setenv("REDIS_ADDR", "redis-stack.homerun2.svc.cluster.local")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PITCHER_URL") {
		t.Errorf("without pitcher: %v", err)
	}
	t.Setenv("DRY_RUN", "true")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	i := c.Incident
	if c.System != "ai-incident" || i.RedisAddr != "redis-stack.homerun2.svc.cluster.local:6379" ||
		strings.Join(i.Streams, ",") != "alerts,messages" || strings.Join(i.ExcludeSystems, ",") != "ai-*,scout-digest" ||
		i.Once || i.Cooldown.String() != "6h0m0s" || i.Window.String() != "15m0s" || i.Location.String() != "Europe/Berlin" {
		t.Errorf("defaults = %+v system=%s", i, c.System)
	}
	t.Setenv("INCIDENT_RUN", "sometimes")
	if _, err := Load(); err == nil {
		t.Error("want an error for INCIDENT_RUN=sometimes")
	}
}
