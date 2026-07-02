package sem

// Kotlin-specific call-site extraction. The generic scanners miss the dominant
// Kotlin call idioms (evidence: on square/okhttp the focus method
// RealWebSocket.failWebSocket resolved 5/5 inbound but 0/4 outbound edges):
//
//   - Class properties (`private var taskQueue = taskRunner.newQueue()`,
//     `internal val listener: WebSocketListener` primary-constructor
//     properties) have no var-type source, so `taskQueue.execute(...)` and
//     `listener.onFailure(...)` never resolve. The property's type is declared
//     at the class level: explicit `val name: Type` annotations, constructor
//     val/var parameters, or a factory initializer whose declared return type
//     is unambiguous in the workspace.
//   - Declared-type locals (`val writerToClose: WebSocketWriter?`) are missed
//     by localVarTypes, which only understands constructor assignments.
//   - Safe calls (`socket?.closeQuietly()`) and trailing-lambda calls
//     (`taskQueue.execute { ... }`, bare `runTask { ... }`) never match
//     receiverCallRe / callLikeIdentifiers, which require a literal `.` and a
//     literal `(` after the method name.
//   - Top-level extension functions (`fun Closeable.closeQuietly()`) are not
//     members of any container, so member lookup on the receiver's type can
//     never find them.
//
// Everything in this file is gated to Language == "Kotlin" by its callers so
// the other languages keep their existing behavior. The extractors follow the
// same conservative straight-line rules as ruby.go/php.go: single-assignment
// tracking, capitalized type names only, conflicting bindings dropped,
// ambiguous lookups resolved to nothing rather than guessed.

import (
	"regexp"
	"strings"
)

var (
	// receiver.method( / receiver?.method( / receiver!!.method( call sites, also
	// accepting a trailing-lambda `{` where the argument list would start
	// (`taskQueue.execute { ... }` has no parentheses at all).
	kotlinReceiverCallRe = regexp.MustCompile(`\b([A-Za-z_]\w*)\s*(?:\?\.|!!\.|\.)\s*([A-Za-z_]\w*)\s*([({])`)
	// Bare trailing-lambda call `name { ... }` (no receiver, no parentheses).
	kotlinBareLambdaCallRe = regexp.MustCompile(`\b([A-Za-z_]\w*)[ \t]*\{`)
	// val/var declarations with an explicit type annotation.
	kotlinTypedDeclRe = regexp.MustCompile(`\b(?:val|var)\s+([A-Za-z_]\w*)\s*:\s*([A-Za-z_][\w.]*)`)
	// Class-body property declarations start with at least one modifier keyword,
	// which locals essentially never carry; this keeps method-body locals out of
	// the class-level property map.
	kotlinModifiedPropTypedRe = regexp.MustCompile(`(?m)^[ \t]*(?:(?:public|protected|private|internal|lateinit|open|final|override|const)\s+)+(?:val|var)\s+([A-Za-z_]\w*)\s*:\s*([A-Za-z_][\w.]*)`)
	// Modifier-prefixed property with an initializer instead of a type
	// annotation: `private var taskQueue = taskRunner.newQueue()` or
	// `private val lock = ReentrantLock()`.
	kotlinModifiedPropInitRe = regexp.MustCompile(`(?m)^[ \t]*(?:(?:public|protected|private|internal|open|final|override)\s+)+(?:val|var)\s+([A-Za-z_]\w*)\s*=\s*([^\n]+)`)
	// Primary-constructor header: `class Name(` with optional modifiers and an
	// optional `internal constructor(` between the name and the parameter list.
	kotlinClassHeaderRe = regexp.MustCompile(`\b(?:class|interface)\s+([A-Za-z_]\w*)[^({\n]*\(`)
	// One primary-constructor parameter: optional annotations, optional
	// visibility, optional val/var (which promotes it to a property), then
	// `name: Type`.
	kotlinCtorParamRe = regexp.MustCompile(`^(?:@\w+(?:\([^)]*\))?\s*)*(?:(?:public|protected|private|internal|override)\s+)*(val\s+|var\s+)?([A-Za-z_]\w*)\s*:\s*([A-Za-z_][\w.]*)`)
	// Constructor-call initializer: `= Klass(...)`.
	kotlinCtorInitRe = regexp.MustCompile(`^([A-Z]\w*)\s*\(`)
	// Factory-call initializer: `= receiver.factory(...)`.
	kotlinFactoryInitRe = regexp.MustCompile(`^([a-z_]\w*)\s*\.\s*([A-Za-z_]\w*)\s*\(`)
	// Extension-function signature: `fun Closeable.closeQuietly(`, including
	// generic receivers (`fun <T> List<T>.foo(`) and nullable ones.
	kotlinExtensionFunRe = regexp.MustCompile(`\bfun\s+(?:<[^>]*>\s+)?([A-Za-z_][\w.]*(?:<[^<>]*>)?\??)\s*\.\s*([A-Za-z_]\w*)\s*\(`)
	// Chained receiver call `base.property.method(...)`, accepting the safe-call
	// and non-null-asserted operators at each hop and a trailing-lambda `{`.
	// Base and property must be lowercase (values, not type references); the
	// leading guard keeps the match from starting mid-selector, so `a.b.c.m()`
	// never binds b as a base.
	kotlinChainedReceiverCallRe = regexp.MustCompile(`(?:^|[^\w.$])([a-z_]\w*)\s*(?:\?\.|!!\.|\.)\s*([a-z_]\w*)\s*(?:\?\.|!!\.|\.)\s*([A-Za-z_]\w*)\s*[({]`)
	// A function-type parameter's type: `suspend (ApplicationCall, HttpStatusCode)
	// -> Unit`, optionally with a lambda receiver (`suspend StatusContext.(...)`).
	// Nested function types (parenthesized parameter types) intentionally fail
	// the match: their positional binding is not worth guessing.
	kotlinFunctionTypeRe = regexp.MustCompile(`^(?:suspend\s+)?(?:[A-Za-z_][\w.]*(?:<[^<>]*>)?\s*\.\s*)?\(([^()]*)\)\s*->`)
)

// kotlinLambdaKeywords are bare words before `{` that are never call sites:
// control flow and declaration keywords whose blocks are plain syntax, plus the
// ubiquitous stdlib lambda-takers whose targets would be noise (a repo symbol
// shadowing `let`/`run` cannot be told apart from the stdlib call).
var kotlinLambdaKeywords = map[string]bool{
	"else": true, "try": true, "finally": true, "do": true, "when": true,
	"init": true, "companion": true, "object": true, "class": true,
	"interface": true, "enum": true, "fun": true, "val": true, "var": true,
	"get": true, "set": true, "where": true, "constructor": true,
	"suspend": true, "in": true, "is": true, "by": true, "catch": true,
	"run": true, "let": true, "also": true, "apply": true, "with": true,
	"use": true, "repeat": true, "lazy": true, "forEach": true, "map": true,
	"filter": true, "takeIf": true, "takeUnless": true, "synchronized": true,
	"withLock": true, "runCatching": true,
}

// stripKotlinCodeText extends the generic literal/comment stripper with raw
// (triple-quoted) strings, whose bodies span lines: the generic stripper resets
// its string state at each newline, so a multi-line `"""..."""` body would
// otherwise leak prose (and any `name(` inside it) into the call scanners.
// Length and line structure are preserved. Single-line strings, `${...}`
// templates inside them, and `//`/`/* */` comments are handled by the generic
// stripper.
func stripKotlinCodeText(content string) string {
	return stripCodeLiteralsAndComments(maskKotlinRawStrings(content))
}

// maskKotlinRawStrings blanks `"""..."""` raw-string literals, including the
// delimiters, preserving newlines.
func maskKotlinRawStrings(content string) string {
	bytes := []byte(content)
	for i := 0; i+2 < len(bytes); i++ {
		if bytes[i] != '"' || bytes[i+1] != '"' || bytes[i+2] != '"' {
			continue
		}
		j := i + 3
		for j+2 < len(bytes) && !(bytes[j] == '"' && bytes[j+1] == '"' && bytes[j+2] == '"') {
			j++
		}
		if j+2 < len(bytes) {
			j += 3
		} else {
			j = len(bytes)
		}
		maskBytes(bytes, i, j)
		i = j - 1
	}
	return string(bytes)
}

// kotlinReceiverCalls extracts `receiver.method(...)` call sites, accepting the
// safe-call (`?.`) and non-null-asserted (`!!.`) operators and trailing-lambda
// invocations (`receiver.method { ... }`), none of which match the generic
// receiverCallRe.
func kotlinReceiverCalls(block string) []receiverCall {
	stripped := stripKotlinCodeText(block)
	var out []receiverCall
	seen := map[string]bool{}
	for _, m := range kotlinReceiverCallRe.FindAllStringSubmatchIndex(stripped, -1) {
		receiver := stripped[m[2]:m[3]]
		method := stripped[m[4]:m[5]]
		key := receiver + "." + method
		if receiver == "" || method == "" || seen[key] {
			continue
		}
		args := ""
		if open := m[6]; stripped[open] == '(' {
			if close := matchingParen(stripped, open); close > open {
				args = stripped[open+1 : close]
			}
		}
		seen[key] = true
		out = append(out, receiverCall{Receiver: receiver, Method: method, Args: args})
	}
	return out
}

// kotlinBareLambdaCallIdentifiers returns bare `name { ... }` trailing-lambda
// call sites, which carry no parentheses and are invisible to the generic
// `name(` scanner. Keywords, stdlib scope functions, declaration names
// (`class Foo {`), supertype lists (`object : Callback {`), and dotted
// selectors (handled by the receiver path) are excluded; resolution precision
// comes from the caller only emitting an edge when the name matches a
// workspace symbol.
func kotlinBareLambdaCallIdentifiers(content string) map[string]struct{} {
	stripped := stripKotlinCodeText(content)
	out := map[string]struct{}{}
	for _, m := range kotlinBareLambdaCallRe.FindAllStringSubmatchIndex(stripped, -1) {
		name := stripped[m[2]:m[3]]
		if kotlinLambdaKeywords[name] || kotlinBareLambdaContextExcluded(stripped, m[2]) {
			continue
		}
		out[name] = struct{}{}
	}
	return out
}

// kotlinBareLambdaContextExcluded reports whether the word starting at start
// cannot be a bare trailing-lambda call site: dotted/safe-call selectors
// (receiver path), annotations, supertype and type-annotation positions, and
// words directly preceded by a declaration keyword (`class Foo {`).
func kotlinBareLambdaContextExcluded(stripped string, start int) bool {
	for i := start - 1; i >= 0; i-- {
		switch stripped[i] {
		case ' ', '\t':
			continue
		case '.', '?', ':', ',', '@', '>', '<':
			return true
		}
		break
	}
	for _, keyword := range []string{"class", "interface", "object", "enum", "fun", "val", "var", "companion", "is", "as"} {
		if rubyWordBefore(stripped, start, keyword) {
			return true
		}
	}
	return false
}

// kotlinTypeName normalizes a matched type reference to the terminal,
// capitalized type segment (`okio.Socket` -> Socket); nullability markers and
// generics never make it into the match. Empty when the segment is not a
// plausible class name.
func kotlinTypeName(ref string) string {
	name := lastTypeSegment(ref)
	if !isCapitalized(name) {
		return ""
	}
	return name
}

// kotlinLocalVarTypes infers variable -> type from declared-type local
// declarations (`val writerToClose: WebSocketWriter?`). Constructor
// assignments (`val w = WebSocketWriter(...)`) are already handled by the
// generic localVarTypes. A name declared with two different types is dropped.
func kotlinLocalVarTypes(block string) map[string]string {
	stripped := stripKotlinCodeText(block)
	out := map[string]string{}
	conflicted := map[string]bool{}
	for _, m := range kotlinTypedDeclRe.FindAllStringSubmatch(stripped, -1) {
		name, typeName := m[1], kotlinTypeName(m[2])
		if typeName == "" {
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

// kotlinPropertyTypes infers property -> type for a Kotlin file from its cheap
// declared sources: primary-constructor val/var parameters, modifier-prefixed
// class-body declarations with explicit types, and modifier-prefixed
// initializers (`= Klass(...)` constructors, `= receiver.factory()` calls
// whose factory's declared return type is workspace-unique). Kotlin files may
// hold several classes; a property name bound to two different types is
// dropped rather than guessed.
func kotlinPropertyTypes(content string, returnTypesBySymbolNameAndFile map[string]map[string][]string) map[string]string {
	stripped := stripKotlinCodeText(content)
	out := map[string]string{}
	conflicted := map[string]bool{}
	record := func(name, typeName string) {
		if name == "" || typeName == "" {
			return
		}
		if existing, ok := out[name]; ok && existing != typeName {
			conflicted[name] = true
			return
		}
		out[name] = typeName
	}
	// Primary-constructor parameters. Plain (non-val/var) parameters are not
	// properties, but they are the only names a property initializer can
	// reference besides other properties, so they participate as factory
	// receivers below.
	ctorParamTypes := map[string]string{}
	for _, loc := range kotlinClassHeaderRe.FindAllStringSubmatchIndex(stripped, -1) {
		open := loc[1] - 1
		close := matchingParen(stripped, open)
		if close < 0 {
			continue
		}
		for _, param := range splitTopLevelCommas(stripped[open+1 : close]) {
			m := kotlinCtorParamRe.FindStringSubmatch(strings.TrimSpace(param))
			if m == nil {
				continue
			}
			propertyMarker, name, typeName := m[1], m[2], kotlinTypeName(m[3])
			if typeName == "" {
				continue
			}
			ctorParamTypes[name] = typeName
			if propertyMarker != "" {
				record(name, typeName)
			}
		}
	}
	// Modifier-prefixed declarations with explicit type annotations.
	for _, m := range kotlinModifiedPropTypedRe.FindAllStringSubmatch(stripped, -1) {
		if typeName := kotlinTypeName(m[2]); typeName != "" {
			record(m[1], typeName)
		}
	}
	// Modifier-prefixed initializers: `= Klass(...)` types the property
	// directly; `= receiver.factory(...)` types it by the factory's declared
	// return type when the receiver is a known constructor parameter or
	// property and the return type is unambiguous across the workspace.
	for _, m := range kotlinModifiedPropInitRe.FindAllStringSubmatch(stripped, -1) {
		name, init := m[1], strings.TrimSpace(m[2])
		if cm := kotlinCtorInitRe.FindStringSubmatch(init); cm != nil {
			record(name, cm[1])
			continue
		}
		fm := kotlinFactoryInitRe.FindStringSubmatch(init)
		if fm == nil {
			continue
		}
		receiver, factory := fm[1], fm[2]
		if _, ok := ctorParamTypes[receiver]; !ok {
			if _, ok := out[receiver]; !ok {
				continue
			}
		}
		if typeName := kotlinUniqueReturnType(factory, returnTypesBySymbolNameAndFile); typeName != "" {
			record(name, typeName)
		}
	}
	for name := range conflicted {
		delete(out, name)
	}
	return out
}

// kotlinUniqueReturnType returns the single declared return type of the named
// callable when every declaration in the workspace agrees on exactly one type
// name (`fun newQueue(): TaskQueue` -> TaskQueue). Ambiguity (overloads with
// different returns, generic returns naming several types) yields "".
func kotlinUniqueReturnType(name string, returnTypesBySymbolNameAndFile map[string]map[string][]string) string {
	unique := ""
	for _, types := range returnTypesBySymbolNameAndFile[name] {
		for _, typeName := range types {
			if unique == "" {
				unique = typeName
			} else if unique != typeName {
				return ""
			}
		}
	}
	return unique
}

// kotlinExtensionReceiver returns the declared extension-receiver type of a
// Kotlin function signature for the given function name
// (`fun Closeable.closeQuietly()` -> Closeable), or "" for ordinary functions.
func kotlinExtensionReceiver(signature, name string) string {
	for _, m := range kotlinExtensionFunRe.FindAllStringSubmatch(signature, -1) {
		if m[2] != name {
			continue
		}
		return kotlinTypeName(strings.TrimSuffix(stripGenerics(m[1]), "?"))
	}
	return ""
}

// kotlinSupertypeNames parses the supertype list from a Kotlin class/interface
// declaration signature (`class WebSocketWriter( ... ) : Closeable` ->
// [Closeable]). Delegation (`by`), superclass constructor arguments, and
// generics are stripped; only capitalized terminal segments are kept.
func kotlinSupertypeNames(signature string) []string {
	sig := stripGenerics(signature)
	rest := sig
	if open := strings.Index(sig, "("); open >= 0 {
		if close := matchingParen(sig, open); close > open {
			rest = sig[close+1:]
		}
	}
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return nil
	}
	identRe := regexp.MustCompile(`^[A-Za-z_][\w.]*`)
	var out []string
	for _, part := range splitTopLevelCommas(rest[colon+1:]) {
		name := kotlinTypeName(identRe.FindString(strings.TrimSpace(part)))
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

// kotlinReceiverTypeNames returns the receiver type plus its (workspace-known)
// supertype names, walking declaration signatures transitively with a small
// depth bound. External supertypes that never resolve to a workspace symbol
// still contribute their name when spelled in a signature, which is exactly
// what extension receivers like java.io.Closeable need.
func kotlinReceiverTypeNames(typeName string, from SymbolRecord, symbolsByShortName map[string][]SymbolRecord) map[string]bool {
	names := map[string]bool{}
	var visit func(name string, depth int)
	visit = func(name string, depth int) {
		if name == "" || names[name] || depth > 4 {
			return
		}
		names[name] = true
		sym, ok := firstTypeLikeNamedPreferFile(symbolsByShortName[name], name, from.FilePath)
		if !ok || sym.Language != "Kotlin" {
			return
		}
		for _, super := range kotlinSupertypeNames(sym.Signature) {
			visit(super, depth+1)
		}
	}
	visit(typeName, 0)
	return names
}

// kotlinExtensionFunctionTarget resolves a `receiver.method(...)` call whose
// member lookup failed to a top-level Kotlin extension function. With a typed
// receiver the extension's declared receiver type must match the receiver's
// type or one of its supertypes, uniquely; with an unknown receiver type only
// a workspace-unique extension name resolves (the same unique-name stance as
// the generic fallbacks). The boolean reports whether the match was
// type-directed.
func kotlinExtensionFunctionTarget(call receiverCall, receiverType string, from SymbolRecord, symbolsByShortName map[string][]SymbolRecord) (SymbolRecord, bool, bool) {
	var extensions []SymbolRecord
	for _, cand := range symbolsByShortName[call.Method] {
		if cand.ID == from.ID || cand.Kind != "function" || cand.Language != "Kotlin" {
			continue
		}
		if kotlinExtensionReceiver(cand.Signature, call.Method) == "" {
			continue
		}
		extensions = append(extensions, cand)
	}
	if len(extensions) == 0 {
		return SymbolRecord{}, false, false
	}
	if receiverType != "" {
		receiverNames := kotlinReceiverTypeNames(receiverType, from, symbolsByShortName)
		var matches []SymbolRecord
		for _, ext := range extensions {
			if receiverNames[kotlinExtensionReceiver(ext.Signature, call.Method)] {
				matches = append(matches, ext)
			}
		}
		if len(matches) == 1 {
			return matches[0], true, true
		}
		return SymbolRecord{}, false, false
	}
	if len(extensions) == 1 {
		return extensions[0], false, true
	}
	return SymbolRecord{}, false, false
}

// kotlinFieldTypeOnType returns the declared type of member property prop on
// typeName (or one of its spelled supertypes), read from the workspace's Kotlin
// field symbols (`public val application: Application` on the ApplicationCall
// interface -> Application). Field signatures are `name Type`; a property name
// declared with two different types across the matching containers yields "".
func kotlinFieldTypeOnType(typeName, prop string, from SymbolRecord, symbolsByShortName map[string][]SymbolRecord) string {
	if typeName == "" || prop == "" {
		return ""
	}
	receiverNames := kotlinReceiverTypeNames(typeName, from, symbolsByShortName)
	unique := ""
	for _, cand := range symbolsByShortName[prop] {
		if cand.Kind != "field" || cand.Language != "Kotlin" {
			continue
		}
		container := containerName(cand.QualifiedName)
		if container == "" || !receiverNames[lastTypeSegment(container)] {
			continue
		}
		parts := strings.Fields(cand.Signature)
		if len(parts) < 2 {
			continue
		}
		declared := kotlinTypeName(strings.TrimSuffix(stripGenerics(parts[1]), "?"))
		if declared == "" {
			continue
		}
		if unique == "" {
			unique = declared
		} else if unique != declared {
			return ""
		}
	}
	return unique
}

// kotlinChainedCall is a `base.property.method(...)` call site: the receiver is
// itself a property access on a typed value (`call.application.resolveResource`).
type kotlinChainedCall struct {
	Base     string
	Property string
	Method   string
	Detail   string
}

// kotlinChainedReceiverCalls extracts `base.property.method(...)` call sites,
// which the flat receiver scanners misread as `property.method(...)` with an
// unknown receiver.
func kotlinChainedReceiverCalls(block string) []kotlinChainedCall {
	stripped := stripKotlinCodeText(block)
	var out []kotlinChainedCall
	seen := map[string]bool{}
	for _, m := range kotlinChainedReceiverCallRe.FindAllStringSubmatch(stripped, -1) {
		base, property, method := m[1], m[2], m[3]
		detail := base + "." + property + "." + method
		if seen[detail] {
			continue
		}
		seen[detail] = true
		out = append(out, kotlinChainedCall{Base: base, Property: property, Method: method, Detail: detail})
	}
	return out
}

// kotlinImplicitReceiverCallTarget resolves a receiver-less call inside a
// Kotlin extension function body: `fun ApplicationCall.respondResource(...)`
// calling bare `resolveResource(...)` dispatches on the implicit extension
// receiver. Resolution order mirrors the language: a member method of the
// receiver type (walking its supertype chain), then a type-directed extension
// function whose declared receiver matches the receiver type or one of its
// spelled supertypes.
func kotlinImplicitReceiverCallTarget(from SymbolRecord, name string, methodsByContainer map[string]map[string]SymbolRecord, superContainerByID map[string]string, symbolsByShortName map[string][]SymbolRecord) (SymbolRecord, string, bool) {
	receiverType := kotlinExtensionReceiver(from.Signature, from.Name)
	if receiverType == "" {
		return SymbolRecord{}, "", false
	}
	if typeSym, ok := firstTypeLikeNamedPreferFile(symbolsByShortName[receiverType], receiverType, from.FilePath); ok {
		if method, _, ok := lookupMethodUpChain(typeSym.ID, name, methodsByContainer, superContainerByID); ok && method.ID != from.ID {
			return method, "receiver-less call in extension function resolved to a method of the extension receiver type", true
		}
	}
	if target, typeDirected, ok := kotlinExtensionFunctionTarget(receiverCall{Method: name}, receiverType, from, symbolsByShortName); ok && typeDirected && target.ID != from.ID {
		return target, "receiver-less call in extension function resolved to a Kotlin extension function on the receiver type", true
	}
	return SymbolRecord{}, "", false
}

// kotlinLambdaParam is one trailing-lambda parameter, with its explicit type
// annotation when spelled (`{ call: ApplicationCall -> ... }`).
type kotlinLambdaParam struct {
	Name string
	Type string
}

type kotlinLambdaSite struct {
	Callee string
	Params []kotlinLambdaParam
}

// kotlinTrailingLambdaSites scans stripped source for
// `callee(args) { p1, p2 -> ...` / `callee { p -> ...` trailing-lambda call
// sites and returns the callee with its lambda parameter list. Dotted callees
// (`x.map { ... }`), destructured parameters, and lambdas without a parameter
// list are skipped.
func kotlinTrailingLambdaSites(stripped string) []kotlinLambdaSite {
	var out []kotlinLambdaSite
	for _, loc := range kotlinBareIdentifierLocations(stripped) {
		callee := stripped[loc[0]:loc[1]]
		if kotlinLambdaKeywords[callee] {
			continue
		}
		pos := skipSpaces(stripped, loc[1])
		if pos < len(stripped) && stripped[pos] == '(' {
			close := matchingParen(stripped, pos)
			if close < 0 {
				continue
			}
			pos = skipSpaces(stripped, close+1)
		}
		if pos >= len(stripped) || stripped[pos] != '{' {
			continue
		}
		params, ok := kotlinLambdaParamList(stripped, pos+1)
		if !ok {
			continue
		}
		out = append(out, kotlinLambdaSite{Callee: callee, Params: params})
	}
	return out
}

// kotlinBareIdentifierLocations returns the [start,end) spans of identifiers
// not preceded by `.`/`?.`/`::` (receiver-qualified names resolve elsewhere).
func kotlinBareIdentifierLocations(stripped string) [][]int {
	var out [][]int
	for _, loc := range kotlinBareLambdaIdentRe.FindAllStringIndex(stripped, -1) {
		if start := loc[0]; start > 0 {
			switch stripped[start-1] {
			case '.', ':', '@', '$':
				continue
			}
			if stripped[start-1] >= '0' && stripped[start-1] <= '9' {
				continue
			}
		}
		out = append(out, loc)
	}
	return out
}

var kotlinBareLambdaIdentRe = regexp.MustCompile(`[A-Za-z_]\w*`)

// kotlinLambdaParamList parses the parameter list of a lambda body starting
// just after its `{`: identifiers with optional `: Type` annotations separated
// by commas, terminated by `->`. Anything else before the arrow (an expression
// body, a destructuring pattern, an operator) reports no parameter list.
func kotlinLambdaParamList(stripped string, start int) ([]kotlinLambdaParam, bool) {
	end := start
	for end < len(stripped) {
		ch := stripped[end]
		if ch == '-' && end+1 < len(stripped) && stripped[end+1] == '>' {
			break
		}
		if ch == '_' || ch == ',' || ch == ':' || ch == '?' || ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' ||
			('a' <= ch && ch <= 'z') || ('A' <= ch && ch <= 'Z') || ('0' <= ch && ch <= '9') {
			end++
			continue
		}
		return nil, false
	}
	if end >= len(stripped) {
		return nil, false
	}
	var params []kotlinLambdaParam
	for _, part := range strings.Split(stripped[start:end], ",") {
		nameAndType := strings.SplitN(part, ":", 2)
		name := strings.TrimSpace(nameAndType[0])
		if !kotlinIdentifierOnly(name) {
			return nil, false
		}
		param := kotlinLambdaParam{Name: name}
		if len(nameAndType) == 2 {
			param.Type = kotlinTypeName(strings.TrimSuffix(strings.TrimSpace(nameAndType[1]), "?"))
		}
		params = append(params, param)
	}
	if len(params) == 0 {
		return nil, false
	}
	return params, true
}

func kotlinIdentifierOnly(value string) bool {
	if value == "" || kotlinLambdaKeywords[value] {
		return false
	}
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if ch == '_' || ('a' <= ch && ch <= 'z') || ('A' <= ch && ch <= 'Z') || (i > 0 && '0' <= ch && ch <= '9') {
			continue
		}
		return false
	}
	return true
}

func skipSpaces(s string, pos int) int {
	for pos < len(s) {
		switch s[pos] {
		case ' ', '\t', '\r', '\n':
			pos++
		default:
			return pos
		}
	}
	return pos
}

// kotlinLambdaParamVarTypes infers lambda parameter -> type bindings for
// trailing-lambda call sites in the block. Explicitly annotated parameters bind
// directly; untyped parameters bind through the callee's declared trailing
// function-type parameter (`status(*code) { call, status -> }` against
// `fun status(vararg status: HttpStatusCode, handler: suspend (ApplicationCall,
// HttpStatusCode) -> Unit)` binds call: ApplicationCall). A name bound to two
// different types anywhere in the block is dropped.
func kotlinLambdaParamVarTypes(block string, from SymbolRecord, symbolsByShortName map[string][]SymbolRecord) map[string]string {
	stripped := stripKotlinCodeText(block)
	sites := kotlinTrailingLambdaSites(stripped)
	if len(sites) == 0 {
		return nil
	}
	out := map[string]string{}
	conflicted := map[string]bool{}
	record := func(name, typeName string) {
		if name == "" || typeName == "" {
			return
		}
		if existing, ok := out[name]; ok && existing != typeName {
			conflicted[name] = true
			return
		}
		out[name] = typeName
	}
	for _, site := range sites {
		var untyped int
		for _, p := range site.Params {
			if p.Type == "" {
				untyped++
			}
		}
		var declared []string
		if untyped > 0 {
			declared = kotlinCalleeLambdaParamTypes(site.Callee, len(site.Params), from, symbolsByShortName)
		}
		for i, p := range site.Params {
			switch {
			case p.Type != "":
				record(p.Name, p.Type)
			case len(declared) == len(site.Params):
				record(p.Name, declared[i])
			}
		}
	}
	for name := range conflicted {
		delete(out, name)
	}
	return out
}

// kotlinCalleeLambdaParamTypes resolves the callee of a trailing-lambda call
// against the enclosing scope (methods of the extension receiver type and its
// spelled supertypes, or of the enclosing container) and returns the parameter
// type names of its trailing function-type parameter when the arity matches.
// Overloads are tolerated as long as exactly one distinct binding survives.
func kotlinCalleeLambdaParamTypes(callee string, arity int, from SymbolRecord, symbolsByShortName map[string][]SymbolRecord) []string {
	scopeNames := map[string]bool{}
	if receiver := kotlinExtensionReceiver(from.Signature, from.Name); receiver != "" {
		for name := range kotlinReceiverTypeNames(receiver, from, symbolsByShortName) {
			scopeNames[name] = true
		}
	}
	if container := lastTypeSegment(containerName(from.QualifiedName)); container != "" {
		scopeNames[container] = true
	}
	var binding []string
	for _, cand := range symbolsByShortName[callee] {
		if cand.ID == from.ID || cand.Language != "Kotlin" || cand.Kind != "method" {
			continue
		}
		if !scopeNames[lastTypeSegment(containerName(cand.QualifiedName))] {
			continue
		}
		types := kotlinTrailingFunctionParamTypes(cand.Signature, cand.Name, arity)
		if types == nil {
			continue
		}
		if binding == nil {
			binding = types
			continue
		}
		if !stringSlicesEqual(binding, types) {
			return nil
		}
	}
	return binding
}

// kotlinTrailingFunctionParamTypes returns the parameter type names of the last
// declared parameter of the named function when that parameter is a function
// type with exactly arity parameters, all spelled as capitalized type names.
func kotlinTrailingFunctionParamTypes(signature, name string, arity int) []string {
	funRe := regexp.MustCompile(`\bfun\s+(?:<[^<>]*>\s+)?(?:[A-Za-z_][\w.]*(?:<[^<>]*>)?\??\s*\.\s*)?` + regexp.QuoteMeta(name) + `\s*\(`)
	loc := funRe.FindStringIndex(signature)
	if loc == nil {
		return nil
	}
	open := loc[1] - 1
	close := matchingParen(signature, open)
	if close < 0 {
		return nil
	}
	params := splitTopLevelCommas(signature[open+1 : close])
	if len(params) == 0 {
		return nil
	}
	last := params[len(params)-1]
	colon := strings.Index(last, ":")
	if colon < 0 {
		return nil
	}
	typeText := strings.TrimSpace(last[colon+1:])
	m := kotlinFunctionTypeRe.FindStringSubmatch(typeText)
	if m == nil {
		return nil
	}
	var out []string
	for _, part := range splitTopLevelCommas(m[1]) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		typeName := kotlinTypeName(strings.TrimSuffix(stripGenerics(part), "?"))
		if typeName == "" {
			return nil
		}
		out = append(out, typeName)
	}
	if len(out) != arity {
		return nil
	}
	return out
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
