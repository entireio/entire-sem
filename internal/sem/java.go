package sem

// Java-specific call-site extraction. The generic scanners miss two dominant
// Java idioms (evidence: on square/retrofit the focus method
// Retrofit.Builder.build resolved 0/5 inbound and 1/3 outbound CALLS edges):
//
//   - Nested-type constructor fluent chains: the samples' main methods call
//     `new Retrofit.Builder().baseUrl(...).addConverterFactory(...).build()`.
//     The generic chain regexes only understand `new Type(...)` with an
//     undotted type name and a fixed chain depth, and their `[^)]*` argument
//     matching breaks on nested call arguments.
//   - Declared-type locals: `BuiltInFactories builtInFactories =
//     Platform.builtInFactories;` types the receiver of the subsequent
//     `builtInFactories.createDefaultCallAdapterFactories(...)` calls, but the
//     generic localVarTypes only understands constructor assignments.
//
// Everything here is gated to Language == "Java" by its callers and follows the
// same conservative rules as the other language files: capitalized type names
// only, conflicting bindings dropped, ambiguity resolved to nothing.

import (
	"regexp"
	"strings"
)

var (
	// Declared-type local at statement position: `Type name = ...;` or
	// `Type name;`, with optional `final`, a possibly nested/dotted type
	// (`Retrofit.Builder`), optional generics and array suffix. The
	// line-anchored match keeps expression fragments (casts, comparisons) out.
	javaTypedLocalRe = regexp.MustCompile(`(?m)^\s*(?:final\s+)?([A-Z]\w*(?:\.[A-Z]\w*)*(?:<[^<>;={}]*>)?(?:\[\])?)\s+([a-z_$][\w$]*)\s*[=;]`)
	// Constructor call with a possibly nested type: `new Retrofit.Builder(`.
	javaNewChainStartRe = regexp.MustCompile(`\bnew\s+([A-Z]\w*(?:\s*\.\s*[A-Z]\w*)*)\s*\(`)
	// One fluent-chain segment following a closed argument list: `.method(`.
	javaChainSegmentRe = regexp.MustCompile(`^\s*\.\s*([A-Za-z_$][\w$]*)\s*\(`)
)

// javaLocalVarTypes infers variable -> type from declared-type local
// declarations (`BuiltInFactories builtInFactories = Platform.builtInFactories;`),
// which the generic constructor-assignment scan cannot type. Dotted types keep
// their terminal segment; a name declared with two different types is dropped.
func javaLocalVarTypes(block string) map[string]string {
	stripped := stripCodeLiteralsAndComments(block)
	out := map[string]string{}
	conflicted := map[string]bool{}
	for _, m := range javaTypedLocalRe.FindAllStringSubmatch(stripped, -1) {
		name := m[2]
		typeName := lastTypeSegment(stripGenerics(strings.TrimSuffix(m[1], "[]")))
		if !isCapitalized(typeName) {
			continue
		}
		if existing, ok := out[name]; ok && existing != typeName {
			conflicted[name] = true
			continue
		}
		out[name] = typeName
	}
	for name := range conflicted {
		delete(out, name)
	}
	return out
}

// javaCtorChainCall is a `new Outer.Inner(args).m1(args)...mN(args)` fluent
// constructor chain: the constructed type (terminal segment), the outer-type
// qualifier when spelled, and the chained methods in call order.
type javaCtorChainCall struct {
	Qualifier string
	TypeName  string
	Methods   []string
	Detail    string
}

// javaConstructorChainCalls extracts fluent constructor chains of any depth,
// walking balanced argument lists so nested call arguments
// (`.addConverterFactory(GsonConverterFactory.create())`) do not truncate the
// chain the way the generic fixed-depth regexes do.
func javaConstructorChainCalls(block string) []javaCtorChainCall {
	stripped := stripCodeLiteralsAndComments(block)
	var out []javaCtorChainCall
	seen := map[string]bool{}
	for _, m := range javaNewChainStartRe.FindAllStringSubmatchIndex(stripped, -1) {
		typeRef := strings.Map(func(r rune) rune {
			if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
				return -1
			}
			return r
		}, stripped[m[2]:m[3]])
		open := m[1] - 1
		pos := matchingParen(stripped, open)
		if pos < 0 {
			continue
		}
		pos++
		var methods []string
		for {
			seg := javaChainSegmentRe.FindStringSubmatchIndex(stripped[pos:])
			if seg == nil {
				break
			}
			method := stripped[pos+seg[2] : pos+seg[3]]
			close := matchingParen(stripped, pos+seg[1]-1)
			if close < 0 {
				break
			}
			methods = append(methods, method)
			pos = close + 1
		}
		if len(methods) == 0 {
			continue
		}
		segments := strings.Split(typeRef, ".")
		typeName := segments[len(segments)-1]
		qualifier := ""
		if len(segments) > 1 {
			qualifier = segments[len(segments)-2]
		}
		detail := "new " + typeRef + "()." + strings.Join(methods, "().")
		if seen[detail] {
			continue
		}
		seen[detail] = true
		out = append(out, javaCtorChainCall{Qualifier: qualifier, TypeName: typeName, Methods: methods, Detail: detail})
	}
	return out
}

// javaResolveConstructedType resolves the constructed type of a fluent chain.
// A spelled outer-type qualifier (`new Retrofit.Builder()`) disambiguates
// same-named nested types by requiring the candidate to live in the same file
// as a type named like the qualifier (Java nests a class inside its outer
// class's file); without a qualifier the plain first-type-like lookup applies.
func javaResolveConstructedType(chain javaCtorChainCall, from SymbolRecord, symbolsByShortName map[string][]SymbolRecord) (SymbolRecord, bool) {
	if chain.Qualifier == "" {
		return firstTypeLikeNamedPreferFile(symbolsByShortName[chain.TypeName], chain.TypeName, from.FilePath)
	}
	qualifierFiles := map[string]bool{}
	for _, outer := range symbolsByShortName[chain.Qualifier] {
		if typeLikeKind(outer.Kind) && outer.Name == chain.Qualifier {
			qualifierFiles[outer.FilePath] = true
		}
	}
	var match SymbolRecord
	found := 0
	for _, cand := range symbolsByShortName[chain.TypeName] {
		if !typeLikeKind(cand.Kind) || cand.Name != chain.TypeName || !qualifierFiles[cand.FilePath] {
			continue
		}
		match = cand
		found++
	}
	if found == 1 {
		return match, true
	}
	return SymbolRecord{}, false
}
