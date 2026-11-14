package clusteranalysis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/stuttgart-things/homerun2-ai-pitcher/internal/archive"
	"github.com/stuttgart-things/homerun2-ai-pitcher/internal/claudecode"
)

// ReportStore is the analysis bucket.
type ReportStore interface {
	List(ctx context.Context, prefix string) ([]archive.Object, error)
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key, contentType string, body []byte) error
}

// AI answers one prompt with a structured output.
type AI interface {
	Run(ctx context.Context, systemPrompt, prompt string, schema json.RawMessage) (*claudecode.Result, error)
}

// Config of one run.
type Config struct {
	Options
	Language string // of summary, assessments and proposals
	DryRun   bool   // build and print, store nothing
	Attempts int    // model attempts
}

// Result of one run.
type Result struct {
	Report  *Report
	Input   Input
	JSONKey string
	MDKey   string
}

// Run builds the input from the archive, asks the model, and stores the
// report (unless DryRun). The model failing is not fatal: the report then
// carries the numbers and the error.
func Run(ctx context.Context, archiveR Reader, reports ReportStore, ai AI, now time.Time, cfg Config) (*Result, error) {
	prev, err := Previous(ctx, reports)
	if err != nil {
		slog.Warn("previous report unreadable, new/resolved not computed", "error", err)
	}
	in, err := Build(ctx, archiveR, now, prev, cfg.Options)
	if err != nil {
		return nil, fmt.Errorf("build input: %w", err)
	}
	runID := now.UTC().Format("20060102T150405Z")
	rep := NewReport(in, runID)

	prompt, err := UserPrompt(in)
	if err != nil {
		return nil, err
	}
	var aerr error
	for attempt := 1; attempt <= max(cfg.Attempts, 1); attempt++ {
		var res *claudecode.Result
		res, aerr = ai.Run(ctx, SystemPrompt(cfg.Language), prompt, AssessmentSchema)
		if aerr == nil {
			var a Assessment
			if aerr = json.Unmarshal(res.Output, &a); aerr == nil {
				if dropped := rep.Apply(a, in); dropped > 0 {
					slog.Warn("model named unknown fingerprints, dropped", "count", dropped)
				}
				rep.Model, rep.CostUSD = res.Model, res.CostUSD
				break
			}
		}
		slog.Warn("model attempt failed", "attempt", attempt, "error", aerr)
	}
	if aerr != nil {
		rep.AssessmentError = aerr.Error()
	}

	day := now.UTC().Format("2006-01-02")
	out := &Result{Report: rep, Input: in,
		JSONKey: fmt.Sprintf("analysis/date=%s/%s.json", day, runID),
		MDKey:   fmt.Sprintf("analysis/date=%s/%s.md", day, runID)}
	if cfg.DryRun {
		return out, nil
	}
	body, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := reports.Put(ctx, out.JSONKey, "application/json", body); err != nil {
		return nil, err
	}
	if err := reports.Put(ctx, out.MDKey, "text/markdown; charset=utf-8", []byte(rep.Markdown())); err != nil {
		return nil, err
	}
	return out, nil
}

// Previous returns the newest report, nil if there is none. Keys sort by
// date and run ID, so the last .json is the newest.
func Previous(ctx context.Context, reports ReportStore) (*Report, error) {
	objs, err := reports.List(ctx, "analysis/")
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, o := range objs {
		if strings.HasSuffix(o.Key, ".json") {
			keys = append(keys, o.Key)
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	sort.Strings(keys)
	data, err := reports.Get(ctx, keys[len(keys)-1])
	if err != nil {
		return nil, err
	}
	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", keys[len(keys)-1], err)
	}
	return &r, nil
}

// SystemPrompt sets the role and the rules of the assessment.
func SystemPrompt(language string) string {
	if language == "" {
		language = "German"
	}
	return `You assess the open findings of a fleet for the platform team, once a day.
Sources: "k8sgpt" (K8sGPT analyzers per Kubernetes cluster), "endpoints" (HTTP heartbeat and TLS certificate probes per cluster), "vm" (health report per virtual machine: updates, disks, systemd units, journal errors, time sync, certificates).
The input is a JSON condensation of the latest snapshot per source and target ("cluster" is the cluster or the host): each finding with a fingerprint (fp), source, kind, name, parent, severity, the error texts, the first day seen in the window, the days present, whether it recurred, and whether it is new since the previous report; plus the findings resolved since then and errors seen on several targets of one source.
"versions" (if present) is context from each cluster's inventory, not findings: Kubernetes and kubelet versions, images without a pinned tag, the same image or chart in different versions across clusters, releases that are not ready. Use it to explain findings or to name version risks in the summary or in a topic; a topic on versions alone needs no fingerprints from it -- name a related finding's fingerprint if there is one, otherwise leave it out of the topics and mention it in the summary.

Rules:
- Group findings into topics: one underlying problem, possibly on several targets or sources (a certificate about to expire may show up as an endpoint and a VM finding). Prefer cross-target and recurring problems, findings with severity error or critical, and new ones; skip noise that needs no attention.
- Call a finding new only if its "new" flag is true. Without a previous report (previousReport empty) nothing is new: do not write "new since the last report" then.
- Name only fingerprints and targets from the input. Never invent findings, objects, clusters or hosts.
- assessment: what the problem most likely is and why it matters, grounded in the error texts. Say when the evidence is thin.
- proposal: a concrete next step a human can take (a command to check, a likely fix, a filter for known noise). You do not act; nothing you write is executed.
- confidence: how sure you are about the assessment. action: "issue" for something to track, "pr-proposal" when a config change in git is the obvious fix, "none" otherwise.
- summary: 3 to 6 sentences for the team: the state of the fleet per area (Kubernetes, endpoints, VMs) that has data, what is new, what matters most.
- Do not rate the report's severity; it is computed from the numbers.
- Write summary, titles, assessments and proposals in ` + language + `.`
}

// UserPrompt is the input as JSON with one line of instruction.
func UserPrompt(in Input) (string, error) {
	data, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	return "Assess these findings and answer with the structured output.\n\n" + string(data), nil
}
