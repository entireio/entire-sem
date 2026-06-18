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
