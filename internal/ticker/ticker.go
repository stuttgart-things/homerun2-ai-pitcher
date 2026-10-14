// Package ticker turns the same input as the summary into a few short lines
// for the LED matrix (homerun2-led-catcher): one pitched message per line,
// ASCII only (its BDF fonts have no umlauts), a few dozen characters each.
//
// The ground rules of the summary hold here too, and are enforced, not just
// asked for: a line with a number that is not in the input is dropped, so
// the model cannot put a made-up count on the wall.
package ticker

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// SystemPrompt asks for at most n lines of at most maxChars characters.
func SystemPrompt(language string, n, maxChars int) string {
	return strings.Join([]string{
		"You write ticker lines for a small LED matrix wall display in the office of a homelab platform team.",
		"Input: a JSON object with message statistics (current and previous window) and open findings (expiring tokens and certificates, failed checks, reminders).",
		"Rules:",
		fmt.Sprintf("- Output 1 to %d lines, one ticker per line, nothing else: no numbering, no bullets, no quotes, no empty lines.", n),
		fmt.Sprintf("- Each line at most %d characters. Short words, no filler.", maxChars),
		"- One fact per line. Most important first: critical/error findings, things due soon, then notable changes, then one calm line if all is fine.",
		"- Each line is a headline with a verb, like a news ticker (\"Git-Pitcher meldet 35 Events\"), never a bare label and value (\"homerun2-git-pitcher 35\"). Shorten long system names (homerun2-git-pitcher -> Git-Pitcher).",
		"- Group findings that belong together into one line (e.g. \"3 Vault tokens expire 2026-11-09\").",
		"- Use ONLY numbers and names from the input. Never invent counts, dates or systems.",
		"- Plain ASCII: no emojis, no special characters.",
		"- Write in " + language + ".",
	}, "\n")
}

// Parse cleans the model's answer into at most n lines of at most maxChars
// characters. input is everything the model was given; a line with a number
// that does not occur in it is dropped. Counts from 1 to maxCount are allowed
// on top: grouping ("3 Vault tokens ...") needs numbers the model counted,
// and a count of findings can never exceed the number of findings.
func Parse(answer string, n, maxChars int, input string, maxCount int) []string {
	allowed := numbers(input)
	for i := 1; i <= maxCount; i++ {
		allowed[fmt.Sprint(i)] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, raw := range strings.Split(answer, "\n") {
		line := clean(raw)
		if line == "" {
			continue
		}
		line = shorten(line, maxChars)
		if line == "" || seen[strings.ToLower(line)] || !numbersAllowed(line, allowed) {
			continue
		}
		seen[strings.ToLower(line)] = true
		out = append(out, line)
		if len(out) == n {
			break
		}
	}
	return out
}

var bullet = regexp.MustCompile(`^\s*(?:[-*\x{2022}>]+|\d+[.)])\s*`)

// clean strips list markers and quotes, folds to ASCII and collapses spaces.
func clean(s string) string {
	s = bullet.ReplaceAllString(s, "")
	s = strings.Trim(strings.TrimSpace(s), "\"'`")
	return strings.Join(strings.Fields(ASCII(s)), " ")
}

var fold = strings.NewReplacer(
	"ä", "ae", "ö", "oe", "ü", "ue", "Ä", "Ae", "Ö", "Oe", "Ü", "Ue", "ß", "ss",
	"–", "-", "—", "-", "„", "\"", "“", "\"", "”", "\"", "‚", "'", "‘", "'", "’", "'",
	"…", "...", "→", "->", "€", "EUR",
)

// ASCII folds German umlauts and common typography and drops everything else
// outside printable ASCII.
func ASCII(s string) string {
	s = fold.Replace(s)
	return strings.Map(func(r rune) rune {
		if r >= 0x20 && r < 0x7f {
			return r
		}
		if unicode.IsSpace(r) {
			return ' '
		}
		return -1
	}, s)
}

// shorten cuts s at the last word boundary within maxChars.
func shorten(s string, maxChars int) string {
	if len(s) <= maxChars {
		return s
	}
	cut := strings.LastIndex(s[:maxChars+1], " ")
	if cut <= 0 {
		return strings.TrimSpace(s[:maxChars])
	}
	return strings.TrimRight(strings.TrimSpace(s[:cut]), ",;:-")
}

var digits = regexp.MustCompile(`\d+`)

// numbers returns every digit run in s, without leading zeros, so "09" in a
// date matches a "9" the model writes.
func numbers(s string) map[string]bool {
	m := map[string]bool{}
	for _, d := range digits.FindAllString(s, -1) {
		m[norm(d)] = true
	}
	return m
}

func numbersAllowed(line string, allowed map[string]bool) bool {
	for _, d := range digits.FindAllString(line, -1) {
		if !allowed[norm(d)] {
			return false
		}
	}
	return true
}

func norm(d string) string {
	t := strings.TrimLeft(d, "0")
	if t == "" {
		return "0"
	}
	return t
}
