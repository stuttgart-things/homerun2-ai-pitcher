package clusteranalysis

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseLabs(t *testing.T) {
	ls, err := ParseLabs(" LabUL=platform-sthings, *-labul ; LabDA=sthings-app*,labda-*;")
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 2 || ls[0].Name != "LabUL" || len(ls[0].Patterns) != 2 || ls[1].Patterns[1] != "labda-*" {
		t.Fatalf("labs = %+v", ls)
	}
	for target, want := range map[string]string{
		"platform-sthings": "LabUL", "dagger-gh-runner-labul": "LabUL",
		"sthings-app": "LabDA", "sthings-app-3": "LabDA", "labda-cicd-test1": "LabDA",
		"app-dev": "",
	} {
		if got := ls.Of(target); got != want {
			t.Errorf("Of(%q) = %q, want %q", target, got, want)
		}
	}
	if ls, err := ParseLabs(""); err != nil || ls != nil {
		t.Errorf("empty: %v, %v", ls, err)
	}
	for _, bad := range []string{"LabUL", "=a", "LabUL=", "A=a;A=b", "A=[", "A= , "} {
		if _, err := ParseLabs(bad); err == nil {
			t.Errorf("ParseLabs(%q): want an error", bad)
		}
	}
}

func TestFirstLabWins(t *testing.T) {
	ls, _ := ParseLabs("A=x-*;B=*")
	if ls.Of("x-1") != "A" || ls.Of("y") != "B" {
		t.Fatal("order")
	}
}

func TestBuildAndReportByLab(t *testing.T) {
	o := opts
	o.Labs, _ = ParseLabs("LabUL=a;LabDA=c")
	in, err := Build(context.Background(), fixture(), now, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(in.Labs, ",") != "LabUL,LabDA" {
		t.Fatalf("labs = %v", in.Labs)
	}
	for _, c := range in.Clusters {
		want := map[string]string{"a": "LabUL", "b": ""}[c.Cluster]
		if c.Lab != want {
			t.Errorf("%s: lab %q, want %q", c.Cluster, c.Lab, want)
		}
	}
	r := NewReport(in, "run-1")
	// LabDA has no target but stays in the list; b (no lab) comes last.
	if len(r.Labs) != 3 || r.Labs[0].Lab != "LabUL" || r.Labs[0].Systems != 1 || r.Labs[0].Open != 2 ||
		r.Labs[1].Lab != "LabDA" || r.Labs[1].Systems != 0 || r.Labs[2].Lab != "" || r.Labs[2].Systems != 1 {
		t.Fatalf("lab stats = %+v", r.Labs)
	}
	r.Topics = []Topic{{Title: "t", Clusters: []string{"a", "b"}, Fingerprints: []string{"fp-svc-a"}}}
	md := r.Markdown()
	for _, want := range []string{
		"| Lab | Systeme | Offen | Neu |", "| LabUL | 1 | 2 | 0 |", "| ohne Lab | 1 | 1 | 0 |",
		"- **Labs:** LabUL, ohne Lab", "| Lab | Bereich | System |", "| LabUL | Kubernetes | a |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	if strings.Index(md, "| LabUL | Kubernetes | a |") > strings.Index(md, "| ohne Lab | Kubernetes | b |") {
		t.Error("systems not ordered by lab")
	}
}

func TestReportWithoutLabsUnchanged(t *testing.T) {
	in, _ := Build(context.Background(), fixture(), now, nil, opts)
	r := NewReport(in, "run-1")
	md := r.Markdown()
	if r.Labs != nil || strings.Contains(md, "Lab") || !strings.Contains(md, "| Bereich | System | Offen | Neu | Snapshot |") {
		t.Fatalf("labs leaked into an ungrouped report:\n%s", md)
	}
}

func TestStaleAfterPerSource(t *testing.T) {
	m, err := ParseStaleAfter(" vm=26h , endpoints=1h,")
	if err != nil || m["vm"] != 26*time.Hour || m["endpoints"] != time.Hour {
		t.Fatalf("%v %v", m, err)
	}
	for _, bad := range []string{"vm", "=1h", "vm=0s", "vm=x"} {
		if _, err := ParseStaleAfter(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	// cluster b's snapshot is 5h old: stale at 3h, not with an override of 6h.
	o := opts
	in, _ := Build(context.Background(), fixture(), now, nil, o)
	o.StaleAfterBySource = map[string]time.Duration{"k8sgpt": 6 * time.Hour}
	in2, _ := Build(context.Background(), fixture(), now, nil, o)
	stale := func(in Input) bool {
		for _, c := range in.Clusters {
			if c.Cluster == "b" {
				return c.Stale
			}
		}
		return false
	}
	if !stale(in) || stale(in2) {
		t.Errorf("stale without override %v, with %v", stale(in), stale(in2))
	}
}
