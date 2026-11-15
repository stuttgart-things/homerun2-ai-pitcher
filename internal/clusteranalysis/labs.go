package clusteranalysis

import (
	"fmt"
	"path"
	"strings"
)

// Lab names the targets (clusters, hosts) of one lab by name patterns
// (homerun2-ai-pitcher#26). The analysis stays across labs; the lab only
// groups the report.
type Lab struct {
	Name     string
	Patterns []string // path.Match globs over the target name
}

// Labs are checked in order: the first lab with a matching pattern wins.
type Labs []Lab

// ParseLabs reads "LabUL=platform-sthings,*-labul;LabDA=sthings-app*,labda-*".
// Empty: no labs.
func ParseLabs(s string) (Labs, error) {
	var out Labs
	seen := map[string]bool{}
	for _, entry := range strings.Split(s, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, pats, ok := strings.Cut(entry, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("%q: want <lab>=<pattern>,<pattern>", entry)
		}
		if seen[name] {
			return nil, fmt.Errorf("lab %q twice", name)
		}
		seen[name] = true
		lab := Lab{Name: name}
		for _, p := range strings.Split(pats, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if _, err := path.Match(p, ""); err != nil {
				return nil, fmt.Errorf("lab %s: pattern %q: %w", name, p, err)
			}
			lab.Patterns = append(lab.Patterns, p)
		}
		if len(lab.Patterns) == 0 {
			return nil, fmt.Errorf("lab %s: no patterns", name)
		}
		out = append(out, lab)
	}
	return out, nil
}

// Of is the lab of a target, "" when none matches.
func (ls Labs) Of(target string) string {
	for _, l := range ls {
		for _, p := range l.Patterns {
			if ok, _ := path.Match(p, target); ok {
				return l.Name
			}
		}
	}
	return ""
}

// order is the position of a lab in the configuration; unknown labs ("")
// come last.
func (ls Labs) order(name string) int {
	for i, l := range ls {
		if l.Name == name {
			return i
		}
	}
	return len(ls)
}

// LabStats are one lab's numbers in the report.
type LabStats struct {
	Lab     string `json:"lab"`
	Systems int    `json:"systems"`
	Open    int    `json:"open"`
	New     int    `json:"new"`
}

// labName is how a target without lab is shown.
func labName(l string) string {
	if l == "" {
		return "ohne Lab"
	}
	return l
}
