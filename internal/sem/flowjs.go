package sem

import (
	"bytes"
	"regexp"
	"strings"
)

// Flow-typed JavaScript (.js with a `@flow` pragma or Flow-only type syntax)
// cannot be parsed by tree-sitter-javascript: every annotation (`fn(a: T): U`,
// `import type {X}`, `type X = ...`) produces ERROR nodes and, in large files,
// degrades the parse mid-file so later declarations are lost entirely
// (facebook/react loses beginWork, createContext, forwardRef, ...). Instead of
// stripping the annotations (structural type-expression scanning, fragile),
// Flow-sniffed files parse with the vendored TSX grammar: TypeScript syntax is
// a near-superset of Flow's, and the vendored tree-sitter-tsx additionally
// retains legacy Flow support for `?T` nullable types, `{| |}` exact objects,
// `(x: T)` casts, `import typeof`, and `Array<*>`. Probing facebook/react
// (1119 Flow-sniffed .js files): plain JS grammar fails ~986, raw TSX fails
// 204, TSX after maskFlowJavaScriptUnsupportedSyntax fails far fewer, and TSX
// beats the plain TypeScript grammar (204 vs 379 raw) because Flow files
// contain JSX. The language label stays "JavaScript", mirroring the .jsx
// routing.

// looksLikeFlowJavaScript reports whether a JavaScript file uses Flow types:
// either the canonical `@flow` pragma comment or unambiguous Flow-only
// top-level syntax (`import type {`, `import typeof`, `export type X`,
// `opaque type`). Plain JavaScript never matches, so non-Flow .js files keep
// the tree-sitter-javascript grammar and byte-identical extraction.
func looksLikeFlowJavaScript(content string) bool {
	return flowPragmaPattern.MatchString(content) || flowOnlySyntaxPattern.MatchString(content)
}

var (
	// `@noflow` also marks a Flow-syntax file (annotations present, checking
	// disabled — e.g. react's ReactForwardRef.js, which still declares
	// `forwardRef<Props, ElementType: React$ElementType>`).
	flowPragmaPattern     = regexp.MustCompile(`(?m)^\s*(?:/?\*+|//+)?\s*@(?:no)?flow(?:\s|$)`)
	flowOnlySyntaxPattern = regexp.MustCompile(`(?m)^\s*(?:import\s+type\s*\{|import\s+typeof\s|export\s+type\s+[A-Za-z_$]|(?:export\s+)?opaque\s+type\s)`)
)

var (
	// `opaque type X = Y` / `export opaque type X = Y`: blank the `opaque`
	// modifier so a plain TS type alias remains.
	flowOpaqueTypePattern = regexp.MustCompile(`(?m)^(\s*(?:export\s+)?)opaque(\s+type\b)`)
	// Opaque type supertype bounds `opaque type X: Super = Y`: blank the
	// `: Super` segment (must run before the `opaque` modifier is blanked).
	flowOpaqueSupertypePattern = regexp.MustCompile(`(?m)^(\s*(?:export\s+)?opaque\s+type\s+[\w$]+)(\s*:\s*[^=\n]*?)(\s*=)`)
	// Unnamed `void` parameters in declared function types
	// (`declare function f(void): void`): rename to `_`. A runtime function
	// can never declare a parameter literally named `void`.
	flowVoidParameterPattern = regexp.MustCompile(`(\bfunction\s+[\w$]+\s*\()void(\s*\))`)
	// Flow inline interface types (`type C = interface extends Element {...}`):
	// blank `interface extends` and join the supertype to the object body with
	// `&`, leaving a TS intersection at identical offsets. Named interface
	// declarations (`interface Foo extends Bar {`) do not match.
	flowInlineInterfacePattern = regexp.MustCompile(`\binterface\s+extends\s+([\w$.]+(?:<[^{}\n]*>)?)(\s)\{`)
	// `declare export function f(...)`: blank `declare` so a plain export
	// declaration remains (TS has `declare` but not `declare export`).
	flowDeclareExportPattern = regexp.MustCompile(`(?m)^(\s*)declare(\s+export\b)`)
	// Inexact object type marker `{x: T, ...}`: `...` directly before `,` or
	// `}` is never valid runtime spread syntax, so blanking it is safe.
	flowInexactObjectPattern = regexp.MustCompile(`\.\.\.(\s*[,}])`)
	// Property variance sigils `{+x: T}` / `{-x: T}` / `{+'x-y': T}` (also on
	// their own lines inside multi-line object types): blank the sigil. The
	// only value-context shape this can touch (`cond ?\n+x : y`) stays valid.
	flowPropertyVariancePattern = regexp.MustCompile(`(?m)([{,(]\s*|^\s*)([+-])((?:[A-Za-z_$][\w$]*|'[^'\n]*'|"[^"\n]*")\??\s*:|\[)`)
	// Type-parameter variance `<+C>` / `<-I,`: blank the sigil. The trailing
	// [,>:=] guard keeps ordinary comparisons (`a < +b`) from matching.
	flowTypeParamVariancePattern = regexp.MustCompile(`([<,]\s*)([+-])([A-Za-z_$][\w$]*\s*[,>:=])`)
	// Nullable function types `fn: ?(x: T) => U`: blank the `?`. The leading
	// delimiter guard excludes ternaries (an expression cannot end with one
	// of these delimiters, so `cond ? (a) : b` never matches).
	flowNullableFunctionTypePattern = regexp.MustCompile(`([:=|&,(<]\s*)\?(\()`)
	// Flow predicate functions `function f(x): boolean %checks {`.
	flowPredicatePattern = regexp.MustCompile(`%checks\b`)
	// Empty type-argument lists `MessageEvent<>`: TS requires at least one
	// argument. The identifier-character guard keeps JSX fragments (`<>`,
	// always preceded by a non-identifier character) intact.
	flowEmptyTypeArgumentsPattern = regexp.MustCompile(`([\w$])<>`)
	// Bare (unparenthesized) function-type parameters `type F = A => B;`,
	// `init?: I => S`, `next: React.Node => void`: TS requires a
	// parenthesized parameter list. Rewritten to `() =>` padded to the same
	// length. Contexts are limited to `(`/`,`/`<`/`=` and to `:` preceded by
	// an identifier character, so the return-type annotation of a real arrow
	// function (`(node: Element): null | Element => {`, where the colon
	// follows `)` and the union follows `|`) is never rewritten. A rewritten
	// single-identifier arrow parameter (`x => e`) stays a valid arrow
	// function as `() => e` — entity text is always sliced from the original
	// source, so extraction output is unchanged.
	flowBareFunctionTypeParamPattern = regexp.MustCompile(`((?:[(,<=]|=>|[\w$?]\s*:)\s*)([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)*)([ \t]*)=>`)
)

// maskFlowJavaScriptUnsupportedSyntax blanks, with same-length whitespace so
// every byte offset is preserved, the few Flow-only forms the vendored TSX
// grammar rejects. See the file comment for the routing rationale.
func maskFlowJavaScriptUnsupportedSyntax(content string) string {
	masked := flowOpaqueSupertypePattern.ReplaceAllStringFunc(content, func(m string) string {
		sub := flowOpaqueSupertypePattern.FindStringSubmatch(m)
		return sub[1] + strings.Repeat(" ", len(sub[2])) + sub[3]
	})
	masked = flowOpaqueTypePattern.ReplaceAllString(masked, "${1}      ${2}")
	masked = flowVoidParameterPattern.ReplaceAllString(masked, "${1}_   ${2}")
	masked = flowInlineInterfacePattern.ReplaceAllStringFunc(masked, func(m string) string {
		sub := flowInlineInterfacePattern.FindStringSubmatch(m)
		pad := len(m) - len(sub[1]) - len(sub[2]) - 1
		return strings.Repeat(" ", pad) + sub[1] + strings.Repeat(" ", len(sub[2])-1) + "&{"
	})
	masked = flowDeclareExportPattern.ReplaceAllString(masked, "${1}       ${2}")
	masked = flowInexactObjectPattern.ReplaceAllString(masked, "   ${1}")
	masked = flowPropertyVariancePattern.ReplaceAllString(masked, "${1} ${3}")
	masked = flowTypeParamVariancePattern.ReplaceAllString(masked, "${1} ${3}")
	masked = flowNullableFunctionTypePattern.ReplaceAllString(masked, "${1} ${2}")
	masked = flowPredicatePattern.ReplaceAllStringFunc(masked, func(m string) string {
		return strings.Repeat(" ", len(m))
	})
	masked = flowEmptyTypeArgumentsPattern.ReplaceAllString(masked, "${1}  ")
	masked = maskFlowTypeSpreadMembers(masked)
	masked = maskFlowComponentTypes(masked)
	masked = flowBareFunctionTypeParamPattern.ReplaceAllStringFunc(masked, func(m string) string {
		sub := flowBareFunctionTypeParamPattern.FindStringSubmatch(m)
		prefix := sub[1]
		pad := len(sub[2]) + len(sub[3]) - 2
		if pad < 0 {
			return m
		}
		return prefix + "()" + strings.Repeat(" ", pad) + "=>"
	})
	return maskFlowParenFunctionTypeParams(masked)
}

// maskFlowParenFunctionTypeParams blanks the interior of a parenthesized
// group directly preceding `=>` when it holds a Flow unnamed-parameter
// function type rather than a TS-parseable parameter list: `(() => void) =>
// () => void`, `(void | T) => void`, `(?React$Node) => ?ReactNodeList`. The
// interior is only blanked when it contains a type-ish marker (`=>`, `|`,
// `&`, `?`, `<`), so plain arrows `(a, b) =>`, destructuring `({x}) =>`, and
// TS-style `(x: T) =>` are untouched; even a false positive on a value arrow
// with marker-bearing default parameters degrades to a still-valid `() =>`.
// Groups containing quotes, backticks, or slashes are skipped because the
// backward paren matcher does not understand string/comment nesting.
func maskFlowParenFunctionTypeParams(content string) string {
	src := []byte(content)
	for i := 0; i+1 < len(src); i++ {
		if src[i] != '=' || src[i+1] != '>' {
			continue
		}
		j := i - 1
		for j >= 0 && (src[j] == ' ' || src[j] == '\t' || src[j] == '\r' || src[j] == '\n') {
			j--
		}
		if j < 0 || src[j] != ')' {
			continue
		}
		depth := 0
		k := j
		for ; k >= 0; k-- {
			if src[k] == ')' {
				depth++
			} else if src[k] == '(' {
				depth--
				if depth == 0 {
					break
				}
			}
		}
		if k < 0 {
			continue
		}
		if flowParenGroupIsArrowReturnType(src, k) {
			// `(params): ((v: number) => number) => body` — the group is the
			// parenthesized return type of an annotated arrow function, and
			// this `=>` is the arrow operator itself; blanking would leave an
			// invalid empty `(  )` type.
			continue
		}
		if !flowParenGroupNeedsMask(src[k+1 : j]) {
			continue
		}
		for m := k + 1; m < j; m++ {
			if src[m] != '\n' && src[m] != '\r' {
				src[m] = ' '
			}
		}
	}
	return string(src)
}

// flowParenGroupIsArrowReturnType reports whether the paren group opening at
// src[open] sits in return-type position of an arrow function: preceded
// (modulo whitespace) by `:` that is itself preceded by `)` — i.e.
// `(params): (GROUP) =>`. A fn-type annotation `name: (GROUP) => U` has an
// identifier before the colon instead.
func flowParenGroupIsArrowReturnType(src []byte, open int) bool {
	p := open - 1
	for p >= 0 && (src[p] == ' ' || src[p] == '\t' || src[p] == '\r' || src[p] == '\n') {
		p--
	}
	if p < 0 || src[p] != ':' {
		return false
	}
	p--
	for p >= 0 && (src[p] == ' ' || src[p] == '\t' || src[p] == '\r' || src[p] == '\n') {
		p--
	}
	return p >= 0 && src[p] == ')'
}

// flowComponentTypeStartPattern anchors Flow component types (`component(ref:
// RefSetter<T>)`, used as return types, in unions, and in `as component(...)`
// casts). The leading context restricts matches to type positions.
var flowComponentTypeStartPattern = regexp.MustCompile(`(\bas\s+|[:=|,<(?]\s*)component\s*\(`)

// maskFlowComponentTypes replaces each Flow `component(...)` type — including
// its parenthesized, possibly multi-line parameter list — with `any` padded
// by same-length whitespace (newlines kept).
func maskFlowComponentTypes(content string) string {
	if !strings.Contains(content, "component") {
		return content
	}
	src := []byte(content)
	for _, loc := range flowComponentTypeStartPattern.FindAllSubmatchIndex(src, -1) {
		start := loc[3] // after the context group: the `component` keyword
		open := loc[1] - 1
		depth := 0
		end := -1
		for i := open; i < len(src); i++ {
			if src[i] == '(' {
				depth++
			} else if src[i] == ')' {
				depth--
				if depth == 0 {
					end = i
					break
				}
			}
		}
		if end < 0 || bytes.ContainsAny(src[open:end], "\"'`") {
			continue
		}
		copy(src[start:], "any")
		for i := start + 3; i <= end; i++ {
			if src[i] != '\n' && src[i] != '\r' {
				src[i] = ' '
			}
		}
	}
	return string(src)
}

func flowParenGroupNeedsMask(interior []byte) bool {
	if bytes.ContainsAny(interior, "\"'`/") {
		return false
	}
	return bytes.Contains(interior, []byte("=>")) || bytes.ContainsAny(interior, "|&?<{")
}

// flowTypeSpreadMemberPattern matches a whole-line object-type spread member
// (`...ElementAndRendererID,`). Only applied inside `type X = {` blocks by
// maskFlowTypeSpreadMembers: the identical runtime spread syntax in object
// literals must stay.
var (
	flowTypeSpreadMemberPattern = regexp.MustCompile(`^(\s*)\.\.\.[\w$.]+,?(\s*)$`)
	// A multi-line object type opens either as a type alias (`type X = ... {`)
	// or as a destructured-parameter annotation (`}: {`).
	flowTypeBlockStartPattern = regexp.MustCompile(`^\s*(?:(?:declare\s+)?(?:export\s+)?type\s[^=\n]*=.*\{[^}]*|\}\s*:\s*\{\s*)$`)
)

// maskFlowTypeSpreadMembers blanks `...Other,` members of multi-line object
// type aliases (Flow object-type spread; TS object types have no spread).
// Tracks brace depth from a `type X = {`-opening line until it closes.
func maskFlowTypeSpreadMembers(content string) string {
	if !strings.Contains(content, "...") {
		return content
	}
	lines := strings.SplitAfter(content, "\n")
	depth := 0
	for i, line := range lines {
		text, newline := splitLineEnding(line)
		if depth == 0 {
			if flowTypeBlockStartPattern.MatchString(text) {
				// Leading close-braces on the opener line (`}: {`) belong to
				// the preceding destructuring pattern, not this block.
				opened := strings.TrimLeft(text, " \t}")
				depth = strings.Count(opened, "{") - strings.Count(opened, "}")
				if depth < 0 {
					depth = 0
				}
			}
			continue
		}
		if flowTypeSpreadMemberPattern.MatchString(text) {
			lines[i] = maskLineText(text) + newline
		}
		depth += strings.Count(text, "{") - strings.Count(text, "}")
		if depth < 0 {
			depth = 0
		}
	}
	return strings.Join(lines, "")
}
