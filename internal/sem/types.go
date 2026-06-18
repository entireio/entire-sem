package sem

import (
	"regexp"
	"strings"
)

// typeLikeKind reports whether a symbol kind can participate in OO/type
// relations as a supertype anchor (class, interface, struct, trait, etc.).
func typeLikeKind(kind string) bool {
	switch kind {
	case "class", "interface", "struct", "trait", "type", "enum", "record", "object", "protocol":
		return true
	}
	return false
}

// rawSupertype is an unresolved supertype reference parsed from a declaration.
type rawSupertype struct {
	Super      string
	Relation   string // "EXTENDS" or "IMPLEMENTS"
	Confidence float64
}

var (
	extendsClauseRe    = regexp.MustCompile(`(?is)\bextends\b(.*?)(?:\bimplements\b|$)`)
	implementsClauseRe = regexp.MustCompile(`(?is)\bimplements\b(.*)$`)
	pythonBasesRe      = regexp.MustCompile(`\(([^)]*)\)`)
	rustImplForRe      = regexp.MustCompile(`(?m)\bimpl\b(?:\s*<[^>]*>)?\s+([A-Za-z_][\w:]*)(?:<[^>]*>)?\s+for\s+([A-Za-z_]\w*)`)
	rustSupertraitRe   = regexp.MustCompile(`(?m)\btrait\s+([A-Za-z_]\w*)(?:<[^>]*>)?\s*:\s*([^{\n]+)`)
)

// supertypesFromSignature parses EXTENDS/IMPLEMENTS edges from a type symbol's
// declaration signature (the header the parser captured up to the body). This
// covers languages whose inheritance is stated in the class/interface header.
func supertypesFromSignature(language, signature string) []rawSupertype {
	switch language {
	case "Java", "TypeScript", "JavaScript", "PHP":
		return extendsImplementsEdges(signature)
	case "C#":
		return csharpBaseEdges(signature)
	case "Python":
		return pythonBaseEdges(signature)
	default:
		return nil
	}
}

func extendsImplementsEdges(signature string) []rawSupertype {
	signature = stripGenerics(signature)
	var edges []rawSupertype
	if m := extendsClauseRe.FindStringSubmatch(signature); m != nil {
		for _, name := range splitTypeList(m[1]) {
			edges = append(edges, rawSupertype{Super: name, Relation: "EXTENDS", Confidence: 0.9})
		}
	}
	if m := implementsClauseRe.FindStringSubmatch(signature); m != nil {
		for _, name := range splitTypeList(m[1]) {
			edges = append(edges, rawSupertype{Super: name, Relation: "IMPLEMENTS", Confidence: 0.9})
		}
	}
	return edges
}

// csharpBaseEdges parses the C# base list after ':'. C# does not syntactically
// separate the base class from interfaces, so it uses the conventional
// I<Upper> interface-naming heuristic to pick IMPLEMENTS vs EXTENDS, at lower
// confidence.
func csharpBaseEdges(signature string) []rawSupertype {
	signature = stripGenerics(signature)
	if idx := strings.Index(signature, " where "); idx >= 0 {
		signature = signature[:idx]
	}
	colon := strings.Index(signature, ":")
	if colon < 0 {
		return nil
	}
	var edges []rawSupertype
	for _, name := range splitTypeList(signature[colon+1:]) {
		relation := "EXTENDS"
		if len(name) >= 2 && name[0] == 'I' && name[1] >= 'A' && name[1] <= 'Z' {
			relation = "IMPLEMENTS"
		}
		edges = append(edges, rawSupertype{Super: name, Relation: relation, Confidence: 0.7})
	}
	return edges
}

func pythonBaseEdges(signature string) []rawSupertype {
	m := pythonBasesRe.FindStringSubmatch(signature)
	if m == nil {
		return nil
	}
	var edges []rawSupertype
	for _, part := range strings.Split(m[1], ",") {
		part = strings.TrimSpace(part)
		if part == "" || strings.Contains(part, "=") {
			continue // skip keyword args like metaclass=...
		}
		for _, name := range splitTypeList(part) {
			edges = append(edges, rawSupertype{Super: name, Relation: "EXTENDS", Confidence: 0.9})
		}
	}
	return edges
}

// rustSupertypeEdges scans Rust source for trait implementations
// (`impl Trait for Type` → Type IMPLEMENTS Trait) and supertraits
// (`trait T: Super` → T EXTENDS Super). Each edge names the anchor type so the
// caller can resolve it to a symbol. Rust inheritance is not in any single
// symbol signature, so it is scanned from content.
type rustSupertypeEdge struct {
	Anchor     string // the type/trait the relation originates from
	Super      string
	Relation   string
	Confidence float64
}

func rustSupertypeEdges(content string) []rustSupertypeEdge {
	var edges []rustSupertypeEdge
	for _, m := range rustImplForRe.FindAllStringSubmatch(content, -1) {
		trait := lastTypeSegment(m[1])
		anchor := m[2]
		if trait == "" || anchor == "" {
			continue
		}
		edges = append(edges, rustSupertypeEdge{Anchor: anchor, Super: trait, Relation: "IMPLEMENTS", Confidence: 0.9})
	}
	for _, m := range rustSupertraitRe.FindAllStringSubmatch(content, -1) {
		anchor := m[1]
		for _, name := range splitTypeList(m[2]) {
			// Skip lifetime/marker bounds that are not real supertraits.
			if name == "" || strings.HasPrefix(name, "'") {
				continue
			}
			edges = append(edges, rustSupertypeEdge{Anchor: anchor, Super: name, Relation: "EXTENDS", Confidence: 0.85})
		}
	}
	return edges
}

// testSubjectName derives the name of the unit a test symbol covers from the
// test's name, following common conventions: TestFoo/testFoo -> Foo,
// test_foo -> foo, FooTest/FooTests/FooSpec -> Foo. Returns "" when the name is
// not a recognizable test name.
func testSubjectName(name string) string {
	upper := func(b byte) bool { return b >= 'A' && b <= 'Z' }
	switch {
	case strings.HasPrefix(name, "Test") && len(name) > 4 && upper(name[4]):
		return name[4:]
	case strings.HasPrefix(name, "test_") && len(name) > 5:
		return name[5:]
	case strings.HasPrefix(name, "test") && len(name) > 4 && upper(name[4]):
		return name[4:]
	case strings.HasSuffix(name, "Tests") && len(name) > 5:
		return name[:len(name)-5]
	case strings.HasSuffix(name, "Test") && len(name) > 4:
		return name[:len(name)-4]
	case strings.HasSuffix(name, "Spec") && len(name) > 4:
		return name[:len(name)-4]
	}
	return ""
}

// isTestName reports whether a symbol name is a test name.
func isTestName(name string) bool {
	return testSubjectName(name) != ""
}

// channelEvent is a pub/sub or event-emitter call to a named channel.
type channelEvent struct {
	Relation string // "EMITS" or "LISTENS_ON"
	Name     string
}

var (
	emitCallRe   = regexp.MustCompile(`(?i)\.\s*(?:emit|publish|dispatch|broadcast)\s*\(\s*["']([^"']+)["']`)
	listenCallRe = regexp.MustCompile(`(?i)\.\s*(?:on|once|subscribe|addeventlistener|addlistener)\s*\(\s*["']([^"']+)["']`)
)

// channelEvents extracts emit/listen calls naming a channel from a code block.
// These are weak, naming-pattern detections (EventEmitter/Socket.IO/pub-sub),
// so callers emit them at low confidence with a warning code. Emitter and
// listener of the same name share a channel endpoint for matching.
func channelEvents(content string) []channelEvent {
	var out []channelEvent
	seen := map[string]bool{}
	add := func(relation, name string) {
		key := relation + " " + name
		if name == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, channelEvent{Relation: relation, Name: name})
	}
	for _, m := range emitCallRe.FindAllStringSubmatch(content, -1) {
		add("EMITS", m[1])
	}
	for _, m := range listenCallRe.FindAllStringSubmatch(content, -1) {
		add("LISTENS_ON", m[1])
	}
	return out
}

// httpCall is an outbound HTTP client call to a (method, path).
type httpCall struct {
	Method   string
	Path     string
	Absolute bool // path came from an absolute URL (cross-service)
}

var (
	// httpClientRe marks a line as a client-side HTTP call. The library/function
	// name (fetch/axios/requests/httpx/http client) is what distinguishes a
	// client call from a server route registration (app.get/router.post), which
	// share the .get(/.post( shape.
	httpClientRe = regexp.MustCompile(`(?i)(\bfetch\s*\(|\baxios\b|\brequests\s*\.|\bhttpx\b|\bhttp\.(get|post|put|patch|delete|head)\b|\.(get|post|put|patch|delete)async\s*\(|\bhttpclient\b|\bresttemplate\b|\bwebclient\b|\bgot\s*\(|\bky\s*\()`)
	httpVerbRe   = regexp.MustCompile(`(?i)\b(?:http\.|requests\.|httpx\.|axios\.)?(get|post|put|patch|delete|head)(?:async)?\s*\(`)
	urlLiteralRe = regexp.MustCompile(`["'](https?://[^"'\s]+|/[A-Za-z0-9_\-/{}:.]*)["']`)
)

// httpCalls extracts outbound HTTP client calls from a code block: lines that
// carry a client-library signal and a URL/path literal. Absolute URLs are
// reduced to their path so a client call and a local route registration to the
// same path share an endpoint node.
func httpCalls(content string) []httpCall {
	var out []httpCall
	seen := map[string]bool{}
	for _, line := range strings.Split(content, "\n") {
		if !httpClientRe.MatchString(line) {
			continue
		}
		method := "GET"
		if m := httpVerbRe.FindStringSubmatch(line); m != nil {
			method = strings.ToUpper(m[1])
		}
		for _, lm := range urlLiteralRe.FindAllStringSubmatch(line, -1) {
			path, absolute := httpPath(lm[1])
			if path == "" {
				continue
			}
			key := method + " " + path
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, httpCall{Method: method, Path: path, Absolute: absolute})
		}
	}
	return out
}

// httpPath reduces a URL literal to its path component. Absolute URLs return
// (path, true); relative paths return (literal, false).
func httpPath(literal string) (string, bool) {
	if i := strings.Index(literal, "://"); i >= 0 {
		rest := literal[i+3:]
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			return rest[slash:], true
		}
		return "/", true
	}
	return literal, false
}

// receiverCall is a `receiver.method(` / `receiver->method(` call site, used by
// receiver-type call resolution.
type receiverCall struct {
	Receiver string
	Method   string
}

var receiverCallRe = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*(?:->|\.)\s*([A-Za-z_]\w*)\s*\(`)

// receiverCalls extracts distinct receiver.method() call sites from a code
// block (literals and comments stripped). Leading `$` is dropped so PHP
// receivers line up with variable names.
func receiverCalls(block string) []receiverCall {
	stripped := stripCodeLiteralsAndComments(block)
	var out []receiverCall
	seen := map[string]bool{}
	for _, m := range receiverCallRe.FindAllStringSubmatch(stripped, -1) {
		receiver := strings.TrimPrefix(m[1], "$")
		key := receiver + "." + m[2]
		if receiver == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, receiverCall{Receiver: receiver, Method: m[2]})
	}
	return out
}

var (
	newAssignRe  = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*:?=\s*new\s+([A-Za-z_]\w*)`)
	ctorAssignRe = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*:?=\s*&?([A-Z][A-Za-z0-9_]*)\s*[({]`)
)

// localVarTypes infers a best-effort variable -> type-name map from constructor
// assignments inside a block: `x = new Type(...)`, `x = Type(...)` (capitalized,
// e.g. Python), and `x := Type{...}` / `&Type{...}` (Go). Capitalization keeps
// the heuristic conservative; results feed type-inferred call resolution.
func localVarTypes(block string) map[string]string {
	stripped := stripCodeLiteralsAndComments(block)
	out := map[string]string{}
	for _, m := range newAssignRe.FindAllStringSubmatch(stripped, -1) {
		out[strings.TrimPrefix(m[1], "$")] = m[2]
	}
	for _, m := range ctorAssignRe.FindAllStringSubmatch(stripped, -1) {
		name := strings.TrimPrefix(m[1], "$")
		if _, exists := out[name]; !exists {
			out[name] = m[2]
		}
	}
	return out
}

// stripGenerics removes balanced <...> sections so type lists can be split on
// commas without splitting inside generic parameters.
func stripGenerics(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// splitTypeList splits a comma-separated type list into bare short type names,
// dropping generic parameters, package/namespace qualifiers, and non-identifier
// noise.
func splitTypeList(s string) []string {
	s = stripGenerics(s)
	var out []string
	for _, part := range strings.Split(s, ",") {
		name := lastTypeSegment(part)
		if isTypeName(name) {
			out = append(out, name)
		}
	}
	return out
}

func lastTypeSegment(part string) string {
	part = strings.TrimSpace(part)
	part = strings.TrimSuffix(part, "{")
	part = strings.TrimSpace(part)
	if i := strings.LastIndexAny(part, ".\\:"); i >= 0 {
		part = part[i+1:]
	}
	return strings.TrimSpace(part)
}

func isTypeName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
