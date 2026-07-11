package sem

import (
	"regexp"
	"strings"
)

var dartSetterAssignmentRe = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_$]*)\s*\.\s*([A-Za-z_][A-Za-z0-9_$]*)\s*=`)

func dartSetterAssignmentCalls(block string) []receiverCall {
	stripped := stripCodeLiteralsAndComments(maskDartMultilineStrings(block))
	var out []receiverCall
	seen := map[string]bool{}
	for _, m := range dartSetterAssignmentRe.FindAllStringSubmatchIndex(stripped, -1) {
		if len(m) < 6 {
			continue
		}
		if m[1] < len(stripped) {
			switch stripped[m[1]] {
			case '=', '>':
				continue
			}
		}
		// Reject a receiver preceded (after optional whitespace) by `.`/`?.`/`..`:
		// a nested field assignment (`a.cfg.value = 1`) matches at the intermediate
		// segment `cfg`, whose true receiver is `a.cfg`, not `cfg`. Emitting
		// {cfg,value} could resolve against a same-named differently-typed local, so
		// nested field assignments emit nothing (recall loss OK; precision first).
		j := m[0] - 1
		for j >= 0 && (stripped[j] == ' ' || stripped[j] == '\t') {
			j--
		}
		if j >= 0 && stripped[j] == '.' {
			continue
		}
		receiver := strings.TrimSpace(stripped[m[2]:m[3]])
		method := strings.TrimSpace(stripped[m[4]:m[5]])
		key := receiver + "." + method
		if receiver == "" || method == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, receiverCall{Receiver: receiver, Method: method, SetterAssign: true})
	}
	return out
}

// dartSetterSignatureRe matches a Dart setter declaration head (`set name(...)`,
// optionally with leading modifiers such as `static`). A getter head
// (`Type get name`) carries no parameter list, so the trailing `(` cleanly
// excludes it.
var dartSetterSignatureRe = regexp.MustCompile(`(?:^|[^\w$])set[ \t]+([A-Za-z_$][\w$]*)[ \t]*\(`)

// dartIsSetterAccessor reports whether a Dart method symbol's signature declares
// the setter accessor named `name`.
func dartIsSetterAccessor(signature, name string) bool {
	for _, m := range dartSetterSignatureRe.FindAllStringSubmatch(signature, -1) {
		if m[1] == name {
			return true
		}
	}
	return false
}

// dartSetterAccessor finds the setter accessor named `name` reachable from
// container `start` up its super chain, scanning the workspace short-name index
// (records == symbolsByShortName[name]). The short-name method index keyed by
// container collapses a read-write property's getter and setter to whichever was
// declared last, so a setter-assignment call resolves through this dedicated
// lookup to target the setter deterministically. Returns the setter and whether
// it was found on a base type (inherited).
func dartSetterAccessor(start, name string, records []SymbolRecord, superContainerByID map[string]string) (SymbolRecord, bool, bool) {
	for c := start; c != ""; c = superContainerByID[c] {
		for _, s := range records {
			if s.Language != "Dart" || s.ContainerID != c {
				continue
			}
			if dartIsSetterAccessor(s.Signature, name) {
				return s, c != start, true
			}
		}
	}
	return SymbolRecord{}, false, false
}

func maskDartMultilineStrings(content string) string {
	bytes := []byte(content)
	for i := 0; i+2 < len(bytes); i++ {
		start := i
		quotePos := i
		if (bytes[i] == 'r' || bytes[i] == 'R') && i+3 < len(bytes) && isDartTripleQuote(bytes[i+1:]) {
			quotePos = i + 1
		} else if !isDartTripleQuote(bytes[i:]) {
			continue
		}
		quote := bytes[quotePos]
		j := quotePos + 3
		for j+2 < len(bytes) && !(bytes[j] == quote && bytes[j+1] == quote && bytes[j+2] == quote) {
			j++
		}
		if j+2 < len(bytes) {
			j += 3
		}
		maskBytes(bytes, start, j)
		i = j - 1
	}
	return string(bytes)
}

func isDartTripleQuote(bytes []byte) bool {
	if len(bytes) < 3 {
		return false
	}
	return (bytes[0] == '\'' || bytes[0] == '"') && bytes[1] == bytes[0] && bytes[2] == bytes[0]
}
