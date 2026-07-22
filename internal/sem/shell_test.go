package sem

import "testing"

func TestShellCommandCallIdentifiers(t *testing.T) {
	got := shellCommandCallIdentifiers(`function dirhistory_back() {
  local cw=""
  setopt localoptions no_ksh_arrays

  pop_past cw
  if [[ "" == "$cw" ]]; then
    dirhistory_past=($PWD)
    return
  fi

  pop_past d
  if [[ "" != "$d" ]]; then
    dirhistory_cd $d && push_future $cw
  else
    push_past $cw
  fi

  DIRHISTORY_CD="1" refresh_prompt
  reply=(not_a_call also_not_a_call)
  result=$(compute_value "$cw")
  cat <<EOF
usage: not-a-call [options]
inner_doc_word
EOF
  zle .kill-buffer
  echo "pop_past inside a string" # trailing pop_future comment
}
`)
	for _, want := range []string{
		"pop_past",       // plain command
		"dirhistory_cd",  // after `then`, before &&
		"push_future",    // after &&
		"push_past",      // after `else`
		"refresh_prompt", // after a VAR=x env prefix
		"compute_value",  // inside $(...)
	} {
		if _, ok := got[want]; !ok {
			t.Fatalf("missing command-position identifier %q in %#v", want, got)
		}
	}
	for _, reject := range []string{
		"return", "setopt", "local", "echo", "zle", "cat", // builtins/keywords are ignored
		"function", "dirhistory_back", "if", "then", "else", "fi",
		"cw", "d", "DIRHISTORY_CD", "dirhistory_past", "reply", "result", // assignments
		"not_a_call", "also_not_a_call", // array literal elements
		"usage", "inner_doc_word", // heredoc body
		"not-a-call",
		"pop_future", // only mentioned in a comment
		".kill-buffer",
	} {
		if _, ok := got[reject]; ok {
			t.Fatalf("unexpected identifier %q collected as a command: %#v", reject, got)
		}
	}
	if _, ok := got["pop_past"]; !ok {
		t.Fatalf("quoted mention should not have removed the real call: %#v", got)
	}
}

// TestMaskBashSubstringVariableOffsets covers the pyenv python-build
// regression: the vendored tree-sitter-bash grammar accepts `${arg:0:1}` and
// `${arg:${i}:1}` but emits a missing-} error for a bare `$var` offset or
// length (`${arg:$index:1}`), and that error derails parsing of everything
// after it. The mask rewrites the bare variable to a same-length digit run.
func TestMaskBashSubstringVariableOffsets(t *testing.T) {
	cases := []struct{ in, want string }{
		{`x="${arg:$index:1}"`, `x="${arg:000000:1}"`},
		{`x="${arg:$index}"`, `x="${arg:000000}"`},
		{`x="${arg:1:$len}"`, `x="${arg:1:0000}"`},
		// Already-parsable forms stay untouched.
		{`x="${arg:0:1}"`, `x="${arg:0:1}"`},
		{`x="${arg:${i}:1}"`, `x="${arg:${i}:1}"`},
		// Operator forms (:- := :+ :?) are not substring expansions.
		{`x="${arg:-$fallback}"`, `x="${arg:-$fallback}"`},
		{`x="${S:=$(uname -s)}"`, `x="${S:=$(uname -s)}"`},
	}
	for _, tc := range cases {
		got := maskBashSubstringVariableOffsets(tc.in)
		if got != tc.want {
			t.Fatalf("maskBashSubstringVariableOffsets(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if len(got) != len(tc.in) {
			t.Fatalf("mask changed length of %q: %q", tc.in, got)
		}
	}
}

// TestMaskBashSplicedTestArgs covers pyenv's `[ "$(osx_version)" "$@" ]`
// idiom: "$@" splices the test operator and operand in at runtime, but the
// grammar has no production for a two-operand test without an operator, and
// the ERROR swallows adjacent function definitions.
func TestMaskBashSplicedTestArgs(t *testing.T) {
	cases := []struct{ in, want string }{
		{`[ "$(osx_version)" "$@" ]`, `[ "$(osx_version)" = xx ]`},
		{`[ "$x" "$@" ]`, `[ "$x" = xx ]`},
		// Well-formed tests stay untouched.
		{`[ "$x" -eq 1 ]`, `[ "$x" -eq 1 ]`},
		{`[ -n "$@" ]`, `[ -n "$@" ]`},
	}
	for _, tc := range cases {
		got := maskBashSplicedTestArgs(tc.in)
		if got != tc.want {
			t.Fatalf("maskBashSplicedTestArgs(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if len(got) != len(tc.in) {
			t.Fatalf("mask changed length of %q: %q", tc.in, got)
		}
	}
}

// TestBashFunctionsSurviveUnsupportedExpansions asserts end to end that
// function definitions adjacent to the two masked constructs still extract
// (python-build's can_use_homebrew was silently missing before the masks).
func TestBashFunctionsSurviveUnsupportedExpansions(t *testing.T) {
	source := `#!/usr/bin/env bash
parse_options() {
  index=1
  while option="${arg:$index:1}"; do
    index=$(($index+1))
  done
}

is_mac() {
  [ "${_S:=$(uname -s)}" = "Darwin" ] || return 1
  [ $# -eq 0 ] || [ "$(osx_version)" "$@" ]
}

can_use_homebrew() {
  is_mac -ge 1014 || return 1
}
`
	entities, language, _ := TreeSitterParser{}.ParseWithStatus("scripts/build-tool", source)
	if language != "Bash" {
		t.Fatalf("language = %q, want Bash", language)
	}
	names := map[string]bool{}
	for _, entity := range entities {
		if entity.Kind == "function" {
			names[entity.Name] = true
		}
	}
	for _, want := range []string{"parse_options", "is_mac", "can_use_homebrew"} {
		if !names[want] {
			t.Fatalf("missing function %q; got %v", want, names)
		}
	}
}

// `exec 3<>/dev/tcp/...` read-write fd redirections have no grammar production
// and were swallowing adjacent definitions (entire-api mise-tasks/dev/db).
func TestBashFdDuplexRedirectMasked(t *testing.T) {
	src := "#!/bin/bash\n" +
		"probe_port() {\n" +
		"  if (exec 3<>\"/dev/tcp/localhost/$1\") 2>/dev/null; then\n" +
		"    return 0\n" +
		"  fi\n" +
		"}\n"
	entities, _, status := TreeSitterParser{}.ParseWithStatus("preflight", src)
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	found := false
	for _, e := range entities {
		if e.Name == "probe_port" {
			found = true
		}
	}
	if !found {
		t.Errorf("probe_port missing: %+v", entities)
	}
}

// Base-N arithmetic literals (`(( 10#$version <= 10#$max ))`) are rejected by
// the grammar; the `N#` prefix is masked (entire-api scripts/check-migration-order.sh).
func TestBashArithmeticBaseLiteralMasked(t *testing.T) {
	src := "#!/bin/bash\n" +
		"check() {\n" +
		"  if (( 10#$version <= 10#$max_base_version )); then\n" +
		"    return 1\n" +
		"  fi\n" +
		"}\n"
	entities, _, status := TreeSitterParser{}.ParseWithStatus("check.sh", src)
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	found := false
	for _, e := range entities {
		if e.Name == "check" {
			found = true
		}
	}
	if !found {
		t.Errorf("check missing: %+v", entities)
	}
}
