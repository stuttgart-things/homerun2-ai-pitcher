package clusteranalysis

import (
	"context"
	"strings"
	"testing"
)

const knownYAML = `
format: known-findings/v1
entries:
  - id: web-no-endpoints
    match:
      source: k8sgpt
      target: "a*"
      kind: Service
      name: "default/*"
      text: "*no endpoints*"
    reason: gewollt, keine Replica
    owner: team
    expires: 2026-12-31
    issue: 4591
  - id: old-acceptance
    fingerprint: fp-pod
    reason: war mal ok
    owner: team
    expires: 2026-11-01
  - id: nothing-matches
    fingerprint: fp-none
    reason: behoben
    owner: team
    expires: 2027-01-31
`

func TestParseKnownStrict(t *testing.T) {
	k, err := ParseKnown([]byte(knownYAML))
	if err != nil {
		t.Fatal(err)
	}
	if len(k.Entries) != 3 || k.Entries[0].Issue != 4591 {
		t.Fatalf("entries %+v", k.Entries)
	}
	for name, bad := range map[string]string{
		"unknown field": "entries:\n  - id: x\n    fingerprint: a\n    reason: r\n    owner: o\n    expires: 2027-01-01\n    comment: typo\n",
		"no reason":     "entries:\n  - id: x\n    fingerprint: a\n    owner: o\n    expires: 2027-01-01\n",
		"no owner":      "entries:\n  - id: x\n    fingerprint: a\n    reason: r\n    expires: 2027-01-01\n",
		"no expires":    "entries:\n  - id: x\n    fingerprint: a\n    reason: r\n    owner: o\n",
		"bad date":      "entries:\n  - id: x\n    fingerprint: a\n    reason: r\n    owner: o\n    expires: 31.12.2026\n",
		"both":          "entries:\n  - id: x\n    fingerprint: a\n    match: {kind: Pod}\n    reason: r\n    owner: o\n    expires: 2027-01-01\n",
		"neither":       "entries:\n  - id: x\n    reason: r\n    owner: o\n    expires: 2027-01-01\n",
		"empty match":   "entries:\n  - id: x\n    match: {}\n    reason: r\n    owner: o\n    expires: 2027-01-01\n",
		"no id":         "entries:\n  - fingerprint: a\n    reason: r\n    owner: o\n    expires: 2027-01-01\n",
		"duplicate id":  "entries:\n  - {id: x, fingerprint: a, reason: r, owner: o, expires: 2027-01-01}\n  - {id: x, fingerprint: b, reason: r, owner: o, expires: 2027-01-01}\n",
		"format":        "format: known-findings/v2\nentries: []\n",
		"match field":   "entries:\n  - id: x\n    match: {cluster: a}\n    reason: r\n    owner: o\n    expires: 2027-01-01\n",
	} {
		if _, err := ParseKnown([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if k, err := ParseKnown([]byte("")); err != nil || len(k.Entries) != 0 {
		t.Errorf("empty file: %v", err)
	}
}

func TestGlob(t *testing.T) {
	for p, cases := range map[string]map[string]bool{
		"default/*":       {"default/web": true, "default/a/b": true, "kube-system/x": false},
		"*no endpoints*":  {"Service has no endpoints": true, "no endpoints": true, "endpoints": false},
		"sthings-app-?":   {"sthings-app-2": true, "sthings-app-22": false},
		"a.b":             {"a.b": true, "axb": false},
		"Service":         {"Service": true, "Services": false},
		"*(x)[y]+{1}|z^$": {"q(x)[y]+{1}|z^$": true},
	} {
		re := glob(p)
		for s, want := range cases {
			if got := re.MatchString(s); got != want {
				t.Errorf("glob %q on %q: %v, want %v", p, s, got, want)
			}
		}
	}
}

// The fixture with the file: the Service on a is accepted (match), b's is
// not (target a*); fp-pod's acceptance has expired; fp-none matches nothing.
func TestBuildKnown(t *testing.T) {
	k, err := ParseKnown([]byte(knownYAML))
	if err != nil {
		t.Fatal(err)
	}
	o := opts
	o.Known = k
	in, err := Build(context.Background(), fixture(), now, &Report{}, o)
	if err != nil {
		t.Fatal(err)
	}
	a, b := in.Clusters[0], in.Clusters[1]
	if a.Open != 1 || a.Known != 1 || b.Open != 1 || b.Known != 0 {
		t.Errorf("a %+v b %+v", a, b)
	}
	// known last within the cluster
	if last := a.Findings[len(a.Findings)-1]; last.Fingerprint != "fp-svc-a" || last.Known != "web-no-endpoints" || last.KnownReason != "gewollt, keine Replica" || last.New {
		t.Errorf("known finding %+v", last)
	}
	if a.Findings[0].Fingerprint != "fp-pod" || a.Findings[0].Known != "" || !a.Findings[0].New {
		t.Errorf("expired acceptance must not apply: %+v", a.Findings[0])
	}
	// b's Service no longer crosses with a's: a's is known.
	if len(in.CrossCluster) != 0 {
		t.Errorf("cross-cluster with a known finding: %+v", in.CrossCluster)
	}
	ks := in.Known
	if ks == nil || len(ks.Matched) != 1 || ks.Matched[0].Count != 1 || strings.Join(ks.Matched[0].Systems, ",") != "k8sgpt/a" ||
		len(ks.Expired) != 1 || ks.Expired[0].ID != "old-acceptance" || len(ks.Unused) != 1 || ks.Unused[0].ID != "nothing-matches" {
		t.Errorf("known state %+v", ks)
	}

	r := NewReport(in, "run1")
	if r.Stats.Open != 2 || r.Stats.Known != 1 || r.Stats.New != 2 {
		t.Errorf("stats %+v", r.Stats)
	}
	// The baseline keeps the known finding: accepting is not resolving.
	found := false
	for _, ref := range r.OpenFindings {
		found = found || ref.Fingerprint == "fp-svc-a"
	}
	if !found {
		t.Error("known finding missing from the baseline")
	}
	// A topic on the known finding loses it; a topic of only known ones goes.
	if dropped := r.Apply(Assessment{Summary: "x", Topics: []Topic{
		{Title: "nur bekannt", Fingerprints: []string{"fp-svc-a"}},
		{Title: "gemischt", Fingerprints: []string{"fp-svc-a", "fp-svc-b"}},
	}}, in); dropped != 0 || len(r.Topics) != 1 || strings.Join(r.Topics[0].Fingerprints, ",") != "fp-svc-b" {
		t.Errorf("dropped %d topics %+v", dropped, r.Topics)
	}
	md := r.Markdown()
	for _, want := range []string{"**2 offene Funde**", "1 weiterer Fund als bekannt akzeptiert", "Akzeptanz abgelaufen:** `old-acceptance`",
		"## Bekannte Funde", "| `web-no-endpoints` | 1 | k8sgpt/a | gewollt, keine Replica | team | 2026-12-31 |",
		"## Akzeptanz abgelaufen", "## Ungenutzte Einträge", "`nothing-matches`"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
}

func TestKnownExpiresEndOfDay(t *testing.T) {
	k, err := ParseKnown([]byte("entries:\n  - {id: x, fingerprint: a, reason: r, owner: o, expires: 2026-11-13}\n"))
	if err != nil {
		t.Fatal(err)
	}
	f := Finding{Fingerprint: "a"}
	if k.Lookup(f, nil, now) == nil { // now is 2026-11-13 07:30
		t.Error("entry expiring today must still apply")
	}
	if k.Lookup(f, nil, now.AddDate(0, 0, 1)) != nil {
		t.Error("entry applies after its day")
	}
	var none *KnownFindings
	if none.Lookup(f, nil, now) != nil {
		t.Error("nil file matched")
	}
}

func TestSystemPromptKnown(t *testing.T) {
	if !strings.Contains(SystemPrompt(""), `"known" set is accepted by the team`) {
		t.Error("prompt lacks the known-findings rule")
	}
}
