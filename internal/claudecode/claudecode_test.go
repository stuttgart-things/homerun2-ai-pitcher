package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeClaude writes a script that records its arguments and stdin and prints
// out as the CLI's JSON result.
func fakeClaude(t *testing.T, out string) (bin, argsFile, stdinFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile, stdinFile = filepath.Join(dir, "args"), filepath.Join(dir, "stdin")
	bin = filepath.Join(dir, "claude")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\ncat > " + stdinFile + "\ncat <<'EOF'\n" + out + "\nEOF\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsFile, stdinFile
}

func TestRun(t *testing.T) {
	bin, argsFile, stdinFile := fakeClaude(t, `{"type":"result","subtype":"success","is_error":false,"result":"{}","structured_output":{"summary":"ok","topics":[]},"total_cost_usd":0.05,"num_turns":2,"modelUsage":{"claude-sonnet-5-5":{}}}`)
	r := &Runner{Bin: bin, Model: "sonnet", MaxBudget: 2, Timeout: 10 * time.Second}
	res, err := r.Run(context.Background(), "SYS", "PROMPT", json.RawMessage(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Output) != `{"summary":"ok","topics":[]}` || res.Model != "claude-sonnet-5-5" || res.CostUSD != 0.05 {
		t.Errorf("result %+v", res)
	}
	args, _ := os.ReadFile(argsFile)
	for _, want := range []string{"-p\n", "--tools\n\n", "--strict-mcp-config", "--setting-sources\n\n", "--json-schema\n{\"type\":\"object\"}", "--model\nsonnet", "--max-budget-usd\n2.00", "--append-system-prompt\nSYS"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("args lack %q:\n%s", want, args)
		}
	}
	if in, _ := os.ReadFile(stdinFile); string(in) != "PROMPT" {
		t.Errorf("stdin %q", in)
	}
}

func TestRunErrors(t *testing.T) {
	for name, out := range map[string]string{
		"error result":  `{"subtype":"error_max_budget_usd","is_error":true,"result":"budget exceeded"}`,
		"no structured": `{"subtype":"success","is_error":false,"result":"text"}`,
		"not json":      `Invalid API key`,
	} {
		bin, _, _ := fakeClaude(t, out)
		if _, err := (&Runner{Bin: bin}).Run(context.Background(), "s", "p", json.RawMessage(`{}`)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
