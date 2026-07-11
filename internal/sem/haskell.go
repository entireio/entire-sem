package sem

// Haskell call-site extraction. Haskell applies functions by juxtaposition —
// `fn arg1 arg2`, `Mod.fn arg`, `fn $ arg`, `x |> ...` has no parentheses at
// all — so the generic `name(` scanner sees almost no Haskell call sites. A
// dedicated scanner uses what a Haskell call site does carry: a qualified
// application `Alias.fn args` names the target module through the file's
// `import qualified ... as Alias` declarations (and a module's source file
// follows the dotted path by convention: Distribution/Simple/Configure.hs
// defines Distribution.Simple.Configure), and a bare `fn args` application can
// be matched against the enclosing file's own top-level bindings or its
// imports. Haskell also has lexical noise the generic stripper does not know:
// `--` line comments share their dashes with operators (`-->` is an operator,
// not a comment), `{- -}` block comments nest, and `'` serves character
// literals, primed identifiers (`foo'`), and Template Haskell quotes at once.
// Everything here is gated to Language == "Haskell" so no other language's
// extraction shifts.

import (
	"regexp"
	"sort"
	"strings"
)

// haskellCallSite is one application expression found in a Haskell block: a
// qualified application `Path.name args` (Path is the dotted qualifier, e.g.
// "BLC8" or "Distribution.Simple.Utils"), or a bare application `name args`
// when Path is empty.
type haskellCallSite struct {
	Path string
	Name string
}

var (
	// Uppercase-rooted dotted qualifier ending in a lowercase value name:
	// `Mod.fn`, `Mod.Sub.fn`. An Uppercase terminal is a constructor or module
	// reference, not a value, and is deliberately not matched.
	haskellQualifiedRe = regexp.MustCompile(`([A-Z][A-Za-z0-9_']*(?:\.[A-Z][A-Za-z0-9_']*)*)\.([a-z_][A-Za-z0-9_']*)`)
	// Anchored variant used when only a match at the start of the slice matters:
	// unanchored FindStringSubmatchIndex would scan to the next qualified name
	// anywhere ahead (potentially to EOF) before the caller discards it for not
	// starting at 0. Anchoring makes the negative case O(1) and is otherwise
	// identical (the caller already requires m[0] == 0).
	haskellQualifiedAnchoredRe = regexp.MustCompile(`^([A-Z][A-Za-z0-9_']*(?:\.[A-Z][A-Za-z0-9_']*)*)\.([a-z_][A-Za-z0-9_']*)`)
	haskellIdentRe             = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_']*`)
	haskellModuleRe            = regexp.MustCompile(`[A-Z][A-Za-z0-9_']*(?:\.[A-Z][A-Za-z0-9_']*)*`)
	haskellLowerRe             = regexp.MustCompile(`\b[a-z_][A-Za-z0-9_']*`)
	haskellImportRe            = regexp.MustCompile(`(?m)^import[ \t]`)
	// The first identifier after a `let` keyword is always the name being
	// bound (a value/function binding LHS or a do/list-comp `let`), never a
	// call. Requiring whitespace after `let` avoids matching primed names such
	// as `let'`; the brace form `let { name = ... }` puts a `{` between the
	// keyword and the leading binder, so it needs its own pattern.
	haskellLetBinderRe      = regexp.MustCompile(`\blet[ \t\r\n]+([a-z_][A-Za-z0-9_']*)`)
	haskellLetBraceBinderRe = regexp.MustCompile(`\blet[ \t\r\n]*\{[ \t\r\n]*([a-z_][A-Za-z0-9_']*)`)
	// Subsequent bindings in a single-line group are `; name =` (let/where) or
	// `; name <-` (do-bind); both bind name. The `=` guard excludes `==`.
	haskellSemiBinderRe = regexp.MustCompile(`;[ \t]*([a-z_][A-Za-z0-9_']*)[ \t]*(?:<-|=(?:[^=]|$))`)
	// A let/where group's pattern LHS runs from the keyword up to its binding
	// `=` and is entirely binders, so every lowercase identifier in that chunk
	// is a bound name — covering tuple/list/constructor destructuring
	// (`let (a, render) = ...`, `let [g] = ...`, `let (Just x) = ...`) that the
	// single-leading-identifier patterns above cannot reach. `[^=\n]` keeps the
	// chunk on one line and stops before the binding `=`.
	haskellLetPatternBinderRe = regexp.MustCompile(`\b(?:let|where)\b([^=\n]*)=`)
	// A `;`-separated continuation of a let/where group (`; (a, b) = ...`) with
	// the same whole-chunk-is-binders rule. `[^=\n<]` also excludes a
	// `;`-separated do-bind (`; x <- ...`), whose LHS is handled by the do-bind
	// pattern.
	haskellSemiPatternBinderRe = regexp.MustCompile(`;([^=\n<]*)=(?:[^=]|$)`)
)

// haskellKeyword reports Haskell reserved words. `_` is included: it matches
// the identifier syntax but is a wildcard, never a callable.
func haskellKeyword(word string) bool {
	switch word {
	case "case", "class", "data", "default", "deriving", "do", "else",
		"foreign", "if", "import", "in", "infix", "infixl", "infixr",
		"instance", "let", "module", "newtype", "of", "then", "type",
		"where", "_":
		return true
	}
	return false
}

func isHaskellIdentByte(b byte) bool {
	return b == '_' || b == '\'' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// isHaskellSymbolByte reports Haskell operator-symbol characters. A `--` run
// is a comment only when it is not glued to these on either side (`-->`,
// `!--` are operators), and binder-head terminators (`=`, `|`, `->`, `<-`,
// `::`) only count when they stand alone as a full operator token.
func isHaskellSymbolByte(b byte) bool {
	switch b {
	case '!', '#', '$', '%', '&', '*', '+', '.', '/', '<', '=', '>', '?',
		'@', '\\', '^', '|', '~', ':', '-':
		return true
	}
	return false
}

// stripHaskellCodeText masks the Haskell syntaxes whose payloads must never
// register as call sites: `--` line comments (only when the dash run is not
// part of an operator), `{- -}` block comments (which nest; pragmas
// `{-# ... #-}` are a special case of the same shape), `"..."` string
// literals (backslash escapes and string gaps), `'c'` character literals (a
// `'` trailing an identifier is a primed name, a `'` before an identifier is
// a Template Haskell quote — both are left alone), and CPP directive lines
// (`#if`, `#define` at column 0). String and character quotes themselves are
// kept so a masked literal still reads as an argument in application
// position. Newlines are preserved so offsets keep line context.
func stripHaskellCodeText(content string) string {
	bytes := []byte(content)
	for i := 0; i < len(bytes); i++ {
		switch bytes[i] {
		case '{':
			if i+1 >= len(bytes) || bytes[i+1] != '-' {
				continue
			}
			depth := 1
			j := i + 2
			for j < len(bytes) && depth > 0 {
				if j+1 < len(bytes) && bytes[j] == '{' && bytes[j+1] == '-' {
					depth++
					j += 2
					continue
				}
				if j+1 < len(bytes) && bytes[j] == '-' && bytes[j+1] == '}' {
					depth--
					j += 2
					continue
				}
				j++
			}
			maskBytes(bytes, i, j)
			i = j - 1
		case '-':
			if i+1 >= len(bytes) || bytes[i+1] != '-' {
				continue
			}
			if i > 0 && isHaskellSymbolByte(bytes[i-1]) {
				continue // `!--`, `<--`: part of an operator, not a comment
			}
			j := i + 2
			for j < len(bytes) && bytes[j] == '-' {
				j++
			}
			if j < len(bytes) && isHaskellSymbolByte(bytes[j]) {
				continue // `-->`: an operator, not a comment
			}
			for j < len(bytes) && bytes[j] != '\n' {
				j++
			}
			maskBytes(bytes, i, j)
			i = j - 1
		case '"':
			j := i + 1
			for j < len(bytes) {
				if bytes[j] == '\\' {
					j += 2 // escapes and string gaps (`\<newline>...\`)
					continue
				}
				if bytes[j] == '"' {
					break
				}
				j++
			}
			if j >= len(bytes) {
				j = len(bytes)
			}
			maskBytes(bytes, i+1, j) // keep the quotes: a literal is an argument
			i = j
		case '\'':
			if i > 0 && isHaskellIdentByte(bytes[i-1]) {
				continue // primed identifier `foo'` — part of the name
			}
			if i+2 < len(bytes) && bytes[i+1] == '\\' {
				// Escaped character literal: '\n', '\'', '\\', '\123', '\xFF'.
				j := i + 2
				for j < len(bytes) && j-i <= 6 && bytes[j] != '\'' && bytes[j] != '\n' {
					j++
				}
				if j < len(bytes) && bytes[j] == '\'' {
					maskBytes(bytes, i+1, j)
					i = j
				}
			} else if i+2 < len(bytes) && bytes[i+1] != '\'' && bytes[i+2] == '\'' {
				maskBytes(bytes, i+1, i+2)
				i += 2
			}
			// Otherwise a Template Haskell quote (`'name`, `''Type`) — leave it.
		case '#':
			if i == 0 || bytes[i-1] == '\n' {
				j := i + 1
				for j < len(bytes) && bytes[j] != '\n' {
					j++
				}
				maskBytes(bytes, i, j)
				i = j - 1
			}
		}
	}
	return string(bytes)
}

// haskellBinderRegions finds the binding-head spans of a stripped block —
// stretches of text where every identifier is a binder or pattern, not a
// call. Haskell's layout rule makes each binding, guard, signature,
// do-notation bind, and case alternative start its own line, so the head of a
// line up to the first stand-alone `=`, `|`, `<-`, `->`, or `::` at the
// line's own bracket depth is a binder region: `go acc x = ...` binds go,
// acc, and x; `res <- action arg` binds res (the call `action arg` sits after
// the `<-`); `Just v -> use v` binds v. The returned set collects the binder
// names so that later *uses* of a shadowing local (`i distPref` where `i` is
// a where-binding) do not resolve against a same-named top-level function.
func haskellBinderRegions(stripped string) (func(int) bool, map[string]bool) {
	type span struct{ start, end int }
	var spans []span
	names := map[string]bool{}
	lineStart := 0
	for i := 0; i <= len(stripped); i++ {
		if i < len(stripped) && stripped[i] != '\n' {
			continue
		}
		line := stripped[lineStart:i]
		if end, ok := haskellBinderHeadEnd(line); ok {
			spans = append(spans, span{lineStart, lineStart + end})
			for _, name := range haskellLowerRe.FindAllString(line[:end], -1) {
				if !haskellKeyword(name) {
					names[name] = true
				}
			}
		}
		lineStart = i + 1
	}
	// The spans are appended in increasing lineStart order and never overlap
	// (one span per physical line), so a binary search on the sorted starts
	// finds the only span that could contain `at` — O(log spans) per query
	// instead of a linear scan, which the two block-wide identifier sweeps call
	// once per identifier.
	starts := make([]int, len(spans))
	ends := make([]int, len(spans))
	for i, s := range spans {
		starts[i] = s.start
		ends[i] = s.end
	}
	inBinder := func(at int) bool {
		idx := sort.Search(len(starts), func(i int) bool { return starts[i] > at }) - 1
		return idx >= 0 && at < ends[idx]
	}
	return inBinder, names
}

// haskellHigherOrderBinderNames extends the block-wide binder set with the
// locally-bound names the physical-line binder scan cannot reach, for the sole
// use of the higher-order argument pass. A function handed as the first
// argument of `map`/`mapM_`/`filter`/… is reported as a call site, so a local
// that shadows a same-named top-level function must be excluded even when it is
// the leading binder of a braced or single-line `do`/`let` block
// (`do { fn <- ...; mapM_ fn }`, `let { f = ...; ... } in map f xs`) that the
// line-head scan steps over. This set is deliberately kept out of the
// bare-application resolver: registering these names block-wide there would
// suppress a genuine head-position call to a same-named imported function
// elsewhere in the block. Over-suppressing inside the higher-order pass only
// costs a higher-order edge, which the precision-first policy prefers to a
// wrong one.
func haskellHigherOrderBinderNames(stripped string, base map[string]bool) map[string]bool {
	names := make(map[string]bool, len(base))
	for name := range base {
		names[name] = true
	}
	add := func(name string) {
		if !haskellKeyword(name) {
			names[name] = true
		}
	}
	// `let`-group `=` binders: inline `let x = ... in`, the leading binder of a
	// brace-let `let { x = ...`, and `;`-separated continuations of a let/where
	// group.
	for _, m := range haskellLetBinderRe.FindAllStringSubmatch(stripped, -1) {
		add(m[1])
	}
	for _, m := range haskellLetBraceBinderRe.FindAllStringSubmatch(stripped, -1) {
		add(m[1])
	}
	for _, m := range haskellSemiBinderRe.FindAllStringSubmatch(stripped, -1) {
		add(m[1])
	}
	// Pattern-destructuring `=` binders (`let (a, render) = ...`, `let [g] = ...`,
	// `; (a, b) = ...`): the whole LHS chunk is binders, so add every lowercase
	// identifier in it. Over-inclusion here only costs a higher-order edge, never
	// emits a wrong one.
	addAll := func(chunk string) {
		for _, name := range haskellLowerRe.FindAllString(chunk, -1) {
			add(name)
		}
	}
	for _, m := range haskellLetPatternBinderRe.FindAllStringSubmatch(stripped, -1) {
		addAll(m[1])
	}
	for _, m := range haskellSemiPatternBinderRe.FindAllStringSubmatch(stripped, -1) {
		addAll(m[1])
	}
	// Arrow-preceded binding forms — do/list-comprehension generators
	// (`(fn, ys) <- pairs`), lambda parameters (`\(a, b) ->`), and
	// case/`\case`/multi-way-if alternatives (`case x of { Just f -> ... }`) —
	// bind their pattern variables on the LEFT of a `<-` or `->` at a bracket
	// depth the line-head scan cannot reach in the single-line brace form.
	// haskellArrowBinders collects them all in one linear pass.
	haskellArrowBinders(stripped, add)
	return names
}

// haskellArrowBinders reports, via add, every lowercase identifier bound on the
// LEFT of a `<-` or `->` arrow anywhere in the stripped block. It replaces the
// former per-arrow backward walk — which re-scanned a growing prefix for every
// arrow and so ran in O(n^2) on a long single-line span of arrows (a wide
// `foo :: A -> B -> ... -> Z` type signature) — with a single linear pass.
//
// The block is split into segments at the binder boundaries `;`, `{`, `|`, `\`,
// and newline (an arrow never binds a name across one of those). Within a
// segment an identifier is a binder iff the nearest arrow-or-`case`/`of` token
// to its right is an arrow: an intervening `case`/`of` marks the identifier as
// part of a case scrutinee (`case x of Pat -> …` — `x` is not a binder), not a
// pattern. That is exactly the union of the old per-arrow left-walk spans (with
// the scrutinee trimmed), computed in O(n). Over-inclusion only costs a
// higher-order edge, never emits a wrong one.
func haskellArrowBinders(stripped string, add func(string)) {
	flushSegment := func(seg string) {
		// One left-to-right tokenization into arrows, `case`/`of` markers, and
		// lowercase identifiers, then a right-to-left sweep so each identifier
		// sees its nearest following token in O(1).
		type htoken struct {
			arrow bool
			caseK bool
			name  string
		}
		var toks []htoken
		for i := 0; i < len(seg); {
			c := seg[i]
			switch {
			case (c == '-' && i+1 < len(seg) && seg[i+1] == '>') ||
				(c == '<' && i+1 < len(seg) && seg[i+1] == '-'):
				toks = append(toks, htoken{arrow: true})
				i += 2
			case c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
				j := i + 1
				for j < len(seg) {
					d := seg[j]
					if d == '_' || d == '\'' || (d >= 'a' && d <= 'z') || (d >= 'A' && d <= 'Z') || (d >= '0' && d <= '9') {
						j++
						continue
					}
					break
				}
				word := seg[i:j]
				switch {
				case word == "case" || word == "of":
					toks = append(toks, htoken{caseK: true})
				case word[0] == '_' || (word[0] >= 'a' && word[0] <= 'z'):
					toks = append(toks, htoken{name: word})
				}
				i = j
			default:
				i++
			}
		}
		nextIsArrow := false
		for k := len(toks) - 1; k >= 0; k-- {
			switch {
			case toks[k].arrow:
				nextIsArrow = true
			case toks[k].caseK:
				nextIsArrow = false
			case toks[k].name != "" && nextIsArrow:
				add(toks[k].name)
			}
		}
	}
	segStart := 0
	for i := 0; i <= len(stripped); i++ {
		if i == len(stripped) {
			flushSegment(stripped[segStart:i])
			break
		}
		switch stripped[i] {
		case ';', '{', '|', '\\', '\n':
			flushSegment(stripped[segStart:i])
			segStart = i + 1
		}
	}
}

// haskellBinderHeadEnd scans one physical line for the earliest stand-alone
// binder terminator (`=`, `|`, `<-`, `->`, `::`) at the line's opening
// bracket depth and returns the offset where the binder head ends. Operators
// that merely contain those characters (`==`, `>>=`, `=>`, `||`) do not
// terminate anything, and terminators nested in brackets (record syntax
// `f x{a = 1}`, lambdas in arguments) belong to the expression, not the
// line's head.
func haskellBinderHeadEnd(line string) (int, bool) {
	depth := 0
	for k := 0; k < len(line); k++ {
		switch line[k] {
		case '(', '[', '{':
			depth++
			continue
		case ')', ']', '}':
			depth--
			continue
		}
		if !isHaskellSymbolByte(line[k]) || depth != 0 {
			continue
		}
		if k > 0 && isHaskellSymbolByte(line[k-1]) {
			continue // interior of an operator run
		}
		r := k
		for r < len(line) && isHaskellSymbolByte(line[r]) {
			r++
		}
		switch line[k:r] {
		case "=", "|", "<-", "->", "::":
			return k, true
		}
		k = r - 1
	}
	return 0, false
}

// haskellArgumentFollows reports whether the text after offset `end` starts
// an application argument: an identifier or constructor (but not a keyword),
// a literal, `(`, `[`, or an application operator whose right side feeds the
// function (`fn $ arg`, `fn =<< action`, `fn <$> x`, `fn . g` composition).
// A following `=`, `::`, `,`, `)`, `->`, backtick (the name is an infix
// operand), or ordinary operator means the name was a definition, a record
// field, or a plain value reference, not an application. Whitespace may cross
// a newline — formatters wrap arguments — but only onto a line indented
// deeper than the applied name's own line; a following line at the same or
// lower indentation is a new declaration or binding under Haskell's layout
// rule, never an argument.
func haskellArgumentFollows(s string, end int) bool {
	i := end
	crossed := false
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		if s[i] == '\n' {
			crossed = true
		}
		i++
	}
	if i >= len(s) {
		return false
	}
	if crossed {
		nameLine := strings.LastIndexByte(s[:end], '\n') + 1
		indent := nameLine
		for indent < len(s) && (s[indent] == ' ' || s[indent] == '\t') {
			indent++
		}
		contLine := strings.LastIndexByte(s[:i], '\n') + 1
		if i-contLine <= indent-nameLine {
			return false
		}
	}
	switch c := s[i]; {
	case isHaskellSymbolByte(c):
		r := i
		for r < len(s) && isHaskellSymbolByte(s[r]) {
			r++
		}
		switch s[i:r] {
		case "$", "$!", ".", "=<<", "<$>", "<$!>", "<*>":
			return true
		}
		return false
	case c == '(' || c == '[' || c == '"' || c == '\'':
		return true
	case c >= '0' && c <= '9':
		return true
	case c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
		word := haskellIdentRe.FindString(s[i:])
		return !haskellKeyword(word)
	}
	return false
}

// haskellHeadPosition reports whether the name starting at `start` can be the
// head of an application, judged by the preceding non-whitespace token. In
// `traverse_ f xs` the argument `f` is followed by another argument and would
// look applied; what gives it away is what precedes it — an identifier, a
// literal, or a closing bracket means the name is itself a trailing argument,
// not a function being applied. Operators, opening brackets, separators,
// keywords (`then f x`, `let y = f x`), and start-of-block all leave the name
// in head position. When the preceding token sits on an earlier line, layout
// decides first: a name indented no deeper than that line's first token opens
// a new statement (`do` blocks put one statement per line, so the previous
// line's trailing `}` or identifier says nothing about this name), while a
// deeper-indented name continues the previous line's expression and is judged
// by the token before it like same-line text.
func haskellHeadPosition(s string, start int) bool {
	i := start - 1
	crossed := false
	for i >= 0 && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		if s[i] == '\n' {
			crossed = true
		}
		i--
	}
	if i < 0 {
		return true
	}
	if crossed {
		nameCol := start - (strings.LastIndexByte(s[:start], '\n') + 1)
		prevLine := strings.LastIndexByte(s[:i], '\n') + 1
		prevIndent := prevLine
		for prevIndent <= i && (s[prevIndent] == ' ' || s[prevIndent] == '\t') {
			prevIndent++
		}
		if nameCol <= prevIndent-prevLine {
			return true // a new statement under the layout rule
		}
	}
	switch c := s[i]; {
	case c == ')' || c == ']' || c == '}':
		return false // trailing argument after a bracketed argument
	case c == '"' || c == '\'':
		return false // trailing argument after a literal
	case c >= '0' && c <= '9':
		return false // trailing argument after a numeric literal
	case isHaskellIdentByte(c):
		ws := i
		for ws > 0 && isHaskellIdentByte(s[ws-1]) {
			ws--
		}
		return haskellKeyword(s[ws : i+1])
	}
	return true
}

// haskellOperatorApplied reports whether the name starting at `start` is
// applied through an operator that feeds it its argument from the left:
// `action >>= fn`, `x & fn`, `f . g` composition, and the Kleisli arrows.
// The mirror-image operators are deliberately not accepted: in `fn $ x` and
// `fn =<< m` the *left* side is the function (handled by
// haskellArgumentFollows), so a name after `$` or `=<<` is an argument.
func haskellOperatorApplied(s string, start int) bool {
	i := start - 1
	for i >= 0 && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i--
	}
	if i < 0 || !isHaskellSymbolByte(s[i]) {
		return false
	}
	rs := i
	for rs > 0 && isHaskellSymbolByte(s[rs-1]) {
		rs--
	}
	switch s[rs : i+1] {
	case ">>=", "&", ".", ">=>", "<=<":
		return true
	}
	return false
}

// haskellInfixApplied reports a backtick-infix application: in
// `x `combine` y` the name between backticks is the function being applied.
func haskellInfixApplied(s string, start, end int) bool {
	return start > 0 && s[start-1] == '`' && end < len(s) && s[end] == '`'
}

// haskellApplied combines the positional evidence: a name is an application
// head when something applies it — an argument follows, an operator feeds it,
// or backticks make it infix.
func haskellApplied(s string, start, end int) bool {
	if haskellInfixApplied(s, start, end) {
		return true
	}
	op := haskellOperatorApplied(s, start)
	if !haskellHeadPosition(s, start) && !op {
		return false
	}
	return op || haskellArgumentFollows(s, end)
}

func haskellFirstArgHigherOrder(word string) bool {
	switch word {
	case "all", "any", "filter", "find", "foldl", "foldl'", "foldr",
		"fmap", "map", "mapM", "mapM_", "sortOn", "traverse", "traverse_":
		return true
	}
	return false
}

func haskellHigherOrderArgSite(s string, end int, inBinder func(int) bool, binderNames map[string]bool) (haskellCallSite, bool) {
	i := skipHaskellSpace(s, end)
	if i >= len(s) {
		return haskellCallSite{}, false
	}
	paren := false
	if s[i] == '(' {
		paren = true
		i = skipHaskellSpace(s, i+1)
	}
	if i >= len(s) || inBinder(i) {
		return haskellCallSite{}, false
	}
	if m := haskellQualifiedAnchoredRe.FindStringSubmatchIndex(s[i:]); m != nil && m[0] == 0 {
		absEnd := i + m[1]
		if paren {
			j := skipHaskellSpace(s, absEnd)
			if j >= len(s) || s[j] != ')' {
				return haskellCallSite{}, false
			}
		}
		return haskellCallSite{Path: s[i+m[2] : i+m[3]], Name: s[i+m[4] : i+m[5]]}, true
	}
	if m := haskellIdentRe.FindStringIndex(s[i:]); m != nil && m[0] == 0 {
		name := s[i : i+m[1]]
		if name[0] >= 'A' && name[0] <= 'Z' {
			return haskellCallSite{}, false
		}
		if haskellKeyword(name) || binderNames[name] {
			return haskellCallSite{}, false
		}
		if paren {
			j := skipHaskellSpace(s, i+m[1])
			if j >= len(s) || s[j] != ')' {
				return haskellCallSite{}, false
			}
		}
		return haskellCallSite{Name: name}, true
	}
	return haskellCallSite{}, false
}

// haskellCallSites scans a Haskell block for application expressions,
// deduplicated and in deterministic order. Qualified applications
// (`Alias.fn args`) are matched anywhere in expression position; bare
// applications are reported for any lowercase identifier in application
// position that is not a keyword, a binder, or a use of a shadowing local
// binder — resolution decides which of them name real symbols.
//
// The higher-order first-argument pass (which treats `map f xs` as a call to
// `f`) fires only for the library combinator names in
// haskellFirstArgHigherOrder. hofUserDefined reports whether the workspace
// defines its own function/bind of that name; when it does, the name is
// suppressed from the HOF pass — the combinator heuristic must not run on a
// user function whose first argument is ordinary data (`find db key` would
// otherwise fabricate a call to `db`). It may be nil (pure-string unit tests),
// which forgoes the suppression. The bare/qualified passes are unaffected, so a
// call to the user's own combinator-named function still resolves normally.
func haskellCallSites(block string, hofUserDefined func(name string) bool) []haskellCallSite {
	stripped := stripHaskellCodeText(block)
	inBinder, binderNames := haskellBinderRegions(stripped)
	hoBinderNames := haskellHigherOrderBinderNames(stripped, binderNames)
	seen := map[haskellCallSite]bool{}
	qualifiedAt := map[int]bool{}
	for _, m := range haskellQualifiedRe.FindAllStringSubmatchIndex(stripped, -1) {
		for at := m[0]; at < m[1]; at++ {
			qualifiedAt[at] = true
		}
		if m[0] > 0 && (isHaskellIdentByte(stripped[m[0]-1]) || stripped[m[0]-1] == '.') {
			continue // mid-identifier or a longer dotted chain
		}
		if inBinder(m[0]) {
			continue // view patterns and pattern guards can apply, but a head is mostly binders
		}
		if !haskellApplied(stripped, m[0], m[1]) {
			continue // value reference or operator operand, not an application
		}
		seen[haskellCallSite{Path: stripped[m[2]:m[3]], Name: stripped[m[4]:m[5]]}] = true
	}
	for _, m := range haskellIdentRe.FindAllStringIndex(stripped, -1) {
		start, end := m[0], m[1]
		word := stripped[start:end]
		if !haskellFirstArgHigherOrder(word) || qualifiedAt[start] {
			continue
		}
		if hofUserDefined != nil && hofUserDefined(word) {
			// The workspace defines its own `word`, so this application is a call
			// to the user function, not the library combinator; its first argument
			// is data, not a callback. Skip the HOF site (the bare pass still
			// resolves the call to the user's function).
			continue
		}
		if inBinder(start) || hoBinderNames[word] {
			continue
		}
		if start > 0 {
			switch stripped[start-1] {
			case '.', '\'', '@', '~', '#', '\\':
				continue
			}
		}
		if !haskellApplied(stripped, start, end) {
			continue
		}
		if site, ok := haskellHigherOrderArgSite(stripped, end, inBinder, hoBinderNames); ok {
			seen[site] = true
		}
	}
	for _, m := range haskellIdentRe.FindAllStringIndex(stripped, -1) {
		start, end := m[0], m[1]
		word := stripped[start:end]
		if word[0] >= 'A' && word[0] <= 'Z' {
			continue // constructor or module reference
		}
		if haskellKeyword(word) || qualifiedAt[start] {
			continue
		}
		if inBinder(start) || binderNames[word] {
			continue // binder, parameter, or use of a shadowing local binding
		}
		if start > 0 {
			switch stripped[start-1] {
			case '.', '\'', '@', '~', '#', '\\':
				continue // record-dot field, TH quote, as-/lazy pattern, lambda binder
			}
		}
		if !haskellApplied(stripped, start, end) {
			continue
		}
		seen[haskellCallSite{Name: word}] = true
	}
	sites := make([]haskellCallSite, 0, len(seen))
	for site := range seen {
		sites = append(sites, site)
	}
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].Path != sites[j].Path {
			return sites[i].Path < sites[j].Path
		}
		return sites[i].Name < sites[j].Name
	})
	return sites
}

// haskellFileImports is what one Haskell file's import section says about
// name resolution: qualifier aliases (`import qualified D.S.Utils as Utils`
// makes `Utils.fn` mean `D.S.Utils.fn`; any imported module is also usable as
// its own full-path qualifier), explicitly imported unqualified names
// (`import D.Parsec (simpleParsec)`), and open imports (`import D.S.Utils`
// with no list — every export arrives unqualified, names unknowable).
type haskellFileImports struct {
	aliases       map[string]string
	explicitNames map[string][]string
	openModules   []string
}

// haskellImports parses the import declarations of a Haskell file. Layout
// makes each declaration start with `import` at column zero; an explicit list
// may span lines inside its parentheses.
func haskellImports(content string) haskellFileImports {
	stripped := stripHaskellCodeText(content)
	imports := haskellFileImports{
		aliases:       map[string]string{},
		explicitNames: map[string][]string{},
	}
	for _, loc := range haskellImportRe.FindAllStringIndex(stripped, -1) {
		i := skipHaskellSpace(stripped, loc[1])
		qualified := false
		if strings.HasPrefix(stripped[i:], "qualified") {
			qualified = true
			i = skipHaskellSpace(stripped, i+len("qualified"))
		}
		module := haskellModuleRe.FindString(stripped[i:])
		if module == "" || !strings.HasPrefix(stripped[i:], module) {
			continue
		}
		i += len(module)
		imports.aliases[module] = module
		hiding := false
		list := ""
		for {
			i = skipHaskellSpace(stripped, i)
			if i < len(stripped) && stripped[i] == '(' {
				if i == strings.LastIndexByte(stripped[:i], '\n')+1 {
					// A `(` at column 0 is a new top-level declaration
					// (an operator binding), not this import's list.
					break
				}
				start := i
				depth := 0
				for i < len(stripped) {
					if stripped[i] == '(' {
						depth++
					} else if stripped[i] == ')' {
						depth--
						if depth == 0 {
							i++
							break
						}
					}
					i++
				}
				list = stripped[start:i]
				continue
			}
			word := haskellIdentRe.FindString(stripped[i:])
			if word == "" || !strings.HasPrefix(stripped[i:], word) {
				break
			}
			switch word {
			case "qualified":
				qualified = true
			case "hiding":
				hiding = true
			case "as":
				i = skipHaskellSpace(stripped, i+len(word))
				alias := haskellModuleRe.FindString(stripped[i:])
				if alias == "" || !strings.HasPrefix(stripped[i:], alias) {
					word = ""
					break
				}
				imports.aliases[alias] = module
				i += len(alias)
				continue
			default:
				word = ""
			}
			if word == "" {
				break
			}
			i += len(word)
		}
		if qualified {
			continue
		}
		if list != "" && !hiding {
			for _, name := range haskellLowerRe.FindAllString(list, -1) {
				if !haskellKeyword(name) {
					imports.explicitNames[name] = append(imports.explicitNames[name], module)
				}
			}
			continue
		}
		// No list (or a hiding list): an open import — every export of the
		// module (minus the hidden names) arrives unqualified.
		imports.openModules = append(imports.openModules, module)
	}
	return imports
}

func skipHaskellSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

// haskellModuleFile reports whether a Haskell source file defines module
// `module` by path convention: the dotted module name mirrors the path under
// some source root, so Cabal/src/Distribution/Simple/Configure.hs defines
// Distribution.Simple.Configure.
func haskellModuleFile(path, module string) bool {
	if module == "" {
		return false
	}
	rel := strings.ReplaceAll(module, ".", "/")
	for _, ext := range []string{".hs", ".hsc"} {
		if path == rel+ext || strings.HasSuffix(path, "/"+rel+ext) {
			return true
		}
	}
	return false
}

// haskellCallableKind reports the symbol kinds a call site can land on:
// top-level bindings (kind "function") and class-member signatures (kind
// "method"). Types and classes are never call targets — a lowercase name in a
// type expression must not fabricate a call.
func haskellCallableKind(kind string) bool {
	return kind == "function" || kind == "method"
}

// haskellCallRelations resolves the application sites in a Haskell symbol's
// block. Qualified applications resolve through the file's import aliases to
// a callable defined in the module's conventional source file. Bare
// applications resolve down a conservative ladder: a same-file top-level
// binding first; then a name the file imports through an explicit import
// list, landing in that module's file; then a name defined in a module the
// file imports open (unqualified, no list); and last — because re-exports
// hide the defining module from the import section entirely — a
// workspace-unique function name, only when the file has open imports that
// could have delivered it, and never for single-letter names (those are
// overwhelmingly local helpers). Recursive calls to the enclosing symbol are
// skipped.
func haskellCallRelations(from SymbolRecord, block string, sameFile []SymbolRecord, symbolsByShortName map[string][]SymbolRecord, imports haskellFileImports) []RelationRecord {
	var relations []RelationRecord
	// A combinator name the workspace defines itself (`find`, `map`, ...) must
	// not trigger the higher-order first-argument pass: on the user's own
	// function the first argument is data, not a callback.
	hofUserDefined := func(name string) bool {
		for _, s := range symbolsByShortName[name] {
			if s.Language == "Haskell" && haskellCallableKind(s.Kind) {
				return true
			}
		}
		return false
	}
	for _, site := range haskellCallSites(block, hofUserDefined) {
		var targets []resolvedCallTarget
		if site.Path != "" {
			module := imports.aliases[site.Path]
			if module == "" && strings.Contains(site.Path, ".") {
				// A full dotted qualifier used without an import in this
				// file's section still names its module unambiguously.
				module = site.Path
			}
			for _, to := range symbolsByShortName[site.Name] {
				if to.ID == from.ID || to.Language != "Haskell" || !haskellCallableKind(to.Kind) {
					continue
				}
				if !haskellModuleFile(to.FilePath, module) {
					continue
				}
				targets = append(targets, resolvedCallTarget{
					SymbolRecord: to,
					Confidence:   0.9,
					Reason:       "qualified application resolved through import qualifier to defining module",
					Resolution:   "exact",
					Scope:        "module",
				})
			}
		} else {
			targets = haskellBareTargets(from, site.Name, sameFile, symbolsByShortName, imports)
		}
		detail := site.Name
		if site.Path != "" {
			detail = site.Path + "." + site.Name
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

// haskellBareTargets resolves a bare application name down the ladder
// described on haskellCallRelations.
func haskellBareTargets(from SymbolRecord, name string, sameFile []SymbolRecord, symbolsByShortName map[string][]SymbolRecord, imports haskellFileImports) []resolvedCallTarget {
	var local []resolvedCallTarget
	for _, to := range sameFile {
		if to.ID == from.ID || to.Kind != "function" || to.Name != name {
			continue
		}
		local = append(local, resolvedCallTarget{
			SymbolRecord: to,
			Confidence:   0.92,
			Reason:       "bare application resolved to same-file top-level binding",
			Resolution:   "exact",
			Scope:        "file",
		})
	}
	if len(local) > 0 {
		return local
	}
	var candidates []SymbolRecord
	for _, to := range symbolsByShortName[name] {
		if to.ID != from.ID && to.Language == "Haskell" && haskellCallableKind(to.Kind) {
			candidates = append(candidates, to)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	var imported []resolvedCallTarget
	for _, to := range candidates {
		for _, module := range imports.explicitNames[name] {
			if haskellModuleFile(to.FilePath, module) {
				imported = append(imported, resolvedCallTarget{
					SymbolRecord: to,
					Confidence:   0.86,
					Reason:       "bare application named in an explicit import list, resolved to the imported module",
					Resolution:   "import_resolved",
					Scope:        "module",
				})
				break
			}
		}
	}
	if len(imported) > 0 {
		return imported
	}
	var open []resolvedCallTarget
	for _, to := range candidates {
		for _, module := range imports.openModules {
			if haskellModuleFile(to.FilePath, module) {
				open = append(open, resolvedCallTarget{
					SymbolRecord: to,
					Confidence:   0.8,
					Reason:       "bare application resolved to a module imported unqualified",
					Resolution:   "import_resolved",
					Scope:        "module",
				})
				break
			}
		}
	}
	if len(open) > 0 {
		return open
	}
	// Re-exports (Distribution.Simple.Utils re-exporting writeFileAtomic from
	// Distribution.Utils.Generic) leave no import trace pointing at the
	// defining file. A workspace-unique function name is still safe to
	// resolve — but only when the file has open imports that could have
	// delivered the name, the name was imported somewhere (not a stray local),
	// and it is longer than one letter (single letters are local helpers).
	if len(name) >= 2 && (len(imports.openModules) > 0 || len(imports.explicitNames[name]) > 0) && len(candidates) == 1 {
		return []resolvedCallTarget{{
			SymbolRecord: candidates[0],
			Confidence:   0.7,
			Reason:       "bare application matched workspace-unique Haskell function name",
			Resolution:   "name_only",
			Scope:        "workspace",
		}}
	}
	return nil
}
