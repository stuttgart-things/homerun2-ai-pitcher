package ticker

import (
	"reflect"
	"strings"
	"testing"
)

const input = `{"current":{"total":31,"severities":{"warning":6}},"previous":{"total":34},
"openFindings":[{"title":"Vault token tekton-vault-labda expires in 25 days (2026-11-09)"}]}`

func TestParse(t *testing.T) {
	answer := strings.Join([]string{
		"1. 3 Vault-Tokens laufen am 09.11. ab", // numbering; 3 is a count of the 3 findings -> kept
		"- Vault Tokens laufen 2026-11-09 ab",   // bullet stripped
		"• 31 Meldungen, Warnungen gesunken",    // ok
		"",                                      // empty
		"\"Alles ruhig – keine Fehler\"",        // quotes, en dash
		"- Vault Tokens laufen 2026-11-09 ab",   // duplicate
		"42 neue PRs heute",                     // invented number
		"Sehr lange Zeile die weit ueber die erlaubte Laenge hinausgeht und gekuerzt wird",
	}, "\n")

	got := Parse(answer, 5, 40, input, 3)
	want := []string{
		"3 Vault-Tokens laufen am 09.11. ab",
		"Vault Tokens laufen 2026-11-09 ab",
		"31 Meldungen, Warnungen gesunken",
		"Alles ruhig - keine Fehler",
		"Sehr lange Zeile die weit ueber die",
	}
	// 42 is neither in the input nor a possible count of 3 findings.
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse =\n%q\nwant\n%q", got, want)
	}
	for _, l := range got {
		if len(l) > 40 {
			t.Errorf("%q longer than 40", l)
		}
	}
}

func TestParseCapsCount(t *testing.T) {
	got := Parse("a\nb\nc\nd", 2, 40, "", 0)
	if len(got) != 2 {
		t.Errorf("got %q, want 2 lines", got)
	}
}

func TestNumbersIgnoreLeadingZeros(t *testing.T) {
	// "09" in the input date allows the model's "9.11."
	if got := Parse("Tokens faellig am 9.11.", 3, 40, "2026-11-09", 0); len(got) != 1 {
		t.Errorf("got %q", got)
	}
}

func TestCountAboveFindingsIsDropped(t *testing.T) {
	if got := Parse("5 Tokens laufen ab", 3, 40, "", 3); len(got) != 0 {
		t.Errorf("got %q: 5 tokens out of 3 findings is made up", got)
	}
}

func TestASCII(t *testing.T) {
	if got := ASCII("Größe übermäßig → 5 € 🚀"); got != "Groesse uebermaessig -> 5 EUR " {
		t.Errorf("ASCII = %q", got)
	}
}
