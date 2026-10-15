package activity

import (
	"strings"
	"testing"
	"time"

	"github.com/stuttgart-things/homerun2-ai-pitcher/internal/sources"
)

var t0 = time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC)

func msg(min int, system, severity, title, body string, tags ...string) sources.Entry {
	return sources.Entry{Timestamp: t0.Add(time.Duration(min) * time.Minute), System: system, Severity: severity, Title: title, Message: body, Tags: tags}
}

func git(min int, severity, title, body, kind, repo string) sources.Entry {
	return msg(min, GitSystem, severity, title, body, "github", kind, repo)
}

func TestGit(t *testing.T) {
	msgs := []sources.Entry{
		git(1, "info", "PR #1: Add messages (opened) on o/scout", "", "pull_request", "o/scout"),
		git(2, "success", "PR #1: Add messages (merged) on o/scout", "", "pull_request", "o/scout"),
		git(3, "success", "PR #2: Retention (merged) on o/platform", "", "pull_request", "o/platform"),
		git(4, "success", "PR #3: Old style merge (closed) on o/scout", "", "pull_request", "o/scout"),
		git(5, "warning", "PR #4: Abandoned (closed) on o/scout", "", "pull_request", "o/scout"),
		git(6, "success", "PR #5 (merged) on o/scout", "", "pull_request", "o/scout"), // slim payload, no title
		git(7, "success", "Release v0.12.0 on o/scout", "v0.12.0\nnotes", "release", "o/scout"),
		git(8, "info", "Push to main on o/scout", "3 commit(s) pushed by pat: feat: x", "push", "o/scout"),
		git(9, "info", "Push to main on o/scout", "2 commit(s) pushed by pat", "push", "o/scout"),
		git(10, "error", "Workflow Build & Test failure on o/scout", "", "workflow_run", "o/scout"),
		git(11, "success", "Workflow Build & Test success on o/scout", "", "workflow_run", "o/scout"),
		git(12, "error", "Workflow Build & Test failure on o/scout", "", "workflow_run", "o/scout"),
		git(13, "info", "Issue #9: Ticker too long (opened) on o/scout", "", "issues", "o/scout"),
		git(14, "success", "Issue #8: Old bug (closed) on o/scout", "", "issues", "o/scout"),
		git(15, "info", "Comment on #7: report on o/scout", "", "issue_comment", "o/scout"),
		git(16, "info", "something unparsable", "", "pull_request", "o/scout"),
		msg(17, "msteams", "success", "PR #99: not git-pitcher (merged) on o/x", "", "pull_request", "o/x"),
	}
	repos, tot := Git(msgs, 2)

	want := Totals{PRsMerged: 4, PRsOpened: 1, Releases: 1, Commits: 5, FailedWorkflows: 2, IssuesOpened: 1, IssuesClosed: 1}
	if tot != want {
		t.Errorf("totals = %+v\nwant     %+v", tot, want)
	}
	if len(repos) != 2 || repos[0].Repo != "o/scout" || repos[1].Repo != "o/platform" {
		t.Fatalf("repos = %+v", repos)
	}
	s := repos[0]
	if s.Merged != 3 || s.Opened != 1 || s.ClosedUnmerged != 1 || s.Pushes != 2 || s.Commits != 5 ||
		strings.Join(s.MergedTitles, "|") != "Add messages|Old style merge" || // capped at 2
		strings.Join(s.Releases, ",") != "v0.12.0" || s.FailedWorkflows["Build & Test"] != 2 ||
		s.IssuesOpened != 1 || s.IssuesClosed != 1 || strings.Join(s.OpenedIssues, ",") != "Ticker too long" {
		t.Errorf("scout = %+v", s)
	}
}

func TestOutages(t *testing.T) {
	msgs := []sources.Entry{
		msg(0, "grafana", "ERROR", "[FIRING:1] Vault unreachable", ""),
		msg(5, "grafana", "error", "[FIRING:1] Vault unreachable", ""),
		msg(10, "grafana", "info", "[FIRING:1] Disk 80%", ""), // not an alert, nothing open
		msg(42, "grafana", "success", "[RESOLVED] Vault unreachable", ""),
		msg(50, "kubernetes", "critical", "Node w1 NotReady", ""),
		msg(60, "grafana", "error", "[FIRING:1] Vault unreachable", ""), // again, a new outage
		msg(61, "msteams", "success", "Node w1 NotReady", ""),          // other system does not clear it
	}
	out := Outages(msgs)
	if len(out) != 3 {
		t.Fatalf("outages = %+v", out)
	}
	v := out[0]
	if v.System != "grafana" || v.Title != "Vault unreachable" || v.Ongoing || v.End == nil || v.Minutes != 42 || v.Alerts != 2 {
		t.Errorf("vault = %+v", v)
	}
	if n := out[1]; n.System != "kubernetes" || !n.Ongoing || n.End != nil || n.Alerts != 1 {
		t.Errorf("node = %+v", n)
	}
	if again := out[2]; !again.Ongoing || !again.Start.Equal(t0.Add(time.Hour)) {
		t.Errorf("second vault outage = %+v", again)
	}
}

func TestLines(t *testing.T) {
	end := t0.Add(42 * time.Minute)
	a := Activity{
		Totals: Totals{PRsMerged: 4, Releases: 2, Commits: 5},
		Repos: []Repo{
			{Repo: "o/scout", Releases: []string{"v0.12.0"}},
			{Repo: "o/ai-pitcher", Releases: []string{"v0.3.0"}},
		},
		Outages: []Outage{
			{System: "grafana", Title: "Vault unreachable", Start: t0, End: &end, Minutes: 42},
			{System: "kubernetes", Title: "Node w1 NotReady", Start: t0, Ongoing: true},
		},
		Incomplete: "outages: only the newest 500 of 800 messages",
	}
	got := strings.Join(a.Lines(), "\n")
	for _, want := range []string{
		"Git: 4 PRs merged, 0 opened, 2 releases, 5 commits",
		"Releases: scout v0.12.0, ai-pitcher v0.3.0",
		"Outage: grafana - Vault unreachable (since 2026-10-14 08:00Z, 42 min)",
		"Outage: kubernetes - Node w1 NotReady (since 2026-10-14 08:00Z, ongoing)",
		"Incomplete: outages: only the newest 500 of 800 messages",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lines lack %q:\n%s", want, got)
		}
	}
	if lines := (Activity{}).Lines(); len(lines) != 0 {
		t.Errorf("empty activity renders %q", lines)
	}
}

func TestWithoutTitles(t *testing.T) {
	a := Activity{Repos: []Repo{{Repo: "o/r", Merged: 1, MergedTitles: []string{"write 999"}, OpenedIssues: []string{"x"}, Releases: []string{"v1"}}}}
	b := a.WithoutTitles()
	if b.Repos[0].MergedTitles != nil || b.Repos[0].OpenedIssues != nil || b.Repos[0].Merged != 1 || b.Repos[0].Releases[0] != "v1" {
		t.Errorf("without titles = %+v", b.Repos[0])
	}
	if a.Repos[0].MergedTitles == nil {
		t.Error("the original lost its titles")
	}
}
