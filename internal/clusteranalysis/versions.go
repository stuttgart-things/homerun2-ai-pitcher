package clusteranalysis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// Versions is the inventory of the clusters as context (#21): what runs
// where, condensed into facts in code. Not findings: they count neither as
// new nor as resolved and do not change the report's severity.
type Versions struct {
	Clusters     []ClusterVersion `json:"clusters"`
	Unpinned     []ImageUse       `json:"unpinned,omitempty"`     // :latest or untagged
	ImageDrift   []Drift          `json:"imageDrift,omitempty"`   // one repository, several tags across clusters
	ReleaseDrift []Drift          `json:"releaseDrift,omitempty"` // one chart or package, several versions across clusters
	KubeletSkew  []string         `json:"kubeletSkew,omitempty"`
	NotReady     []ReleaseState   `json:"notReady,omitempty"`
	Omitted      int              `json:"omitted,omitempty"` // entries cut from the lists above
}

// ClusterVersion is one cluster's inventory summary.
type ClusterVersion struct {
	Cluster    string    `json:"cluster"`
	Version    string    `json:"version"`
	Nodes      int       `json:"nodes"`
	Images     int       `json:"images"`
	Releases   int       `json:"releases"`
	Unpinned   int       `json:"unpinned"`
	SnapshotAt time.Time `json:"snapshotAt"`
	Stale      bool      `json:"stale,omitempty"`
}

// ImageUse is an image reference on a cluster with its first workloads.
type ImageUse struct {
	Cluster   string   `json:"cluster"`
	Image     string   `json:"image"`
	Workloads []string `json:"workloads,omitempty"`
}

// Drift is one repository, chart or package in several versions; Versions
// maps each version to the clusters running it.
type Drift struct {
	Kind     string              `json:"kind"`
	Name     string              `json:"name"`
	Versions map[string][]string `json:"versions"`
}

// ReleaseState is a release that is not ready.
type ReleaseState struct {
	Cluster string `json:"cluster"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Status  string `json:"status"`
}

// MaxVersionFacts bounds each list of Versions.
const MaxVersionFacts = 20

// inventory is the inventory/v1 payload of homerun2-k8s-pitcher#108.
type inventory struct {
	Format  string `json:"format"`
	Version string `json:"version"`
	Count   int    `json:"count"`
	Nodes   []struct {
		Name    string `json:"name"`
		Kubelet string `json:"kubelet"`
	} `json:"nodes"`
	Images []struct {
		Image     string   `json:"image"`
		Digest    string   `json:"digest"`
		Workloads []string `json:"workloads"`
		Latest    bool     `json:"latest"`
	} `json:"images"`
	Releases []struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		Chart     string `json:"chart"`
		Version   string `json:"version"`
		Status    string `json:"status"`
	} `json:"releases"`
}

type inventoryRecord struct {
	Message struct {
		Tags          string          `json:"tags"`
		Timestamp     string          `json:"timestamp"`
		TimestampUnix int64           `json:"timestamp_unix"`
		Payload       json.RawMessage `json:"payload"`
	} `json:"message"`
}

// parseInventory returns the inventory in an archived object, false for
// anything else.
func parseInventory(data []byte) (inventory, time.Time, bool) {
	var r inventoryRecord
	if err := json.Unmarshal(data, &r); err != nil || len(r.Message.Payload) == 0 {
		return inventory{}, time.Time{}, false
	}
	raw := r.Message.Payload
	if raw[0] == '"' { // a payload stored as a JSON string
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return inventory{}, time.Time{}, false
		}
		raw = json.RawMessage(s)
	}
	var inv inventory
	if err := json.Unmarshal(raw, &inv); err != nil || inv.Format != "inventory/v1" {
		return inventory{}, time.Time{}, false
	}
	at := time.Unix(r.Message.TimestampUnix, 0).UTC()
	if r.Message.TimestampUnix == 0 {
		at, _ = time.Parse(time.RFC3339, r.Message.Timestamp)
	}
	return inv, at, true
}

// latestInventory reads the days newest first until an inventory.
func latestInventory(ctx context.Context, r Reader, prefix string, cluster string, days []string, maxGets int) (inventory, time.Time, bool, error) {
	for i := len(days) - 1; i >= 0; i-- {
		objs, err := r.List(ctx, fmt.Sprintf("system=%s%s/date=%s/", prefix, cluster, days[i]))
		if err != nil {
			return inventory{}, time.Time{}, false, err
		}
		sort.Slice(objs, func(a, b int) bool { return objs[a].LastModified.After(objs[b].LastModified) })
		for j, o := range objs {
			if j == maxGets {
				break
			}
			data, err := r.Get(ctx, o.Key)
			if err != nil {
				return inventory{}, time.Time{}, false, err
			}
			if inv, at, ok := parseInventory(data); ok {
				return inv, at, true, nil
			}
		}
	}
	return inventory{}, time.Time{}, false, nil
}

// BuildVersions condenses the latest inventory of every cluster. nil if
// the prefix is empty or no cluster has an inventory in the window.
func BuildVersions(ctx context.Context, r Reader, now time.Time, days []string, o Options) (*Versions, error) {
	if o.InventoryPrefix == "" {
		return nil, nil
	}
	targets, err := Targets(ctx, r, []Source{{Name: "inventory", Prefix: o.InventoryPrefix}})
	if err != nil {
		return nil, err
	}
	invs := map[string]inventory{}
	v := &Versions{}
	for _, t := range targets {
		inv, at, ok, err := latestInventory(ctx, r, o.InventoryPrefix, t.Name, days, o.MaxGetsPerDay)
		if err != nil {
			return nil, fmt.Errorf("inventory %s: %w", t.Name, err)
		}
		if !ok {
			slog.Warn("no inventory in the window", "cluster", t.Name)
			continue
		}
		invs[t.Name] = inv
		cv := ClusterVersion{Cluster: t.Name, Version: inv.Version, Nodes: len(inv.Nodes), Images: max(inv.Count, len(inv.Images)),
			Releases: len(inv.Releases), SnapshotAt: at, Stale: now.Sub(at) > o.InventoryStaleAfter}
		for _, im := range inv.Images {
			if im.Latest {
				cv.Unpinned++
			}
		}
		v.Clusters = append(v.Clusters, cv)
	}
	if len(v.Clusters) == 0 {
		return nil, nil
	}
	clusters := make([]string, 0, len(invs))
	for c := range invs {
		clusters = append(clusters, c)
	}
	sort.Strings(clusters)

	imageTags := map[string]map[string]map[string]bool{}   // repo -> tag -> clusters
	releaseVers := map[string]map[string]map[string]bool{} // kind/chart -> version -> clusters
	for _, c := range clusters {
		inv := invs[c]
		for _, im := range inv.Images {
			if im.Latest {
				w := im.Workloads
				if len(w) > 3 {
					w = w[:3]
				}
				v.Unpinned = append(v.Unpinned, ImageUse{Cluster: c, Image: im.Image, Workloads: w})
			}
			repo, tag := splitImage(im.Image)
			if tag == "" {
				continue
			}
			add(imageTags, repo, tag, c)
		}
		for _, rel := range inv.Releases {
			if rel.Chart != "" && rel.Version != "" {
				add(releaseVers, rel.Kind+" "+rel.Chart, rel.Version, c)
			}
			if !ready(rel.Kind, rel.Status) {
				v.NotReady = append(v.NotReady, ReleaseState{Cluster: c, Kind: rel.Kind,
					Name: strings.TrimPrefix(rel.Namespace+"/"+rel.Name, "/"), Version: rel.Version, Status: orDash(rel.Status)})
			}
		}
		if skew := kubeletSkew(inv); skew != "" {
			v.KubeletSkew = append(v.KubeletSkew, c+": "+skew)
		}
	}
	v.ImageDrift = drift("image", imageTags)
	v.ReleaseDrift = drift("", releaseVers)
	v.bound()
	return v, nil
}

func add(m map[string]map[string]map[string]bool, key, version, cluster string) {
	if m[key] == nil {
		m[key] = map[string]map[string]bool{}
	}
	if m[key][version] == nil {
		m[key][version] = map[string]bool{}
	}
	m[key][version][cluster] = true
}

// drift keeps the keys with two versions or more seen on two clusters or
// more: within one cluster, several versions are often deliberate.
func drift(kind string, m map[string]map[string]map[string]bool) []Drift {
	var out []Drift
	for key, vers := range m {
		if len(vers) < 2 {
			continue
		}
		all := map[string]bool{}
		d := Drift{Kind: kind, Name: key, Versions: map[string][]string{}}
		if kind == "" {
			d.Kind, d.Name, _ = strings.Cut(key, " ")
		}
		for ver, cs := range vers {
			for c := range cs {
				all[c] = true
				d.Versions[ver] = append(d.Versions[ver], c)
			}
			sort.Strings(d.Versions[ver])
		}
		if len(all) < 2 {
			continue
		}
		out = append(out, d)
	}
	sort.Slice(out, func(a, b int) bool {
		if len(out[a].Versions) != len(out[b].Versions) {
			return len(out[a].Versions) > len(out[b].Versions)
		}
		return out[a].Kind+out[a].Name < out[b].Kind+out[b].Name
	})
	return out
}

// splitImage returns repository and tag; the tag is empty for a reference
// without one (the digest is not a version here).
func splitImage(ref string) (repo, tag string) {
	ref, _, _ = strings.Cut(ref, "@")
	slash := strings.LastIndex(ref, "/")
	if i := strings.LastIndex(ref, ":"); i > slash {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

// kubeletSkew describes kubelets on several versions or on another minor
// than the API server, empty if none.
func kubeletSkew(inv inventory) string {
	set := map[string]bool{}
	for _, n := range inv.Nodes {
		if n.Kubelet != "" {
			set[n.Kubelet] = true
		}
	}
	vers := make([]string, 0, len(set))
	for k := range set {
		vers = append(vers, k)
	}
	sort.Strings(vers)
	other := false
	for _, k := range vers {
		if inv.Version != "" && minor(k) != minor(inv.Version) {
			other = true
		}
	}
	if len(vers) < 2 && !other {
		return ""
	}
	return fmt.Sprintf("API server %s, kubelets %s", orDash(inv.Version), strings.Join(vers, ", "))
}

// minor is "v1.33" of "v1.33.2+rke2r1".
func minor(v string) string {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return v
	}
	return parts[0] + "." + parts[1]
}

func ready(kind, status string) bool {
	switch kind {
	case "HelmRelease":
		return status == "Ready"
	case "Application":
		return status == "Synced/Healthy"
	case "Configuration", "Provider", "Function":
		return status == "Healthy"
	default:
		return true
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (v *Versions) bound() {
	cut := func(n int) int {
		if n > MaxVersionFacts {
			v.Omitted += n - MaxVersionFacts
			return MaxVersionFacts
		}
		return n
	}
	v.Unpinned = v.Unpinned[:cut(len(v.Unpinned))]
	v.ImageDrift = v.ImageDrift[:cut(len(v.ImageDrift))]
	v.ReleaseDrift = v.ReleaseDrift[:cut(len(v.ReleaseDrift))]
	v.NotReady = v.NotReady[:cut(len(v.NotReady))]
}

// markdown renders the "Versionen" section.
func (v *Versions) markdown(b *strings.Builder) {
	b.WriteString("\n## Versionen\n\n| Cluster | Kubernetes | Nodes | Images | ohne festen Tag | Releases | Inventar |\n|---|---|---:|---:|---:|---:|---|\n")
	for _, c := range v.Clusters {
		stale := ""
		if c.Stale {
			stale = " ⚠ veraltet"
		}
		fmt.Fprintf(b, "| %s | %s | %d | %d | %d | %d | %s%s |\n", c.Cluster, orDash(c.Version), c.Nodes, c.Images, c.Unpinned, c.Releases, c.SnapshotAt.Format("2006-01-02 15:04"), stale)
	}
	if len(v.KubeletSkew) > 0 {
		b.WriteString("\n**Versionsabweichung Kubelet:**\n\n")
		for _, s := range v.KubeletSkew {
			fmt.Fprintf(b, "- %s\n", s)
		}
	}
	if len(v.NotReady) > 0 {
		b.WriteString("\n**Nicht bereit:**\n\n")
		for _, r := range v.NotReady {
			fmt.Fprintf(b, "- %s: %s %s `%s`\n", r.Cluster, r.Kind, r.Name, r.Status)
		}
	}
	for _, d := range []struct {
		title string
		list  []Drift
	}{{"Unterschiedliche Image-Versionen über Cluster", v.ImageDrift}, {"Unterschiedliche Chart-/Paket-Versionen über Cluster", v.ReleaseDrift}} {
		if len(d.list) == 0 {
			continue
		}
		fmt.Fprintf(b, "\n**%s:**\n\n", d.title)
		for _, x := range d.list {
			vers := make([]string, 0, len(x.Versions))
			for ver, cs := range x.Versions {
				vers = append(vers, fmt.Sprintf("`%s` (%s)", ver, strings.Join(cs, ", ")))
			}
			sort.Strings(vers)
			fmt.Fprintf(b, "- %s %s: %s\n", x.Kind, x.Name, strings.Join(vers, " · "))
		}
	}
	if len(v.Unpinned) > 0 {
		b.WriteString("\n**Images ohne festen Tag (`:latest` oder keiner):**\n\n")
		for _, u := range v.Unpinned {
			fmt.Fprintf(b, "- %s: `%s` (%s)\n", u.Cluster, u.Image, strings.Join(u.Workloads, ", "))
		}
	}
	if v.Omitted > 0 {
		fmt.Fprintf(b, "\n(%d weitere Einträge nicht aufgeführt)\n", v.Omitted)
	}
}
