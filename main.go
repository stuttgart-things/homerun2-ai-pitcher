// homerun2-ai-pitcher turns scout's digest and the schedule-pitcher's open
// findings into a short AI summary (OpenRouter) and pitches it through
// omni-pitcher. One run, one message: it is meant to run as a CronJob.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/stuttgart-things/homerun2-ai-pitcher/internal/config"
	"github.com/stuttgart-things/homerun2-ai-pitcher/internal/llm"
	"github.com/stuttgart-things/homerun2-ai-pitcher/internal/pitch"
	"github.com/stuttgart-things/homerun2-ai-pitcher/internal/sources"
	"github.com/stuttgart-things/homerun2-ai-pitcher/internal/summary"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()})))

	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Printf("homerun2-ai-pitcher %s (%s, %s)\n", version, commit, date)
		return
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(2)
	}
	slog.Info("starting", "version", version, "model", cfg.Model, "schedule", cfg.DigestSchedule, "dryRun", cfg.DryRun)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.LLMTimeout+2*time.Minute)
	defer cancel()

	msg, err := Run(ctx, cfg, sources.NewClient(), &llm.Client{
		BaseURL:   cfg.OpenRouterBaseURL,
		APIKey:    cfg.OpenRouterAPIKey,
		Model:     cfg.Model,
		MaxTokens: cfg.MaxTokens,
		HTTP:      &http.Client{Timeout: cfg.LLMTimeout},
	})
	if err != nil {
		slog.Error("run failed", "error", err)
		os.Exit(1)
	}

	if cfg.DryRun {
		out, _ := json.MarshalIndent(msg, "", "  ")
		fmt.Println(string(out))
		return
	}
	pc := &pitch.Client{URL: cfg.PitcherURL, Token: cfg.PitcherToken, HTTP: &http.Client{Timeout: 30 * time.Second}}
	id, err := pc.Pitch(ctx, msg)
	if err != nil {
		slog.Error("pitch failed", "error", err)
		os.Exit(1)
	}
	slog.Info("pitched", "objectId", id, "title", msg.Title)
}

// Completer is the part of the LLM client Run needs.
type Completer interface {
	Complete(ctx context.Context, system, user string) (*llm.Result, error)
}

// Run collects the inputs, asks the model and composes the message. Only a
// missing digest is fatal: without scout's numbers there is nothing to
// summarise. A failing schedule-pitcher or model degrades the message.
func Run(ctx context.Context, cfg *config.Config, src *sources.Client, ai Completer) (pitch.Message, error) {
	d, err := src.FetchDigest(ctx, cfg.ScoutURL, cfg.ScoutToken, cfg.DigestSchedule)
	if err != nil {
		return pitch.Message{}, err
	}
	in := summary.Input{Digest: d, Now: time.Now()}

	if cfg.FindingsURL != "" {
		in.Findings, in.FindingsErr = src.FetchOpenFindings(ctx, cfg.FindingsURL, cfg.FindingsToken)
		if in.FindingsErr != nil {
			slog.Warn("findings unavailable", "error", in.FindingsErr)
		}
	}

	user, err := summary.UserPrompt(in, cfg.MaxFindings)
	if err != nil {
		return pitch.Message{}, err
	}
	res, aiErr := ai.Complete(ctx, summary.SystemPrompt(cfg.Language), user)
	if aiErr != nil {
		slog.Warn("AI summary unavailable, pitching the plain digest", "error", aiErr)
		return summary.Compose(in, "", "", false, aiErr, cfg.System, cfg.Author), nil
	}
	slog.Info("AI summary", "model", res.Model, "chars", len(res.Text), "truncated", res.Truncated)
	return summary.Compose(in, res.Text, res.Model, res.Truncated, nil, cfg.System, cfg.Author), nil
}

func logLevel() slog.Level {
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}
