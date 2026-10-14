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
	// LLMRetries more tries after a failed model call, LLMRetryDelay before
	// the first of them (doubled after each).
	LLMRetries    int
	LLMRetryDelay time.Duration

	// Ticker: short ASCII lines for the LED matrix, one message each.
	// ModelDeny: substrings of model ids whose answers are rejected.
	ModelDeny       []string
	SummaryMinChars int

	TickerEnabled  bool
	TickerModel    string
	TickerMax      int
	TickerMaxChars int
	TickerSystem   string
	TickerDelay    time.Duration

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
		TickerEnabled:     getEnv("TICKER_ENABLED", "true") == "true",
		TickerSystem:      getEnv("TICKER_SYSTEM", "ai-ticker"),
	}
	c.TickerModel = getEnv("OPENROUTER_TICKER_MODEL", c.Model)
	for _, d := range strings.Split(getEnv("OPENROUTER_MODEL_DENYLIST", "safety,guard"), ",") {
		if d = strings.TrimSpace(d); d != "" {
			c.ModelDeny = append(c.ModelDeny, d)
		}
	}

	ints := []struct {
		env string
		def string
		min int
		dst *int
	}{
		{"LLM_RETRIES", "2", 0, &c.LLMRetries},
		{"SUMMARY_MIN_CHARS", "80", 0, &c.SummaryMinChars},
		{"TICKER_MAX", "4", 1, &c.TickerMax},
		{"TICKER_MAX_CHARS", "40", 10, &c.TickerMaxChars},
	}
	for _, i := range ints {
		v, err := strconv.Atoi(getEnv(i.env, i.def))
		if err != nil || v < i.min {
			return nil, fmt.Errorf("invalid %s %q (want an integer >= %d)", i.env, os.Getenv(i.env), i.min)
		}
		*i.dst = v
	}
	durs := []struct {
		env string
		def string
		dst *time.Duration
	}{
		{"LLM_RETRY_DELAY", "5s", &c.LLMRetryDelay},
		{"TICKER_DELAY", "3s", &c.TickerDelay},
	}
	for _, d := range durs {
		v, err := time.ParseDuration(getEnv(d.env, d.def))
		if err != nil || v < 0 {
			return nil, fmt.Errorf("invalid %s %q", d.env, os.Getenv(d.env))
		}
		*d.dst = v
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
