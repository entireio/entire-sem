package sem

import (
	"path/filepath"
	"regexp"
	"sort"
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

var hclRefRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_-]*)+`)

// hclReferences returns distinct dotted reference tokens (e.g. aws_vpc.main.id,
// module.network.id) found in an HCL block body.
func hclReferences(block string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range hclRefRe.FindAllString(block, -1) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
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

type serviceBoundary struct {
	Relation     string
	Kind         string
	Name         string
	Confidence   float64
	Reason       string
	EvidenceKind string
	WarningCodes []string
}

var (
	graphqlOperationRe = regexp.MustCompile(`(?is)\b(query|mutation|subscription)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	trpcProcedureRe    = regexp.MustCompile(`(?m)([A-Za-z_$][\w$]*)\s*:\s*(?:publicProcedure|protectedProcedure|procedure)\s*\.\s*(query|mutation|subscription)\s*\(`)
)

func serviceBoundaries(symbol SymbolRecord, block string) []serviceBoundary {
	var out []serviceBoundary
	seen := map[string]bool{}
	add := func(boundary serviceBoundary) {
		key := boundary.Relation + "\x00" + boundary.Kind + "\x00" + boundary.Name
		if boundary.Name == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, boundary)
	}
	if symbol.Language == "Protocol Buffers" && symbol.Kind == "rpc" {
		add(serviceBoundary{
			Relation:     "HANDLES_GRPC",
			Kind:         "grpc",
			Name:         strings.ReplaceAll(symbol.QualifiedName, ".", "/"),
			Confidence:   0.95,
			Reason:       "protobuf rpc declaration defines a gRPC method",
			EvidenceKind: "grpc_rpc",
		})
	}
	if symbol.Kind == "graphql_resolver" {
		fields := strings.Fields(symbol.Signature)
		if len(fields) >= 4 && fields[0] == "GraphQL" && fields[1] == "resolver" {
			add(serviceBoundary{
				Relation:     "HANDLES_GRAPHQL",
				Kind:         "graphql",
				Name:         strings.ToLower(fields[2]) + " " + fields[3],
				Confidence:   0.85,
				Reason:       "GraphQL resolver field detected in resolver map",
				EvidenceKind: "graphql_resolver",
			})
		}
	}
	if symbol.Kind == "graphql_schema_field" {
		fields := strings.Fields(symbol.Signature)
		if len(fields) >= 4 && fields[0] == "GraphQL" && fields[1] == "schema" {
			add(serviceBoundary{
				Relation:     "HANDLES_GRAPHQL",
				Kind:         "graphql",
				Name:         strings.ToLower(fields[2]) + " " + fields[3],
				Confidence:   0.9,
				Reason:       "GraphQL schema root field detected in schema document",
				EvidenceKind: "graphql_schema_field",
			})
		}
	}
	for _, match := range graphqlOperationRe.FindAllStringSubmatch(block, -1) {
		add(serviceBoundary{
			Relation:     "HANDLES_GRAPHQL",
			Kind:         "graphql",
			Name:         strings.ToLower(match[1]) + " " + match[2],
			Confidence:   0.75,
			Reason:       "GraphQL operation literal detected in symbol body",
			EvidenceKind: "graphql_operation",
		})
	}
	for _, match := range trpcProcedureRe.FindAllStringSubmatch(block, -1) {
		add(serviceBoundary{
			Relation:     "HANDLES_TRPC",
			Kind:         "trpc",
			Name:         match[2] + " " + match[1],
			Confidence:   0.8,
			Reason:       "tRPC procedure declaration detected in symbol body",
			EvidenceKind: "trpc_procedure",
		})
	}
	return out
}

var (
	awaitCallRe          = regexp.MustCompile(`\bawait\s+([A-Za-z_$][\w$]*)\s*\(`)
	goRoutineCallRe      = regexp.MustCompile(`(?m)\bgo\s+([A-Za-z_]\w*)\s*\(`)
	spawnCallRe          = regexp.MustCompile(`\b(?:Promise\.all|Promise\.race|asyncio\.gather|tokio::spawn|Task\.Run)\s*\([^)]*?([A-Za-z_]\w*)\s*\(`)
	returnCallRe         = regexp.MustCompile(`(?m)\breturn\s+(?:await\s+)?([A-Za-z_$][\w$]*)\s*\(`)
	ternaryReturnCallRe  = regexp.MustCompile(`(?m)\breturn\s+[^?\n]+?\?\s*(?:await\s+)?([A-Za-z_$][\w$]*)\s*\([^:\n]*\)\s*:\s*(?:await\s+)?([A-Za-z_$][\w$]*)\s*\(`)
	pythonIfReturnRe     = regexp.MustCompile(`(?m)\breturn\s+(?:await\s+)?([A-Za-z_$][\w$]*)\s*\([^\n]*?\)\s+if\s+[^\n]+?\s+else\s+(?:await\s+)?([A-Za-z_$][\w$]*)\s*\(`)
	assignCallRe         = regexp.MustCompile(`(?m)\b(?:const|let|var)?\s*\$?([A-Za-z_$][\w$]*)\s*(?:\:\s*[^=\n]+)?\s*(?::=|=)\s*(?:await\s+)?([A-Za-z_$][\w$]*)\s*\(`)
	returnVarRe          = regexp.MustCompile(`(?m)\breturn\s+\$?([A-Za-z_$][\w$]*)\b`)
	aliasAssignRe        = regexp.MustCompile(`(?m)\b(?:const|let|var)?\s*\$?([A-Za-z_$][\w$]*)\s*(?:\:\s*[^=\n]+)?\s*(?::=|=)\s*\$?([A-Za-z_$][\w$]*)\b`)
	localObjectVarRe     = regexp.MustCompile(`(?m)\b(?:const|let|var)?\s*\$?([A-Za-z_$][\w$]*)\s*(?:\:\s*[^=\n]+)?\s*(?::=|=)\s*(?:\{\s*\}|new\s+[A-Za-z_$][\w$]*\s*\(\s*\))`)
	objectLiteralVarRe   = regexp.MustCompile(`(?s)\b(?:const|let|var)?\s*\$?([A-Za-z_$][\w$]*)\s*(?:\:\s*[^=\n]+)?\s*(?::=|=)\s*\{([^{}]*)\}`)
	objectLiteralFieldRe = regexp.MustCompile(`(?:^|,|\n)\s*([A-Za-z_$][\w$]*)\s*:\s*\$?([A-Za-z_$][\w$]*)\b`)
	objectFieldAssignRe  = regexp.MustCompile(`(?m)\b\$?([A-Za-z_$][\w$]*)\s*\.\s*([A-Za-z_$][\w$]*)\s*=\s*\$?([A-Za-z_$][\w$]*)\b`)
	localCollectionVarRe = regexp.MustCompile(`(?m)\b(?:const|let|var)?\s*\$?([A-Za-z_$][\w$]*)\s*(?:\:\s*[^=\n]+)?\s*(?::=|=)\s*(?:\[\s*\]|new\s+(?:Array|Set|Map)\s*\(\s*\))`)
	collectionAddRe      = regexp.MustCompile(`(?m)\b\$?([A-Za-z_$][\w$]*)\s*\.\s*(?:push|append|add)\s*\(\s*\$?([A-Za-z_$][\w$]*)\s*\)`)
)

func asyncCallNames(block string) []string {
	stripped := stripCodeLiteralsAndComments(block)
	seen := map[string]struct{}{}
	addMatches := func(re *regexp.Regexp) {
		for _, match := range re.FindAllStringSubmatch(stripped, -1) {
			if len(match) > 1 && match[1] != "" {
				seen[strings.TrimPrefix(match[1], "$")] = struct{}{}
			}
		}
	}
	addMatches(awaitCallRe)
	addMatches(goRoutineCallRe)
	addMatches(spawnCallRe)
	return sortedStringSet(seen)
}

func returnFlowCallNames(block string) []string {
	stripped := stripCodeLiteralsAndComments(block)
	seen := map[string]struct{}{}
	for _, match := range returnCallRe.FindAllStringSubmatchIndex(stripped, -1) {
		if len(match) < 4 || match[2] < 0 || match[3] < 0 {
			continue
		}
		if isPythonConditionalReturnLine(stripped, match[0]) {
			continue
		}
		name := stripped[match[2]:match[3]]
		if name != "" {
			seen[strings.TrimPrefix(name, "$")] = struct{}{}
		}
	}
	return sortedStringSet(seen)
}

func isPythonConditionalReturnLine(block string, pos int) bool {
	if pos < 0 || pos >= len(block) {
		return false
	}
	start := strings.LastIndex(block[:pos], "\n") + 1
	end := strings.Index(block[pos:], "\n")
	if end < 0 {
		end = len(block)
	} else {
		end += pos
	}
	line := block[start:end]
	return strings.Contains(line, " if ") && strings.Contains(line, " else ")
}

type returnFlowCall struct {
	Name         string
	Reason       string
	EvidenceKind string
	Detail       string
	Direction    string
}

func returnFlowCalls(block, signature string) []returnFlowCall {
	stripped := stripCodeLiteralsAndComments(block)
	flows := map[string]returnFlowCall{}
	for _, name := range returnFlowCallNames(block) {
		flows[name+"\x00return_flow"] = returnFlowCall{
			Name:         name,
			Reason:       "callee return value flows into caller return value",
			EvidenceKind: "return_flow",
			Detail:       name,
			Direction:    "callee_to_caller",
		}
	}
	for _, flow := range ternaryReturnFlows(stripped) {
		flows[flow.Name+"\x00"+flow.EvidenceKind] = flow
	}
	assigned := map[string]string{}
	for _, match := range assignCallRe.FindAllStringSubmatch(stripped, -1) {
		if len(match) == 3 && match[1] != "" && match[2] != "" {
			assigned[strings.TrimPrefix(match[1], "$")] = strings.TrimPrefix(match[2], "$")
		}
	}
	for _, match := range returnVarRe.FindAllStringSubmatchIndex(stripped, -1) {
		if len(match) != 4 {
			continue
		}
		if followsReturnedVariable(stripped, match[1]) {
			continue
		}
		varName := strings.TrimPrefix(stripped[match[2]:match[3]], "$")
		name := assigned[varName]
		if name == "" {
			continue
		}
		flows[name+"\x00assigned_return_flow"] = returnFlowCall{
			Name:         name,
			Reason:       "callee return value assigned to local and returned by caller",
			EvidenceKind: "assigned_return_flow",
			Detail:       name + " -> " + varName,
			Direction:    "callee_to_caller",
		}
	}
	for _, flow := range branchAssignedReturnFlows(stripped) {
		flows[flow.Name+"\x00assigned_return_flow"] = flow
	}
	for _, flow := range argumentForwardingFlows(stripped, signature) {
		flows[flow.Name+"\x00"+flow.EvidenceKind+"\x00"+flow.Detail] = flow
	}
	for _, flow := range aliasForwardingFlows(stripped, signature) {
		flows[flow.Name+"\x00"+flow.EvidenceKind+"\x00"+flow.Detail] = flow
	}
	for _, flow := range objectFieldForwardingFlows(stripped, signature) {
		flows[flow.Name+"\x00"+flow.EvidenceKind+"\x00"+flow.Detail] = flow
	}
	for _, flow := range collectionElementForwardingFlows(stripped, signature) {
		flows[flow.Name+"\x00"+flow.EvidenceKind+"\x00"+flow.Detail] = flow
	}
	out := make([]returnFlowCall, 0, len(flows))
	for _, flow := range flows {
		out = append(out, flow)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].EvidenceKind < out[j].EvidenceKind
	})
	return out
}

func ternaryReturnFlows(block string) []returnFlowCall {
	seen := map[string]bool{}
	var flows []returnFlowCall
	addMatches := func(re *regexp.Regexp) {
		for _, match := range re.FindAllStringSubmatch(block, -1) {
			if len(match) != 3 {
				continue
			}
			for _, name := range []string{match[1], match[2]} {
				name = strings.TrimPrefix(name, "$")
				if name == "" || seen[name] {
					continue
				}
				seen[name] = true
				flows = append(flows, returnFlowCall{
					Name:         name,
					Reason:       "callee return value returned through conditional expression",
					EvidenceKind: "conditional_return_flow",
					Detail:       name,
					Direction:    "callee_to_caller",
				})
			}
		}
	}
	addMatches(ternaryReturnCallRe)
	addMatches(pythonIfReturnRe)
	if len(flows) == 0 {
		return nil
	}
	sort.Slice(flows, func(i, j int) bool {
		return flows[i].Name < flows[j].Name
	})
	return flows
}

func branchAssignedReturnFlows(block string) []returnFlowCall {
	branchRe := regexp.MustCompile(`(?s)\bif\s*\([^)]*\)\s*\{(.*?)\}\s*else\s*\{(.*?)\}.*?\breturn\s+\$?([A-Za-z_$][\w$]*)\b`)
	matches := branchRe.FindAllStringSubmatch(block, -1)
	if len(matches) == 0 {
		return nil
	}
	var flows []returnFlowCall
	seen := map[string]bool{}
	for _, match := range matches {
		if len(match) != 4 {
			continue
		}
		returned := strings.TrimPrefix(match[3], "$")
		for _, branch := range []string{match[1], match[2]} {
			for _, name := range branchCallAssignments(branch, returned) {
				key := name + "\x00" + returned
				if seen[key] {
					continue
				}
				seen[key] = true
				flows = append(flows, returnFlowCall{
					Name:         name,
					Reason:       "callee return value assigned in branch and returned by caller",
					EvidenceKind: "branch_assigned_return_flow",
					Detail:       name + " -> " + returned,
					Direction:    "callee_to_caller",
				})
			}
		}
	}
	sort.Slice(flows, func(i, j int) bool {
		if flows[i].Name != flows[j].Name {
			return flows[i].Name < flows[j].Name
		}
		return flows[i].Detail < flows[j].Detail
	})
	return flows
}

func branchCallAssignments(block, variable string) []string {
	if variable == "" {
		return nil
	}
	re := regexp.MustCompile(`(?m)\b(?:const|let|var)?\s*\$?` + regexp.QuoteMeta(variable) + `\s*(?:\:\s*[^=\n]+)?\s*(?::=|=)\s*(?:await\s+)?([A-Za-z_$][\w$]*)\s*\(`)
	seen := map[string]struct{}{}
	for _, match := range re.FindAllStringSubmatch(block, -1) {
		if len(match) == 2 && match[1] != "" {
			seen[strings.TrimPrefix(match[1], "$")] = struct{}{}
		}
	}
	return sortedStringSet(seen)
}

func argumentForwardingFlows(block, signature string) []returnFlowCall {
	params := parameterNames(signature)
	if len(params) == 0 {
		return nil
	}
	callRe := regexp.MustCompile(`\b([A-Za-z_$][A-Za-z0-9_$]*)\s*\(([^()\n]*)\)`)
	var flows []returnFlowCall
	seen := map[string]bool{}
	for _, match := range callRe.FindAllStringSubmatch(block, -1) {
		if len(match) != 3 {
			continue
		}
		name := strings.TrimPrefix(match[1], "$")
		if dataFlowCallNameIgnored(name) {
			continue
		}
		for _, arg := range splitSimpleArguments(match[2]) {
			arg = strings.TrimPrefix(strings.TrimSpace(arg), "$")
			if !params[arg] {
				continue
			}
			key := name + "\x00" + arg
			if seen[key] {
				continue
			}
			seen[key] = true
			flows = append(flows, returnFlowCall{
				Name:         name,
				Reason:       "caller parameter forwarded into callee argument",
				EvidenceKind: "argument_forward_flow",
				Detail:       arg + " -> " + name + "()",
				Direction:    "caller_to_callee",
			})
		}
	}
	sort.Slice(flows, func(i, j int) bool {
		if flows[i].Name != flows[j].Name {
			return flows[i].Name < flows[j].Name
		}
		return flows[i].Detail < flows[j].Detail
	})
	return flows
}

func aliasForwardingFlows(block, signature string) []returnFlowCall {
	params := parameterNames(signature)
	if len(params) == 0 {
		return nil
	}
	aliasToParam := map[string]string{}
	for _, match := range aliasAssignRe.FindAllStringSubmatch(block, -1) {
		if len(match) != 3 {
			continue
		}
		alias := strings.TrimPrefix(match[1], "$")
		param := strings.TrimPrefix(match[2], "$")
		if alias == "" || alias == param || !params[param] {
			continue
		}
		aliasToParam[alias] = param
	}
	if len(aliasToParam) == 0 {
		return nil
	}
	callRe := regexp.MustCompile(`\b([A-Za-z_$][A-Za-z0-9_$]*)\s*\(([^()\n]*)\)`)
	var flows []returnFlowCall
	seen := map[string]bool{}
	for _, match := range callRe.FindAllStringSubmatch(block, -1) {
		if len(match) != 3 {
			continue
		}
		name := strings.TrimPrefix(match[1], "$")
		if dataFlowCallNameIgnored(name) {
			continue
		}
		for _, arg := range splitSimpleArguments(match[2]) {
			alias := strings.TrimPrefix(strings.TrimSpace(arg), "$")
			param := aliasToParam[alias]
			if param == "" {
				continue
			}
			key := name + "\x00" + alias + "\x00" + param
			if seen[key] {
				continue
			}
			seen[key] = true
			flows = append(flows, returnFlowCall{
				Name:         name,
				Reason:       "caller parameter alias forwarded into callee argument",
				EvidenceKind: "alias_forward_flow",
				Detail:       param + " -> " + alias + " -> " + name + "()",
				Direction:    "caller_to_callee",
			})
		}
	}
	sort.Slice(flows, func(i, j int) bool {
		if flows[i].Name != flows[j].Name {
			return flows[i].Name < flows[j].Name
		}
		return flows[i].Detail < flows[j].Detail
	})
	return flows
}

func objectFieldForwardingFlows(block, signature string) []returnFlowCall {
	params := parameterNames(signature)
	if len(params) == 0 {
		return nil
	}
	objectVars := localObjectVars(block)
	fieldParamByObject := map[string]map[string]bool{}
	for _, match := range objectFieldAssignRe.FindAllStringSubmatch(block, -1) {
		if len(match) != 4 {
			continue
		}
		objectName := strings.TrimPrefix(match[1], "$")
		paramName := strings.TrimPrefix(match[3], "$")
		if !objectVars[objectName] || !params[paramName] {
			continue
		}
		if fieldParamByObject[objectName] == nil {
			fieldParamByObject[objectName] = map[string]bool{}
		}
		fieldParamByObject[objectName][paramName] = true
	}
	for objectName, paramNames := range objectLiteralFieldParams(block, params) {
		if fieldParamByObject[objectName] == nil {
			fieldParamByObject[objectName] = map[string]bool{}
		}
		for paramName := range paramNames {
			fieldParamByObject[objectName][paramName] = true
		}
	}
	if len(fieldParamByObject) == 0 {
		return nil
	}
	callRe := regexp.MustCompile(`\b([A-Za-z_$][A-Za-z0-9_$]*)\s*\(([^()\n]*)\)`)
	var flows []returnFlowCall
	seen := map[string]bool{}
	for _, match := range callRe.FindAllStringSubmatch(block, -1) {
		if len(match) != 3 {
			continue
		}
		name := strings.TrimPrefix(match[1], "$")
		if dataFlowCallNameIgnored(name) {
			continue
		}
		for _, arg := range splitSimpleArguments(match[2]) {
			objectName := strings.TrimPrefix(strings.TrimSpace(arg), "$")
			for paramName := range fieldParamByObject[objectName] {
				key := name + "\x00" + objectName + "\x00" + paramName
				if seen[key] {
					continue
				}
				seen[key] = true
				flows = append(flows, returnFlowCall{
					Name:         name,
					Reason:       "caller parameter assigned into object field forwarded to callee argument",
					EvidenceKind: "object_field_forward_flow",
					Detail:       paramName + " -> " + objectName + " -> " + name + "()",
					Direction:    "caller_to_callee",
				})
			}
		}
	}
	sort.Slice(flows, func(i, j int) bool {
		if flows[i].Name != flows[j].Name {
			return flows[i].Name < flows[j].Name
		}
		return flows[i].Detail < flows[j].Detail
	})
	return flows
}

func objectLiteralFieldParams(block string, params map[string]bool) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, match := range objectLiteralVarRe.FindAllStringSubmatch(block, -1) {
		if len(match) != 3 {
			continue
		}
		objectName := strings.TrimPrefix(match[1], "$")
		if objectName == "" {
			continue
		}
		for _, fieldMatch := range objectLiteralFieldRe.FindAllStringSubmatch(match[2], -1) {
			if len(fieldMatch) != 3 {
				continue
			}
			paramName := strings.TrimPrefix(fieldMatch[2], "$")
			if !params[paramName] {
				continue
			}
			if out[objectName] == nil {
				out[objectName] = map[string]bool{}
			}
			out[objectName][paramName] = true
		}
	}
	return out
}

func collectionElementForwardingFlows(block, signature string) []returnFlowCall {
	params := parameterNames(signature)
	if len(params) == 0 {
		return nil
	}
	collectionVars := localCollectionVars(block)
	if len(collectionVars) == 0 {
		return nil
	}
	paramByCollection := map[string]map[string]bool{}
	for _, match := range collectionAddRe.FindAllStringSubmatch(block, -1) {
		if len(match) != 3 {
			continue
		}
		collectionName := strings.TrimPrefix(match[1], "$")
		paramName := strings.TrimPrefix(match[2], "$")
		if !collectionVars[collectionName] || !params[paramName] {
			continue
		}
		if paramByCollection[collectionName] == nil {
			paramByCollection[collectionName] = map[string]bool{}
		}
		paramByCollection[collectionName][paramName] = true
	}
	if len(paramByCollection) == 0 {
		return nil
	}
	callRe := regexp.MustCompile(`\b([A-Za-z_$][A-Za-z0-9_$]*)\s*\(([^()\n]*)\)`)
	var flows []returnFlowCall
	seen := map[string]bool{}
	for _, match := range callRe.FindAllStringSubmatch(block, -1) {
		if len(match) != 3 {
			continue
		}
		name := strings.TrimPrefix(match[1], "$")
		if dataFlowCallNameIgnored(name) {
			continue
		}
		for _, arg := range splitSimpleArguments(match[2]) {
			collectionName := strings.TrimPrefix(strings.TrimSpace(arg), "$")
			for paramName := range paramByCollection[collectionName] {
				key := name + "\x00" + collectionName + "\x00" + paramName
				if seen[key] {
					continue
				}
				seen[key] = true
				flows = append(flows, returnFlowCall{
					Name:         name,
					Reason:       "caller parameter inserted into collection forwarded to callee argument",
					EvidenceKind: "collection_element_forward_flow",
					Detail:       paramName + " -> " + collectionName + "[] -> " + name + "()",
					Direction:    "caller_to_callee",
				})
			}
		}
	}
	sort.Slice(flows, func(i, j int) bool {
		if flows[i].Name != flows[j].Name {
			return flows[i].Name < flows[j].Name
		}
		return flows[i].Detail < flows[j].Detail
	})
	return flows
}

func localObjectVars(block string) map[string]bool {
	vars := map[string]bool{}
	for _, match := range localObjectVarRe.FindAllStringSubmatch(block, -1) {
		if len(match) == 2 && match[1] != "" {
			vars[strings.TrimPrefix(match[1], "$")] = true
		}
	}
	return vars
}

func localCollectionVars(block string) map[string]bool {
	vars := map[string]bool{}
	for _, match := range localCollectionVarRe.FindAllStringSubmatch(block, -1) {
		if len(match) == 2 && match[1] != "" {
			vars[strings.TrimPrefix(match[1], "$")] = true
		}
	}
	return vars
}

func parameterNames(signature string) map[string]bool {
	out := map[string]bool{}
	start := strings.Index(signature, "(")
	end := strings.LastIndex(signature, ")")
	if start < 0 || end <= start {
		return out
	}
	for _, part := range splitSimpleArguments(signature[start+1 : end]) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if eq := strings.Index(part, "="); eq >= 0 {
			part = strings.TrimSpace(part[:eq])
		}
		part = strings.TrimPrefix(part, "...")
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		if colon := strings.Index(name, ":"); colon >= 0 {
			name = name[:colon]
		}
		name = strings.TrimPrefix(strings.TrimSpace(name), "$")
		if name == "" || name == "self" || name == "this" {
			continue
		}
		out[name] = true
	}
	return out
}

func splitSimpleArguments(args string) []string {
	if strings.TrimSpace(args) == "" {
		return nil
	}
	parts := strings.Split(args, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func dataFlowCallNameIgnored(name string) bool {
	switch name {
	case "", "if", "for", "while", "switch", "return", "new", "function":
		return true
	default:
		return false
	}
}

func followsReturnedVariable(stripped string, end int) bool {
	for end < len(stripped) {
		switch stripped[end] {
		case ' ', '\t':
			end++
			continue
		case '.', '(', '-', '[':
			return true
		default:
			return false
		}
	}
	return false
}

func sortedStringSet(seen map[string]struct{}) []string {
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func signatureTypeReferences(language, signature string) map[string][]string {
	out := map[string][]string{"PARAM_TYPE": []string{}, "RETURNS_TYPE": []string{}}
	paramText, returnText := splitSignatureTypes(language, signature)
	out["PARAM_TYPE"] = typeNamesFromText(paramText)
	out["RETURNS_TYPE"] = typeNamesFromText(returnText)
	return out
}

func splitSignatureTypes(language, signature string) (string, string) {
	open := strings.Index(signature, "(")
	close := matchingParen(signature, open)
	if open < 0 || close < 0 {
		return "", ""
	}
	params := signature[open+1 : close]
	after := strings.TrimSpace(signature[close+1:])
	before := strings.TrimSpace(signature[:open])
	switch language {
	case "Go", "Rust":
		return params, after
	case "TypeScript", "JavaScript":
		if strings.HasPrefix(after, ":") {
			after = strings.TrimSpace(strings.TrimPrefix(after, ":"))
		} else {
			after = ""
		}
		return params, after
	default:
		fields := strings.Fields(before)
		if len(fields) >= 2 {
			return params, fields[len(fields)-2]
		}
		return params, ""
	}
}

func matchingParen(s string, open int) int {
	if open < 0 || open >= len(s) {
		return -1
	}
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func typeNamesFromText(text string) []string {
	seen := map[string]struct{}{}
	for name := range identifiersIn(text) {
		if !likelyUserTypeName(name) {
			continue
		}
		seen[name] = struct{}{}
	}
	return sortedStringSet(seen)
}

func likelyUserTypeName(name string) bool {
	if !isTypeName(name) {
		return false
	}
	switch strings.ToLower(name) {
	case "any", "bool", "boolean", "byte", "char", "context", "double", "error", "float", "float32", "float64", "int", "int32", "int64", "integer", "long", "map", "number", "object", "promise", "record", "result", "self", "short", "string", "str", "void":
		return false
	default:
		return true
	}
}

type configTarget struct {
	Name         string
	Confidence   float64
	Reason       string
	EvidenceKind string
	WarningCodes []string
}

func configTargets(symbol SymbolRecord, content string) []configTarget {
	switch symbol.Language {
	case "HCL":
		if symbol.Kind == "block" {
			return []configTarget{{
				Name:         hclReferenceableName(symbol.QualifiedName),
				Confidence:   0.9,
				Reason:       "HCL block declares configurable infrastructure",
				EvidenceKind: "hcl_block",
			}}
		}
	case "YAML":
		if targets := composeServiceConfigTargets(symbol, content); len(targets) > 0 {
			return targets
		}
		if symbol.Kind == "resource" && (isKubernetesPath(symbol.FilePath) || looksLikeKubernetesManifest(content)) {
			targets := []configTarget{{
				Name:         "kubernetes/" + strings.ToLower(symbol.QualifiedName),
				Confidence:   0.9,
				Reason:       "YAML manifest declares a Kubernetes resource",
				EvidenceKind: "kubernetes_resource",
			}}
			targets = append(targets, kubernetesResourceConfigTargets(content)...)
			return targets
		}
		if isKubernetesPath(symbol.FilePath) || looksLikeKubernetesManifest(content) {
			return []configTarget{{
				Name:         "kubernetes/" + symbol.Name,
				Confidence:   0.75,
				Reason:       "YAML manifest configures a Kubernetes resource",
				EvidenceKind: "kubernetes_yaml",
			}}
		}
		if yamlWorkflowPath(symbol.FilePath) {
			return []configTarget{{
				Name:         "github-actions/" + symbol.Name,
				Confidence:   0.85,
				Reason:       "GitHub Actions workflow/job configures automation",
				EvidenceKind: "workflow_yaml",
			}}
		}
	case "Kustomize":
		return []configTarget{{
			Name:         "kustomize/" + symbol.Name,
			Confidence:   0.85,
			Reason:       "Kustomize manifest configures Kubernetes overlays and resources",
			EvidenceKind: "kustomize_yaml",
		}}
	case "Dockerfile":
		if symbol.Kind == "stage" {
			return []configTarget{{
				Name:         "docker/" + symbol.Name,
				Confidence:   0.85,
				Reason:       "Dockerfile stage configures a container image",
				EvidenceKind: "dockerfile_stage",
			}}
		}
	case "JSON", "JSON5":
		if symbol.Kind == "section" {
			return []configTarget{{
				Name:         strings.ToLower(symbol.Language) + "/" + symbol.Name,
				Confidence:   0.65,
				Reason:       "JSON project/config key detected",
				EvidenceKind: "json_config",
			}}
		}
	case "TOML":
		if symbol.Kind == "section" || symbol.Kind == "setting" {
			return []configTarget{{
				Name:         "toml/" + symbol.Name,
				Confidence:   0.7,
				Reason:       "TOML project/config entry detected",
				EvidenceKind: "toml_config",
			}}
		}
	case "XML":
		if symbol.Kind == "element" {
			return []configTarget{{
				Name:         "xml/" + symbol.Name,
				Confidence:   0.65,
				Reason:       "XML project/config element detected",
				EvidenceKind: "xml_config",
			}}
		}
	case "Make":
		if symbol.Kind == "target" {
			return []configTarget{{
				Name:         "make/" + symbol.Name,
				Confidence:   0.75,
				Reason:       "Make target configures build automation",
				EvidenceKind: "make_target",
			}}
		}
	}
	return nil
}

func kubernetesResourceConfigTargets(content string) []configTarget {
	var targets []configTarget
	for _, image := range kubernetesImageRefs(content) {
		targets = append(targets, configTarget{
			Name:         "kubernetes/image/" + image,
			Confidence:   0.82,
			Reason:       "Kubernetes resource references a container image",
			EvidenceKind: "kubernetes_image",
		})
	}
	for _, env := range kubernetesEnvVarRefs(content) {
		targets = append(targets, configTarget{
			Name:         "kubernetes/env/" + env,
			Confidence:   0.78,
			Reason:       "Kubernetes resource declares an environment variable",
			EvidenceKind: "kubernetes_env",
		})
	}
	for _, port := range kubernetesPortRefs(content) {
		targets = append(targets, configTarget{
			Name:         "kubernetes/port/" + port,
			Confidence:   0.78,
			Reason:       "Kubernetes resource declares a port",
			EvidenceKind: "kubernetes_port",
		})
	}
	return targets
}

func kubernetesImageRefs(content string) []string {
	re := regexp.MustCompile(`(?im)^\s*image:\s*["']?([^"'\s#]+)`)
	var refs []string
	for _, match := range re.FindAllStringSubmatch(content, -1) {
		if len(match) == 2 {
			refs = append(refs, strings.TrimSpace(match[1]))
		}
	}
	return dedupeConfigValues(refs)
}

func kubernetesEnvVarRefs(content string) []string {
	var refs []string
	inEnv := false
	envIndent := -1
	nameRe := regexp.MustCompile(`^\s*-\s*name:\s*["']?([A-Za-z_][A-Za-z0-9_]*)`)
	for _, line := range strings.Split(content, "\n") {
		if yamlIgnoreLine(line) {
			continue
		}
		indent := yamlIndent(line)
		if inEnv && indent <= envIndent {
			inEnv = false
			envIndent = -1
		}
		if key, ok := yamlLineKey(line); ok && key == "env" {
			inEnv = true
			envIndent = indent
			continue
		}
		if !inEnv {
			continue
		}
		if match := nameRe.FindStringSubmatch(line); len(match) == 2 {
			refs = append(refs, match[1])
		}
	}
	return dedupeConfigValues(refs)
}

func kubernetesPortRefs(content string) []string {
	re := regexp.MustCompile(`(?im)^\s*(?:-\s*)?(?:containerPort|targetPort|nodePort|port):\s*["']?([0-9]+)`)
	var refs []string
	for _, match := range re.FindAllStringSubmatch(content, -1) {
		if len(match) == 2 {
			refs = append(refs, strings.TrimSpace(match[1]))
		}
	}
	return dedupeConfigValues(refs)
}

func dedupeConfigValues(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func isKubernetesPath(path string) bool {
	slash := filepath.ToSlash(strings.ToLower(path))
	return strings.Contains(slash, "k8s/") || strings.Contains(slash, "kubernetes/") || strings.Contains(slash, "manifests/")
}

func looksLikeKubernetesManifest(content string) bool {
	return regexp.MustCompile(`(?m)^apiVersion:\s*`).MatchString(content) &&
		regexp.MustCompile(`(?m)^kind:\s*`).MatchString(content)
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
	httpClientRe           = regexp.MustCompile(`(?i)(\bfetch\s*\(|\baxios\b|\brequests\s*\.|\bhttpx\b|\bhttp::\s*(get|post|put|patch|delete|head)\s*\(|\bhttp\.(get|post|put|patch|delete|head)\b|\.(get|post|put|patch|delete|head)(?:fromjson|asjson)?async(?:<[^>]+>)?\s*\(|\bhttpclient\b|\bresttemplate\b|\bwebclient\b|\bgot\s*\(|\bky\s*\()`)
	httpVerbRe             = regexp.MustCompile(`(?i)\b(?:http\.|http::\s*|requests\.|httpx\.|axios\.)?(get|post|put|patch|delete|head)(?:fromjson|asjson)?(?:async)?(?:<[^>]+>)?\s*\(`)
	urlLiteralRe           = regexp.MustCompile(`["'](https?://[^"'\s]+|/[A-Za-z0-9_\-/{}\[\]:.]*)["']`)
	httpCallArgRe          = regexp.MustCompile(`(?i)(?:\b(?:fetch|got|ky)\s*\(|\b(?:axios|requests|httpx|http)\s*\.\s*(?:get|post|put|patch|delete|head)\s*\(|\bhttp::\s*(?:get|post|put|patch|delete|head)\s*\(|\.\s*(?:get|post|put|patch|delete|head)(?:fromjson|asjson)?async(?:<[^>]+>)?\s*\()\s*([^,\n)]+)`)
	httpCallArrayJoinArgRe = regexp.MustCompile(`(?i)(?:\b(?:fetch|got|ky)\s*\(|\b(?:axios|requests|httpx|http)\s*\.\s*(?:get|post|put|patch|delete|head)\s*\(|\bhttp::\s*(?:get|post|put|patch|delete|head)\s*\(|\.\s*(?:get|post|put|patch|delete|head)(?:fromjson|asjson)?async(?:<[^>]+>)?\s*\()\s*(\[[^\]\n]*\]\s*\.\s*join\s*\([^\)\n]*\))`)
)

// httpCalls extracts outbound HTTP client calls from a code block: lines that
// carry a client-library signal and a URL/path literal. Absolute URLs are
// reduced to their path so a client call and a local route registration to the
// same path share an endpoint node.
func httpCalls(content string) []httpCall {
	return httpCallsWithConstants(content, nil)
}

func httpCallsWithConstants(content string, constants map[string]string) []httpCall {
	var out []httpCall
	seen := map[string]bool{}
	add := func(method, path string, absolute bool) {
		if path == "" {
			return
		}
		path = normalizeRouteParamSyntax(path)
		key := method + " " + path
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, httpCall{Method: method, Path: path, Absolute: absolute})
	}
	for _, line := range strings.Split(content, "\n") {
		if !httpClientRe.MatchString(line) {
			continue
		}
		method := "GET"
		if m := httpVerbRe.FindStringSubmatch(line); m != nil {
			method = strings.ToUpper(m[1])
		}
		for _, match := range httpCallArrayJoinArgRe.FindAllStringSubmatch(line, -1) {
			if len(match) != 2 {
				continue
			}
			if path, absolute, ok := staticHTTPCallExpressionValue(match[1], constants); ok {
				add(method, path, absolute)
			}
		}
		for _, match := range httpCallArgRe.FindAllStringSubmatch(line, -1) {
			if len(match) != 2 {
				continue
			}
			if path, absolute, ok := staticHTTPCallExpressionValue(match[1], constants); ok {
				add(method, path, absolute)
			}
		}
		for _, lm := range urlLiteralRe.FindAllStringSubmatch(line, -1) {
			path, absolute := httpPath(lm[1])
			add(method, path, absolute)
		}
	}
	return out
}

func staticHTTPCallExpressionValue(expr string, constants map[string]string) (string, bool, bool) {
	expr = strings.TrimSpace(expr)
	if (strings.HasPrefix(expr, `"`) && strings.HasSuffix(expr, `"`)) || (strings.HasPrefix(expr, `'`) && strings.HasSuffix(expr, `'`)) {
		literal := strings.Trim(expr, `"'`)
		path, absolute := httpPath(literal)
		return path, absolute, path != ""
	}
	route, ok := staticRouteExpressionValue(expr, constants)
	return route, false, ok
}

func normalizeRouteParamSyntax(path string) string {
	path = regexp.MustCompile(`\[\.{0,3}([A-Za-z_][A-Za-z0-9_]*)\]`).ReplaceAllString(path, `{$1}`)
	return regexp.MustCompile(`<(?:(?:[A-Za-z_][A-Za-z0-9_]*):)?([A-Za-z_][A-Za-z0-9_]*)>`).ReplaceAllString(path, `{$1}`)
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

// fieldAccess is a `receiver.field` / `receiver->field` access (not a call),
// classified as a read, a write (assignment target), or an address-of.
type fieldAccess struct {
	Receiver  string
	Field     string
	Write     bool
	AddressOf bool
}

var fieldAccessRe = regexp.MustCompile(`(&?)\s*([A-Za-z_$][\w$]*)\s*(?:->|\.)\s*([A-Za-z_]\w*)`)
var goReceiverRe = regexp.MustCompile(`^func\s*\(\s*([A-Za-z_]\w*)\s+\*?[A-Za-z_]`)

// fieldAccesses extracts distinct receiver.field accesses from a block,
// classifying each as a write (followed by an assignment operator), an
// address-of, or a read. Method calls (field followed by "(") are excluded.
func fieldAccesses(block string) []fieldAccess {
	stripped := stripCodeLiteralsAndComments(block)
	var out []fieldAccess
	seen := map[string]bool{}
	for _, m := range fieldAccessRe.FindAllStringSubmatchIndex(stripped, -1) {
		amp := stripped[m[2]:m[3]]
		receiver := strings.TrimPrefix(stripped[m[4]:m[5]], "$")
		field := stripped[m[6]:m[7]]
		after := strings.TrimLeft(stripped[m[7]:], " \t")
		if strings.HasPrefix(after, "(") {
			continue // method call, handled by receiver-call resolution
		}
		access := fieldAccess{Receiver: receiver, Field: field, AddressOf: amp == "&", Write: isAssignTarget(after)}
		key := receiver + "." + field
		switch {
		case access.AddressOf:
			key += "&"
		case access.Write:
			key += "w"
		default:
			key += "r"
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, access)
	}
	return out
}

// isAssignTarget reports whether the text immediately after a field reference
// makes it the target of a write.
func isAssignTarget(after string) bool {
	if strings.HasPrefix(after, "==") {
		return false
	}
	if strings.HasPrefix(after, "=") {
		return true
	}
	for _, op := range []string{"+=", "-=", "*=", "/=", "%=", "|=", "&=", "^=", "<<=", ">>=", "++", "--"} {
		if strings.HasPrefix(after, op) {
			return true
		}
	}
	return false
}

// goReceiverVar returns the receiver variable name from a Go method signature
// (`func (a *T) M(...)` -> "a"), so a.field accesses resolve to the enclosing
// type. Empty for non-Go or non-method signatures.
func goReceiverVar(signature string) string {
	if m := goReceiverRe.FindStringSubmatch(signature); m != nil {
		return m[1]
	}
	return ""
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

// typedMethodCall is a `new Type().method(` / `Type().method(` call site. It is
// intentionally limited to direct constructor chains so relation extraction can
// resolve the receiver without tracking arbitrary returned values.
type typedMethodCall struct {
	TypeName string
	Method   string
	Detail   string
}

type returnedMethodCall struct {
	Factory string
	Method  string
	Detail  string
}

type returnedMethodChainCall struct {
	Factory     string
	FirstMethod string
	Method      string
	Detail      string
}

type typedMethodChainCall struct {
	TypeName    string
	FirstMethod string
	Method      string
	Detail      string
}

var (
	newCtorMethodChainCallRe  = regexp.MustCompile(`\bnew\s+([A-Z][A-Za-z0-9_]*)\s*\([^)]*\)\s*\.\s*([A-Za-z_]\w*)\s*\([^)]*\)\s*\.\s*([A-Za-z_]\w*)\s*\(`)
	ctorMethodChainCallRe     = regexp.MustCompile(`\b([A-Z][A-Za-z0-9_]*)\s*\([^)]*\)\s*\.\s*([A-Za-z_]\w*)\s*\([^)]*\)\s*\.\s*([A-Za-z_]\w*)\s*\(`)
	returnedMethodChainCallRe = regexp.MustCompile(`\b([A-Za-z_$][\w$]*)\s*\([^)]*\)\s*\.\s*([A-Za-z_]\w*)\s*\([^)]*\)\s*\.\s*([A-Za-z_]\w*)\s*\(`)
	newCtorMethodCallRe       = regexp.MustCompile(`\bnew\s+([A-Z][A-Za-z0-9_]*)\s*\([^)]*\)\s*\.\s*([A-Za-z_]\w*)\s*\(`)
	ctorMethodCallRe          = regexp.MustCompile(`\b([A-Z][A-Za-z0-9_]*)\s*\([^)]*\)\s*\.\s*([A-Za-z_]\w*)\s*\(`)
	returnedMethodCallRe      = regexp.MustCompile(`\b([A-Za-z_$][\w$]*)\s*\([^)]*\)\s*\.\s*([A-Za-z_]\w*)\s*\(`)
)

func chainedConstructorCalls(block string) []typedMethodCall {
	stripped := stripCodeLiteralsAndComments(block)
	var out []typedMethodCall
	seen := map[string]bool{}
	add := func(typeName, method, detail string) {
		key := typeName + "." + method
		if typeName == "" || method == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, typedMethodCall{TypeName: typeName, Method: method, Detail: detail})
	}
	for _, m := range newCtorMethodCallRe.FindAllStringSubmatch(stripped, -1) {
		add(m[1], m[2], "new "+m[1]+"()."+m[2])
	}
	for _, m := range ctorMethodCallRe.FindAllStringSubmatch(stripped, -1) {
		add(m[1], m[2], m[1]+"()."+m[2])
	}
	return out
}

func returnedReceiverCalls(block string) []returnedMethodCall {
	stripped := stripCodeLiteralsAndComments(block)
	var out []returnedMethodCall
	seen := map[string]bool{}
	for _, m := range returnedMethodCallRe.FindAllStringSubmatch(stripped, -1) {
		if len(m) != 3 {
			continue
		}
		factory := strings.TrimPrefix(m[1], "$")
		if factory == "" || factory == "new" || isCapitalized(factory) {
			continue
		}
		key := factory + "." + m[2]
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, returnedMethodCall{Factory: factory, Method: m[2], Detail: factory + "()." + m[2]})
	}
	return out
}

func chainedConstructorReturnCalls(block string) []typedMethodChainCall {
	stripped := stripCodeLiteralsAndComments(block)
	var out []typedMethodChainCall
	seen := map[string]bool{}
	add := func(typeName, firstMethod, method, detail string) {
		key := typeName + "." + firstMethod + "." + method
		if typeName == "" || firstMethod == "" || method == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, typedMethodChainCall{TypeName: typeName, FirstMethod: firstMethod, Method: method, Detail: detail})
	}
	for _, m := range newCtorMethodChainCallRe.FindAllStringSubmatch(stripped, -1) {
		if len(m) == 4 {
			add(m[1], m[2], m[3], "new "+m[1]+"()."+m[2]+"()."+m[3])
		}
	}
	for _, m := range ctorMethodChainCallRe.FindAllStringSubmatch(stripped, -1) {
		if len(m) == 4 {
			add(m[1], m[2], m[3], m[1]+"()."+m[2]+"()."+m[3])
		}
	}
	return out
}

func returnedReceiverChainCalls(block string) []returnedMethodChainCall {
	stripped := stripCodeLiteralsAndComments(block)
	var out []returnedMethodChainCall
	seen := map[string]bool{}
	for _, m := range returnedMethodChainCallRe.FindAllStringSubmatch(stripped, -1) {
		if len(m) != 4 {
			continue
		}
		factory := strings.TrimPrefix(m[1], "$")
		if factory == "" || factory == "new" || isCapitalized(factory) {
			continue
		}
		key := factory + "." + m[2] + "." + m[3]
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, returnedMethodChainCall{Factory: factory, FirstMethod: m[2], Method: m[3], Detail: factory + "()." + m[2] + "()." + m[3]})
	}
	return out
}

func isCapitalized(value string) bool {
	if value == "" {
		return false
	}
	r := rune(value[0])
	return r >= 'A' && r <= 'Z'
}

var (
	newAssignRe           = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*:?=\s*new\s+([A-Za-z_]\w*)`)
	ctorAssignRe          = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*:?=\s*&?([A-Z][A-Za-z0-9_]*)\s*[({]`)
	factoryReturnAssignRe = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*(?::[^=\n]+)?\s*(?::=|=)\s*(?:await\s+)?([A-Za-z_$][\w$]*)\s*\(`)
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

func factoryReturnVarTypes(block, filePath string, returnTypesBySymbolNameAndFile map[string]map[string][]string) map[string]string {
	stripped := stripCodeLiteralsAndComments(block)
	out := map[string]string{}
	for _, m := range factoryReturnAssignRe.FindAllStringSubmatch(stripped, -1) {
		if len(m) != 3 {
			continue
		}
		name := strings.TrimPrefix(m[1], "$")
		factory := strings.TrimPrefix(m[2], "$")
		if name == "" || factory == "" || factory == "new" || isCapitalized(factory) {
			continue
		}
		types := returnTypesBySymbolNameAndFile[factory][filePath]
		if len(types) == 0 {
			continue
		}
		out[name] = types[0]
	}
	return out
}

func parameterVarTypes(signature string) map[string]string {
	out := map[string]string{}
	start := strings.Index(signature, "(")
	end := strings.LastIndex(signature, ")")
	if start < 0 || end <= start {
		return out
	}
	params := strings.Split(stripGenerics(signature[start+1:end]), ",")
	colonParamRe := regexp.MustCompile(`^\s*\$?([A-Za-z_][A-Za-z0-9_]*)\??\s*:\s*\??([A-Z][A-Za-z0-9_]*)\b`)
	typeFirstParamRe := regexp.MustCompile(`^\s*(?:final\s+)?(?:[*&]\s*)?([A-Z][A-Za-z0-9_]*)\s+\$?([A-Za-z_][A-Za-z0-9_]*)\b`)
	nameFirstParamRe := regexp.MustCompile(`^\s*\$?([A-Za-z_][A-Za-z0-9_]*)\s+(?:[*&]\s*)?([A-Z][A-Za-z0-9_]*)\b`)
	for _, param := range params {
		param = strings.TrimSpace(strings.SplitN(param, "=", 2)[0])
		if param == "" {
			continue
		}
		if m := colonParamRe.FindStringSubmatch(param); len(m) == 3 {
			out[m[1]] = m[2]
			continue
		}
		if m := typeFirstParamRe.FindStringSubmatch(param); len(m) == 3 {
			out[m[2]] = m[1]
			continue
		}
		if m := nameFirstParamRe.FindStringSubmatch(param); len(m) == 3 {
			out[m[1]] = m[2]
		}
	}
	delete(out, "self")
	delete(out, "this")
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
