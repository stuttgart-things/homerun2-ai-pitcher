package incident

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stuttgart-things/homerun2-ai-pitcher/internal/llm"
	"github.com/stuttgart-things/homerun2-ai-pitcher/internal/pitch"
)

var t0 = time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)

func alert(name, status, fp, cluster, ns, sev string, at time.Time) Event {
	tags := "status=" + status + ",fingerprint=" + fp + ",cluster=" + cluster
	if ns != "" {
		tags += ",namespace=" + ns
	}
	return NewEvent("alerts", at.Format("150405")+"-0", "obj-"+fp, Message{
		Title: name, Severity: sev, System: cluster + "-receiver", Author: "alertmanager", Tags: tags,
		Timestamp: at.Format(time.RFC3339),
	}, at)
}

func TestNewEventAndKeys(t *testing.T) {
	e := alert("NodeClockNotSynchronising", "firing", "abc123", "sthings-infra", "monitoring", "warning", t0)
	if !e.IsAlert() || e.Key() != "fp:abc123" || e.Cluster() != "sthings-infra" || e.Namespace() != "monitoring" || !e.At.Equal(t0) {
		t.Errorf("event = %+v key %s", e, e.Key())
	}
	plain1 := NewEvent("messages", "1-0", "o", Message{Title: "Pod web-7d9f8c restarted 3 times", System: "k8s", Severity: "error"}, t0)
	plain2 := NewEvent("messages", "2-0", "o", Message{Title: "Pod web-5b2a1e restarted 4 times", System: "k8s", Severity: "error"}, t0)
	other := NewEvent("messages", "3-0", "o", Message{Title: "Pod web-5b2a1e restarted 4 times", System: "other", Severity: "error"}, t0)
	if plain1.IsAlert() || plain1.Key() != plain2.Key() || plain1.Key() == other.Key() || !strings.HasPrefix(plain1.Key(), "ts:") {
		t.Errorf("keys %s %s %s", plain1.Key(), plain2.Key(), other.Key())
	}
	if plain1.Cluster() != "k8s" {
		t.Errorf("cluster fallback = %q", plain1.Cluster())
	}
	// A timestamp from the future is not trusted.
	f := NewEvent("m", "1", "o", Message{Timestamp: t0.Add(time.Hour).Format(time.RFC3339)}, t0)
	if !f.At.Equal(t0) {
		t.Errorf("future timestamp used: %v", f.At)
	}
}

func TestExcluded(t *testing.T) {
	ex := []string{"ai-*", "scout-digest"}
	for sys, want := range map[string]bool{"ai-incident": true, "ai-digest": true, "ai-ticker": true, "scout-digest": true, "scout": false, "sthings-infra": false} {
		if Excluded(sys, ex) != want {
			t.Errorf("Excluded(%s) != %v", sys, want)
		}
	}
}

// The case of #135's 2026-10-22 comment: NodeClockNotSynchronising fires on
// two clusters, Alertmanager repeats it every 4h, it never resolves.
func TestCorrelatorStillFiringOnTwoClusters(t *testing.T) {
	ctx := context.Background()
	c := &Correlator{Store: &MemStore{}, Opt: Options{Cooldown: 6 * time.Hour}}
	observe := func(e Event, now time.Time) *Decision {
		t.Helper()
		d, err := c.Observe(ctx, e, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Commit(ctx, d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	a := func(fp, cluster string, at time.Time) Event {
		return alert("NodeClockNotSynchronising", "firing", fp, cluster, "", "warning", at)
	}

	if d := observe(a("fp1", "sthings-infra", t0), t0); len(d.Messages) != 0 || !d.New {
		t.Fatalf("first occurrence: %+v (the original card says it)", d)
	}
	observe(a("fp2", "sthings-platform", t0.Add(time.Minute)), t0.Add(time.Minute))

	at := t0.Add(4 * time.Hour)
	d := observe(a("fp1", "sthings-infra", at), at)
	if len(d.Messages) != 1 {
		t.Fatalf("repeat: %d messages", len(d.Messages))
	}
	m := d.Messages[0]
	want := "[AI] NodeClockNotSynchronising: still firing since 2026-11-20 08:00, on 2 clusters, never resolved"
	if m.Title != want || m.System != "ai-incident" || m.Severity != "info" {
		t.Errorf("message = %+v\nwant title %s", m, want)
	}
	if strings.Contains(m.Tags, "status=") || strings.Contains(m.Tags, "fingerprint=") || !strings.HasPrefix(m.Tags, "ai,incident") {
		t.Errorf("tags = %q (must not look like an alert)", m.Tags)
	}
	if !strings.Contains(m.Message, "sthings-infra, sthings-platform") || !strings.Contains(m.Message, "3 messages") {
		t.Errorf("body = %s", m.Message)
	}

	// The next repeat within the cooldown: nothing.
	at = t0.Add(8 * time.Hour)
	if d := observe(a("fp1", "sthings-infra", at), at); len(d.Messages) != 0 {
		t.Errorf("within cooldown: %+v", d.Messages)
	}
	// After the cooldown: again.
	at = t0.Add(12 * time.Hour)
	if d := observe(a("fp1", "sthings-infra", at), at); len(d.Messages) != 1 {
		t.Errorf("after cooldown: %d messages", len(d.Messages))
	}

	// Resolved: one closing message, because this mode talked about it.
	at = t0.Add(13 * time.Hour)
	d = observe(alert("NodeClockNotSynchronising", "resolved", "fp1", "sthings-infra", "", "success", at), at)
	if len(d.Messages) != 1 || d.Messages[0].Title != "[AI] NodeClockNotSynchronising: resolved after 13h" {
		t.Fatalf("resolved: %+v", d.Messages)
	}
	if !strings.Contains(d.Messages[0].Message, "5 alert messages, 3 AI updates including this one") {
		t.Errorf("resolved body = %s", d.Messages[0].Message)
	}
	st, _ := c.Store.Get(ctx, "fp:fp1")
	if !st.Resolved || st.Cards != 5 {
		t.Errorf("state = %+v", st)
	}
	// A second resolved for the same key: nothing.
	if d := observe(alert("NodeClockNotSynchronising", "resolved", "fp1", "sthings-infra", "", "success", at), at); len(d.Messages) != 0 {
		t.Errorf("double resolve: %+v", d.Messages)
	}

	// Firing again: a new episode, flapping on the second occurrence.
	at = t0.Add(14 * time.Hour)
	if d := observe(a("fp1", "sthings-infra", at), at); !d.New || len(d.Messages) != 0 {
		t.Errorf("reopen: %+v", d)
	}
	at = t0.Add(18 * time.Hour)
	d = observe(a("fp1", "sthings-infra", at), at)
	if len(d.Messages) != 1 || !strings.Contains(d.Messages[0].Title, "resolved 1 times before (flapping)") {
		t.Errorf("flapping: %+v", d.Messages)
	}
}

func TestCorrelatorResolvedWithoutAIMessageStaysQuiet(t *testing.T) {
	ctx := context.Background()
	c := &Correlator{Store: &MemStore{}}
	d, _ := c.Observe(ctx, alert("X", "firing", "f", "c", "", "error", t0), t0)
	_ = c.Commit(ctx, d)
	d, _ = c.Observe(ctx, alert("X", "resolved", "f", "c", "", "success", t0.Add(time.Hour)), t0.Add(time.Hour))
	if len(d.Messages) != 0 || !d.State.Resolved {
		t.Errorf("decision = %+v", d)
	}
	// Unknown key resolved: nothing, no state.
	d, _ = c.Observe(ctx, alert("Y", "resolved", "g", "c", "", "success", t0), t0)
	if d.State != nil || len(d.Messages) != 0 {
		t.Errorf("unknown resolve = %+v", d)
	}
}

func TestCorrelatorPlainMessagesAndStaleness(t *testing.T) {
	ctx := context.Background()
	c := &Correlator{Store: &MemStore{}, Opt: Options{StaleAfter: 26 * time.Hour}}
	msg := func(title string, at time.Time) Event {
		return NewEvent("messages", "1-0", "o", Message{Title: title, System: "homerun2-git-pitcher", Severity: "error"}, at)
	}
	d, _ := c.Observe(ctx, msg("Workflow build failed on run 41", t0), t0)
	_ = c.Commit(ctx, d)
	d, _ = c.Observe(ctx, msg("Workflow build failed on run 42", t0.Add(time.Hour)), t0.Add(time.Hour))
	_ = c.Commit(ctx, d)
	if len(d.Messages) != 1 || d.Messages[0].Title != "[AI] Workflow build failed on run 41: seen 2 times since 2026-11-20 08:00" {
		t.Fatalf("plain repeat: %+v", d.Messages)
	}
	// Two days later: a new episode, not "seen 3 times".
	at := t0.Add(50 * time.Hour)
	d, _ = c.Observe(ctx, msg("Workflow build failed on run 43", at), at)
	if !d.New || len(d.Messages) != 0 || d.State.Occurrences != 1 {
		t.Errorf("stale: %+v", d)
	}
}

type fakeAI struct {
	text  string
	err   error
	calls int
	input string
}

func (f *fakeAI) Complete(_ context.Context, _, user string) (*llm.Result, error) {
	f.calls++
	f.input = user
	if f.err != nil {
		return nil, f.err
	}
	return &llm.Result{Text: f.text, Model: "test/model"}, nil
}

func windowOf(events ...Event) *Window {
	var w *Window
	for _, e := range events {
		w = w.Add(e, e.At)
	}
	return w
}

func TestWindowGroupsAndSummary(t *testing.T) {
	w := windowOf(
		alert("KubePodCrashLooping", "firing", "a", "sthings-app", "shop", "warning", t0),
		alert("KubeDeploymentReplicasMismatch", "firing", "b", "sthings-app", "shop", "warning", t0.Add(2*time.Minute)),
		alert("TargetDown", "firing", "c", "sthings-app", "shop", "error", t0.Add(4*time.Minute)),
		alert("KubePodCrashLooping", "firing", "a", "sthings-app", "shop", "warning", t0.Add(5*time.Minute)), // same key again
		alert("NodeClockNotSynchronising", "firing", "d", "sthings-infra", "", "warning", t0.Add(6*time.Minute)),
	)
	if len(w.Events) != 4 {
		t.Fatalf("events = %d, want 4 distinct keys", len(w.Events))
	}
	gs := w.Groups()
	if len(gs) != 2 || len(gs[0].Events) != 3 || len(gs[1].Events) != 1 {
		t.Fatalf("groups = %+v", gs)
	}
	if w.Due(t0.Add(10*time.Minute), WindowOptions{}) {
		t.Error("due after 4 quiet minutes")
	}
	if !w.Due(t0.Add(11*time.Minute+time.Second), WindowOptions{}) {
		t.Error("not due after 5 quiet minutes")
	}

	ai := &fakeAI{text: "G1: Wahrscheinlich startet der Shop-Pod nicht; zuerst die Logs von shop pruefen.\nG2: Das sind 17 Pods."}
	s := &Summarizer{AI: ai}
	msgs := s.Summarize(context.Background(), w)
	if ai.calls != 1 || len(msgs) != 1 {
		t.Fatalf("calls=%d msgs=%d: one call per window, one message per group of >= 2 keys", ai.calls, len(msgs))
	}
	m := msgs[0]
	if m.Title != "[AI] Incident on sthings-app/shop: 3 alerts within 4m" || m.System != "ai-incident" || m.Severity != "info" {
		t.Errorf("title = %q", m.Title)
	}
	if !strings.Contains(m.Message, "Probable common cause (AI, model test/model): Wahrscheinlich startet der Shop-Pod nicht") {
		t.Errorf("body = %s", m.Message)
	}
	for _, line := range []string{"KubePodCrashLooping (warning) on sthings-app/shop", "TargetDown (error)"} {
		if !strings.Contains(m.Message, line) {
			t.Errorf("body misses %q:\n%s", line, m.Message)
		}
	}
	if strings.Contains(ai.input, "NodeClockNotSynchronising") {
		t.Error("single-key group sent to the model")
	}
}

func TestSummaryDropsInventedNumbersAndFailsOpen(t *testing.T) {
	w := windowOf(
		alert("A", "firing", "a", "c1", "ns", "error", t0),
		alert("B", "firing", "b", "c1", "ns", "error", t0.Add(time.Minute)),
	)
	// The only line carries a number that is not in the input.
	msgs := (&Summarizer{AI: &fakeAI{text: "G1: Seit 37 Minuten ist der Knoten voll."}}).Summarize(context.Background(), w)
	if len(msgs) != 1 || strings.Contains(msgs[0].Message, "37") || !strings.Contains(msgs[0].Message, "[AI n/a] no usable line") {
		t.Errorf("invented number: %+v", msgs)
	}
	// Model down: the deterministic summary goes out anyway.
	msgs = (&Summarizer{AI: &fakeAI{err: errors.New("openrouter: HTTP 503")}}).Summarize(context.Background(), w)
	if len(msgs) != 1 || !strings.Contains(msgs[0].Message, "[AI n/a] openrouter: HTTP 503") || !strings.Contains(msgs[0].Message, "- 08:00 A (error) on c1/ns") {
		t.Errorf("model down: %+v", msgs)
	}
	// No model configured: deterministic, no note.
	msgs = (&Summarizer{}).Summarize(context.Background(), w)
	if len(msgs) != 1 || strings.Contains(msgs[0].Message, "AI n/a") || strings.Contains(msgs[0].Message, "Probable") {
		t.Errorf("no model: %+v", msgs)
	}
}

type fakeSource struct {
	batches [][]Event
	acked   []string
}

func (f *fakeSource) Read(context.Context, int, time.Duration, time.Time) ([]Event, error) {
	if len(f.batches) == 0 {
		return nil, nil
	}
	b := f.batches[0]
	f.batches = f.batches[1:]
	return b, nil
}

func (f *fakeSource) Ack(_ context.Context, e Event) error {
	f.acked = append(f.acked, e.EntryID)
	return nil
}

type recorder struct {
	sent []pitch.Message
	fail bool
}

func (r *recorder) Pitch(_ context.Context, m pitch.Message) (string, error) {
	if r.fail {
		return "", errors.New("omni-pitcher down")
	}
	r.sent = append(r.sent, m)
	return "id", nil
}

func TestRunnerEndToEnd(t *testing.T) {
	ctx := context.Background()
	now := t0
	store := &MemStore{}
	own := NewEvent("messages", "own-1", "o", Message{Title: "[AI] X: still firing", System: "ai-incident", Severity: "info"}, t0)
	info := NewEvent("messages", "info-1", "o", Message{Title: "PR merged", System: "homerun2-git-pitcher", Severity: "info"}, t0)
	src := &fakeSource{batches: [][]Event{
		{
			alert("A", "firing", "a", "c1", "ns", "error", t0), alert("B", "firing", "b", "c1", "ns", "error", t0),
			own, info, {EntryID: "gone-1", Stream: "messages"},
		},
		{alert("A", "firing", "a", "c1", "ns", "error", t0.Add(4*time.Hour))},
	}}
	rec := &recorder{}
	r := &Runner{
		Source: src, Pitcher: rec,
		Correlator: &Correlator{Store: store},
		Summarizer: &Summarizer{},
		Filter:     Filter{ExcludeSystems: []string{"ai-*"}, Severities: []string{"error", "critical"}},
		Now:        func() time.Time { return now },
	}
	if _, err := r.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if len(src.acked) != 5 || len(rec.sent) != 0 {
		t.Fatalf("acked=%v sent=%v: everything acked, nothing to say yet", src.acked, rec.sent)
	}
	if st, _ := store.Get(ctx, own.Key()); st != nil {
		t.Error("own output was correlated")
	}
	if w, _ := store.Window(ctx); w == nil || len(w.Events) != 2 {
		t.Fatalf("window = %+v", w)
	}

	// Four hours later A fires again: one "still firing" message; and the
	// window, quiet since, closes with one incident for A and B.
	now = t0.Add(4 * time.Hour)
	if _, err := r.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if len(rec.sent) != 2 || rec.sent[0].Title != "[AI] A: still firing since 2026-11-20 08:00, never resolved" ||
		rec.sent[1].Title != "[AI] Incident on c1/ns: 2 alerts within 1m" {
		t.Fatalf("sent = %+v", rec.sent)
	}
	if w, _ := store.Window(ctx); w != nil {
		t.Error("window not closed")
	}
}

func TestRunnerPitchFailureRetriesOnNextOccurrence(t *testing.T) {
	ctx := context.Background()
	store := &MemStore{}
	src := &fakeSource{batches: [][]Event{
		{alert("A", "firing", "a", "c1", "", "error", t0)},
		{alert("A", "firing", "a", "c1", "", "error", t0.Add(4*time.Hour))},
		{alert("A", "firing", "a", "c1", "", "error", t0.Add(5*time.Hour))},
	}}
	rec := &recorder{}
	now := t0
	r := &Runner{Source: src, Pitcher: rec, Correlator: &Correlator{Store: store}, Summarizer: &Summarizer{},
		Filter: Filter{ExcludeSystems: []string{"ai-*"}}, Now: func() time.Time { return now }}
	_, _ = r.Step(ctx)
	rec.fail = true
	now = t0.Add(4 * time.Hour)
	_, _ = r.Step(ctx)
	if len(src.acked) != 2 {
		t.Errorf("acked = %v: fail-open, acked anyway", src.acked)
	}
	rec.fail = false
	now = t0.Add(5 * time.Hour)
	_, _ = r.Step(ctx)
	if len(rec.sent) != 1 {
		t.Errorf("sent = %d: the failed message is not under cooldown", len(rec.sent))
	}
}

func TestRunnerWindowSummaryAndOnce(t *testing.T) {
	ctx := context.Background()
	store := &MemStore{}
	src := &fakeSource{batches: [][]Event{{
		alert("A", "firing", "a", "c1", "ns", "error", t0), alert("B", "firing", "b", "c1", "ns", "error", t0.Add(time.Minute)),
	}}}
	rec := &recorder{}
	now := t0
	r := &Runner{Source: src, Pitcher: rec, Correlator: &Correlator{Store: store}, Summarizer: &Summarizer{},
		Filter: Filter{ExcludeSystems: []string{"ai-*"}},
		Now: func() time.Time {
			now = now.Add(time.Minute) // every call moves the clock: the quiet time passes
			return now
		}}
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx, true) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("once did not return")
	}
	if len(rec.sent) != 1 || !strings.HasPrefix(rec.sent[0].Title, "[AI] Incident on c1/ns: 2 alerts") {
		t.Errorf("sent = %+v", rec.sent)
	}
	if w, _ := store.Window(ctx); w != nil {
		t.Error("window still open")
	}
}

func TestPrune(t *testing.T) {
	ctx := context.Background()
	store := &MemStore{}
	_ = store.Put(ctx, &State{Key: "old", LastSeen: t0, Resolved: true, ResolvedAt: t0})
	_ = store.Put(ctx, &State{Key: "new", LastSeen: t0.Add(7 * 24 * time.Hour)})
	r := &Runner{Correlator: &Correlator{Store: store}, Now: func() time.Time { return t0.Add(8 * 24 * time.Hour) }}
	if err := r.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	all, _ := store.All(ctx)
	if len(all) != 1 || all[0].Key != "new" {
		t.Errorf("after prune: %+v", all)
	}
}

// slowAI waits for its context: the window's budget must end the call.
type slowAI struct{ deadline time.Time }

func (s *slowAI) Complete(ctx context.Context, _, _ string) (*llm.Result, error) {
	s.deadline, _ = ctx.Deadline()
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestSummaryAIBudget(t *testing.T) {
	w := windowOf(
		alert("A", "firing", "a", "c1", "ns", "error", t0),
		alert("B", "firing", "b", "c1", "ns", "error", t0.Add(time.Minute)),
	)
	ai := &slowAI{}
	start := time.Now()
	msgs := (&Summarizer{AI: ai, Opt: WindowOptions{AIBudget: 50 * time.Millisecond}}).Summarize(context.Background(), w)
	if time.Since(start) > 2*time.Second || ai.deadline.IsZero() {
		t.Fatalf("budget not applied (deadline %v)", ai.deadline)
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0].Message, "[AI n/a]") || !strings.Contains(msgs[0].Message, "- 08:00 A") {
		t.Errorf("fail-open after the budget: %+v", msgs)
	}
}
