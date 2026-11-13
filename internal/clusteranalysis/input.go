// Package clusteranalysis turns the archived K8sGPT findings of all clusters
// into one report (homerun2-ai-pitcher#12): a deterministic condensation of
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

// Finding is one open K8sGPT finding of one cluster.
type Finding struct {
	Fingerprint string   `json:"fp"`
	Cluster     string   `json:"cluster"`
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Parent      string   `json:"parent,omitempty"`
	Errors      []string `json:"errors"`
	// FirstSeen is the first day in the window with the finding in a
	// snapshot; DaysPresent how many days of the window had it.
	FirstSeen   string `json:"firstSeen"`
	DaysPresent int    `json:"daysPresent"`
	// Recurring: gone on a day in between, back now.
	Recurring bool `json:"recurring,omitempty"`
	// New since the previous report.
	New bool `json:"new,omitempty"`
}

// ClusterState is the latest snapshot of one cluster.
type ClusterState struct {
	Cluster    string    `json:"cluster"`
	SnapshotAt time.Time `json:"snapshotAt"`
	Stale      bool      `json:"stale,omitempty"`
	Open       int       `json:"open"`
	Findings   []Finding `json:"findings"`
	Omitted    int       `json:"omitted,omitempty"`
}

// CrossCluster is the same error on several clusters.
type CrossCluster struct {
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
}

// Ref names a finding without its details.
type Ref struct {
	Fingerprint string `json:"fp"`
	Cluster     string `json:"cluster"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
}

// Options of the condensation.
type Options struct {
	SystemPrefix        string        // archive key prefix of a cluster: system=<SystemPrefix><cluster>/
	LookbackDays        int           // days of snapshots read per cluster
	StaleAfter          time.Duration // a newer snapshot is expected every hour
	MaxFindingsPerClu   int           // findings passed on per cluster
	MaxGetsPerDay       int           // objects read per cluster and day to find its last snapshot
	MaxErrorChars       int
	MaxErrorsPerFinding int
}

// snapshot is the archived message of a snapshot collector.
type snapshot struct {
	At    time.Time
	Items []snapshotItem
}

type snapshotItem struct {
	Fingerprint string `json:"fingerprint"`
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
	return snapshot{At: at, Items: r.Message.Payload.Items}, true
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

// Clusters lists the clusters with K8sGPT findings in the archive.
func Clusters(ctx context.Context, r Reader, systemPrefix string) ([]string, error) {
	prefixes, err := r.Prefixes(ctx, "")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range prefixes {
		p = strings.TrimSuffix(p, "/")
		if c, ok := strings.CutPrefix(p, "system="+systemPrefix); ok && c != "" {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Build condenses the archive into the Input: per cluster the latest
// snapshot, each finding with first day seen, days present and recurrence
// from one snapshot per day of the window, and new/resolved against the
// previous report's open findings.
func Build(ctx context.Context, r Reader, now time.Time, previous *Report, o Options) (Input, error) {
	in := Input{GeneratedAt: now.UTC(), LookbackDays: o.LookbackDays}
	clusters, err := Clusters(ctx, r, o.SystemPrefix)
	if err != nil {
		return in, err
	}
	prevOpen := map[string]Ref{}
	if previous != nil {
		for _, f := range previous.OpenFindings {
			prevOpen[f.Fingerprint] = f
		}
		in.PreviousReport = previous.Summary
	}

	var days []string // oldest first
	for d := o.LookbackDays - 1; d >= 0; d-- {
		days = append(days, now.UTC().AddDate(0, 0, -d).Format("2006-01-02"))
	}

	open := map[string]bool{}
	for _, c := range clusters {
		perDay := map[string]map[string]bool{} // day -> fingerprints
		var latest snapshot
		found := false
		for i := len(days) - 1; i >= 0; i-- {
			prefix := fmt.Sprintf("system=%s%s/date=%s/", o.SystemPrefix, c, days[i])
			s, ok, err := lastSnapshotOfDay(ctx, r, prefix, o.MaxGetsPerDay)
			if err != nil {
				return in, fmt.Errorf("cluster %s, %s: %w", c, days[i], err)
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
			slog.Warn("no snapshot in the window", "cluster", c, "days", o.LookbackDays)
			continue
		}
		st := ClusterState{Cluster: c, SnapshotAt: latest.At, Stale: now.Sub(latest.At) > o.StaleAfter, Open: len(latest.Items)}
		for _, it := range latest.Items {
			f := Finding{
				Fingerprint: it.Fingerprint, Cluster: c,
				Kind: it.Object.Spec.Kind, Name: it.Object.Spec.Name, Parent: it.Object.Spec.ParentObject,
			}
			for _, e := range it.Object.Spec.Error {
				if len(f.Errors) == o.MaxErrorsPerFinding {
					break
				}
				f.Errors = append(f.Errors, clip(e.Text, o.MaxErrorChars))
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
		return in.Resolved[a].Cluster+in.Resolved[a].Name < in.Resolved[b].Cluster+in.Resolved[b].Name
	})
	in.CrossCluster = crossCluster(in.Clusters)
	return in, nil
}

// crossCluster groups the open findings by kind and first error text and
// keeps the groups seen on two clusters or more.
func crossCluster(states []ClusterState) []CrossCluster {
	groups := map[string]map[string]bool{}
	kinds := map[string]CrossCluster{}
	for _, st := range states {
		for _, f := range st.Findings {
			if len(f.Errors) == 0 {
				continue
			}
			k := f.Kind + "\x00" + f.Errors[0]
			if groups[k] == nil {
				groups[k] = map[string]bool{}
				kinds[k] = CrossCluster{Kind: f.Kind, Error: f.Errors[0]}
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
