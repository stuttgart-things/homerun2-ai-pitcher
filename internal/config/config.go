// Package config loads the pitcher's settings from the environment, once at
// startup.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds everything one run needs.
type Config struct {
	// scout: the deterministic numbers (GET /analytics/digest).
	ScoutURL       string
	ScoutToken     string
	DigestSchedule string

	// schedule-pitcher: open findings and reminders (GET /api/findings).
	// Optional; without a URL the summary is built from the digest alone.
	FindingsURL   string
	FindingsToken string
	MaxFindings   int

	// OpenRouter (OpenAI-compatible chat completions).
	OpenRouterBaseURL string
	OpenRouterAPIKey  string
	Model             string
	LLMTimeout        time.Duration
	MaxTokens         int
	Language          string

	// omni-pitcher: where the result goes (POST <PitcherURL>/pitch).
	PitcherURL   string
	PitcherToken string
	System       string
	Author       string

	// DryRun prints the message instead of pitching it.
	DryRun bool
}

// Load reads the environment and checks the required settings.
func Load() (*Config, error) {
	c := &Config{
		ScoutURL:          strings.TrimRight(getEnv("SCOUT_URL", ""), "/"),
		ScoutToken:        getEnv("SCOUT_TOKEN", ""),
		DigestSchedule:    getEnv("DIGEST_SCHEDULE", "daily"),
		FindingsURL:       strings.TrimRight(getEnv("SCHEDULE_PITCHER_URL", ""), "/"),
		FindingsToken:     getEnv("SCHEDULE_PITCHER_TOKEN", ""),
		OpenRouterBaseURL: strings.TrimRight(getEnv("OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1"), "/"),
		OpenRouterAPIKey:  getEnv("OPENROUTER_API_KEY", ""),
		Model:             getEnv("OPENROUTER_MODEL", "openrouter/free"),
		Language:          getEnv("SUMMARY_LANGUAGE", "German"),
		PitcherURL:        strings.TrimRight(getEnv("PITCHER_URL", ""), "/"),
		PitcherToken:      getEnv("PITCHER_TOKEN", ""),
		System:            getEnv("PITCH_SYSTEM", "ai-digest"),
		Author:            getEnv("PITCH_AUTHOR", "homerun2-ai-pitcher"),
		DryRun:            getEnv("DRY_RUN", "false") == "true",
	}

	n, err := strconv.Atoi(getEnv("MAX_FINDINGS", "20"))
	if err != nil || n < 0 {
		return nil, fmt.Errorf("invalid MAX_FINDINGS %q", os.Getenv("MAX_FINDINGS"))
	}
	c.MaxFindings = n

	mt, err := strconv.Atoi(getEnv("LLM_MAX_TOKENS", "4000"))
	if err != nil || mt <= 0 {
		return nil, fmt.Errorf("invalid LLM_MAX_TOKENS %q", os.Getenv("LLM_MAX_TOKENS"))
	}
	c.MaxTokens = mt

	d, err := time.ParseDuration(getEnv("LLM_TIMEOUT", "90s"))
	if err != nil {
		return nil, fmt.Errorf("invalid LLM_TIMEOUT: %w", err)
	}
	c.LLMTimeout = d

	switch c.DigestSchedule {
	case "daily", "hourly":
	default:
		return nil, fmt.Errorf("DIGEST_SCHEDULE must be daily or hourly, got %q", c.DigestSchedule)
	}

	var missing []string
	if c.ScoutURL == "" {
		missing = append(missing, "SCOUT_URL")
	}
	if c.ScoutToken == "" {
		missing = append(missing, "SCOUT_TOKEN")
	}
	if !c.DryRun {
		if c.PitcherURL == "" {
			missing = append(missing, "PITCHER_URL")
		}
		if c.PitcherToken == "" {
			missing = append(missing, "PITCHER_TOKEN")
		}
	}
	if len(missing) > 0 {
		return nil, errors.New("missing required settings: " + strings.Join(missing, ", "))
	}
	// No OPENROUTER_API_KEY is not an error: the run is fail-open and pitches
	// the plain digest, marked as such.
	return c, nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
