package sem

// Erlang call-site extraction. The generic identifier scanner cannot use the
// information an Erlang call site carries: a remote call `mod:fun(Args)` names
// the target module explicitly (and by language convention module `mod` lives
// in `mod.erl`), while a bare `fun(Args)` call is module-local by language
// rule (barring the rarely used -import attribute). Erlang also has its own
// lexical noise the generic scanner trips over: capitalized identifiers are
// variables (`Fun(Args)` is a call through a variable, not to a named
// function), `?MACRO(...)` is preprocessor expansion, `#rec{...}` is record
// syntax, and comments start with `%`. Everything here is gated to
// Language == "Erlang" so no other language's extraction shifts.

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// erlangCallSite is one call expression found in an Erlang block: a remote
// call `Module:Name(...)`, or a local call `Name(...)` when Module is empty.
type erlangCallSite struct {
	Module string
	Name   string
}

var (
	// Atoms: lowercase-initial identifier (quoted atoms are masked away by
	// stripErlangCodeText and are not resolved).
	erlangRemoteCallRe = regexp.MustCompile(`\b([a-z][A-Za-z0-9_@]*)\s*:\s*([a-z][A-Za-z0-9_@]*)\s*\(`)
	erlangLocalCallRe  = regexp.MustCompile(`\b([a-z][A-Za-z0-9_@]*)\s*\(`)
	// `?MODULE:name(...)` always targets the enclosing module, so it is the one
	// macro-qualified call that resolves reliably — as a local call.
	erlangModuleMacroCallRe = regexp.MustCompile(`\?MODULE\s*:\s*([a-z][A-Za-z0-9_@]*)\s*\(`)
)

// erlangKeyword reports reserved words that can precede `(` without being a
// call (`fun (X) -> ...`, `case (X) of`, `not (A)`).
func erlangKeyword(name string) bool {
	switch name {
	case "fun", "case", "if", "receive", "after", "begin", "end", "try", "catch", "when", "of",
		"andalso", "orelse", "not", "and", "or", "xor", "band", "bor", "bxor", "bnot", "bsl", "bsr", "div", "rem":
		return true
	}
	return false
}

// stripErlangCodeText masks the Erlang syntaxes the generic stripper does not
// know: `%` line comments, `$c` character literals (whose payload could
// otherwise open a bogus string or comment mask, e.g. `$"` or `$%`), and
// string/quoted-atom literals, which may span lines. Newlines are preserved.
func stripErlangCodeText(content string) string {
	bytes := []byte(content)
	for i := 0; i < len(bytes); i++ {
		switch bytes[i] {
		case '$':
			// Character literal: `$c` or escaped `$\c`.
			end := i + 2
			if i+1 < len(bytes) && bytes[i+1] == '\\' {
				end = i + 3
			}
			maskBytes(bytes, i, end)
			i = end - 1
		case '%':
			j := i
			for j < len(bytes) && bytes[j] != '\n' && bytes[j] != '\r' {
				j++
			}
			maskBytes(bytes, i, j)
			i = j
		case '"', '\'':
			quote := bytes[i]
			j := i + 1
			for j < len(bytes) {
				if bytes[j] == '\\' {
					j += 2
					continue
				}
				if bytes[j] == quote {
					break
				}
				j++
			}
			if j >= len(bytes) {
				j = len(bytes) - 1
			}
			maskBytes(bytes, i, j+1)
			i = j
		}
	}
	return string(bytes)
}

// erlangCallSites scans an Erlang block for call expressions, deduplicated and
// in deterministic order. Remote calls yield (module, name) pairs; local calls
// yield bare names. Variables (capitalized, so never matched by the
// lowercase-initial atom pattern), `?MACRO(...)` expansions, `#rec` record
// syntax, and `fun name/arity` references (no argument list) are not calls.
func erlangCallSites(block string) []erlangCallSite {
	stripped := stripErlangCodeText(block)
	seen := map[erlangCallSite]bool{}
	remoteName := map[int]bool{} // start offsets of remote-call function names
	for _, m := range erlangRemoteCallRe.FindAllStringSubmatchIndex(stripped, -1) {
		if erlangCallPrefixIgnored(stripped, m[2]) {
			continue // e.g. `?mod:fun(` macro-built qualifier
		}
		seen[erlangCallSite{Module: stripped[m[2]:m[3]], Name: stripped[m[4]:m[5]]}] = true
		remoteName[m[4]] = true
	}
	for _, m := range erlangModuleMacroCallRe.FindAllStringSubmatchIndex(stripped, -1) {
		seen[erlangCallSite{Name: stripped[m[2]:m[3]]}] = true
	}
	for _, m := range erlangLocalCallRe.FindAllStringSubmatchIndex(stripped, -1) {
		start := m[2]
		name := stripped[m[2]:m[3]]
		// A `name(` at column 0 is a function clause head (`name(Args) ->`),
		// not a call site: definitions start at the left margin and body
		// expressions are indented. Without this, every folded clause head
		// would fabricate a call edge to same-named sibling-arity functions.
		if start == 0 || stripped[start-1] == '\n' || stripped[start-1] == '\r' {
			continue
		}
		if remoteName[start] || erlangKeyword(name) || erlangCallPrefixIgnored(stripped, start) {
			continue
		}
		seen[erlangCallSite{Name: name}] = true
	}
	sites := make([]erlangCallSite, 0, len(seen))
	for site := range seen {
		sites = append(sites, site)
	}
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].Module != sites[j].Module {
			return sites[i].Module < sites[j].Module
		}
		return sites[i].Name < sites[j].Name
	})
	return sites
}

// erlangCallPrefixIgnored reports whether the character preceding a candidate
// call name disqualifies it: `:` (the function part of a remote call, handled
// by the remote pattern), `?` (macro expansion), `#` (record syntax), `.` or
// `'` (masked quoted-atom remainder / dotted context).
func erlangCallPrefixIgnored(content string, start int) bool {
	if start == 0 {
		return false
	}
	switch content[start-1] {
	case ':', '?', '#', '.', '\'':
		return true
	}
	return false
}

// erlangFileDefinesModule reports whether an Erlang source file defines module
// `module`: by filename convention (`mod` lives in `mod.erl`) or by the
// extracted `-module(mod).` attribute symbol.
func erlangFileDefinesModule(path, module string, symbolsByShortName map[string][]SymbolRecord) bool {
	base := filepath.Base(path)
	if strings.HasSuffix(base, ".erl") && strings.TrimSuffix(base, ".erl") == module {
		return true
	}
	for _, s := range symbolsByShortName[module] {
		if s.Language == "Erlang" && s.Kind == "module" && s.FilePath == path {
			return true
		}
	}
	return false
}

// erlangCallRelations resolves the call sites in an Erlang function's block.
// Local calls resolve within the same file only (a bare call is module-local
// by language rule); remote calls resolve to functions defined in the file of
// the module named by the qualifier. Erlang identifies functions by
// name/arity and the parser folds each arity into its own symbol, so a name
// may match several arity symbols in the target module — each is emitted,
// matching the conservative name-level resolution used elsewhere. Recursive
// calls to the enclosing symbol itself are skipped; a same-name call landing
// on a sibling arity (`f(X) -> f(X, default)`) is a real edge and kept.
func erlangCallRelations(from SymbolRecord, block string, sameFile []SymbolRecord, symbolsByShortName map[string][]SymbolRecord) []RelationRecord {
	var relations []RelationRecord
	for _, site := range erlangCallSites(block) {
		var targets []resolvedCallTarget
		if site.Module == "" {
			for _, to := range sameFile {
				if to.ID == from.ID || to.Kind != "function" || to.Name != site.Name {
					continue
				}
				targets = append(targets, resolvedCallTarget{
					SymbolRecord: to,
					Confidence:   0.92,
					Reason:       "local call resolved to same-module function",
					Resolution:   "exact",
					Scope:        "file",
				})
			}
		} else {
			for _, to := range symbolsByShortName[site.Name] {
				if to.ID == from.ID || to.Language != "Erlang" || to.Kind != "function" {
					continue
				}
				if !erlangFileDefinesModule(to.FilePath, site.Module, symbolsByShortName) {
					continue
				}
				targets = append(targets, resolvedCallTarget{
					SymbolRecord: to,
					Confidence:   0.9,
					Reason:       "remote call resolved to the module named by the qualifier",
					Resolution:   "exact",
					Scope:        "module",
				})
			}
		}
		detail := site.Name
		if site.Module != "" {
			detail = site.Module + ":" + site.Name
		}
		for _, to := range targets {
			relations = append(relations, RelationRecord{
				RecordType:    "relation",
				FromID:        from.ID,
				ToID:          to.ID,
				Type:          "CALLS",
				Confidence:    to.Confidence,
				Reason:        to.Reason,
				RelationScope: to.Scope,
				Resolution:    to.Resolution,
				TargetKind:    "symbol",
				Evidence: []Evidence{{
					Kind:      "call_site",
					FilePath:  from.FilePath,
					StartLine: from.StartLine,
					EndLine:   from.EndLine,
					Detail:    detail,
				}},
				WarningCodes: []string{},
			})
		}
	}
	return relations
}
