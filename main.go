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
	"github.com/stuttgart-things/homerun2-ai-pitcher/internal/ticker"
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

	ctx, cancel := context.WithTimeout(context.Background(), runBudget(cfg))
	defer cancel()

	client := func(model string) llm.Completer {
		return &llm.Retrying{
			Next: &llm.Client{
				BaseURL:   cfg.OpenRouterBaseURL,
				APIKey:    cfg.OpenRouterAPIKey,
				Model:     model,
				MaxTokens: cfg.MaxTokens,
				HTTP:      &http.Client{Timeout: cfg.LLMTimeout},
			},
			Attempts: cfg.LLMRetries + 1,
			Delay:    cfg.LLMRetryDelay,
		}
	}
	out, err := Run(ctx, cfg, sources.NewClient(), client(cfg.Model), client(cfg.TickerModel))
	if err != nil {
		slog.Error("run failed", "error", err)
		os.Exit(1)
	}

	if cfg.DryRun {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return
	}
	pc := &pitch.Client{URL: cfg.PitcherURL, Token: cfg.PitcherToken, HTTP: &http.Client{Timeout: 30 * time.Second}}
	id, err := pc.Pitch(ctx, out.Summary)
	if err != nil {
		slog.Error("pitch failed", "error", err)
		os.Exit(1)
	}
	slog.Info("pitched", "objectId", id, "title", out.Summary.Title)

	// One message per ticker line, spaced so the matrix shows them one after
	// the other instead of dropping a burst. A lost line is logged, not fatal.
	for _, t := range out.Tickers {
		select {
		case <-ctx.Done():
			slog.Error("tickers not pitched", "error", ctx.Err())
			os.Exit(1)
		case <-time.After(cfg.TickerDelay):
		}
		if id, err := pc.Pitch(ctx, t); err != nil {
			slog.Error("ticker pitch failed", "title", t.Title, "error", err)
		} else {
			slog.Info("ticker pitched", "objectId", id, "title", t.Title)
		}
	}
}

// Output is what one run pitches: the summary, then the ticker lines.
type Output struct {
	Summary pitch.Message   `json:"summary"`
	Tickers []pitch.Message `json:"tickers,omitempty"`
}

// Run collects the inputs, asks the model(s) and composes the messages. Only
// a missing digest is fatal: without scout's numbers there is nothing to
// summarise. A failing schedule-pitcher or model degrades the messages.
func Run(ctx context.Context, cfg *config.Config, src *sources.Client, ai, tickerAI llm.Completer) (Output, error) {
	d, err := src.FetchDigest(ctx, cfg.ScoutURL, cfg.ScoutToken, cfg.DigestSchedule)
	if err != nil {
		return Output{}, err
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
		return Output{}, err
	}

	var out Output
	res, aiErr := ai.Complete(ctx, summary.SystemPrompt(cfg.Language), user)
	if aiErr != nil {
		slog.Warn("AI summary unavailable, pitching the plain digest", "error", aiErr)
		out.Summary = summary.Compose(in, "", "", false, aiErr, cfg.System, cfg.Author)
	} else {
		slog.Info("AI summary", "model", res.Model, "chars", len(res.Text), "truncated", res.Truncated)
		out.Summary = summary.Compose(in, res.Text, res.Model, res.Truncated, nil, cfg.System, cfg.Author)
	}

	if cfg.TickerEnabled {
		out.Tickers = tickers(ctx, cfg, in, user, tickerAI)
	}
	return out, nil
}

// tickers asks for short lines and keeps the ones that pass ticker.Parse.
// Without any, the matrix still gets scout's own digest title, unmarked as
// AI because it is not.
func tickers(ctx context.Context, cfg *config.Config, in summary.Input, user string, ai llm.Completer) []pitch.Message {
	msg := func(line, note, tags string) pitch.Message {
		return pitch.Message{
			Title:    line,
			Message:  line + "\n\n" + note,
			Severity: "info",
			Author:   cfg.Author,
			System:   cfg.TickerSystem,
			Tags:     tags,
		}
	}
	fallback := func(why string) []pitch.Message {
		slog.Warn("no AI ticker, using scout's digest title", "reason", why)
		line := ticker.ASCII(in.Digest.Message.Title)
		return []pitch.Message{msg(line, "scout "+in.Digest.Schedule+" digest (AI ticker not available)", "ticker,digest")}
	}

	res, err := ai.Complete(ctx, ticker.SystemPrompt(cfg.Language, cfg.TickerMax, cfg.TickerMaxChars), user)
	if err != nil {
		return fallback(err.Error())
	}
	lines := ticker.Parse(res.Text, cfg.TickerMax, cfg.TickerMaxChars, user+"\n"+in.Digest.Message.Message, len(in.Findings))
	if len(lines) == 0 {
		return fallback("no usable line in the answer")
	}
	slog.Info("AI ticker", "model", res.Model, "lines", len(lines))
	var out []pitch.Message
	for _, l := range lines {
		out = append(out, msg(l, "AI-generated ticker (model "+res.Model+")", "ai,ticker"))
	}
	return out
}

// runBudget is the deadline of one run: two model calls (summary, ticker),
// each with all its retries and back-off, the ticker spacing, and 2 min for
// the HTTP calls around them.
func runBudget(cfg *config.Config) time.Duration {
	attempts := cfg.LLMRetries + 1
	perCall := time.Duration(attempts) * cfg.LLMTimeout
	for i, d := 1, cfg.LLMRetryDelay; i < attempts; i, d = i+1, d*2 {
		perCall += d
	}
	calls := 1
	if cfg.TickerEnabled {
		calls = 2
	}
	return time.Duration(calls)*perCall + time.Duration(cfg.TickerMax)*cfg.TickerDelay + 2*time.Minute
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
