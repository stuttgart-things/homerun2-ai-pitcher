// Package summary turns the inputs into the prompt and the pitched message.
//
// The ground rules of stuttgart-things/homerun-library#135 live here:
//   - the numbers are scout's, never the model's: the title is scout's digest
//     title, and scout's own digest text is appended under the AI text;
//   - the AI never decides severity: the message is always info;
//   - fail-open: without an AI answer the plain digest is pitched, marked;
//   - everything AI-written is marked as such.
package summary

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/stuttgart-things/homerun2-ai-pitcher/internal/pitch"
	"github.com/stuttgart-things/homerun2-ai-pitcher/internal/sources"
)

// Input is everything a summary is built from.
type Input struct {
	Digest   *sources.Digest
	Findings []sources.Finding
	// FindingsErr is set when schedule-pitcher could not be read; the prompt
	// then says so instead of claiming there is nothing open.
	FindingsErr error
	Now         time.Time
}

// SystemPrompt returns the instructions for the model.
func SystemPrompt(language string) string {
	return strings.Join([]string{
		"You write the daily status summary of a homelab platform for its operators.",
		"Input: a JSON object with scout's message statistics for the period (current and previous window) and the open findings of the schedule-pitcher (expiring tokens and certificates, failed checks, reminders, findings from cron jobs).",
		"Rules:",
		"- Use ONLY facts from the input. Never invent systems, causes, numbers or links.",
		"- Do not recompute or restate the statistics in detail; they are shown below your text anyway. Interpret them: what changed, what stands out.",
		"- Order: what needs attention today first (critical/error findings, overdue reminders, long-open findings), then notable changes, then what is fine.",
		"- Group findings that belong together into ONE statement: same kind (e.g. several tokens or certificates), same source, same host, or due on the same date or within a few days of each other. Name the shared date and what to do, e.g. \"three Vault tokens expire on 2026-11-09; rotate them together\".",
		"- Warnings with a due date are worth one sentence even when nothing is urgent, so they can be planned.",
		"- Acknowledged findings: mention who is on it, do not escalate them.",
		"- If nothing needs attention, say that in one sentence.",
		"- 3 to 6 sentences, plain text, no headings, no markdown tables, no emojis.",
		"- Write in " + language + ".",
	}, "\n")
}

type promptFinding struct {
	Title    string `json:"title"`
	Severity string `json:"severity"`
	Source   string `json:"source"`
	Status   string `json:"status"`
	OpenDays int    `json:"openDays"`
	AckBy    string `json:"acknowledgedBy,omitempty"`
	AckNote  string `json:"ackNote,omitempty"`
	Message  string `json:"message,omitempty"`
}

type promptInput struct {
	Schedule       string          `json:"schedule"`
	Current        sources.Window  `json:"current"`
	Previous       *sources.Window `json:"previous,omitempty"`
	OpenFindings   []promptFinding `json:"openFindings"`
	FindingsNote   string          `json:"findingsNote,omitempty"`
	OmittedFinding int             `json:"omittedFindings,omitempty"`
}

// UserPrompt renders the input as JSON for the model. At most maxFindings
// findings go in (schedule-pitcher lists them worst first).
func UserPrompt(in Input, maxFindings int) (string, error) {
	p := promptInput{
		Schedule:     in.Digest.Schedule,
		Current:      in.Digest.Current,
		Previous:     in.Digest.Previous,
		OpenFindings: []promptFinding{},
	}
	if in.FindingsErr != nil {
		p.FindingsNote = "the schedule-pitcher could not be read; open findings are unknown, do not claim there are none"
	}
	for i, f := range in.Findings {
		if i >= maxFindings {
			p.OmittedFinding = len(in.Findings) - maxFindings
			break
		}
		days := 0
		if !f.FirstSeen.IsZero() {
			days = int(in.Now.Sub(f.FirstSeen).Hours() / 24)
		}
		p.OpenFindings = append(p.OpenFindings, promptFinding{
			Title:    f.Title,
			Severity: f.Severity,
			Source:   f.Source,
			Status:   f.Status,
			OpenDays: days,
			AckBy:    f.AckBy,
			AckNote:  f.AckNote,
			Message:  truncate(f.Message, 300),
		})
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Compose builds the pitched message. aiText and model are empty when the AI
// answer is missing; aiErr says why (shown, so a silent fallback is visible).
func Compose(in Input, aiText, model string, truncated bool, aiErr error, system, author string) pitch.Message {
	d := in.Digest
	var b strings.Builder
	title := "[AI] " + d.Message.Title
	if aiText != "" {
		b.WriteString(aiText)
		b.WriteString("\n\n---\n")
		fmt.Fprintf(&b, "AI-generated summary (model %s) of the scout %s digest and %d open findings. The numbers below are scout's, not the model's.", model, d.Schedule, len(in.Findings))
		if truncated {
			b.WriteString(" The answer hit the token limit and was cut after its last complete sentence.")
		}
		b.WriteString("\n\n")
	} else {
		title = "[AI n/a] " + d.Message.Title
		reason := "no answer"
		if aiErr != nil {
			reason = aiErr.Error()
		}
		fmt.Fprintf(&b, "AI summary not available (%s). The plain scout digest follows.\n\n", truncate(reason, 200))
	}
	b.WriteString(d.Message.Message)
	if in.FindingsErr != nil {
		fmt.Fprintf(&b, "\n\nOpen findings could not be read: %s", truncate(in.FindingsErr.Error(), 200))
	} else if len(in.Findings) > 0 {
		b.WriteString("\n\nOpen findings:")
		for i, f := range in.Findings {
			if i >= 10 {
				fmt.Fprintf(&b, "\n- … and %d more", len(in.Findings)-10)
				break
			}
			ack := ""
			if f.AckBy != "" {
				ack = " (ack " + f.AckBy + ")"
			}
			fmt.Fprintf(&b, "\n- [%s] %s%s", f.Severity, f.Title, ack)
		}
	}
	return pitch.Message{
		Title:    truncate(title, 120),
		Message:  b.String(),
		Severity: "info",
		Author:   author,
		System:   system,
		Tags:     "ai,digest," + d.Schedule,
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
