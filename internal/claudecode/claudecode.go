// Package claudecode runs Claude Code headless (`claude -p`) for one
// structured answer: no tools, no MCP servers, no settings from disk, the
// answer validated against a JSON schema by the CLI itself.
package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Runner runs the claude CLI. Auth comes from the environment the process
// inherits: CLAUDE_CODE_OAUTH_TOKEN (subscription) or ANTHROPIC_API_KEY.
type Runner struct {
	Bin       string // default "claude"
	Model     string // alias (sonnet, opus) or full name
	MaxBudget float64
	Timeout   time.Duration
}

// Result is one answer.
type Result struct {
	// Output is the structured answer, valid against the schema.
	Output    json.RawMessage
	Model     string
	CostUSD   float64
	Turns     int
	SessionID string
}

// cliResult is the part of `--output-format json` read here.
type cliResult struct {
	IsError          bool                       `json:"is_error"`
	Subtype          string                     `json:"subtype"`
	Result           string                     `json:"result"`
	StructuredOutput json.RawMessage            `json:"structured_output"`
	TotalCostUSD     float64                    `json:"total_cost_usd"`
	NumTurns         int                        `json:"num_turns"`
	SessionID        string                     `json:"session_id"`
	ModelUsage       map[string]json.RawMessage `json:"modelUsage"`
}

// Run sends prompt on stdin and returns the structured output. The working
// directory is an empty temporary one; no tool could reach anything anyway.
func (r *Runner) Run(ctx context.Context, systemPrompt, prompt string, schema json.RawMessage) (*Result, error) {
	bin := r.Bin
	if bin == "" {
		bin = "claude"
	}
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	dir, err := os.MkdirTemp("", "claude-run-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	args := []string{"-p",
		"--output-format", "json",
		"--tools", "",
		"--strict-mcp-config",
		"--setting-sources", "",
		"--no-session-persistence",
		"--json-schema", string(schema),
		"--append-system-prompt", systemPrompt,
	}
	if r.Model != "" {
		args = append(args, "--model", r.Model)
	}
	if r.MaxBudget > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%.2f", r.MaxBudget))
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()

	var res cliResult
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		if runErr != nil {
			return nil, fmt.Errorf("claude: %w: %s", runErr, truncate(stderr.String(), 500))
		}
		return nil, fmt.Errorf("claude: unreadable output: %w: %s", err, truncate(stdout.String(), 300))
	}
	if res.IsError || res.Subtype != "success" {
		return nil, fmt.Errorf("claude: %s: %s", res.Subtype, truncate(res.Result, 500))
	}
	if len(res.StructuredOutput) == 0 || string(res.StructuredOutput) == "null" {
		return nil, fmt.Errorf("claude: no structured output")
	}
	out := &Result{Output: res.StructuredOutput, CostUSD: res.TotalCostUSD, Turns: res.NumTurns, SessionID: res.SessionID}
	for m := range res.ModelUsage {
		out.Model = m
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
