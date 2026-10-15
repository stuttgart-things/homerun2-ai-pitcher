// homerun2-ai-pitcher turns scout's digest, the messages of the same window
// (git activity, outages) and the schedule-pitcher's open findings into a
// short AI summary (OpenRouter) and pitches it through omni-pitcher. One run,
// one message: it is meant to run as a CronJob.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/stuttgart-things/homerun2-ai-pitcher/internal/activity"
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

	client := func(model string, minChars int) llm.Completer {
		return &llm.Retrying{
			Next: &llm.Client{
				BaseURL:   cfg.OpenRouterBaseURL,
				APIKey:    cfg.OpenRouterAPIKey,
				Model:     model,
				MaxTokens: cfg.MaxTokens,
				Deny:      cfg.ModelDeny,
				MinChars:  minChars,
				HTTP:      &http.Client{Timeout: cfg.LLMTimeout},
			},
			Attempts: cfg.LLMRetries + 1,
			Delay:    cfg.LLMRetryDelay,
		}
	}
	out, err := Run(ctx, cfg, sources.NewClient(), client(cfg.Model, cfg.SummaryMinChars), client(cfg.TickerModel, 0))
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
// summarise. Failing messages, schedule-pitcher or model degrade the
// messages.
func Run(ctx context.Context, cfg *config.Config, src *sources.Client, ai, tickerAI llm.Completer) (Output, error) {
	d, err := src.FetchDigest(ctx, cfg.ScoutURL, cfg.ScoutToken, cfg.DigestSchedule)
	if err != nil {
		return Output{}, err
	}
	in := summary.Input{Digest: d, Now: time.Now()}

	if cfg.MessagesEnabled || cfg.OutagesEnabled {
		in.Activity, in.ActivityErr = collectActivity(ctx, cfg, src)
		if in.ActivityErr != nil {
			slog.Warn("activity unavailable", "error", in.ActivityErr)
		}
	}

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

// messagesLimit is the most messages scout's /analytics/messages returns.
const messagesLimit = 500

// collectActivity reads scout's messages of the digest's window: the git
// activity of cfg.MessagesSystems, and outages of every system that had an
// error or critical message, except cfg.OutageExcludeSystems (their errors
// are findings or failed workflows, not outages). Message texts are read for
// the commit counts but never reach the model.
func collectActivity(ctx context.Context, cfg *config.Config, src *sources.Client) (*activity.Activity, error) {
	a := &activity.Activity{}
	var incomplete []string
	note := func(m *sources.Messages, what string) {
		switch {
		case m.WindowIncomplete:
			incomplete = append(incomplete, what+": scout's retention is shorter than the window")
		case m.Truncated || m.Omitted > 0:
			incomplete = append(incomplete, fmt.Sprintf("%s: only the newest %d of %d messages", what, len(m.Messages), m.Total))
		}
	}

	if cfg.MessagesEnabled && len(cfg.MessagesSystems) > 0 {
		m, err := src.FetchMessages(ctx, cfg.ScoutURL, cfg.ScoutToken, sources.MessageQuery{
			Schedule: cfg.DigestSchedule, Systems: cfg.MessagesSystems, Limit: messagesLimit, Body: true,
		})
		if err != nil {
			return nil, err
		}
		note(m, "git activity")
		a.Repos, a.Totals = activity.Git(m.Messages, cfg.MaxTitles)
	}

	if cfg.OutagesEnabled {
		alerts, err := src.FetchMessages(ctx, cfg.ScoutURL, cfg.ScoutToken, sources.MessageQuery{
			Schedule: cfg.DigestSchedule, Severities: []string{"error", "critical"}, Limit: messagesLimit,
		})
		if err != nil {
			return nil, err
		}
		var systems []string
		for _, e := range alerts.Messages {
			if !slices.Contains(cfg.OutageExcludeSystems, e.System) && !slices.Contains(systems, e.System) {
				systems = append(systems, e.System)
			}
		}
		if len(systems) > 0 {
			// Every message of those systems: the alerts and what cleared them.
			m, err := src.FetchMessages(ctx, cfg.ScoutURL, cfg.ScoutToken, sources.MessageQuery{
				Schedule: cfg.DigestSchedule, Systems: systems, Limit: messagesLimit,
			})
			if err != nil {
				return nil, err
			}
			note(m, "outages")
			a.Outages = activity.Outages(m.Messages)
			a.Totals.Outages = len(a.Outages)
			for _, o := range a.Outages {
				if o.Ongoing {
					a.Totals.OngoingOutages++
				}
			}
		}
	}
	a.Incomplete = strings.Join(incomplete, "; ")
	return a, nil
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
	// The numbers a line may use: the input without the pull request and
	// issue titles, so a number written into a title by anyone is not a fact.
	facts := in
	if in.Activity != nil {
		a := in.Activity.WithoutTitles()
		facts.Activity = &a
	}
	factsJSON, err := summary.UserPrompt(facts, cfg.MaxFindings)
	if err != nil {
		return fallback(err.Error())
	}
	lines := ticker.Parse(res.Text, cfg.TickerMax, cfg.TickerMaxChars, factsJSON+"\n"+in.Digest.Message.Message, len(in.Findings))
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
