// Package clusteranalysis turns the archived findings of the fleet into one
// report (homerun2-ai-pitcher#12, #18): K8sGPT findings per cluster, endpoint
// probes, VM health reports -- every source that sends snapshots in the
// findings/v1 format (stuttgart-things#4556). A deterministic condensation of
// the snapshots, an assessment by Claude Code over all of it, and the report
// stored in S3 plus one pitch. It acts on nothing.
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
)

// Reader reads the archive bucket.
type Reader interface {
	Prefixes(ctx context.Context, prefix string) ([]string, error)
	List(ctx context.Context, prefix string) ([]archive.Object, error)
	Get(ctx context.Context, key string) ([]byte, error)
}

// Finding is one open finding of one target (a cluster, a host).
type Finding struct {
	Fingerprint string `json:"fp"`
	Source      string `json:"source"`
	// Cluster is the target: a cluster for k8sgpt and endpoints, a host for vm.
	Cluster   string   `json:"cluster"`
	Kind      string   `json:"kind"`
	Name      string   `json:"name"`
	Namespace string   `json:"namespace,omitempty"`
	Parent    string   `json:"parent,omitempty"`
	Severity  string   `json:"severity,omitempty"`
	Errors    []string `json:"errors"`
	// FirstSeen is the first day in the window with the finding in a
	// snapshot; DaysPresent how many days of the window had it.
	FirstSeen   string `json:"firstSeen"`
	DaysPresent int    `json:"daysPresent"`
	// Recurring: gone on a day in between, back now.
	Recurring bool `json:"recurring,omitempty"`
	// New since the previous report.
	New bool `json:"new,omitempty"`
}

// ClusterState is the latest snapshot of one target of one source.
type ClusterState struct {
	Source     string    `json:"source"`
	Cluster    string    `json:"cluster"`
	SnapshotAt time.Time `json:"snapshotAt"`
	Stale      bool      `json:"stale,omitempty"`
	Open       int       `json:"open"`
	Findings   []Finding `json:"findings"`
	Omitted    int       `json:"omitted,omitempty"`
}

// CrossCluster is the same error on several targets of one source.
type CrossCluster struct {
	Source   string   `json:"source"`
	Kind     string   `json:"kind"`
	Error    string   `json:"error"`
	Clusters []string `json:"clusters"`
}

// Input is what the model gets, and what the report's numbers come from.
type Input struct {
	GeneratedAt    time.Time      `json:"generatedAt"`
	LookbackDays   int            `json:"lookbackDays"`
	Clusters       []ClusterState `json:"clusters"`
	Resolved       []Ref          `json:"resolvedSinceLastReport"`
	CrossCluster   []CrossCluster `json:"crossCluster,omitempty"`
	PreviousReport string         `json:"previousReport,omitempty"` // its summary
	// Versions is the clusters' inventory as context (#21), nil without one.
	Versions *Versions `json:"versions,omitempty"`
}

// Ref names a finding without its details.
type Ref struct {
	Fingerprint string `json:"fp"`
	Source      string `json:"source,omitempty"` // empty in reports before #18: k8sgpt
	Cluster     string `json:"cluster"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
}

// Source is one kind of system in the archive: system=<Prefix><target>/.
type Source struct {
	Name   string // k8sgpt, endpoints, vm
	Prefix string // k8sgpt-
}

// DefaultSources are the sources of stuttgart-things#4556.
var DefaultSources = []Source{{"k8sgpt", "k8sgpt-"}, {"endpoints", "endpoints-"}, {"vm", "vm-"}}

// ParseSources reads "k8sgpt,endpoints,vm" (prefix <name>-) or
// "name=prefix" entries.
func ParseSources(s string) ([]Source, error) {
	var out []Source
	for _, e := range strings.Split(s, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		name, prefix, ok := strings.Cut(e, "=")
		if !ok {
			prefix = name + "-"
		}
		if name == "" || prefix == "" {
			return nil, fmt.Errorf("invalid source %q", e)
		}
		out = append(out, Source{Name: name, Prefix: prefix})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no sources in %q", s)
	}
	return out, nil
}

// Options of the condensation.
type Options struct {
	Sources             []Source      // DefaultSources when empty
	LookbackDays        int           // days of snapshots read per cluster
	InventoryPrefix     string        // system=<InventoryPrefix><cluster>/ of the inventory (#21); empty: none
	InventoryStaleAfter time.Duration // the inventory comes daily
	StaleAfter          time.Duration // a newer snapshot is expected every hour
	MaxFindingsPerClu   int           // findings passed on per target
	MaxGetsPerDay       int           // objects read per target and day to find its last snapshot
	MaxErrorChars       int
	MaxErrorsPerFinding int
}

// snapshot is the archived message of a snapshot collector.
type snapshot struct {
	At    time.Time
	Items []snapshotItem
}

// snapshotItem is one finding: findings/v1 fields, or a K8sGPT Result CR in
// object (the k8s-pitcher snapshot before findings/v1).
type snapshotItem struct {
	Fingerprint string   `json:"fingerprint"`
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Namespace   string   `json:"namespace"`
	Parent      string   `json:"parent"`
	Severity    string   `json:"severity"`
	Texts       []string `json:"texts"`
	Object      struct {
		Spec struct {
			Kind         string `json:"kind"`
			Name         string `json:"name"`
			ParentObject string `json:"parentObject"`
			Error        []struct {
				Text string `json:"text"`
			} `json:"error"`
		} `json:"spec"`
	} `json:"object"`
}

// normalize maps a K8sGPT Result item onto the findings/v1 fields.
func (it snapshotItem) normalize() snapshotItem {
	if it.Kind != "" || it.Object.Spec.Kind == "" {
		return it
	}
	it.Kind, it.Name, it.Parent, it.Severity = it.Object.Spec.Kind, it.Object.Spec.Name, it.Object.Spec.ParentObject, "warning"
	for _, e := range it.Object.Spec.Error {
		it.Texts = append(it.Texts, e.Text)
	}
	return it
}

// record is the archive envelope (homerun2-s3-catcher).
type record struct {
	Message struct {
		Title         string `json:"title"`
		Tags          string `json:"tags"`
		Timestamp     string `json:"timestamp"`
		TimestampUnix int64  `json:"timestamp_unix"`
		Payload       *struct {
			Count int            `json:"count"`
			Items []snapshotItem `json:"items"`
		} `json:"payload"`
	} `json:"message"`
}

// parseSnapshot returns the snapshot in an archived object, false for any
// other message (a finding event, a different system's message).
func parseSnapshot(data []byte) (snapshot, bool) {
	var r record
	if err := json.Unmarshal(data, &r); err != nil || r.Message.Payload == nil {
		return snapshot{}, false
	}
	if !strings.Contains(","+r.Message.Tags+",", ",snapshot,") {
		return snapshot{}, false
	}
	at := time.Unix(r.Message.TimestampUnix, 0).UTC()
	if r.Message.TimestampUnix == 0 {
		at, _ = time.Parse(time.RFC3339, r.Message.Timestamp)
	}
	items := make([]snapshotItem, 0, len(r.Message.Payload.Items))
	for _, it := range r.Message.Payload.Items {
		items = append(items, it.normalize())
	}
	return snapshot{At: at, Items: items}, true
}

// lastSnapshotOfDay reads the day's objects newest first until a snapshot.
func lastSnapshotOfDay(ctx context.Context, r Reader, prefix string, maxGets int) (snapshot, bool, error) {
	objs, err := r.List(ctx, prefix)
	if err != nil {
		return snapshot{}, false, err
	}
	sort.Slice(objs, func(a, b int) bool { return objs[a].LastModified.After(objs[b].LastModified) })
	for i, o := range objs {
		if i == maxGets {
			break
		}
		data, err := r.Get(ctx, o.Key)
		if err != nil {
			return snapshot{}, false, err
		}
		if s, ok := parseSnapshot(data); ok {
			return s, true, nil
		}
	}
	return snapshot{}, false, nil
}

// target is one system in the archive: a source and its cluster or host.
type target struct {
	Source Source
	Name   string
}

// Targets lists the systems of the sources in the archive, by source order
// and name. A system matching several prefixes belongs to the longest.
func Targets(ctx context.Context, r Reader, sources []Source) ([]target, error) {
	prefixes, err := r.Prefixes(ctx, "")
	if err != nil {
		return nil, err
	}
	var out []target
	for _, p := range prefixes {
		sys, ok := strings.CutPrefix(strings.TrimSuffix(p, "/"), "system=")
		if !ok {
			continue
		}
		best := -1
		for i, s := range sources {
			if strings.HasPrefix(sys, s.Prefix) && len(sys) > len(s.Prefix) && (best < 0 || len(s.Prefix) > len(sources[best].Prefix)) {
				best = i
			}
		}
		if best >= 0 {
			out = append(out, target{Source: sources[best], Name: strings.TrimPrefix(sys, sources[best].Prefix)})
		}
	}
	order := map[string]int{}
	for i, s := range sources {
		order[s.Name] = i
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Source.Name != out[b].Source.Name {
			return order[out[a].Source.Name] < order[out[b].Source.Name]
		}
		return out[a].Name < out[b].Name
	})
	return out, nil
}

// refSource is the source of a baseline entry; reports before #18 had k8sgpt only.
func refSource(r Ref) string {
	if r.Source == "" {
		return "k8sgpt"
	}
	return r.Source
}

// Build condenses the archive into the Input: per cluster the latest
// snapshot, each finding with first day seen, days present and recurrence
// from one snapshot per day of the window, and new/resolved against the
// previous report's open findings.
func Build(ctx context.Context, r Reader, now time.Time, previous *Report, o Options) (Input, error) {
	in := Input{GeneratedAt: now.UTC(), LookbackDays: o.LookbackDays}
	sources := o.Sources
	if len(sources) == 0 {
		sources = DefaultSources
	}
	targets, err := Targets(ctx, r, sources)
	if err != nil {
		return in, err
	}
	prevOpen := map[string]Ref{}
	if previous != nil {
		for _, f := range previous.OpenFindings {
			f.Source = refSource(f)
			prevOpen[f.Fingerprint] = f
		}
		in.PreviousReport = previous.Summary
	}

	var days []string // oldest first
	for d := o.LookbackDays - 1; d >= 0; d-- {
		days = append(days, now.UTC().AddDate(0, 0, -d).Format("2006-01-02"))
	}

	open := map[string]bool{}
	for _, t := range targets {
		c := t.Name
		perDay := map[string]map[string]bool{} // day -> fingerprints
		var latest snapshot
		found := false
		for i := len(days) - 1; i >= 0; i-- {
			prefix := fmt.Sprintf("system=%s%s/date=%s/", t.Source.Prefix, c, days[i])
			s, ok, err := lastSnapshotOfDay(ctx, r, prefix, o.MaxGetsPerDay)
			if err != nil {
				return in, fmt.Errorf("%s %s, %s: %w", t.Source.Name, c, days[i], err)
			}
			if !ok {
				continue
			}
			fps := map[string]bool{}
			for _, it := range s.Items {
				fps[it.Fingerprint] = true
			}
			perDay[days[i]] = fps
			if !found {
				latest, found = s, true
			}
		}
		if !found {
			slog.Warn("no snapshot in the window", "source", t.Source.Name, "target", c, "days", o.LookbackDays)
			continue
		}
		st := ClusterState{Source: t.Source.Name, Cluster: c, SnapshotAt: latest.At, Stale: now.Sub(latest.At) > o.StaleAfter, Open: len(latest.Items)}
		for _, it := range latest.Items {
			f := Finding{
				Fingerprint: it.Fingerprint, Source: t.Source.Name, Cluster: c,
				Kind: it.Kind, Name: it.Name, Namespace: it.Namespace, Parent: it.Parent, Severity: it.Severity,
			}
			for _, e := range it.Texts {
				if len(f.Errors) == o.MaxErrorsPerFinding {
					break
				}
				f.Errors = append(f.Errors, clip(e, o.MaxErrorChars))
			}
			seenGap := false
			for _, d := range days {
				present := perDay[d][f.Fingerprint]
				if present {
					if f.FirstSeen == "" {
						f.FirstSeen = d
					} else if seenGap {
						f.Recurring = true
					}
					f.DaysPresent++
				} else if f.FirstSeen != "" && perDay[d] != nil {
					seenGap = true // a snapshot that day, without it
				}
			}
			_, wasOpen := prevOpen[f.Fingerprint]
			f.New = previous != nil && !wasOpen
			open[f.Fingerprint] = true
			st.Findings = append(st.Findings, f)
		}
		sort.Slice(st.Findings, func(a, b int) bool {
			fa, fb := st.Findings[a], st.Findings[b]
			if fa.New != fb.New {
				return fa.New
			}
			if fa.Recurring != fb.Recurring {
				return fa.Recurring
			}
			return fa.Kind+fa.Name < fb.Kind+fb.Name
		})
		if len(st.Findings) > o.MaxFindingsPerClu {
			st.Omitted = len(st.Findings) - o.MaxFindingsPerClu
			st.Findings = st.Findings[:o.MaxFindingsPerClu]
		}
		in.Clusters = append(in.Clusters, st)
	}

	for fp, ref := range prevOpen {
		if !open[fp] {
			in.Resolved = append(in.Resolved, ref)
		}
	}
	sort.Slice(in.Resolved, func(a, b int) bool {
		ra, rb := in.Resolved[a], in.Resolved[b]
		return ra.Source+ra.Cluster+ra.Name < rb.Source+rb.Cluster+rb.Name
	})
	in.CrossCluster = crossCluster(in.Clusters)
	if in.Versions, err = BuildVersions(ctx, r, now, days, o); err != nil {
		return in, err
	}
	return in, nil
}

// crossCluster groups the open findings by source, kind and first error
// text and keeps the groups seen on two targets or more.
func crossCluster(states []ClusterState) []CrossCluster {
	groups := map[string]map[string]bool{}
	kinds := map[string]CrossCluster{}
	for _, st := range states {
		for _, f := range st.Findings {
			if len(f.Errors) == 0 {
				continue
			}
			k := st.Source + "\x00" + f.Kind + "\x00" + f.Errors[0]
			if groups[k] == nil {
				groups[k] = map[string]bool{}
				kinds[k] = CrossCluster{Source: st.Source, Kind: f.Kind, Error: f.Errors[0]}
			}
			groups[k][st.Cluster] = true
		}
	}
	var out []CrossCluster
	for k, cs := range groups {
		if len(cs) < 2 {
			continue
		}
		cc := kinds[k]
		for c := range cs {
			cc.Clusters = append(cc.Clusters, c)
		}
		sort.Strings(cc.Clusters)
		out = append(out, cc)
	}
	sort.Slice(out, func(a, b int) bool { return len(out[a].Clusters) > len(out[b].Clusters) })
	return out
}

func clip(s string, n int) string {
	if n > 0 && len(s) > n {
		return s[:n] + "…"
	}
	return s
}
