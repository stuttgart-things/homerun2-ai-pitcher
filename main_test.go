package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stuttgart-things/homerun2-ai-pitcher/internal/config"
	"github.com/stuttgart-things/homerun2-ai-pitcher/internal/llm"
	"github.com/stuttgart-things/homerun2-ai-pitcher/internal/sources"
)

const digestJSON = `{
  "schedule": "daily",
  "message": {"title": "24h: 12 msgs 0 err 0 crit", "message": "daily digest ...\nMessages: 12 (previous 34, -22)", "severity": "success"},
  "current": {"total": 12, "severities": {"warning": 5, "success": 5, "info": 2},
              "topSystems": [{"system": "homerun2-git-pitcher", "count": 3, "alerts": 0}]},
  "previous": {"total": 34, "severities": {"warning": 33, "info": 1}, "topSystems": []}
}`

func findingsJSON() string {
	first := time.Now().Add(-72 * time.Hour).UTC().Format(time.RFC3339)
	return `[{"source": "reminders", "key": "runner-replacement-labda", "title": "Decommission the old LabDA runners",
	          "severity": "warning", "status": "open", "firstSeen": "` + first + `"},
	         {"source": "checks-platform-sthings", "key": "backstage-github-token", "title": "GitHub token of Backstage expires in 5 days",
	          "severity": "error", "status": "acknowledged", "ackBy": "patrick.hermann", "firstSeen": "` + first + `"}]`
}

// fakes starts scout + schedule-pitcher behind one server and checks auth.
func fakes(t *testing.T, findingsStatus int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /analytics/digest", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer scout-token" {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("schedule") != "daily" {
			http.Error(w, "schedule", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(digestJSON))
	})
	mux.HandleFunc("GET /api/findings", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("status") != "open" || r.Header.Get("Authorization") != "Bearer sp-token" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if findingsStatus != http.StatusOK {
			http.Error(w, "redis down", findingsStatus)
			return
		}
		_, _ = w.Write([]byte(findingsJSON()))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type fakeAI struct {
	text      string
	err       error
	gotUser   string
	gotSystem string
}

func (f *fakeAI) Complete(_ context.Context, system, user string) (*llm.Result, error) {
	f.gotSystem, f.gotUser = system, user
	if f.err != nil {
		return nil, f.err
	}
	return &llm.Result{Text: f.text, Model: "meta-llama/some-free-model:free"}, nil
}

func testConfig(url string) *config.Config {
	return &config.Config{
		ScoutURL: url, ScoutToken: "scout-token", DigestSchedule: "daily",
		FindingsURL: url, FindingsToken: "sp-token", MaxFindings: 20,
		Language: "German", System: "ai-digest", Author: "homerun2-ai-pitcher",
		TickerEnabled: true, TickerMax: 4, TickerMaxChars: 40, TickerSystem: "ai-ticker",
	}
}

func TestRunWithAI(t *testing.T) {
	srv := fakes(t, http.StatusOK)
	ai := &fakeAI{text: "Heute ist ein Token in 5 Tagen fällig; Patrick ist dran."}

	out, err := Run(context.Background(), testConfig(srv.URL), sources.NewClient(), ai, &fakeAI{err: context.Canceled})
	msg := out.Summary
	if err != nil {
		t.Fatal(err)
	}
	if msg.Title != "[AI] 24h: 12 msgs 0 err 0 crit" {
		t.Errorf("title = %q: must be scout's numbers with the AI marker", msg.Title)
	}
	if msg.Severity != "info" || msg.System != "ai-digest" || msg.Tags != "ai,digest,daily" {
		t.Errorf("severity/system/tags = %q/%q/%q", msg.Severity, msg.System, msg.Tags)
	}
	for _, want := range []string{
		"Heute ist ein Token",                   // the AI text
		"model meta-llama/some-free-model:free", // the model that answered, not the router
		"Messages: 12 (previous 34, -22)",       // scout's numbers verbatim
		"[error] GitHub token of Backstage",     // the findings list
		"(ack patrick.hermann)",
	} {
		if !strings.Contains(msg.Message, want) {
			t.Errorf("message lacks %q:\n%s", want, msg.Message)
		}
	}
	// The prompt carries both sources as JSON.
	var p map[string]any
	if err := json.Unmarshal([]byte(ai.gotUser), &p); err != nil {
		t.Fatalf("user prompt is not JSON: %v", err)
	}
	if got := len(p["openFindings"].([]any)); got != 2 {
		t.Errorf("prompt has %d findings, want 2", got)
	}
	if !strings.Contains(ai.gotSystem, "Write in German") {
		t.Error("system prompt lacks the language")
	}
}

func TestRunFailOpenWithoutAI(t *testing.T) {
	srv := fakes(t, http.StatusOK)
	ai := &fakeAI{err: context.DeadlineExceeded}

	out, err := Run(context.Background(), testConfig(srv.URL), sources.NewClient(), ai, &fakeAI{err: context.Canceled})
	msg := out.Summary
	if err != nil {
		t.Fatalf("a failing model must not fail the run: %v", err)
	}
	if !strings.HasPrefix(msg.Title, "[AI n/a] ") {
		t.Errorf("title = %q, want the n/a marker", msg.Title)
	}
	if !strings.Contains(msg.Message, "AI summary not available (context deadline exceeded)") ||
		!strings.Contains(msg.Message, "Messages: 12") {
		t.Errorf("fallback must say why and carry the plain digest:\n%s", msg.Message)
	}
}

func TestRunFindingsUnavailable(t *testing.T) {
	srv := fakes(t, http.StatusServiceUnavailable)
	ai := &fakeAI{text: "ok"}

	out, err := Run(context.Background(), testConfig(srv.URL), sources.NewClient(), ai, &fakeAI{err: context.Canceled})
	msg := out.Summary
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ai.gotUser, "open findings are unknown") {
		t.Error("prompt must not let the model claim there are no findings")
	}
	if !strings.Contains(msg.Message, "Open findings could not be read") {
		t.Errorf("message must say findings are missing:\n%s", msg.Message)
	}
}

func TestRunWithoutFindingsURL(t *testing.T) {
	srv := fakes(t, http.StatusOK)
	cfg := testConfig(srv.URL)
	cfg.FindingsURL = ""
	ai := &fakeAI{text: "ok"}

	if _, err := Run(context.Background(), cfg, sources.NewClient(), ai, ai); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ai.gotUser, "unknown") {
		t.Error("no findings source configured is not an error")
	}
}

func TestRunDigestMissingIsFatal(t *testing.T) {
	srv := fakes(t, http.StatusOK)
	cfg := testConfig(srv.URL)
	cfg.ScoutToken = "wrong"

	if _, err := Run(context.Background(), cfg, sources.NewClient(), &fakeAI{text: "x"}, &fakeAI{text: "x"}); err == nil {
		t.Fatal("without scout's digest there is nothing to summarise; want an error")
	}
}

func TestRunTickers(t *testing.T) {
	srv := fakes(t, http.StatusOK)
	tick := &fakeAI{text: "1. 2 Tokens laufen ab, Backstage zuerst\n- 12 Meldungen, weniger Warnungen\n99 Deployments heute\n- Ärger? Keiner."}

	out, err := Run(context.Background(), testConfig(srv.URL), sources.NewClient(), &fakeAI{text: "ok"}, tick)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, m := range out.Tickers {
		titles = append(titles, m.Title)
		if m.System != "ai-ticker" || m.Severity != "info" || m.Tags != "ai,ticker" {
			t.Errorf("ticker message = %+v", m)
		}
		if !strings.Contains(m.Message, "AI-generated ticker (model meta-llama/some-free-model:free)") {
			t.Errorf("ticker not marked: %q", m.Message)
		}
	}
	// "2" is a count of the 2 findings, "12" is scout's total; "99" is made up.
	want := []string{"2 Tokens laufen ab, Backstage zuerst", "12 Meldungen, weniger Warnungen", "Aerger? Keiner."}
	if strings.Join(titles, "|") != strings.Join(want, "|") {
		t.Errorf("tickers = %q, want %q", titles, want)
	}
	if !strings.Contains(tick.gotSystem, "at most 40 characters") {
		t.Error("ticker prompt lacks the length limit")
	}
}

func TestRunTickerFallback(t *testing.T) {
	srv := fakes(t, http.StatusOK)
	for name, tick := range map[string]*fakeAI{
		"model fails":    {err: context.DeadlineExceeded},
		"nothing usable": {text: "99 erfundene Dinge"},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := Run(context.Background(), testConfig(srv.URL), sources.NewClient(), &fakeAI{text: "ok"}, tick)
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Tickers) != 1 || out.Tickers[0].Title != "24h: 12 msgs 0 err 0 crit" || out.Tickers[0].Tags != "ticker,digest" {
				t.Errorf("fallback tickers = %+v", out.Tickers)
			}
		})
	}
}

func TestRunTickerDisabled(t *testing.T) {
	srv := fakes(t, http.StatusOK)
	cfg := testConfig(srv.URL)
	cfg.TickerEnabled = false
	tick := &fakeAI{text: "x"}

	out, err := Run(context.Background(), cfg, sources.NewClient(), &fakeAI{text: "ok"}, tick)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Tickers) != 0 || tick.gotSystem != "" {
		t.Errorf("tickers disabled but got %+v", out.Tickers)
	}
}

func TestRunBudget(t *testing.T) {
	cfg := &config.Config{LLMRetries: 2, LLMTimeout: 90 * time.Second, LLMRetryDelay: 5 * time.Second,
		TickerEnabled: true, TickerMax: 4, TickerDelay: 3 * time.Second}
	// per call 3*90s + 5s + 10s = 285s; two calls 570s; tickers 12s; +120s
	if got, want := runBudget(cfg), 702*time.Second; got != want {
		t.Errorf("runBudget = %v, want %v", got, want)
	}
}
