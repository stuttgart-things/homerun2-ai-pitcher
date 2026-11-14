package clusteranalysis

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Assessment is the part of the report the model writes, validated by the
// CLI against AssessmentSchema and here against the input.
type Assessment struct {
	Summary string  `json:"summary"`
	Topics  []Topic `json:"topics"`
}

// Topic is one theme across findings: a problem, its targets (clusters,
// hosts), a proposal.
type Topic struct {
	Title        string   `json:"title"`
	Fingerprints []string `json:"fingerprints"`
	Clusters     []string `json:"clusters"`
	Assessment   string   `json:"assessment"`
	Proposal     string   `json:"proposal"`
	Confidence   string   `json:"confidence"` // low, medium, high
	Action       string   `json:"action"`     // none, issue, pr-proposal
}

// MaxTopics bounds the topics of one report.
const MaxTopics = 10

// AssessmentSchema is passed to `claude -p --json-schema`.
var AssessmentSchema = json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["summary", "topics"],
  "properties": {
    "summary": {"type": "string", "minLength": 1},
    "topics": {
      "type": "array",
      "maxItems": %d,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["title", "fingerprints", "clusters", "assessment", "proposal", "confidence", "action"],
        "properties": {
          "title": {"type": "string", "minLength": 1},
          "fingerprints": {"type": "array", "items": {"type": "string"}},
          "clusters": {"type": "array", "items": {"type": "string"}},
          "assessment": {"type": "string"},
          "proposal": {"type": "string"},
          "confidence": {"type": "string", "enum": ["low", "medium", "high"]},
          "action": {"type": "string", "enum": ["none", "issue", "pr-proposal"]}
        }
      }
    }
  }
}`, MaxTopics))

// Stats are counted from the input, never by the model.
type Stats struct {
	Clusters int `json:"clusters"` // targets: clusters per source, hosts
	Open     int `json:"open"`
	New      int `json:"new"`
	// NewErrors are new findings with severity error or critical.
	NewErrors int      `json:"newErrors,omitempty"`
	Resolved  int      `json:"resolved"`
	Recurring int      `json:"recurring"`
	Stale     []string `json:"stale,omitempty"`
}

// ClusterLine is one target of one source in the report.
type ClusterLine struct {
	Source     string    `json:"source,omitempty"`
	Cluster    string    `json:"cluster"`
	SnapshotAt time.Time `json:"snapshotAt"`
	Open       int       `json:"open"`
	New        int       `json:"new"`
	Stale      bool      `json:"stale,omitempty"`
}

// Report is what one run stores: analysis/date=<day>/<RunID>.json (+ .md).
type Report struct {
	Version      int           `json:"version"`
	RunID        string        `json:"runId"`
	GeneratedAt  time.Time     `json:"generatedAt"`
	Model        string        `json:"model,omitempty"`
	CostUSD      float64       `json:"costUsd,omitempty"`
	LookbackDays int           `json:"lookbackDays"`
	Severity     string        `json:"severity"`
	Stats        Stats         `json:"stats"`
	Clusters     []ClusterLine `json:"clusters"`
	Summary      string        `json:"summary"`
	Topics       []Topic       `json:"topics"`
	// OpenFindings are the next run's baseline for new and resolved.
	OpenFindings []Ref `json:"openFindings"`
	// Versions is the inventory context (#21), copied from the input.
	Versions *Versions `json:"versions,omitempty"`
	Resolved []Ref     `json:"resolved,omitempty"`
	// Issues are the GitHub issues this run created, commented or would
	// have (dry run), #22.
	Issues      []IssueAction `json:"issues,omitempty"`
	IssuesError string        `json:"issuesError,omitempty"`
	// AssessmentError is set when the model failed: the report then carries
	// the numbers only.
	AssessmentError string `json:"assessmentError,omitempty"`
}

// NewReport fills everything but the model's assessment from the input.
func NewReport(in Input, runID string) *Report {
	r := &Report{Version: 1, RunID: runID, GeneratedAt: in.GeneratedAt, LookbackDays: in.LookbackDays, Resolved: in.Resolved, Versions: in.Versions}
	r.Stats.Clusters = len(in.Clusters)
	r.Stats.Resolved = len(in.Resolved)
	for _, c := range in.Clusters {
		line := ClusterLine{Source: c.Source, Cluster: c.Cluster, SnapshotAt: c.SnapshotAt, Open: c.Open, Stale: c.Stale}
		for _, f := range c.Findings {
			if f.New {
				line.New++
				if f.Severity == "error" || f.Severity == "critical" {
					r.Stats.NewErrors++
				}
			}
			if f.Recurring {
				r.Stats.Recurring++
			}
			r.OpenFindings = append(r.OpenFindings, Ref{Fingerprint: f.Fingerprint, Source: f.Source, Cluster: f.Cluster, Kind: f.Kind, Name: f.Name})
		}
		r.Stats.Open += c.Open
		r.Stats.New += line.New
		if c.Stale {
			r.Stats.Stale = append(r.Stats.Stale, c.Source+"/"+c.Cluster)
		}
		r.Clusters = append(r.Clusters, line)
	}
	r.Severity = Severity(r.Stats)
	return r
}

// Severity follows from the counts (homerun-library#135: the model never
// decides it): a new finding with severity error or critical is an error, a
// new finding or a target whose snapshots stopped a warning, no open findings
// success, else info.
func Severity(s Stats) string {
	switch {
	case s.NewErrors > 0:
		return "error"
	case s.New > 0 || len(s.Stale) > 0:
		return "warning"
	case s.Open == 0:
		return "success"
	default:
		return "info"
	}
}

// Apply validates the model's assessment against the input and adds it:
// fingerprints and clusters the input does not know are dropped (a model
// must not invent findings), topics without any known fingerprint go.
func (r *Report) Apply(a Assessment, in Input) (dropped int) {
	known := map[string]bool{}
	clusters := map[string]bool{}
	for _, c := range in.Clusters {
		clusters[c.Cluster] = true
		for _, f := range c.Findings {
			known[f.Fingerprint] = true
		}
	}
	for _, ref := range in.Resolved {
		known[ref.Fingerprint] = true
		clusters[ref.Cluster] = true
	}
	r.Summary = strings.TrimSpace(a.Summary)
	for _, t := range a.Topics {
		var fps, cs []string
		for _, fp := range t.Fingerprints {
			if known[fp] {
				fps = append(fps, fp)
			} else {
				dropped++
			}
		}
		for _, c := range t.Clusters {
			if clusters[c] {
				cs = append(cs, c)
			}
		}
		if len(fps) == 0 {
			continue
		}
		t.Fingerprints, t.Clusters = fps, cs
		r.Topics = append(r.Topics, t)
		if len(r.Topics) == MaxTopics {
			break
		}
	}
	return dropped
}

// IssueAction is what happened to one tracking issue.
type IssueAction struct {
	Action string `json:"action"` // created, commented, reopened, resolved, would-create, would-comment, would-resolve, skipped
	Title  string `json:"title"`
	Number int    `json:"number,omitempty"`
	URL    string `json:"url,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Title of the run's pitch.
func (r *Report) Title() string {
	return fmt.Sprintf("[AI fleet analysis] %d open %s on %d %s (%d new, %d resolved)",
		r.Stats.Open, plural(r.Stats.Open, "finding", "findings"), r.Stats.Clusters, plural(r.Stats.Clusters, "system", "systems"),
		r.Stats.New, r.Stats.Resolved)
}

// Markdown renders the report for humans (homerun2-s3-catcher's Analyses tab).
func (r *Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Fleet-Analyse %s\n\n", r.GeneratedAt.Format("2006-01-02 15:04 UTC"))
	fmt.Fprintf(&b, "**%d %s** auf %d %s · %d neu · %d behoben · %d wiederkehrend · Schweregrad `%s`\n\n",
		r.Stats.Open, plural(r.Stats.Open, "offener Fund", "offene Funde"), r.Stats.Clusters, plural(r.Stats.Clusters, "System", "Systemen"),
		r.Stats.New, r.Stats.Resolved, r.Stats.Recurring, r.Severity)
	if len(r.Stats.Stale) > 0 {
		fmt.Fprintf(&b, "> **Keine aktuellen Snapshots** von: %s — dort ist der Stand veraltet.\n\n", strings.Join(r.Stats.Stale, ", "))
	}
	if r.Summary != "" {
		fmt.Fprintf(&b, "## Zusammenfassung\n\n%s\n\n", r.Summary)
	}
	if r.AssessmentError != "" {
		fmt.Fprintf(&b, "> Keine KI-Einschätzung: %s\n\n", r.AssessmentError)
	}
	if len(r.Topics) > 0 {
		b.WriteString("## Themen\n\n")
		for i, t := range r.Topics {
			fmt.Fprintf(&b, "### %d. %s\n\n", i+1, t.Title)
			fmt.Fprintf(&b, "- **Systeme:** %s\n- **Funde:** %s\n- **Konfidenz:** %s · **Vorschlag als:** %s\n\n",
				strings.Join(t.Clusters, ", "), code(t.Fingerprints), t.Confidence, t.Action)
			if t.Assessment != "" {
				fmt.Fprintf(&b, "%s\n\n", t.Assessment)
			}
			if t.Proposal != "" {
				fmt.Fprintf(&b, "**Vorschlag:** %s\n\n", t.Proposal)
			}
		}
	}
	b.WriteString("## Systeme\n\n| Bereich | System | Offen | Neu | Snapshot |\n|---|---|---:|---:|---|\n")
	lines := append([]ClusterLine(nil), r.Clusters...)
	sort.SliceStable(lines, func(a, c int) bool {
		if area(lines[a].Source) != area(lines[c].Source) {
			return area(lines[a].Source) < area(lines[c].Source)
		}
		return lines[a].Open > lines[c].Open
	})
	for _, c := range lines {
		stale := ""
		if c.Stale {
			stale = " ⚠ veraltet"
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %s%s |\n", area(c.Source), c.Cluster, c.Open, c.New, c.SnapshotAt.Format("2006-01-02 15:04"), stale)
	}
	if r.Versions != nil {
		r.Versions.markdown(&b)
	}
	if len(r.Resolved) > 0 {
		b.WriteString("\n## Behoben seit dem letzten Bericht\n\n")
		for _, f := range r.Resolved {
			fmt.Fprintf(&b, "- %s: %s %s (%s) `%s`\n", area(f.Source), f.Kind, f.Name, f.Cluster, f.Fingerprint)
		}
	}
	if len(r.Issues) > 0 || r.IssuesError != "" {
		b.WriteString("\n## GitHub-Issues\n\n")
		for _, i := range r.Issues {
			ref := i.Title
			if i.URL != "" {
				ref = fmt.Sprintf("[#%d %s](%s)", i.Number, i.Title, i.URL)
			}
			fmt.Fprintf(&b, "- `%s` %s", i.Action, ref)
			if i.Reason != "" {
				fmt.Fprintf(&b, " — %s", i.Reason)
			}
			b.WriteString("\n")
		}
		if r.IssuesError != "" {
			fmt.Fprintf(&b, "\n> Issue-Abgleich fehlgeschlagen: %s\n", r.IssuesError)
		}
	}
	fmt.Fprintf(&b, "\n---\nLauf `%s`", r.RunID)
	if r.Model != "" {
		fmt.Fprintf(&b, " · Modell `%s`", r.Model)
	}
	b.WriteString(" · Die Einschätzung ist ein Vorschlag, keine Aktion wurde ausgeführt.\n")
	return b.String()
}

// area names a source for humans.
func area(source string) string {
	switch source {
	case "", "k8sgpt":
		return "Kubernetes"
	case "endpoints":
		return "Endpunkte"
	case "vm":
		return "VMs"
	default:
		return source
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func code(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = "`" + s + "`"
	}
	return strings.Join(q, ", ")
}
