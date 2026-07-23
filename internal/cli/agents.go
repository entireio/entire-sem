package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// agentGuide is the canonical, agent-agnostic operating guide for coding agents using the
// graph in a CONSUMING project (not this repo). It ships inside the binary so every install
// carries the current doctrine; `init-agents` distributes it into a project's AGENTS.md /
// CLAUDE.md via a small pointer block, and `agent-guide` prints it for any agent or human.
// The prompt block is the exact instruction set that measured best on the graphmark
// agentic-swebench benchmark (23 SWE-bench instances / 10 languages, savings vs a no-tool
// agent; single-run board 62%, 3x-replicated 58% weighted, ahead of alternatives on every
// language mean — see the graphmark repo for methodology and caveats).
const agentGuide = `# entire-graph — coding-agent guide (universal)

A deterministic, local code graph is available via ` + "`entire graph`" + ` (functions, classes,
methods, types, routes + call/inheritance/field relations; tree-sitter; no network). Use it to
LOCATE and UNDERSTAND code before any grep/find/cat/whole-file read — exploration is where most
of a session's tokens go.

## The measured-best instruction block (paste into your agent's system/tool prompt)

    A precomputed code-search tool is available:
      entire graph search --repo . --profile full --query "<task>"
    Use it to LOCATE the fix BEFORE any grep/find. Your FIRST action must be ONE search:
      entire graph search --repo . --profile full --query "<the bug in one sentence>"
    Then open the top hit's file with your file-read tool (pass a line range around the reported
    line), make the minimal edit, and STOP. The search top hit is the fix site on most tasks — go
    straight there and edit; do NOT re-search or grep to 'confirm'. Reach the edit in as FEW turns
    as possible (every turn re-reads your whole context — that is the token cost). Hard rules:
    (1) SEARCH FIRST. (2) After search, READ the file directly (line range) and EDIT — do not
    chain more searches. (3) NEVER read a whole file to explore; pass a line range. (4) NEVER
    search outside this repo. Apply the minimal fix and STOP the moment you can justify it.

## Quick model

    locate  ->  entire graph search --repo . --profile full --query "..."   (ranked code + file:line)
    impact  ->  entire graph neighbors --repo . --symbol X --relation CALLS --direction in
    change  ->  entire graph diff --base A --head B --json                   (entity-level)
    detect  ->  entire graph capabilities --json                             (semantic vs inventory-only)

## Rules that save tokens

1. Search first — always. One plain-language query; the graph is deterministic, trust it.
2. Read line ranges, never whole files. The search output already shows the top hits' source.
3. Impact = one targeted neighbors query, never a whole-graph dump or repo-wide grep.
4. Results ranks 3+ are terse locators by design — open one, don't re-search to compare.
5. Chaining search -> definitions -> callers to "explore the tool" is the #1 measured waste.
`

// agentPointerBegin/End delimit the block init-agents manages inside AGENTS.md / CLAUDE.md,
// so re-runs update in place instead of appending duplicates.
const (
	agentPointerBegin = "<!-- entire-graph:begin -->"
	agentPointerEnd   = "<!-- entire-graph:end -->"
)

func runAgentGuide(opts Options, args []string) error {
	fs := flag.NewFlagSet("agent-guide", flag.ContinueOnError)
	fs.SetOutput(opts.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	fmt.Fprint(opts.Stdout, agentGuide)
	return nil
}

// runInitAgents installs the guide into a consuming project so ANY coding agent finds it:
// writes .entire/graph-agent.md (plugin-managed, overwritten on re-run) and upserts a
// marker-guarded pointer block into AGENTS.md (the cross-agent convention) and CLAUDE.md
// (which additionally understands the @-import line).
func runInitAgents(opts Options, args []string) error {
	fs := flag.NewFlagSet("init-agents", flag.ContinueOnError)
	fs.SetOutput(opts.Stderr)
	repo := fs.String("repo", ".", "project root to install the agent guide into")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root, err := filepath.Abs(*repo)
	if err != nil {
		return err
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return fmt.Errorf("init-agents: %s is not a directory", root)
	}

	guidePath := filepath.Join(root, ".entire", "graph-agent.md")
	if err := os.MkdirAll(filepath.Dir(guidePath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(guidePath, []byte(agentGuide), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(opts.Stdout, "wrote %s\n", guidePath)

	pointer := agentPointerBegin + "\n" +
		"This repo has the entire-graph code graph installed. Before exploring code with\n" +
		"grep/find/whole-file reads, read .entire/graph-agent.md — search-first doctrine for\n" +
		"coding agents (measured to cut agent token usage roughly in half on SWE-bench tasks).\n" +
		"@.entire/graph-agent.md\n" +
		agentPointerEnd + "\n"

	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		path := filepath.Join(root, name)
		if err := upsertPointerBlock(path, pointer); err != nil {
			return fmt.Errorf("init-agents: %s: %w", name, err)
		}
		fmt.Fprintf(opts.Stdout, "updated %s\n", path)
	}
	return nil
}

// upsertPointerBlock appends the block to path (creating the file if absent), or replaces the
// existing marker-delimited block in place, so repeated runs never duplicate content.
func upsertPointerBlock(path, block string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	content := string(existing)
	begin := strings.Index(content, agentPointerBegin)
	end := strings.Index(content, agentPointerEnd)
	switch {
	case begin >= 0 && end > begin:
		content = content[:begin] + strings.TrimSuffix(block, "\n") + content[end+len(agentPointerEnd):]
	case len(content) == 0:
		content = block
	default:
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += "\n" + block
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
