package sem

// Swift-specific receiver typing. The generic scanners miss the dominant Swift
// call idioms (evidence: on apple/swift-nio the focus method
// ByteBuffer.discardReadBytes resolved 0/3 inbound CALLS edges):
//
//   - Parameters are spelled `label name: inout Type` (`func finishProcessing(
//     remainder buffer: inout ByteBuffer)`), which none of parameterVarTypes'
//     branches understand: the colon branch requires the colon directly after
//     the first identifier and cannot skip `inout`/`borrowing`/`consuming` or
//     an argument label, so `buffer.discardReadBytes()` never resolves.
//   - Locals bound by enum-case patterns (`case .available(var buffer):`)
//     carry their type on the enum declaration's payload
//     (`case available(ByteBuffer)`), not on the binding.
//   - Stored properties (`internal private(set) var _buffer: ByteBuffer?`)
//     have no var-type source, and force-unwrapped/optional-chained receivers
//     (`self._buffer!.discardReadBytes()`, `delegate?.retry(...)`) never match
//     the generic receiverCallRe, which requires a literal `.` directly after
//     the receiver name.
//   - Multiline string literals (`"""..."""`) span lines, so the generic
//     line-scoped stripper leaks their bodies (and any `name(` inside) into
//     the call scanners.
//
// Everything in this file is gated to Language == "Swift" by its callers so
// the other languages keep their existing behavior. The extractors follow the
// same conservative straight-line rules as kotlin.go/php.go: capitalized type
// names only, conflicting bindings dropped, ambiguous lookups resolved to
// nothing rather than guessed.

import (
	"regexp"
	"strings"
)

// swiftModifierPattern is one declaration modifier (with an optional argument,
// covering `private(set)` and `unowned(unsafe)`). Class-body property maps
// require at least one so method-body locals stay out of them, mirroring
// kotlinModifiedPropTypedRe.
const swiftModifierPattern = `(?:(?:public|private|fileprivate|internal|open|package|static|class|final|lazy|weak|unowned|override|required|nonisolated|indirect)(?:\(\w+\))?\s+)`

var (
	// receiver.method( / receiver?.method( / receiver!.method( call sites. The
	// generic receiverCallRe already covers the plain-dot form; this adds the
	// optional-chaining and force-unwrap operators.
	swiftReceiverCallRe = regexp.MustCompile(`\b([A-Za-z_]\w*)\s*[!?]?\.\s*([A-Za-z_]\w*)\s*\(`)
	// let/var declarations with an explicit type annotation.
	swiftTypedDeclRe = regexp.MustCompile(`\b(?:let|var)\s+([A-Za-z_]\w*)\s*:\s*([A-Za-z_][\w.]*)`)
	// Enum-case pattern bindings: `case .available(var buffer)` and the
	// binding-first spelling `case let .available(buffer)`, both also behind
	// `if`/`guard case`. Multi-payload patterns intentionally fail the match.
	swiftCaseBindingRe    = regexp.MustCompile(`\bcase\s+\.([A-Za-z_]\w*)\s*\(\s*(?:let|var)\s+([A-Za-z_]\w*)\s*\)`)
	swiftCaseLetBindingRe = regexp.MustCompile(`\bcase\s+(?:let|var)\s+\.([A-Za-z_]\w*)\s*\(\s*([A-Za-z_]\w*)\s*\)`)
	// Modifier-prefixed class-body stored properties with explicit types
	// (`internal private(set) var _buffer: ByteBuffer?`).
	swiftPropTypedRe = regexp.MustCompile(`(?m)^[ \t]*` + swiftModifierPattern + `+(?:let|var)\s+([A-Za-z_]\w*)\s*:\s*([A-Za-z_][\w.]*)`)
	// Modifier-prefixed stored property with a constructor initializer instead
	// of a type annotation: `private var buffers = CircularBuffer<ByteBuffer>()`.
	swiftPropInitRe = regexp.MustCompile(`(?m)^[ \t]*` + swiftModifierPattern + `+(?:let|var)\s+([A-Za-z_]\w*)\s*=\s*([A-Z]\w*)\s*[<(]`)
	// Computed property declaration: `public var fileio: FileIO {`. The
	// trailing `{` (a getter body, or a protocol requirement's `{ get }`)
	// keeps initialized locals/stored properties out, so no modifier is
	// required; this is the only spelling `extension Request { var fileio:
	// FileIO { ... } }` members have, and the extension file is usually not
	// the extended type's file.
	swiftComputedPropRe = regexp.MustCompile(`(?m)^[ \t]*` + swiftModifierPattern + `*var\s+([A-Za-z_]\w*)\s*:\s*([A-Za-z_][\w.]*)[?!]?\s*\{`)
	// Enum-case declaration with an associated-value list: `case available(ByteBuffer)`.
	// Switch patterns never match (they spell the case with a leading dot).
	swiftEnumCaseDeclRe = regexp.MustCompile(`(?m)^[ \t]*(?:(?:indirect|public|private|fileprivate|internal|package)\s+)*case\s+([A-Za-z_]\w*)\s*\(([^()\n]*)\)`)
	// Chained receiver call `base.property.method(...)`, accepting the
	// optional-chaining (`?.`) and force-unwrap (`!.`) operators at each hop
	// and a trailing-closure `{`. Base and property must be lowercase (values,
	// not type references); the leading guard keeps the match from starting
	// mid-selector or on a call/subscript result, so `a.b.c.m()` never binds b
	// as a base and `f().a.m()` never binds f's result. `\s*` around the dots
	// accepts Vapor-style multi-line chains (`request\n  .fileio\n
	// .streamFile(...)`).
	swiftChainedReceiverCallRe = regexp.MustCompile(`(?:^|[^\w.$)\]!?])([a-z_]\w*)[!?]?\s*\.\s*([a-z_]\w*)[!?]?\s*\.\s*([A-Za-z_]\w*)\s*[({]`)
	// Local bound from a property chain: `let contentRange = request.headers.range`,
	// `if let firstRange = contentRange.ranges.first`. The hops are lowercase
	// property names; validation of what follows the chain happens in
	// swiftChainBoundLocalTypes.
	swiftChainBindingRe = regexp.MustCompile(`\b(?:let|var)\s+([a-z_]\w*)\s*=\s*(?:try[!?]?\s+)?(?:await\s+)?([a-z_]\w*)((?:[!?]?\s*\.\s*[a-z_]\w*)+)`)
	swiftChainHopRe     = regexp.MustCompile(`[a-z_]\w*`)
	// One parameter of a Swift function signature: optional argument label,
	// internal name, then `:` and the type, skipping attributes
	// (`@escaping`), ownership modifiers (`inout`, `borrowing`, ...) and
	// existential/opaque markers (`any`, `some`). Function-type parameters
	// (`(inout Decoder, inout ByteBuffer) -> DecodingState`) intentionally
	// fail the match: their receiver typing is not worth guessing.
	swiftParamRe = regexp.MustCompile(`^(?:([A-Za-z_]\w*)\s+)?([A-Za-z_]\w*)\s*:\s*(?:@\w+(?:\([^)]*\))?\s+)*(?:(?:inout|borrowing|consuming|__owned|__shared)\s+)?(?:(?:some|any)\s+)?([A-Za-z_][\w.]*)`)
)

// swiftFileTypes carries the per-file declared-type maps that live outside any
// method's block: stored-property types and enum-case payload types. Collected
// once per Swift file by the relation pass and threaded into
// receiverCallRelations, like phpPropTypes/kotlinPropTypes.
type swiftFileTypes struct {
	props        map[string]string
	enumPayloads map[string]string
}

// maskSwiftMultilineStrings blanks `"""..."""` multiline string literals,
// including the delimiters, preserving newlines. Swift's delimiters and
// line-spanning bodies match Kotlin's raw strings exactly, so the masking is
// shared.
func maskSwiftMultilineStrings(content string) string {
	return maskKotlinRawStrings(content)
}

// stripSwiftCodeText extends the generic literal/comment stripper with Swift's
// multiline string literals, whose `"""` delimiters and line-spanning bodies
// match Kotlin's raw strings exactly; without the masking the generic stripper
// (which resets its string state at each newline) would leak their prose into
// the call scanners. Single-line strings — including `\(...)` interpolations,
// whose leading backslash escapes the paren so the stripper stays inside the
// literal — and comments are handled by the generic stripper.
func stripSwiftCodeText(content string) string {
	return stripCodeLiteralsAndComments(maskSwiftMultilineStrings(content))
}

// swiftTypeName normalizes a matched type reference to the terminal,
// capitalized type segment (`B2MDBuffer.BufferAvailability` ->
// BufferAvailability); optionality markers and generics never make it into
// the match. Empty when the segment is not a plausible type name.
func swiftTypeName(ref string) string {
	name := lastTypeSegment(strings.TrimSuffix(strings.TrimSuffix(stripGenerics(ref), "?"), "!"))
	if !isCapitalized(name) {
		return ""
	}
	return name
}

// swiftReceiverCalls extracts `receiver.method(...)` call sites, accepting the
// optional-chaining (`?.`) and force-unwrap (`!.`) operators, neither of which
// matches the generic receiverCallRe. `self._buffer!.discardReadBytes()`
// yields receiver `_buffer`: the `self` hop cannot match (no `(` after
// `_buffer`), so the scan resumes at the property, which is exactly the name
// the stored-property type map binds.
func swiftReceiverCalls(block string) []receiverCall {
	stripped := stripSwiftCodeText(block)
	var out []receiverCall
	seen := map[string]bool{}
	for _, m := range swiftReceiverCallRe.FindAllStringSubmatchIndex(stripped, -1) {
		receiver := stripped[m[2]:m[3]]
		method := stripped[m[4]:m[5]]
		key := receiver + "." + method
		if receiver == "" || method == "" || seen[key] {
			continue
		}
		args := ""
		open := m[1] - 1
		if close := matchingParen(stripped, open); close > open {
			args = stripped[open+1 : close]
		}
		seen[key] = true
		out = append(out, receiverCall{Receiver: receiver, Method: method, Args: args})
	}
	return out
}

// swiftParameterVarTypes infers parameter -> type from a Swift function
// signature: `func f(buffer: inout ByteBuffer)`, argument labels
// (`remainder buffer:`, `_ buffer:`), attributes, and defaulted parameters.
// The generic parameterVarTypes' colon branch already types the bare
// `name: Type` form; this covers the spellings it cannot.
func swiftParameterVarTypes(signature, name string) map[string]string {
	out := map[string]string{}
	if name == "" {
		return out
	}
	pattern := `\bfunc\s+` + regexp.QuoteMeta(name) + `\s*(?:<[^<>]*>)?\s*\(`
	if name == "init" {
		pattern = `\binit\s*(?:<[^<>]*>)?\s*[?!]?\s*\(`
	}
	loc := regexp.MustCompile(pattern).FindStringIndex(signature)
	if loc == nil {
		return out
	}
	open := loc[1] - 1
	close := matchingParen(signature, open)
	if close < 0 {
		return out
	}
	for _, param := range splitTopLevelCommas(signature[open+1 : close]) {
		param = strings.TrimSpace(strings.SplitN(param, "=", 2)[0])
		m := swiftParamRe.FindStringSubmatch(param)
		if m == nil {
			continue
		}
		typeName := swiftTypeName(m[3])
		if m[2] == "" || m[2] == "_" || typeName == "" {
			continue
		}
		out[m[2]] = typeName
	}
	delete(out, "self")
	return out
}

// swiftLocalVarTypes infers variable -> type from declared-type local
// declarations (`let decoded: ByteBuffer? = nil`) and enum-case pattern
// bindings (`case .available(var buffer):` against the file's
// `case available(ByteBuffer)` declaration). Constructor assignments
// (`var buffer = ByteBuffer()`) are already handled by the generic
// localVarTypes. A name bound to two different types is dropped.
func swiftLocalVarTypes(block string, enumPayloads map[string]string) map[string]string {
	stripped := stripSwiftCodeText(block)
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
	for _, m := range swiftTypedDeclRe.FindAllStringSubmatch(stripped, -1) {
		record(m[1], swiftTypeName(m[2]))
	}
	for _, re := range []*regexp.Regexp{swiftCaseBindingRe, swiftCaseLetBindingRe} {
		for _, m := range re.FindAllStringSubmatch(stripped, -1) {
			record(m[2], enumPayloads[m[1]])
		}
	}
	for name := range conflicted {
		delete(out, name)
	}
	return out
}

// swiftChainedCall is a `base.property.method(...)` call site: the receiver is
// itself a property access on another value (`request.fileio.streamFile`).
type swiftChainedCall struct {
	Base     string
	Property string
	Method   string
	Detail   string
}

// swiftChainedReceiverCalls extracts `base.property.method(...)` call sites
// (including `?.`/`!.` links, multi-line chains, and trailing-closure
// invocations), which the flat receiver scanners misread as
// `property.method(...)` with an unknown receiver.
func swiftChainedReceiverCalls(block string) []swiftChainedCall {
	stripped := stripSwiftCodeText(block)
	var out []swiftChainedCall
	seen := map[string]bool{}
	for _, m := range swiftChainedReceiverCallRe.FindAllStringSubmatch(stripped, -1) {
		base, property, method := m[1], m[2], m[3]
		detail := base + "." + property + "." + method
		if seen[detail] {
			continue
		}
		seen[detail] = true
		out = append(out, swiftChainedCall{Base: base, Property: property, Method: method, Detail: detail})
	}
	return out
}

// swiftTypeRefText reports whether text is a plain (possibly dotted) type
// reference: word characters and dots only. Signature-derived type text can
// carry arrays, tuples, or generics, which the chain typing never guesses at.
func swiftTypeRefText(text string) bool {
	if text == "" {
		return false
	}
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if ch == '.' || ch == '_' || ('a' <= ch && ch <= 'z') || ('A' <= ch && ch <= 'Z') || ('0' <= ch && ch <= '9') {
			continue
		}
		return false
	}
	return true
}

// swiftDeclaredTypeName normalizes raw declared-type text from a field
// symbol's signature to the terminal, capitalized type segment
// (`HTTPFields.Range?` -> Range), or "" when the text is not a plain type
// reference (arrays, dictionaries, tuples, function types).
func swiftDeclaredTypeName(raw string) string {
	raw = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(stripGenerics(raw)), "?"), "!")
	if !swiftTypeRefText(raw) {
		return ""
	}
	return swiftTypeName(raw)
}

// swiftFieldSignatureTypeText extracts the declared-type text from a field
// symbol's `name Type` signature ("fileio FileIO" -> "FileIO"), or "" when the
// symbol carries no type.
func swiftFieldSignatureTypeText(name, signature string) string {
	if !strings.HasPrefix(signature, name+" ") {
		return ""
	}
	return strings.TrimSpace(signature[len(name)+1:])
}

// swiftFieldRawTypeOnType returns the raw declared-type text of property prop
// on typeName, read from the workspace's Swift field symbols. Members added in
// `extension TypeName { ... }` blocks qualify: their field symbols carry the
// extended type in their qualified name even when the extension lives in a
// different file than the type. A property name declared with two different
// types across the matching containers yields "".
func swiftFieldRawTypeOnType(typeName, prop string, symbolsByShortName map[string][]SymbolRecord) string {
	if typeName == "" || prop == "" {
		return ""
	}
	want := lastTypeSegment(typeName)
	unique := ""
	for _, cand := range symbolsByShortName[prop] {
		if cand.Kind != "field" || cand.Language != "Swift" {
			continue
		}
		container := containerName(cand.QualifiedName)
		if container == "" || lastTypeSegment(container) != want {
			continue
		}
		text := swiftFieldSignatureTypeText(prop, cand.Signature)
		if text == "" {
			continue
		}
		if unique == "" {
			unique = text
		} else if unique != text {
			return ""
		}
	}
	return unique
}

// swiftFieldTypeOnType is swiftFieldRawTypeOnType normalized to a plain
// capitalized type name (`request.headers` on Request -> HTTPFields), or ""
// when the declared type is not a plain reference.
func swiftFieldTypeOnType(typeName, prop string, symbolsByShortName map[string][]SymbolRecord) string {
	return swiftDeclaredTypeName(swiftFieldRawTypeOnType(typeName, prop, symbolsByShortName))
}

// swiftUniqueFieldType resolves a property name to its declared type when
// exactly one Swift field symbol in the workspace carries the name — the same
// "workspace-unique name" stance as the extension-method fallbacks. This types
// chain hops whose base is unannotated (`app.get { req ... in
// req.fileio.streamFile(...) }` closure parameters carry no type).
func swiftUniqueFieldType(prop string, symbolsByShortName map[string][]SymbolRecord) string {
	var field SymbolRecord
	count := 0
	for _, cand := range symbolsByShortName[prop] {
		if cand.Kind != "field" || cand.Language != "Swift" {
			continue
		}
		field = cand
		if count++; count > 1 {
			return ""
		}
	}
	if count != 1 {
		return ""
	}
	return swiftDeclaredTypeName(swiftFieldSignatureTypeText(prop, field.Signature))
}

// swiftUniqueQualifiedMethod resolves a method on typeRef by qualified name:
// exactly one Swift method symbol spells `typeRef.name` (or
// `LastSegment.name`). This is how extension members declared away from their
// type's file resolve — they are scoped under the extended type's (possibly
// dotted) name but never linked to its container symbol, so
// lookupMethodUpChain cannot find them.
func swiftUniqueQualifiedMethod(typeRef, name string, symbolsByShortName map[string][]SymbolRecord) (SymbolRecord, bool) {
	if typeRef == "" || name == "" {
		return SymbolRecord{}, false
	}
	qualified := typeRef + "." + name
	short := lastTypeSegment(typeRef) + "." + name
	var match SymbolRecord
	count := 0
	for _, cand := range symbolsByShortName[name] {
		if cand.Kind != "method" || cand.Language != "Swift" {
			continue
		}
		if cand.QualifiedName != qualified && cand.QualifiedName != short {
			continue
		}
		match = cand
		if count++; count > 1 {
			return SymbolRecord{}, false
		}
	}
	if count != 1 {
		return SymbolRecord{}, false
	}
	return match, true
}

// swiftChainBoundLocalTypes infers local -> type for locals bound from
// property chains (`let contentRange = request.headers.range`, `if let
// firstRange = contentRange.ranges.first`), hopping declared property types
// through the workspace's Swift field symbols. A `first`/`last` hop on an
// array-typed property yields the element type. The first map holds plain
// capitalized type names; the second holds dotted nested-type references
// (`HTTPFields.Range.Value`), whose type symbols keep their short name and
// therefore resolve by qualified method name instead of container lookup.
// Bindings followed by anything but the end of the statement (subscripts,
// calls, operators, ternaries) are skipped, and a name bound to two different
// types is dropped.
func swiftChainBoundLocalTypes(block string, varTypes map[string]string, symbolsByShortName map[string][]SymbolRecord) (map[string]string, map[string]string) {
	stripped := stripSwiftCodeText(block)
	plain, dotted := map[string]string{}, map[string]string{}
	conflicted := map[string]bool{}
	record := func(into map[string]string, name, typeName string) {
		if other, ok := plain[name]; ok && other != typeName {
			conflicted[name] = true
			return
		}
		if other, ok := dotted[name]; ok && other != typeName {
			conflicted[name] = true
			return
		}
		into[name] = typeName
	}
	for _, m := range swiftChainBindingRe.FindAllStringSubmatchIndex(stripped, -1) {
		name := stripped[m[2]:m[3]]
		base := stripped[m[4]:m[5]]
		hops := swiftChainHopRe.FindAllString(stripped[m[6]:m[7]], -1)
		// Only a binding whose right-hand side IS the chain types the local:
		// a trailing subscript, call, or operator changes the value's type.
		rest := stripped[m[1]:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[:nl]
		}
		rest = strings.TrimSpace(strings.TrimLeft(rest, "!?"))
		if rest != "" && !strings.HasPrefix(rest, "{") && !strings.HasPrefix(rest, ",") && rest != "else" && !strings.HasPrefix(rest, "else ") && !strings.HasPrefix(rest, "else{") {
			continue
		}
		typeText := varTypes[base]
		resolved := typeText != ""
		for _, hop := range hops {
			if !resolved {
				break
			}
			trimmed := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(typeText), "!"), "?")
			if (hop == "first" || hop == "last") && strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") && !strings.Contains(trimmed, ":") {
				typeText = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
				continue
			}
			if !swiftTypeRefText(trimmed) {
				resolved = false
				break
			}
			typeText = swiftFieldRawTypeOnType(trimmed, hop, symbolsByShortName)
			resolved = typeText != ""
		}
		if !resolved {
			continue
		}
		final := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(typeText), "!"), "?")
		if !swiftTypeRefText(final) || !swiftAllSegmentsCapitalized(final) {
			continue
		}
		if strings.Contains(final, ".") {
			record(dotted, name, final)
		} else {
			record(plain, name, final)
		}
	}
	for name := range conflicted {
		delete(plain, name)
		delete(dotted, name)
	}
	return plain, dotted
}

// swiftAllSegmentsCapitalized reports whether every dot-separated segment of a
// type reference is capitalized (`HTTPFields.Range.Value`), ruling out
// module-qualified or value expressions.
func swiftAllSegmentsCapitalized(ref string) bool {
	for _, segment := range strings.Split(ref, ".") {
		if !isCapitalized(segment) {
			return false
		}
	}
	return true
}

// swiftFileTypeInfo collects a Swift file's class-level declared types: stored
// properties (modifier-prefixed explicit annotations and constructor
// initializers), computed properties (`var name: Type {`, in type bodies and
// extension blocks), and enum-case associated-value payloads (single payload
// only, labeled or not). Swift files may hold several types; a name bound to
// two different types is dropped rather than guessed.
func swiftFileTypeInfo(content string) swiftFileTypes {
	stripped := stripSwiftCodeText(content)
	info := swiftFileTypes{props: map[string]string{}, enumPayloads: map[string]string{}}
	record := func(into map[string]string, conflicted map[string]bool, name, typeName string) {
		if name == "" || typeName == "" {
			return
		}
		if existing, ok := into[name]; ok && existing != typeName {
			conflicted[name] = true
			return
		}
		into[name] = typeName
	}
	propConflicts := map[string]bool{}
	for _, m := range swiftPropTypedRe.FindAllStringSubmatch(stripped, -1) {
		record(info.props, propConflicts, m[1], swiftTypeName(m[2]))
	}
	for _, m := range swiftPropInitRe.FindAllStringSubmatch(stripped, -1) {
		record(info.props, propConflicts, m[1], m[2])
	}
	// Computed properties (`public var fileio: FileIO { ... }`), in type
	// bodies, protocol requirements (`var fileio: FileIO { get }`), and
	// `extension TypeName { ... }` blocks alike.
	for _, m := range swiftComputedPropRe.FindAllStringSubmatch(stripped, -1) {
		record(info.props, propConflicts, m[1], swiftTypeName(m[2]))
	}
	for name := range propConflicts {
		delete(info.props, name)
	}
	caseConflicts := map[string]bool{}
	for _, m := range swiftEnumCaseDeclRe.FindAllStringSubmatch(stripped, -1) {
		payload := strings.TrimSpace(m[2])
		if payload == "" || strings.Contains(payload, ",") {
			continue // no payload, or several: positional binding not worth guessing
		}
		if colon := strings.LastIndex(payload, ":"); colon >= 0 {
			payload = payload[colon+1:] // labeled payload: `case available(buffer: ByteBuffer)`
		}
		record(info.enumPayloads, caseConflicts, m[1], swiftTypeName(payload))
	}
	for name := range caseConflicts {
		delete(info.enumPayloads, name)
	}
	return info
}
