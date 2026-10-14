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
	t.Setenv("DIGEST_SCHEDULE", "weekly")
	if _, err := Load(); err == nil {
		t.Fatal("want an error for weekly")
	}
}
