// Package activity turns scout's messages of a window into what happened:
// pull requests, releases, pushes, failed workflows and issues per repository
// (from homerun2-git-pitcher), and outages (an error or critical message and
// the later message that cleared it).
//
// The counts are computed here, not by the model: the ground rule "the
// numbers are scout's, never the model's" (stuttgart-things/homerun-library#135)
// holds for these numbers too. The model only gets titles to name things.
package activity

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/stuttgart-things/homerun2-ai-pitcher/internal/sources"
)

// GitSystem is the system homerun2-git-pitcher pitches as.
const GitSystem = "homerun2-git-pitcher"

// Repo is one repository's git activity.
type Repo struct {
	Repo string `json:"repo"`
	// Pull requests by state change in the window.
	Merged         int `json:"prsMerged,omitempty"`
	Opened         int `json:"prsOpened,omitempty"`
	ClosedUnmerged int `json:"prsClosedUnmerged,omitempty"`
	// MergedTitles names the merged pull requests, at most maxTitles.
	MergedTitles []string `json:"mergedTitles,omitempty"`
	Releases     []string `json:"releases,omitempty"`
	Pushes       int      `json:"pushes,omitempty"`
	Commits      int      `json:"commits,omitempty"`
	// FailedWorkflows counts failed runs per workflow name.
	FailedWorkflows map[string]int `json:"failedWorkflows,omitempty"`
	IssuesOpened    int            `json:"issuesOpened,omitempty"`
	IssuesClosed    int            `json:"issuesClosed,omitempty"`
	OpenedIssues    []string       `json:"openedIssues,omitempty"`
}

// Outage is one error or critical condition in the window.
type Outage struct {
	System string    `json:"system"`
	Title  string    `json:"title"`
	Start  time.Time `json:"start"`
	// End is when a later, non-alert message with the same title cleared
	// it; nil while it is ongoing.
	End     *time.Time `json:"end,omitempty"`
	Minutes int        `json:"minutes,omitempty"`
	Ongoing bool       `json:"ongoing"`
	// Alerts counts the error/critical messages of the outage.
	Alerts int `json:"alerts"`
}

// Totals are the window's counts over all repositories.
type Totals struct {
	PRsMerged       int `json:"prsMerged"`
	PRsOpened       int `json:"prsOpened"`
	Releases        int `json:"releases"`
	Commits         int `json:"commits"`
	FailedWorkflows int `json:"failedWorkflows"`
	IssuesOpened    int `json:"issuesOpened"`
	IssuesClosed    int `json:"issuesClosed"`
	Outages         int `json:"outages"`
	OngoingOutages  int `json:"ongoingOutages"`
}

// Activity is what happened in the window.
type Activity struct {
	Totals  Totals   `json:"totals"`
	Repos   []Repo   `json:"repos,omitempty"`
	Outages []Outage `json:"outages,omitempty"`
	// Incomplete says why the lists may miss messages (scout's limit or
	// retention); empty when they are complete.
	Incomplete string `json:"incomplete,omitempty"`
}

var (
	// "PR #12: title (merged) on owner/repo", "Issue #3: title (opened) on owner/repo"
	stateRe    = regexp.MustCompile(`^(?:PR|Issue) #\d+(?:: (.*))? \((\w+)\) on (\S+)$`)
	releaseRe  = regexp.MustCompile(`^Release (\S+) on (\S+)$`)
	workflowRe = regexp.MustCompile(`^Workflow (.*) (\w+) on (\S+)$`)
	commitsRe  = regexp.MustCompile(`^(\d+) commit\(s\) pushed`)
	// "[FIRING:1] ", "[RESOLVED] ", "[Alerting] " in front of alert titles
	alertPrefixRe = regexp.MustCompile(`^(\s*\[[^\]]*\]\s*)+`)
)

// Git counts homerun2-git-pitcher's messages per repository. maxTitles caps
// the titles kept per list.
func Git(msgs []sources.Entry, maxTitles int) ([]Repo, Totals) {
	repos := map[string]*Repo{}
	repo := func(name string) *Repo {
		r, ok := repos[name]
		if !ok {
			r = &Repo{Repo: name}
			repos[name] = r
		}
		return r
	}
	add := func(list []string, s string) []string {
		if len(list) < maxTitles {
			return append(list, clip(s, 100))
		}
		return list
	}

	var t Totals
	for _, m := range msgs {
		if m.System != GitSystem {
			continue
		}
		switch {
		case hasTag(m, "pull_request"):
			g := stateRe.FindStringSubmatch(m.Title)
			if g == nil {
				continue
			}
			r := repo(g[3])
			switch g[2] {
			case "merged":
				r.Merged++
				t.PRsMerged++
				if g[1] != "" {
					r.MergedTitles = add(r.MergedTitles, g[1])
				}
			case "opened", "reopened", "ready_for_review":
				r.Opened++
				t.PRsOpened++
			case "closed":
				// Older events report a merge as closed with severity success.
				if strings.EqualFold(m.Severity, "success") {
					r.Merged++
					t.PRsMerged++
					if g[1] != "" {
						r.MergedTitles = add(r.MergedTitles, g[1])
					}
				} else {
					r.ClosedUnmerged++
				}
			}
		case hasTag(m, "issues"):
			g := stateRe.FindStringSubmatch(m.Title)
			if g == nil {
				continue
			}
			r := repo(g[3])
			switch g[2] {
			case "opened", "reopened":
				r.IssuesOpened++
				t.IssuesOpened++
				if g[1] != "" {
					r.OpenedIssues = add(r.OpenedIssues, g[1])
				}
			case "closed":
				r.IssuesClosed++
				t.IssuesClosed++
			}
		case hasTag(m, "release"):
			g := releaseRe.FindStringSubmatch(m.Title)
			if g == nil {
				continue
			}
			r := repo(g[2])
			r.Releases = append(r.Releases, g[1])
			t.Releases++
		case hasTag(m, "push"):
			name := repoTag(m)
			if name == "" {
				continue
			}
			r := repo(name)
			r.Pushes++
			if g := commitsRe.FindStringSubmatch(m.Message); g != nil {
				n, _ := strconv.Atoi(g[1])
				r.Commits += n
				t.Commits += n
			}
		case hasTag(m, "workflow_run"):
			g := workflowRe.FindStringSubmatch(m.Title)
			if g == nil || g[2] != "failure" {
				continue
			}
			r := repo(g[3])
			if r.FailedWorkflows == nil {
				r.FailedWorkflows = map[string]int{}
			}
			r.FailedWorkflows[g[1]]++
			t.FailedWorkflows++
		}
	}

	out := make([]Repo, 0, len(repos))
	for _, r := range repos {
		out = append(out, *r)
	}
	// Most merged first, then most activity, then by name.
	slices.SortFunc(out, func(a, b Repo) int {
		return cmp.Or(cmp.Compare(b.Merged, a.Merged), cmp.Compare(len(b.Releases), len(a.Releases)),
			cmp.Compare(b.Commits, a.Commits), strings.Compare(a.Repo, b.Repo))
	})
	return out, t
}

// Outages pairs error and critical messages with the later message of the
// same system and title that is not an alert, oldest first. msgs must be
// oldest first, as scout returns them. Alert prefixes like "[FIRING:1]" and
// "[RESOLVED]" are ignored when titles are compared.
func Outages(msgs []sources.Entry) []Outage {
	type key struct{ system, title string }
	open := map[key]*Outage{}
	var out []*Outage
	for _, m := range msgs {
		k := key{m.System, normTitle(m.Title)}
		if isAlert(m.Severity) {
			if o, ok := open[k]; ok {
				o.Alerts++
				continue
			}
			o := &Outage{System: m.System, Title: clip(alertPrefixRe.ReplaceAllString(m.Title, ""), 100), Start: m.Timestamp, Ongoing: true, Alerts: 1}
			open[k] = o
			out = append(out, o)
			continue
		}
		if o, ok := open[k]; ok {
			end := m.Timestamp
			o.End, o.Ongoing = &end, false
			o.Minutes = int(end.Sub(o.Start).Round(time.Minute) / time.Minute)
			delete(open, k)
		}
	}
	res := make([]Outage, 0, len(out))
	for _, o := range out {
		res = append(res, *o)
	}
	return res
}

// WithoutTitles returns a copy without the pull request and issue titles:
// text written by others, whose numbers must not count as facts.
func (a Activity) WithoutTitles() Activity {
	repos := make([]Repo, len(a.Repos))
	for i, r := range a.Repos {
		r.MergedTitles, r.OpenedIssues = nil, nil
		repos[i] = r
	}
	a.Repos = repos
	return a
}

// Lines renders the activity as plain text for the pitched message, under
// scout's digest: the same counts the model was given, so the reader can
// check the AI text against them.
func (a Activity) Lines() []string {
	t := a.Totals
	var lines []string
	if t.PRsMerged+t.PRsOpened+t.Releases+t.Commits+t.FailedWorkflows+t.IssuesOpened+t.IssuesClosed > 0 {
		lines = append(lines, fmt.Sprintf("Git: %d PRs merged, %d opened, %d releases, %d commits, %d failed workflow runs, %d issues opened, %d closed",
			t.PRsMerged, t.PRsOpened, t.Releases, t.Commits, t.FailedWorkflows, t.IssuesOpened, t.IssuesClosed))
	}
	var rel []string
	for _, r := range a.Repos {
		for _, v := range r.Releases {
			rel = append(rel, shortRepo(r.Repo)+" "+v)
		}
	}
	if len(rel) > 0 {
		lines = append(lines, "Releases: "+strings.Join(rel, ", "))
	}
	for _, o := range a.Outages {
		state := fmt.Sprintf("%d min", o.Minutes)
		if o.Ongoing {
			state = "ongoing"
		}
		lines = append(lines, fmt.Sprintf("Outage: %s - %s (since %s, %s)", o.System, o.Title, o.Start.UTC().Format("2006-01-02 15:04Z"), state))
	}
	if a.Incomplete != "" {
		lines = append(lines, "Incomplete: "+a.Incomplete)
	}
	return lines
}

func hasTag(m sources.Entry, tag string) bool { return slices.Contains(m.Tags, tag) }

// repoTag returns the owner/repo tag git-pitcher sets.
func repoTag(m sources.Entry) string {
	for _, t := range m.Tags {
		if strings.Contains(t, "/") {
			return t
		}
	}
	return ""
}

func isAlert(severity string) bool {
	s := strings.ToLower(severity)
	return s == "error" || s == "critical"
}

func normTitle(t string) string {
	return strings.ToLower(strings.TrimSpace(alertPrefixRe.ReplaceAllString(t, "")))
}

func shortRepo(r string) string {
	if i := strings.LastIndex(r, "/"); i >= 0 {
		return r[i+1:]
	}
	return r
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
