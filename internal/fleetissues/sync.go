// Package fleetissues turns the fleet analysis into GitHub issues
// (homerun2-ai-pitcher#22): one issue per topic that needs tracking,
// deduplicated by the findings' fingerprints in a hidden marker, a
// throttled comment while it stays open, a comment and a label once all of
// its findings are gone. Deterministic: the model only proposes topics.
package fleetissues

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	ca "github.com/stuttgart-things/homerun2-ai-pitcher/internal/clusteranalysis"
)

// Modes.
const (
	ModeOff    = "off"
	ModeDryRun = "dry-run"
	ModeOn     = "on"
)

// Config of the sync.
type Config struct {
	Mode          string
	Label         string        // every issue of the sync carries it; default fleet-analysis
	ResolvedLabel string        // set once all findings are gone; default fleet-analysis/resolved
	MaxPerRun     int           // new issues per run; default 5
	CommentEvery  time.Duration // at most one "still open" comment per issue; default 7 days
	CloseResolved bool
	ReportURL     string
	Now           func() time.Time
}

// Syncer implements clusteranalysis.IssueSyncer.
type Syncer struct {
	Client Client // nil in a dry run without a token: no existing issues are read
	Cfg    Config
}

var (
	markerFP  = regexp.MustCompile(`<!-- fleet-fp: ([^ ]*) -->`)
	markerSys = regexp.MustCompile(`<!-- fleet-sys: ([^ ]*) -->`)
)

func split(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// markers reads the fingerprints and systems of an issue body.
func markers(body string) (fps, systems []string) {
	if m := markerFP.FindStringSubmatch(body); m != nil {
		fps = split(m[1])
	}
	if m := markerSys.FindStringSubmatch(body); m != nil {
		systems = split(m[1])
	}
	return fps, systems
}

type finding struct {
	ca.Finding
	system string // source/target
}

type candidate struct {
	title      string
	topic      *ca.Topic
	findings   []finding
	assessment string
	proposal   string
}

func (c candidate) fps() []string {
	out := make([]string, 0, len(c.findings))
	for _, f := range c.findings {
		out = append(out, f.Fingerprint)
	}
	return out
}

func (c candidate) systems() []string {
	set := map[string]bool{}
	for _, f := range c.findings {
		set[f.system] = true
	}
	return keys(set)
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var rank = map[string]int{"critical": 4, "error": 3, "warning": 2, "info": 1}

func severe(f ca.Finding) bool { return rank[f.Severity] >= rank["error"] }

func (c candidate) worst() string {
	w := "info"
	for _, f := range c.findings {
		if rank[f.Severity] > rank[w] {
			w = f.Severity
		}
	}
	return w
}

func area(source string) string {
	switch source {
	case "endpoints":
		return "endpoints"
	case "vm":
		return "vm"
	default:
		return "kubernetes"
	}
}

func (s *Syncer) defaults() Config {
	c := s.Cfg
	if c.Label == "" {
		c.Label = "fleet-analysis"
	}
	if c.ResolvedLabel == "" {
		c.ResolvedLabel = "fleet-analysis/resolved"
	}
	if c.MaxPerRun <= 0 {
		c.MaxPerRun = 5
	}
	if c.CommentEvery <= 0 {
		c.CommentEvery = 7 * 24 * time.Hour
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// candidates are the topics to track: action issue or pr-proposal with medium or high
// confidence, or a new finding of severity error or critical; and every
// new severe finding no topic covers (also when the model failed).
func candidates(rep *ca.Report, in ca.Input) []candidate {
	open := map[string]finding{}
	for _, st := range in.Clusters {
		for _, f := range st.Findings {
			open[f.Fingerprint] = finding{Finding: f, system: st.Source + "/" + st.Cluster}
		}
	}
	covered := map[string]bool{}
	var out []candidate
	for i := range rep.Topics {
		t := &rep.Topics[i]
		c := candidate{title: t.Title, topic: t, assessment: t.Assessment, proposal: t.Proposal}
		newSevere := false
		for _, fp := range t.Fingerprints {
			if f, ok := open[fp]; ok {
				c.findings = append(c.findings, f)
				if f.New && severe(f.Finding) {
					newSevere = true
				}
			}
		}
		if len(c.findings) == 0 {
			continue
		}
		// pr-proposal: a config change in git is the fix -- tracked too,
		// labelled fleet-analysis/pr-proposal.
		tracked := (t.Action == "issue" || t.Action == "pr-proposal") && (t.Confidence == "medium" || t.Confidence == "high")
		if !tracked && !newSevere {
			continue
		}
		for _, f := range c.findings {
			covered[f.Fingerprint] = true
		}
		out = append(out, c)
	}
	var rest []finding
	for fp, f := range open {
		if f.New && severe(f.Finding) && !covered[fp] {
			rest = append(rest, f)
		}
	}
	sort.Slice(rest, func(a, b int) bool { return rest[a].system+rest[a].Name < rest[b].system+rest[b].Name })
	for _, f := range rest {
		text := ""
		if len(f.Errors) > 0 {
			text = ": " + f.Errors[0]
		}
		out = append(out, candidate{title: fmt.Sprintf("%s %s auf %s%s", f.Kind, f.Name, f.Cluster, clipTitle(text)), findings: []finding{f}})
	}
	return out
}

func clipTitle(s string) string {
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}

// Sync creates, comments and resolves issues; in a dry run it only says
// what it would do.
func (s *Syncer) Sync(ctx context.Context, rep *ca.Report, in ca.Input) ([]ca.IssueAction, error) {
	cfg := s.defaults()
	if cfg.Mode != ModeOn && cfg.Mode != ModeDryRun {
		return nil, nil
	}
	dry := cfg.Mode == ModeDryRun
	if !dry && s.Client == nil {
		return nil, fmt.Errorf("issues mode on without a GitHub client")
	}
	var existing []Issue
	if s.Client != nil {
		var err error
		if existing, err = s.Client.ListOpen(ctx, cfg.Label); err != nil {
			return nil, err
		}
	}
	now := cfg.Now()
	var acts []ca.IssueAction
	act := func(a ca.IssueAction) { acts = append(acts, a) }
	verb := func(v string) string {
		if dry {
			return "would-" + v
		}
		return v
	}

	matched := map[int]bool{}
	created := 0
	thisRun := map[string]bool{} // fingerprints of the issues filed by this run
	for _, c := range candidates(rep, in) {
		if overlaps(keys(thisRun), c.fps()) {
			act(ca.IssueAction{Action: "skipped", Title: c.title, Reason: "findings already in another issue of this run"})
			continue
		}
		for _, fp := range c.fps() {
			thisRun[fp] = true
		}
		var hit *Issue
		for i := range existing {
			fps, _ := markers(existing[i].Body)
			if overlaps(fps, c.fps()) {
				hit = &existing[i]
				break
			}
		}
		if hit == nil {
			if created == cfg.MaxPerRun {
				act(ca.IssueAction{Action: "skipped", Title: c.title, Reason: fmt.Sprintf("limit of %d new issues per run", cfg.MaxPerRun)})
				continue
			}
			created++
			a := ca.IssueAction{Action: verb("create"), Title: c.title}
			if !dry {
				iss, err := s.Client.Create(ctx, c.title, body(c, rep, cfg), labels(c, cfg))
				if err != nil {
					return acts, err
				}
				a.Action, a.Number, a.URL = "created", iss.Number, iss.URL
			}
			act(a)
			continue
		}
		matched[hit.Number] = true
		a := ca.IssueAction{Title: hit.Title, Number: hit.Number, URL: hit.URL}
		oldFPs, oldSys := markers(hit.Body)
		fps, systems := union(oldFPs, c.fps()), union(oldSys, c.systems())
		grown := len(fps) > len(oldFPs) || len(systems) > len(oldSys)
		switch {
		case hit.HasLabel(cfg.ResolvedLabel):
			a.Action = verb("reopen")
			if !dry {
				if err := s.Client.RemoveLabel(ctx, hit.Number, cfg.ResolvedLabel); err != nil {
					return acts, err
				}
				if err := s.Client.Comment(ctx, hit.Number, comment("Wieder aufgetreten", c, rep, cfg)); err != nil {
					return acts, err
				}
				a.Action = "reopened"
			}
		case grown || now.Sub(hit.UpdatedAt) >= cfg.CommentEvery:
			a.Action = verb("comment")
			if !dry {
				if err := s.Client.Comment(ctx, hit.Number, comment("Besteht weiter", c, rep, cfg)); err != nil {
					return acts, err
				}
				a.Action = "commented"
			}
		default:
			a.Action, a.Reason = "skipped", "still open, commented within "+cfg.CommentEvery.String()
		}
		if grown && !dry {
			if err := s.Client.SetBody(ctx, hit.Number, setMarkers(hit.Body, fps, systems)); err != nil {
				return acts, err
			}
		}
		act(a)
	}

	// Resolved: every fingerprint of the issue is gone while each of its
	// systems still reports (a system without snapshots resolves nothing).
	open := map[string]bool{}
	reporting := map[string]bool{}
	for _, st := range in.Clusters {
		if !st.Stale {
			reporting[st.Source+"/"+st.Cluster] = true
		}
		for _, f := range st.Findings {
			open[f.Fingerprint] = true
		}
	}
	for _, iss := range existing {
		if matched[iss.Number] || iss.HasLabel(cfg.ResolvedLabel) {
			continue
		}
		fps, systems := markers(iss.Body)
		if len(fps) == 0 || len(systems) == 0 {
			continue // not ours
		}
		gone := true
		for _, fp := range fps {
			if open[fp] {
				gone = false
			}
		}
		for _, sys := range systems {
			if !reporting[sys] {
				gone = false
			}
		}
		if !gone {
			continue
		}
		a := ca.IssueAction{Action: verb("resolve"), Title: iss.Title, Number: iss.Number, URL: iss.URL}
		if !dry {
			msg := fmt.Sprintf("Alle Funde dieses Issues sind laut Fleet-Analyse `%s` behoben.%s", rep.RunID, reportLink(cfg))
			if err := s.Client.Comment(ctx, iss.Number, msg); err != nil {
				return acts, err
			}
			if err := s.Client.AddLabel(ctx, iss.Number, cfg.ResolvedLabel); err != nil {
				return acts, err
			}
			if cfg.CloseResolved {
				if err := s.Client.Close(ctx, iss.Number); err != nil {
					return acts, err
				}
			}
			a.Action = "resolved"
		}
		act(a)
	}
	return acts, nil
}

func overlaps(a, b []string) bool {
	set := map[string]bool{}
	for _, x := range a {
		set[x] = true
	}
	for _, x := range b {
		if set[x] {
			return true
		}
	}
	return false
}

func union(a, b []string) []string {
	set := map[string]bool{}
	for _, x := range append(append([]string{}, a...), b...) {
		set[x] = true
	}
	return keys(set)
}

func setMarkers(body string, fps, systems []string) string {
	body = markerFP.ReplaceAllString(body, "<!-- fleet-fp: "+strings.Join(fps, ",")+" -->")
	return markerSys.ReplaceAllString(body, "<!-- fleet-sys: "+strings.Join(systems, ",")+" -->")
}

func labels(c candidate, cfg Config) []string {
	set := map[string]bool{cfg.Label: true, "severity/" + c.worst(): true}
	if c.topic != nil && c.topic.Action == "pr-proposal" {
		set[cfg.Label+"/pr-proposal"] = true
	}
	for _, f := range c.findings {
		set["area/"+area(f.Source)] = true
	}
	return keys(set)
}

func reportLink(cfg Config) string {
	if cfg.ReportURL == "" {
		return ""
	}
	return fmt.Sprintf(" [Bericht](%s)", cfg.ReportURL)
}

func findingsTable(b *strings.Builder, c candidate) {
	b.WriteString("| System | Art | Name | Schweregrad | Text | Fingerprint |\n|---|---|---|---|---|---|\n")
	for _, f := range c.findings {
		text := ""
		if len(f.Errors) > 0 {
			text = strings.ReplaceAll(f.Errors[0], "|", "\\|")
		}
		name := f.Name
		if f.Namespace != "" && !strings.Contains(name, "/") {
			name = f.Namespace + "/" + name
		}
		fmt.Fprintf(b, "| %s | %s | %s | %s | %s | `%s` |\n", f.system, f.Kind, name, f.Severity, text, f.Fingerprint)
	}
}

func body(c candidate, rep *ca.Report, cfg Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Aus der Fleet-Analyse `%s` (homerun2-ai-pitcher).%s\n\n", rep.RunID, reportLink(cfg))
	if c.topic != nil {
		fmt.Fprintf(&b, "**Konfidenz:** %s · **Vorschlag als:** %s\n\n", c.topic.Confidence, c.topic.Action)
	} else {
		b.WriteString("Neuer Fund mit Schweregrad error/critical, von keinem Thema der Analyse abgedeckt.\n\n")
	}
	if c.assessment != "" {
		fmt.Fprintf(&b, "## Einschätzung\n\n%s\n\n", c.assessment)
	}
	if c.proposal != "" {
		fmt.Fprintf(&b, "## Vorschlag\n\n%s\n\n", c.proposal)
	}
	b.WriteString("## Funde\n\n")
	findingsTable(&b, c)
	b.WriteString("\n---\nAutomatisch angelegt; die Einschätzung ist ein Vorschlag, es wurde nichts ausgeführt. ")
	b.WriteString("Sind alle Funde weg, kommentiert die Analyse das Issue.\n\n")
	fmt.Fprintf(&b, "<!-- fleet-fp: %s -->\n<!-- fleet-sys: %s -->\n", strings.Join(c.fps(), ","), strings.Join(c.systems(), ","))
	return b.String()
}

func comment(head string, c candidate, rep *ca.Report, cfg Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** laut Fleet-Analyse `%s`.%s\n\n", head, rep.RunID, reportLink(cfg))
	findingsTable(&b, c)
	if c.proposal != "" {
		fmt.Fprintf(&b, "\n**Vorschlag:** %s\n", c.proposal)
	}
	return b.String()
}
