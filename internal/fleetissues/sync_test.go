package fleetissues

import (
	"context"
	"strings"
	"testing"
	"time"

	ca "github.com/stuttgart-things/homerun2-ai-pitcher/internal/clusteranalysis"
)

type fake struct {
	issues   []Issue
	created  []Issue
	comments map[int][]string
	labels   map[int][]string
	removed  map[int][]string
	bodies   map[int]string
	closed   []int
}

func newFake(issues ...Issue) *fake {
	return &fake{issues: issues, comments: map[int][]string{}, labels: map[int][]string{}, removed: map[int][]string{}, bodies: map[int]string{}}
}

func (f *fake) ListOpen(_ context.Context, label string) ([]Issue, error) {
	var out []Issue
	for _, i := range f.issues {
		if i.HasLabel(label) {
			out = append(out, i)
		}
	}
	return out, nil
}

func (f *fake) Create(_ context.Context, title, body string, labels []string) (Issue, error) {
	i := Issue{Number: 100 + len(f.created), Title: title, Body: body, Labels: labels, URL: "https://github.com/x/y/issues/1"}
	f.created = append(f.created, i)
	return i, nil
}
func (f *fake) Comment(_ context.Context, n int, body string) error {
	f.comments[n] = append(f.comments[n], body)
	return nil
}
func (f *fake) SetBody(_ context.Context, n int, body string) error { f.bodies[n] = body; return nil }
func (f *fake) AddLabel(_ context.Context, n int, l string) error {
	f.labels[n] = append(f.labels[n], l)
	return nil
}
func (f *fake) RemoveLabel(_ context.Context, n int, l string) error {
	f.removed[n] = append(f.removed[n], l)
	return nil
}
func (f *fake) Close(_ context.Context, n int) error { f.closed = append(f.closed, n); return nil }

var now = time.Date(2026, 11, 15, 7, 30, 0, 0, time.UTC)

func mkFinding(fp, kind, name, sev string, isNew bool) ca.Finding {
	return ca.Finding{Fingerprint: fp, Kind: kind, Name: name, Severity: sev, New: isNew, Errors: []string{kind + " " + name + " broken"}}
}

// input: vm/sthings-app-2 with a full disk (new, error), six LabDA VMs with
// NTP (warning), platform-sthings a K8sGPT warning.
func input() (*ca.Report, ca.Input) {
	in := ca.Input{Clusters: []ca.ClusterState{
		{Source: "vm", Cluster: "sthings-app-2", Findings: []ca.Finding{
			mkFinding("fp-disk", "Filesystem", "/", "error", true), mkFinding("fp-ntp2", "TimeSync", "clock", "warning", true)}},
		{Source: "vm", Cluster: "sthings-app", Findings: []ca.Finding{mkFinding("fp-ntp1", "TimeSync", "clock", "warning", true)}},
		{Source: "k8sgpt", Cluster: "platform-sthings", Findings: []ca.Finding{mkFinding("fp-svc", "Service", "backstage/rag", "warning", false)}},
	}}
	for i := range in.Clusters {
		for j := range in.Clusters[i].Findings {
			in.Clusters[i].Findings[j].Source = in.Clusters[i].Source
			in.Clusters[i].Findings[j].Cluster = in.Clusters[i].Cluster
		}
	}
	rep := &ca.Report{RunID: "20261115T073000Z", Topics: []ca.Topic{
		{Title: "NTP auf LabDA-VMs nicht synchron", Fingerprints: []string{"fp-ntp1", "fp-ntp2"}, Confidence: "high", Action: "issue", Assessment: "Zeitquelle fehlt.", Proposal: "chrony prüfen"},
		{Title: "Service ohne Endpoints", Fingerprints: []string{"fp-svc"}, Confidence: "low", Action: "issue"},
		{Title: "Rauschen", Fingerprints: []string{"fp-svc"}, Confidence: "high", Action: "none"},
		{Title: "Paketquelle per Git", Fingerprints: []string{"fp-svc"}, Confidence: "medium", Action: "pr-proposal"},
	}}
	return rep, in
}

func cfg(mode string) Config {
	return Config{Mode: mode, ReportURL: "https://portal/archive/?tab=analyses", Now: func() time.Time { return now }}
}

func TestCreateCandidates(t *testing.T) {
	f := newFake()
	rep, in := input()
	acts, err := (&Syncer{Client: f, Cfg: cfg(ModeOn)}).Sync(context.Background(), rep, in)
	if err != nil {
		t.Fatal(err)
	}
	// NTP topic (issue, high), the pr-proposal topic + the uncovered new
	// error (disk); not the low-confidence topic, not the action none topic.
	if len(f.created) != 3 || len(acts) != 3 {
		t.Fatalf("created %d acts %+v", len(f.created), acts)
	}
	ntp, pr, disk := f.created[0], f.created[1], f.created[2]
	if strings.Join(pr.Labels, ",") != "area/kubernetes,fleet-analysis,fleet-analysis/pr-proposal,severity/warning" {
		t.Errorf("pr-proposal labels %v", pr.Labels)
	}
	if ntp.Title != "NTP auf LabDA-VMs nicht synchron" || !strings.Contains(ntp.Body, "<!-- fleet-fp: fp-ntp1,fp-ntp2 -->") ||
		!strings.Contains(ntp.Body, "<!-- fleet-sys: vm/sthings-app,vm/sthings-app-2 -->") || !strings.Contains(ntp.Body, "chrony prüfen") {
		t.Errorf("ntp issue %+v", ntp)
	}
	if strings.Join(ntp.Labels, ",") != "area/vm,fleet-analysis,severity/warning" {
		t.Errorf("labels %v", ntp.Labels)
	}
	if !strings.HasPrefix(disk.Title, "Filesystem / auf sthings-app-2") || strings.Join(disk.Labels, ",") != "area/vm,fleet-analysis,severity/error" {
		t.Errorf("disk issue %+v", disk)
	}
	if acts[0].Action != "created" || acts[0].Number != 100 {
		t.Errorf("acts %+v", acts)
	}
}

// A topic with a new severe finding is filed even when the model rated it
// low or proposed no issue.
func TestNewSevereOverridesModel(t *testing.T) {
	f := newFake()
	rep, in := input()
	rep.Topics = append(rep.Topics, ca.Topic{Title: "Platte voll", Fingerprints: []string{"fp-disk"}, Confidence: "low", Action: "none"})
	if _, err := (&Syncer{Client: f, Cfg: cfg(ModeOn)}).Sync(context.Background(), rep, in); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 3 || f.created[2].Title != "Platte voll" {
		t.Errorf("created %+v", f.created)
	}
}

func TestDedupeAndThrottle(t *testing.T) {
	old := Issue{Number: 7, Title: "NTP", Labels: []string{"fleet-analysis"}, UpdatedAt: now.Add(-24 * time.Hour),
		Body: "x\n<!-- fleet-fp: fp-ntp1,fp-ntp2 -->\n<!-- fleet-sys: vm/sthings-app,vm/sthings-app-2 -->\n"}
	f := newFake(old)
	rep, in := input()
	acts, err := (&Syncer{Client: f, Cfg: cfg(ModeOn)}).Sync(context.Background(), rep, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 2 || len(f.comments[7]) != 0 || acts[0].Action != "skipped" {
		t.Errorf("no second issue, no comment within 7d: created %d comments %v acts %+v", len(f.created), f.comments, acts)
	}
	// A week later: one comment.
	f.issues[0].UpdatedAt = now.Add(-8 * 24 * time.Hour)
	f.created = nil
	if _, err := (&Syncer{Client: f, Cfg: cfg(ModeOn)}).Sync(context.Background(), rep, in); err != nil {
		t.Fatal(err)
	}
	if len(f.comments[7]) != 1 || !strings.Contains(f.comments[7][0], "Besteht weiter") {
		t.Errorf("comments %v", f.comments)
	}
}

// A new VM with the same problem extends the issue at once.
func TestGrownIssue(t *testing.T) {
	old := Issue{Number: 7, Title: "NTP", Labels: []string{"fleet-analysis"}, UpdatedAt: now.Add(-time.Hour),
		Body: "x\n<!-- fleet-fp: fp-ntp1 -->\n<!-- fleet-sys: vm/sthings-app -->\n"}
	f := newFake(old)
	rep, in := input()
	if _, err := (&Syncer{Client: f, Cfg: cfg(ModeOn)}).Sync(context.Background(), rep, in); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.bodies[7], "<!-- fleet-fp: fp-ntp1,fp-ntp2 -->") || len(f.comments[7]) != 1 {
		t.Errorf("body %q comments %v", f.bodies[7], f.comments)
	}
}

func TestResolvedAndReopen(t *testing.T) {
	gone := Issue{Number: 8, Title: "Alt", Labels: []string{"fleet-analysis"},
		Body: "<!-- fleet-fp: fp-old -->\n<!-- fleet-sys: k8sgpt/platform-sthings -->"}
	staleSys := Issue{Number: 9, Title: "Ohne Daten", Labels: []string{"fleet-analysis"},
		Body: "<!-- fleet-fp: fp-x -->\n<!-- fleet-sys: vm/expired-vm -->"}
	notOurs := Issue{Number: 10, Title: "Hand", Labels: []string{"fleet-analysis"}, Body: "kein Marker"}
	back := Issue{Number: 11, Title: "NTP", Labels: []string{"fleet-analysis", "fleet-analysis/resolved"},
		Body: "<!-- fleet-fp: fp-ntp1 -->\n<!-- fleet-sys: vm/sthings-app -->"}
	f := newFake(gone, staleSys, notOurs, back)
	rep, in := input()
	c := cfg(ModeOn)
	c.CloseResolved = true
	acts, err := (&Syncer{Client: f, Cfg: c}).Sync(context.Background(), rep, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.labels[8]) != 1 || f.labels[8][0] != "fleet-analysis/resolved" || len(f.closed) != 1 || f.closed[0] != 8 {
		t.Errorf("resolved: labels %v closed %v", f.labels, f.closed)
	}
	if len(f.comments[9]) != 0 || len(f.comments[10]) != 0 {
		t.Error("an issue of a system without data or without markers was touched")
	}
	if len(f.removed[11]) != 1 || !strings.Contains(f.comments[11][0], "Wieder aufgetreten") {
		t.Errorf("reopen: removed %v comments %v", f.removed, f.comments[11])
	}
	var got []string
	for _, a := range acts {
		got = append(got, a.Action)
	}
	if strings.Join(got, ",") != "reopened,created,created,resolved" {
		t.Errorf("actions %v", got)
	}
}

func TestDryRunAndLimit(t *testing.T) {
	f := newFake()
	rep, in := input()
	c := cfg(ModeDryRun)
	c.MaxPerRun = 1
	acts, err := (&Syncer{Client: f, Cfg: c}).Sync(context.Background(), rep, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 0 || len(acts) != 3 || acts[0].Action != "would-create" || acts[1].Action != "skipped" {
		t.Errorf("acts %+v created %d", acts, len(f.created))
	}
	// Dry run without a token: no client at all.
	if _, err := (&Syncer{Cfg: cfg(ModeDryRun)}).Sync(context.Background(), rep, in); err != nil {
		t.Fatal(err)
	}
	if acts, _ := (&Syncer{Client: f, Cfg: cfg(ModeOff)}).Sync(context.Background(), rep, in); acts != nil {
		t.Error("mode off acted")
	}
	if _, err := (&Syncer{Cfg: cfg(ModeOn)}).Sync(context.Background(), rep, in); err == nil {
		t.Error("mode on without a client must fail")
	}
}

// Two topics on the same findings file one issue.
func TestOneIssuePerFindingsPerRun(t *testing.T) {
	f := newFake()
	rep, in := input()
	rep.Topics = append(rep.Topics, ca.Topic{Title: "NTP nochmal", Fingerprints: []string{"fp-ntp2"}, Confidence: "high", Action: "issue"})
	acts, err := (&Syncer{Client: f, Cfg: cfg(ModeOn)}).Sync(context.Background(), rep, in)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range f.created {
		if c.Title == "NTP nochmal" {
			t.Errorf("duplicate issue: %+v", acts)
		}
	}
}
