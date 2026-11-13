package clusteranalysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stuttgart-things/homerun2-ai-pitcher/internal/archive"
	"github.com/stuttgart-things/homerun2-ai-pitcher/internal/claudecode"
)

type memStore struct{ objs map[string]memObj }

type memObj struct {
	data []byte
	at   time.Time
}

func newStore() *memStore { return &memStore{objs: map[string]memObj{}} }

func (m *memStore) Prefixes(_ context.Context, prefix string) ([]string, error) {
	seen := map[string]bool{}
	for k := range m.objs {
		if rest, ok := strings.CutPrefix(k, prefix); ok {
			if i := strings.Index(rest, "/"); i >= 0 {
				seen[prefix+rest[:i+1]] = true
			}
		}
	}
	var out []string
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

func (m *memStore) List(_ context.Context, prefix string) ([]archive.Object, error) {
	var out []archive.Object
	for k, o := range m.objs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, archive.Object{Key: k, Size: int64(len(o.data)), LastModified: o.at})
		}
	}
	return out, nil
}

func (m *memStore) Get(_ context.Context, key string) ([]byte, error) {
	o, ok := m.objs[key]
	if !ok {
		return nil, errors.New("NoSuchKey")
	}
	return o.data, nil
}

func (m *memStore) Put(_ context.Context, key, _ string, body []byte) error {
	m.objs[key] = memObj{data: body, at: time.Now()}
	return nil
}

var now = time.Date(2026, 11, 13, 7, 30, 0, 0, time.UTC)

type item struct{ fp, kind, name, err string }

// snap stores a snapshot of cluster c at t with the items, plus an event
// object at the same time (which Build must skip).
func snap(m *memStore, c string, t time.Time, items ...item) {
	var its []map[string]any
	for _, i := range items {
		its = append(its, map[string]any{"fingerprint": i.fp, "object": map[string]any{"spec": map[string]any{
			"kind": i.kind, "name": i.name, "error": []any{map[string]any{"text": i.err}}}}})
	}
	rec := map[string]any{"message": map[string]any{
		"title": "K8sGPT snapshot", "tags": "k8s,Result,snapshot,", "timestamp_unix": t.Unix(),
		"payload": map[string]any{"count": len(its), "items": its}}}
	data, _ := json.Marshal(rec)
	day := t.Format("2006-01-02")
	m.objs[fmt.Sprintf("system=k8sgpt-%s/date=%s/snap-%d.json", c, day, t.Unix())] = memObj{data, t}
	ev, _ := json.Marshal(map[string]any{"message": map[string]any{"title": "Pod x: 1 finding", "tags": "k8s,Result,add,"}})
	m.objs[fmt.Sprintf("system=k8sgpt-%s/date=%s/event-%d.json", c, day, t.Unix())] = memObj{ev, t.Add(time.Second)}
}

var opts = Options{SystemPrefix: "k8sgpt-", LookbackDays: 7, StaleAfter: 3 * time.Hour, MaxFindingsPerClu: 50, MaxGetsPerDay: 50, MaxErrorChars: 400, MaxErrorsPerFinding: 3}

func day(d int) time.Time { return now.AddDate(0, 0, -d).Add(-time.Hour) }

func fixture() *memStore {
	m := newStore()
	svc := item{"fp-svc-a", "Service", "default/web", "Service has no endpoints"}
	svcB := item{"fp-svc-b", "Service", "default/web", "Service has no endpoints"}
	pod := item{"fp-pod", "Pod", "default/api-0", "Back-off restarting failed container"}
	// cluster a: svc every day; pod on day 3, gone on day 2, back today (recurring).
	for d := 6; d >= 0; d-- {
		items := []item{svc}
		if d == 3 || d == 0 {
			items = append(items, pod)
		}
		snap(m, "a", day(d), items...)
	}
	// cluster b: same Service error, only today; its last snapshot is old (stale).
	snap(m, "b", now.Add(-5*time.Hour), svcB)
	// not a K8sGPT system: ignored
	m.objs["system=flux/date=2026-11-13/x.json"] = memObj{[]byte(`{}`), now}
	return m
}

func TestBuild(t *testing.T) {
	prev := &Report{Summary: "gestern", OpenFindings: []Ref{
		{Fingerprint: "fp-svc-a", Cluster: "a", Kind: "Service", Name: "default/web"},
		{Fingerprint: "fp-gone", Cluster: "a", Kind: "Pod", Name: "default/old"},
	}}
	in, err := Build(context.Background(), fixture(), now, prev, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Clusters) != 2 {
		t.Fatalf("clusters %d", len(in.Clusters))
	}
	a, b := in.Clusters[0], in.Clusters[1]
	if a.Cluster != "a" || a.Open != 2 || a.Stale {
		t.Errorf("a: %+v", a)
	}
	if !b.Stale || b.Open != 1 {
		t.Errorf("b should be stale with 1 open: %+v", b)
	}
	byFP := map[string]Finding{}
	for _, f := range append(a.Findings, b.Findings...) {
		byFP[f.Fingerprint] = f
	}
	if f := byFP["fp-pod"]; !f.Recurring || !f.New || f.DaysPresent != 2 || f.FirstSeen != day(3).Format("2006-01-02") {
		t.Errorf("pod: %+v", f)
	}
	if f := byFP["fp-svc-a"]; f.New || f.Recurring || f.DaysPresent != 7 {
		t.Errorf("svc a: %+v", f)
	}
	if len(in.Resolved) != 1 || in.Resolved[0].Fingerprint != "fp-gone" {
		t.Errorf("resolved %+v", in.Resolved)
	}
	if len(in.CrossCluster) != 1 || strings.Join(in.CrossCluster[0].Clusters, ",") != "a,b" {
		t.Errorf("cross-cluster %+v", in.CrossCluster)
	}
	if in.PreviousReport != "gestern" {
		t.Errorf("previous %q", in.PreviousReport)
	}
}

// Without a previous report nothing is "new": there is no baseline.
func TestBuildFirstRun(t *testing.T) {
	in, err := Build(context.Background(), fixture(), now, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range in.Clusters {
		for _, f := range c.Findings {
			if f.New {
				t.Errorf("first run marks %s new", f.Fingerprint)
			}
		}
	}
}

func TestReportNumbersAndApply(t *testing.T) {
	in, _ := Build(context.Background(), fixture(), now, &Report{}, opts)
	r := NewReport(in, "run1")
	if r.Stats.Open != 3 || r.Stats.New != 3 || r.Stats.Recurring != 1 || len(r.Stats.Stale) != 1 || r.Severity != "warning" {
		t.Errorf("stats %+v severity %s", r.Stats, r.Severity)
	}
	dropped := r.Apply(Assessment{Summary: " Lage ", Topics: []Topic{
		{Title: "Service ohne Endpoints", Fingerprints: []string{"fp-svc-a", "fp-svc-b", "invented"}, Clusters: []string{"a", "b", "zz"}, Confidence: "high", Action: "issue"},
		{Title: "nur erfunden", Fingerprints: []string{"x"}},
	}}, in)
	if dropped != 2 || len(r.Topics) != 1 || len(r.Topics[0].Fingerprints) != 2 || strings.Join(r.Topics[0].Clusters, ",") != "a,b" || r.Summary != "Lage" {
		t.Errorf("dropped %d topics %+v summary %q", dropped, r.Topics, r.Summary)
	}
	md := r.Markdown()
	for _, want := range []string{"**3 offene Funde** auf 2 Clustern", "Keine aktuellen Snapshots** von: b", "### 1. Service ohne Endpoints", "| a | 2 |"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q", want)
		}
	}
}

func TestSeverity(t *testing.T) {
	for s, want := range map[*Stats]string{
		{Open: 0}:                       "success",
		{Open: 4}:                       "info",
		{Open: 4, New: 1}:               "warning",
		{Open: 0, Stale: []string{"x"}}: "warning",
	} {
		if got := Severity(*s); got != want {
			t.Errorf("%+v: %s, want %s", *s, got, want)
		}
	}
}

type fakeAI struct {
	out   string
	err   error
	calls int
}

func (f *fakeAI) Run(context.Context, string, string, json.RawMessage) (*claudecode.Result, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &claudecode.Result{Output: json.RawMessage(f.out), Model: "claude-sonnet-5-5", CostUSD: 0.12}, nil
}

func TestRunStoresReportAndBaseline(t *testing.T) {
	arch, reports := fixture(), newStore()
	ai := &fakeAI{out: `{"summary":"Zwei Cluster, ein gemeinsames Problem.","topics":[{"title":"Service ohne Endpoints","fingerprints":["fp-svc-a","fp-svc-b"],"clusters":["a","b"],"assessment":"Selector passt nicht.","proposal":"kubectl get endpoints","confidence":"medium","action":"issue"}]}`}
	res, err := Run(context.Background(), arch, reports, ai, now, Config{Options: opts, Attempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res.JSONKey != "analysis/date=2026-11-13/20261113T073000Z.json" {
		t.Errorf("key %s", res.JSONKey)
	}
	if _, ok := reports.objs[res.MDKey]; !ok {
		t.Error("markdown not stored")
	}
	if res.Report.Model != "claude-sonnet-5-5" || len(res.Report.Topics) != 1 {
		t.Errorf("report %+v", res.Report)
	}

	// The next run reads it as the baseline: nothing new, nothing resolved.
	res2, err := Run(context.Background(), arch, reports, ai, now.Add(24*time.Hour-time.Hour), Config{Options: opts, Attempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Report.Stats.New != 0 {
		t.Errorf("second run: %d new", res2.Report.Stats.New)
	}
}

// The model failing leaves a report with the numbers and the error.
func TestRunModelFails(t *testing.T) {
	ai := &fakeAI{err: errors.New("rate limited")}
	res, err := Run(context.Background(), fixture(), newStore(), ai, now, Config{Options: opts, Attempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	if ai.calls != 2 || res.Report.AssessmentError == "" || res.Report.Stats.Open != 3 || len(res.Report.Topics) != 0 {
		t.Errorf("calls %d report %+v", ai.calls, res.Report)
	}
	if !strings.Contains(res.Report.Markdown(), "Keine KI-Einschätzung: rate limited") {
		t.Error("markdown does not say the assessment is missing")
	}
}

func TestDryRunStoresNothing(t *testing.T) {
	reports := newStore()
	if _, err := Run(context.Background(), fixture(), reports, &fakeAI{out: `{"summary":"s","topics":[]}`}, now, Config{Options: opts, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if len(reports.objs) != 0 {
		t.Errorf("dry run stored %d objects", len(reports.objs))
	}
}

func TestSchemaIsJSON(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal(AssessmentSchema, &v); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(SystemPrompt("German"), "in German") {
		t.Error("language not in the system prompt")
	}
}
