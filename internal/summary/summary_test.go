package summary

import "testing"

func TestTidy(t *testing.T) {
	// From the seventh real run (nvidia/nemotron-3-super).
	in := "Drei Vault\u2011Tokens (tekton\u2011vault\u2011labda) laufen am 2026\u201111\u201109 ab.  \nIm Vergleich\u00a0zum Vortag – mehr Meldungen.  "
	want := "Drei Vault-Tokens (tekton-vault-labda) laufen am 2026-11-09 ab.\nIm Vergleich zum Vortag – mehr Meldungen."
	if got := Tidy(in); got != want {
		t.Errorf("Tidy =\n%q\nwant\n%q", got, want)
	}
}
