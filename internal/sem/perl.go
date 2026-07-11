package sem

import (
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	perlReceiverChainRe   = regexp.MustCompile(`(?:\$?[A-Za-z_]\w*|[A-Za-z_]\w*(?:::[A-Za-z_]\w*)*)[ \t]*(?:->[ \t]*[A-Za-z_]\w*[ \t]*(?:\([^()\n]*\))?[ \t]*)+`)
	perlReceiverSegmentRe = regexp.MustCompile(`->[ \t]*([A-Za-z_]\w*)`)
	// Both assignment leads accept a keywordless target (`$x = ...`, no
	// my/our/state) anchored to a line start or a non-newline whitespace char, so
	// a bare reassignment at column 0 is still recognised. Capture groups are
	// unchanged from the keyword-required form (group 1 = target, ctor group 2 =
	// package / chain group 2 = receiver, chain group 3 = tail), so the
	// offset-based span bookkeeping below is unaffected.
	perlCtorAssignRe  = regexp.MustCompile(`(?m)(?:^|[^\S\n])(?:(?:my|our|state)\s+)?\$([A-Za-z_]\w*)\s*=\s*([A-Z][A-Za-z0-9_]*(?:::[A-Za-z_]\w*)*)\s*->[ \t]*new\b`)
	perlChainAssignRe = regexp.MustCompile(`(?m)(?:^|[^\S\n])(?:(?:my|our|state)\s+)?\$([A-Za-z_]\w*)\s*=\s*\$([A-Za-z_]\w*)([^\n;]*)`)
	// perlAssignOpRe locates every write-shaped scalar-assignment lead: `$name`
	// (captured), optional whitespace (`\s*`, not `[ \t]*`, so a continuation-line
	// `=` still counts), an optional compound-assignment operator chunk, then `=`.
	// The chunk covers the multi-byte operators (`**= ||= //= &&= <<= >>=`) and the
	// single-byte ones (`+= -= *= /= %= .= x= |= &= ^=`); the empty-chunk case is a
	// plain `$name =`. The chunk char class deliberately excludes `<`, `>`, `!`,
	// and `=`, so no comparison operator (`<=`, `>=`, `!=`, `<=>`, `==`) can ever
	// form the lead. The reassignment-invalidation pass additionally filters the
	// byte immediately after the trailing `=` to reject `==`, `=~`, and `=>`, which
	// share the `=` byte but are not assignments.
	perlAssignOpRe = regexp.MustCompile(`\$([A-Za-z_]\w*)\s*(?:\*\*|\|\||//|&&|<<|>>|[-+*/%.x|&^])?=`)
	// perlListAssignRe matches a parenthesized group followed by `=` — a
	// list-assignment LHS (`($x, $y) = fetch()`, `my ($self) = @_`). Every scalar
	// inside the group is a write target. The byte after `=` is filtered the same
	// way as perlAssignOpRe so `($x) == 3` and `($x) => 1` are not writes.
	perlListAssignRe = regexp.MustCompile(`\(([^()\n]*)\)\s*=`)
	// perlScalarVarRe finds bare scalar variable names, used to pull the write
	// targets out of a list-assignment group.
	perlScalarVarRe = regexp.MustCompile(`\$([A-Za-z_]\w*)`)
	// perlForeachAliasRe / perlForAliasRe match foreach-loop aliasing, which binds
	// the loop variable to each element of the list (a write, not a read):
	// `foreach my $x (@list)` and `for my $x (@list)`. `for` requires the trailing
	// `(` to distinguish the foreach alias from a C-style `for (my $i = 0; ...)`.
	perlForeachAliasRe = regexp.MustCompile(`\bforeach\s+(?:my\s+)?\$([A-Za-z_]\w*)\b`)
	perlForAliasRe     = regexp.MustCompile(`\bfor\s+(?:my\s+)?\$([A-Za-z_]\w*)\s*\(`)
)

// perlReceiverCalls extracts terminal `$obj->method` call sites. Perl commonly
// omits parentheses for method calls (`$self->stash`, `$base->protocol`), so the
// generic receiver scanner only sees a subset of real call sites.
func perlReceiverCalls(block string) []receiverCall {
	stripped := stripPerlCodeLiteralsAndComments(block)
	var out []receiverCall
	seen := map[string]bool{}
	for _, loc := range perlReceiverChainRe.FindAllStringIndex(stripped, -1) {
		chain := stripped[loc[0]:loc[1]]
		// Mask argument-paren interiors so a nested receiver chain inside a hop's
		// arguments (`$url->base($req->url->base->clone)`) contributes no
		// segments: only the outer `->method` hops applied directly to the head
		// receiver are counted, and the terminal method is the last outer hop.
		topLevelChain := maskPerlParenthesizedArgumentContents(chain)
		segments := perlReceiverSegmentRe.FindAllStringSubmatch(topLevelChain, -1)
		if len(segments) == 0 {
			continue
		}
		method := segments[len(segments)-1][1]
		if method == "" || method == "new" {
			continue
		}
		receiver := strings.TrimSpace(strings.SplitN(topLevelChain, "->", 2)[0])
		receiver = strings.TrimPrefix(receiver, "$")
		if receiver == "" {
			continue
		}
		key := receiver + "." + method
		if seen[key] {
			continue
		}
		seen[key] = true
		// Hops records how many `->` segments the flattened chain carried. A
		// single-hop `$receiver->method` (Hops==1) invokes method directly on
		// receiver; a multi-hop chain (`$url->path->to_string`, Hops>=2) reports
		// the terminal method against the head receiver even though it runs on an
		// intermediate object, so the type-inference consumer skips those.
		out = append(out, receiverCall{Receiver: receiver, Method: method, Hops: len(segments)})
	}
	return out
}

// perlLocalVarTypes infers each local's package type from `my $x = Pkg->new`
// constructors and from fluent assignment chains on an already-typed receiver.
// hopResolvable(hop, pkgType) reports whether a method name `hop` is defined as
// a sub in the file of package `pkgType`; it gates the fluent rule (see below)
// and may be nil in pure-string unit tests, which forgoes fluent typing.
//
// A local keeps its inferred type only while every assignment to it in the
// block is one the typer itself modeled. If the block reassigns the local
// through anything the typer does not understand — `$x = build_widget()`, `$x =
// shift`, `$x = $h->{k}`, a compound assignment (`$x .= ...`, `$x ||= ...`), a
// list assignment (`($x, $y) = ...`), foreach aliasing (`foreach my $x (...)`),
// or a redeclaration to a different constructor package — the earlier inference
// is stale and the local is dropped from the map, so a later single-hop
// `$x->method` cannot be resolved against a package the value no longer holds
// (precision-first: a missing edge beats a wrong one). The scan is deliberately
// conservative: it invalidates on any write-shaped occurrence of the variable
// that does not fall inside a span the ctor/chain passes consumed, and never on
// a plain read (`foo($x)`, `$x->m`, string interpolation).
func perlLocalVarTypes(block string, hopResolvable func(hop, pkgType string) bool) map[string]string {
	stripped := stripPerlCodeLiteralsAndComments(block)
	out := map[string]string{}
	// consumedSpans records the [start,end) byte offsets, within stripped, of
	// every assignment the typer modeled (a ctor assignment that set a type, or a
	// fluent chain assignment that propagated one). The reassignment scan below
	// treats any `$name =` to a typed local that falls OUTSIDE these spans as an
	// untracked reassignment and drops the local.
	var consumedSpans [][2]int
	// ctorPkgs collects the distinct constructor packages bound to each local.
	// Two different packages (`$x = Foo->new; $x = Bar->new`) is ambiguous — both
	// are consumed ctor spans, so the reassignment scan cannot catch it — and the
	// local is dropped rather than left bound to whichever assignment ran last.
	ctorPkgs := map[string]map[string]bool{}
	recordCtor := func(dst, pkg string, start, end int) {
		out[dst] = pkg
		if ctorPkgs[dst] == nil {
			ctorPkgs[dst] = map[string]bool{}
		}
		ctorPkgs[dst][pkg] = true
		consumedSpans = append(consumedSpans, [2]int{start, end})
	}
	// Package-membership gate shared by the fluent chain-assign rule and the
	// chained-constructor rule below. It reports whether a depth-0 hop chain on a
	// receiver of type receiverType stays entirely inside receiverType's package:
	// a genuine fluent chain returns `$self` so every hop is a sub in the
	// receiver's file (`$base->base->userinfo` all live in Mojo/URL.pm), whereas
	// getter navigation returns a different object so some hop escapes the package
	// (`$tx->req->url`: `url` is not a Mojo::Transaction sub). At least two hops
	// are required — a single hop is an ambiguous getter-vs-fluent — and a nil
	// resolver (pure-string unit tests) forgoes the propagation. Only hops applied
	// directly to the receiver count: a getter argument that is itself a method
	// chain (`$url->path($u->host->port->scheme)`) lives inside argument parens at
	// depth >= 1 and must not inflate the hop count. The effective chain (up to a
	// depth-0 comma that starts a new same-line statement) must also be a PURE
	// outer receiver chain (perlOuterReceiverChainOnly): the >=2 hops have to
	// consume it with nothing trailing, so a mixed expression (`$url->base($req) +
	// $other->x`) whose depth-0 scan would otherwise see two hops is rejected — it
	// is not a fluent same-object assignment — while a trailing comma-separated
	// statement (`$y->a->b, $z = build()`) still types the LHS from `$y->a->b`.
	packageStableChain := func(chain, receiverType string) bool {
		effective := perlChainBeforeStatementComma(chain)
		if !perlOuterReceiverChainOnly(effective) {
			return false
		}
		hops := perlReceiverSegmentRe.FindAllStringSubmatch(perlDepthZeroChain(effective), -1)
		for _, hop := range hops {
			if hopResolvable == nil || !hopResolvable(hop[1], receiverType) {
				return false
			}
		}
		return true
	}
	// Constructor assignments. A bare `$x = Pkg->new` (optionally `->new(...)`)
	// types $x as Pkg. A constructor immediately chained into further hops
	// (`$x = Pkg->new->request`) is NOT typed from the constructor alone: the
	// returned object is whatever the trailing hops navigate to, not necessarily
	// Pkg. It types $x as Pkg only when every depth-0 hop after `new` stays inside
	// Pkg (the fluent-builder shape `Pkg->new->set(...)->set(...)`); if any hop
	// escapes the package — or there is a single ambiguous hop — $x is left
	// untyped rather than mistyped as the constructor class (the same
	// getter-navigation mistype the fluent chain-assign path guards against).
	for _, loc := range perlCtorAssignRe.FindAllStringSubmatchIndex(stripped, -1) {
		dst := stripped[loc[2]:loc[3]]
		pkg := stripped[loc[4]:loc[5]]
		rest := perlSkipCtorNewCall(stripped[loc[1]:])
		if strings.HasPrefix(rest, "->") {
			chain := rest
			if end := strings.IndexAny(chain, "\n;"); end >= 0 {
				chain = chain[:end]
			}
			if packageStableChain(chain, pkg) {
				recordCtor(dst, pkg, loc[0], loc[1])
			}
			continue
		}
		recordCtor(dst, pkg, loc[0], loc[1])
	}
	// The stripped block is immutable across passes, so scan the chain
	// assignments once and iterate the cached matches inside the fixed point.
	// The index form lets each typed assignment record its span as consumed.
	chainMatches := perlChainAssignRe.FindAllStringSubmatchIndex(stripped, -1)
	for changed := true; changed; {
		changed = false
		for _, m := range chainMatches {
			if len(m) < 8 {
				continue
			}
			dst := stripped[m[2]:m[3]]
			receiver := stripped[m[4]:m[5]]
			chain := stripped[m[6]:m[7]]
			if _, exists := out[dst]; exists {
				continue
			}
			receiverType := out[receiver]
			if receiverType == "" {
				continue
			}
			if !packageStableChain(chain, receiverType) {
				continue
			}
			out[dst] = receiverType
			// perlChainAssignRe's tail (group 3, `[^\n;]*`) greedily spans the whole
			// physical line, so a same-line trailing statement (`$y->a->b, $z =
			// build()`) is captured but NOT consumed by the typer. Record only up to
			// the end of the depth-0 hop chain the typer actually consumed, and punch
			// out each hop's argument-paren interior, so both the trailing `$z =`
			// write and a reassignment of another local nested inside a hop's args
			// (`$u->set($w = get())`) stay visible to the invalidation scan.
			hopEnd, argInteriors := perlHopChainEnd(chain)
			consumedSpans = append(consumedSpans, perlChainConsumedSpans(m[0], m[6], hopEnd, argInteriors)...)
			changed = true
		}
	}
	// Drop locals bound to more than one constructor package: the value's type
	// is ambiguous, and both assignments are consumed ctor spans so the
	// reassignment scan below would otherwise leave the stale binding in place.
	for dst, pkgs := range ctorPkgs {
		if len(pkgs) > 1 {
			delete(out, dst)
		}
	}
	// Invalidate any typed local the block also reassigns through a form the typer
	// did not model. The rule is not a list of assignment spellings but a single
	// question — does the variable occur in a write-shaped position outside the
	// spans the ctor/chain passes already consumed? Three write shapes are
	// recognised: (a) a generalized scalar assignment `$name <op>= ...` (plain or
	// compound), (b) a list-assignment LHS `(... $name ...) = ...`, and (c)
	// foreach aliasing `foreach my $name (...)`. Any of these, outside a consumed
	// span, drops the local. Reads never do: `$name->m`, `foo($name)`, and string
	// interpolation (already masked to spaces by stripCodeLiteralsAndComments)
	// carry no `= ` after the name and no enclosing `(...) =`, so they are ignored.
	// Over-invalidation is preferred to a wrong edge (precision-first).
	if len(out) > 0 {
		untracked := map[string]bool{}
		// markWrite drops a typed local when the write occurrence at [start,end) is
		// not inside a consumed ctor/chain span (which would be the assignment that
		// established the type, not an untracked reassignment).
		markWrite := func(name string, start, end int) {
			if _, typed := out[name]; !typed {
				return
			}
			if perlSpansContain(consumedSpans, start, end) {
				return
			}
			untracked[name] = true
		}
		// afterByteRejects reports whether the byte after a trailing `=` at index
		// end makes the match a non-assignment (`==`, `=~`, `=>`).
		afterByteRejects := func(end int) bool {
			if end < len(stripped) {
				switch stripped[end] {
				case '=', '~', '>':
					return true
				}
			}
			return false
		}
		// (a) Generalized scalar assignment: `$name` + optional operator chunk + `=`.
		for _, m := range perlAssignOpRe.FindAllStringSubmatchIndex(stripped, -1) {
			if afterByteRejects(m[1]) {
				continue
			}
			markWrite(stripped[m[2]:m[3]], m[0], m[1])
		}
		// (b) List-assignment LHS: every scalar inside a `(...) =` group is written.
		for _, m := range perlListAssignRe.FindAllStringSubmatchIndex(stripped, -1) {
			if afterByteRejects(m[1]) {
				continue
			}
			group := stripped[m[2]:m[3]]
			for _, v := range perlScalarVarRe.FindAllStringSubmatchIndex(group, -1) {
				markWrite(group[v[2]:v[3]], m[2]+v[0], m[2]+v[1])
			}
		}
		// (c) foreach aliasing binds the loop variable to each element (a write).
		for _, re := range []*regexp.Regexp{perlForeachAliasRe, perlForAliasRe} {
			for _, m := range re.FindAllStringSubmatchIndex(stripped, -1) {
				markWrite(stripped[m[2]:m[3]], m[2], m[3])
			}
		}
		for name := range untracked {
			delete(out, name)
		}
	}
	return out
}

// perlSpansContain reports whether [start,end) lies wholly inside one of the
// consumed assignment spans.
func perlSpansContain(spans [][2]int, start, end int) bool {
	for _, s := range spans {
		if start >= s[0] && end <= s[1] {
			return true
		}
	}
	return false
}

// perlSkipCtorNewCall trims leading whitespace and an optional balanced argument
// list of a constructor call (`->new` matched, then `( ... )`) from the text
// after a perlCtorAssignRe match, so the caller can test whether the constructor
// is immediately chained into further hops. `Pkg->new(cfg)->request` and
// `Pkg->new ->request` both leave a `->request` remainder; `Pkg->new(cfg);`
// leaves `;`.
func perlSkipCtorNewCall(rest string) string {
	rest = strings.TrimLeft(rest, " \t")
	if strings.HasPrefix(rest, "(") {
		depth := 0
		for i := 0; i < len(rest); i++ {
			switch rest[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					return strings.TrimLeft(rest[i+1:], " \t")
				}
			}
		}
		return "" // unbalanced: no discernible trailing chain
	}
	return rest
}

// perlDepthZeroChain drops the spans nested inside argument parentheses from a
// captured fluent chain, keeping only the characters at paren depth 0 (the
// `->method` hops applied directly to the receiver). Counting hops on the
// result keeps a single-hop getter whose argument is itself a method chain
// (`->path($u->host->port->scheme)`) from being mistaken for a multi-hop fluent
// assignment and mistyped as the receiver's package.
// perlHopChainEnd returns the byte offset within a chain-assign tail (group 3 of
// perlChainAssignRe: everything after `$receiver` up to the next `;`/newline) at
// which the contiguous depth-0 `->method(...)` hop chain ends, together with the
// chain-relative interior ranges of each hop's argument parens. The tail is
// greedy, so any same-line text past the hops (`->a->b, $z = build()`) is
// captured but not part of the chain the typer consumed; recording the consumed
// span only up to this offset keeps that trailing text visible to the
// write-shaped invalidation scan. The argument-paren interiors are reported so
// the caller can exclude them from the consumed span too: a write to a DIFFERENT
// local nested inside a hop's arguments (`$u->set($w = get())`) was never the
// typer's own write and must stay visible to that scan. A hop is `->`, an
// identifier, then an optional balanced argument list; whitespace between tokens
// is allowed.
func perlHopChainEnd(chain string) (int, [][2]int) {
	isIdentStart := func(b byte) bool {
		return b == '_' || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
	}
	isIdentChar := func(b byte) bool {
		return isIdentStart(b) || (b >= '0' && b <= '9')
	}
	skipSpace := func(i int) int {
		for i < len(chain) && (chain[i] == ' ' || chain[i] == '\t') {
			i++
		}
		return i
	}
	end := 0
	var argInteriors [][2]int
	for i := 0; ; {
		j := skipSpace(i)
		if j+1 >= len(chain) || chain[j] != '-' || chain[j+1] != '>' {
			break
		}
		j = skipSpace(j + 2)
		if j >= len(chain) || !isIdentStart(chain[j]) {
			break
		}
		for j < len(chain) && isIdentChar(chain[j]) {
			j++
		}
		// Optional balanced argument list applied directly to this hop.
		if openIdx := skipSpace(j); openIdx < len(chain) && chain[openIdx] == '(' {
			depth, closed, k := 0, false, openIdx
			for ; k < len(chain); k++ {
				switch chain[k] {
				case '(':
					depth++
				case ')':
					depth--
					if depth == 0 {
						k++
						closed = true
					}
				}
				if closed {
					break
				}
			}
			if !closed {
				break // unbalanced parens: do not consume past them
			}
			// k is the index just past the matching ')'; the paren interior is
			// (openIdx, k-1) — the bytes strictly between the outer parens.
			if k-1 > openIdx+1 {
				argInteriors = append(argInteriors, [2]int{openIdx + 1, k - 1})
			}
			j = k
		}
		end = j
		i = j
	}
	return end, argInteriors
}

// perlChainConsumedSpans returns the structural byte spans of a fluent
// chain-assign that the typer consumed, with each hop argument-paren interior
// PUNCHED OUT. The consumed protection exists so the typer's own `$dst =` lead
// is not read as a self-invalidating reassignment; a write to a different local
// nested inside a hop's arguments was never the typer's write, so excluding the
// arg interiors keeps those reassignments visible to the invalidation scan.
// spanStart is the match start (m[0]), chainBase is the chain tail's offset
// (m[6]), hopEnd is perlHopChainEnd's end offset within the chain, and
// argInteriors are the chain-relative interior ranges perlHopChainEnd emitted.
func perlChainConsumedSpans(spanStart, chainBase, hopEnd int, argInteriors [][2]int) [][2]int {
	spanEnd := chainBase + hopEnd
	var spans [][2]int
	cursor := spanStart
	for _, ai := range argInteriors {
		start := chainBase + ai[0]
		if start > cursor {
			spans = append(spans, [2]int{cursor, start})
		}
		if end := chainBase + ai[1]; end > cursor {
			cursor = end
		}
	}
	if cursor < spanEnd {
		spans = append(spans, [2]int{cursor, spanEnd})
	}
	return spans
}

func perlDepthZeroChain(chain string) string {
	var b strings.Builder
	depth := 0
	for i := 0; i < len(chain); i++ {
		switch chain[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteByte(chain[i])
			}
		}
	}
	return b.String()
}

// perlChainBeforeStatementComma truncates a captured chain-assign tail at the
// first comma that sits at bracket depth 0. Perl's `=` binds tighter than the
// comma operator, so `$x = $y->a->b, $z = build()` assigns `$y->a->b` to $x and
// then evaluates `$z = build()` as a separate comma-expression: only the text
// before that comma is $x's fluent value. Commas nested inside a hop's argument
// list (`->base($a, $b)`) are at depth >= 1 and are not statement separators.
func perlChainBeforeStatementComma(chain string) string {
	depth := 0
	for i := 0; i < len(chain); i++ {
		switch chain[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				return chain[:i]
			}
		}
	}
	return chain
}

func perlCallableForType(typeName, method string, candidates []SymbolRecord) (SymbolRecord, bool) {
	var matches []SymbolRecord
	for _, candidate := range candidates {
		if candidate.Language != "Perl" || candidate.Name != method {
			continue
		}
		if candidate.Kind != "function" && candidate.Kind != "method" {
			continue
		}
		if perlSymbolFileMatchesType(candidate.FilePath, typeName) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return SymbolRecord{}, false
}

func perlSymbolFileMatchesType(filePath, typeName string) bool {
	filePath = filepath.ToSlash(filePath)
	typeName = strings.TrimSpace(typeName)
	typePath := strings.ReplaceAll(typeName, "::", "/")
	if typePath == "" {
		return false
	}
	// Require a path-component boundary: `Mojo::URL` (-> Mojo/URL) matches
	// lib/Mojo/URL.pm but not a same-basename file at another depth. Without
	// the leading "/" a bare `Foo` would loosely match any *Foo.pm (e.g.
	// XFoo.pm), so anchor on a directory separator or the whole path.
	if filePath != typePath+".pm" && !strings.HasSuffix(filePath, "/"+typePath+".pm") {
		return false
	}
	// A bare type (no `::`) names a top-level package, whose file sits directly
	// under a source root (lib/Foo.pm, Foo.pm) — never under a namespace
	// directory. lib/Deep/Foo.pm is package Deep::Foo, not Foo: the Perl sub
	// record carries no package (statement-form `package Name;` leaves subs
	// unqualified), so an uppercase parent directory is the signal that the
	// file belongs to a deeper namespace and must not match a bare receiver
	// type. Perl namespace components are conventionally capitalised while
	// source roots (lib, blib, t, script) are lower-case.
	if !strings.Contains(typeName, "::") {
		if parent := path.Base(path.Dir(filePath)); parent != "." && parent != "/" && parent != "" {
			if c := parent[0]; c >= 'A' && c <= 'Z' {
				return false
			}
		}
	}
	return true
}

func stripPerlCodeLiteralsAndComments(content string) string {
	bytes := []byte(content)
	for i := 0; i < len(bytes); i++ {
		switch bytes[i] {
		case '"', '\'', '`':
			quote := bytes[i]
			for j := i + 1; j < len(bytes); j++ {
				if bytes[j] == '\n' || bytes[j] == '\r' {
					i = j
					break
				}
				if bytes[j] == '\\' {
					j++
					continue
				}
				if bytes[j] == quote {
					maskBytes(bytes, i, j+1)
					i = j
					break
				}
			}
		case '#':
			if !perlHashStartsComment(bytes, i) {
				continue
			}
			j := i + 1
			for j < len(bytes) && bytes[j] != '\n' && bytes[j] != '\r' {
				j++
			}
			maskBytes(bytes, i, j)
			i = j
		}
	}
	return string(bytes)
}

func perlOuterReceiverChainOnly(chain string) bool {
	masked := maskPerlParenthesizedArgumentContents(chain)
	i := 0
	segments := 0
	for {
		for i < len(masked) && (masked[i] == ' ' || masked[i] == '\t') {
			i++
		}
		if !strings.HasPrefix(masked[i:], "->") {
			break
		}
		i += 2
		for i < len(masked) && (masked[i] == ' ' || masked[i] == '\t') {
			i++
		}
		if i >= len(masked) || !perlIdentStartByte(masked[i]) {
			return false
		}
		i++
		for i < len(masked) && isASCIIIdentifierByte(masked[i]) {
			i++
		}
		for i < len(masked) && (masked[i] == ' ' || masked[i] == '\t') {
			i++
		}
		if i < len(masked) && masked[i] == '(' {
			close := findMatchingStaticDelimiter(masked, i, '(', ')')
			if close < 0 {
				return false
			}
			i = close + 1
		}
		segments++
	}
	for i < len(masked) && (masked[i] == ' ' || masked[i] == '\t') {
		i++
	}
	return i == len(masked) && segments >= 2
}

func maskPerlParenthesizedArgumentContents(content string) string {
	bytes := []byte(content)
	depth := 0
	start := -1
	for i, ch := range bytes {
		switch ch {
		case '(':
			if depth == 0 {
				start = i + 1
			}
			depth++
		case ')':
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 && start >= 0 {
				maskBytes(bytes, start, i)
				start = -1
			}
		}
	}
	if depth > 0 && start >= 0 {
		maskBytes(bytes, start, len(bytes))
	}
	return string(bytes)
}

func perlHashStartsComment(bytes []byte, pos int) bool {
	if pos == 0 {
		return true
	}
	if bytes[pos-1] == '$' {
		return false
	}
	if perlHashInHashDelimitedRegex(bytes, pos) {
		return false
	}
	if perlHashStartsRegexLiteral(bytes, pos) {
		return false
	}
	return true
}

func perlHashInHashDelimitedRegex(bytes []byte, pos int) bool {
	lineStart := pos
	for lineStart > 0 && bytes[lineStart-1] != '\n' && bytes[lineStart-1] != '\r' {
		lineStart--
	}
	line := string(bytes[lineStart : pos+1])
	for i := 0; i < len(line); i++ {
		for _, operator := range []string{"qr", "tr", "s", "m", "y"} {
			if !strings.HasPrefix(line[i:], operator+"#") {
				continue
			}
			if i > 0 && perlIdentByte(line[i-1]) {
				continue
			}
			segment := line[i:]
			if semi := strings.IndexByte(segment, ';'); semi >= 0 && i+semi < pos-lineStart {
				continue
			}
			hashes := strings.Count(segment, "#")
			if hashes == 0 {
				continue
			}
			limit := 2
			if operator == "s" || operator == "tr" || operator == "y" {
				limit = 3
			}
			if hashes <= limit {
				return true
			}
		}
	}
	return false
}

func perlHashStartsRegexLiteral(bytes []byte, pos int) bool {
	if pos == 0 {
		return false
	}
	opener := bytes[pos-1]
	if !perlRegexOpeningDelimiter(opener) {
		return false
	}
	lineStart := pos - 1
	for lineStart > 0 && bytes[lineStart-1] != '\n' && bytes[lineStart-1] != '\r' {
		lineStart--
	}
	prefix := strings.TrimRight(string(bytes[lineStart:pos-1]), " \t")
	if strings.HasSuffix(prefix, "=~") || strings.HasSuffix(prefix, "!~") {
		return true
	}
	if opener == '/' && perlRegexAtExpressionStart(prefix) {
		return true
	}
	for _, operator := range []string{"qr", "tr", "s", "m", "y"} {
		if !strings.HasSuffix(prefix, operator) {
			continue
		}
		start := len(prefix) - len(operator)
		if start == 0 || !perlIdentByte(prefix[start-1]) {
			return true
		}
	}
	return false
}

func perlRegexAtExpressionStart(prefix string) bool {
	trimmed := strings.TrimSpace(prefix)
	if trimmed == "" {
		return true
	}
	last := trimmed[len(trimmed)-1]
	switch last {
	case '(', '[', '{', ',', ';', '=', '!', '~', '?', ':':
		return true
	default:
		return false
	}
}

func perlRegexOpeningDelimiter(delimiter byte) bool {
	switch delimiter {
	case '/', '{', '(', '[', '<':
		return true
	default:
		return false
	}
}

func perlIdentByte(ch byte) bool {
	return ch == '_' || ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
}

func perlIdentStartByte(ch byte) bool {
	return ch == '_' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
}
