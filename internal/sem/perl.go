package sem

import (
	"path/filepath"
	"regexp"
	"strings"
)

var (
	perlReceiverChainRe   = regexp.MustCompile(`(?:\$?[A-Za-z_]\w*|[A-Za-z_]\w*(?:::[A-Za-z_]\w*)*)[ \t]*(?:->[ \t]*[A-Za-z_]\w*[ \t]*(?:\([^()\n]*\))?[ \t]*)+`)
	perlReceiverSegmentRe = regexp.MustCompile(`->[ \t]*([A-Za-z_]\w*)`)
	perlCtorAssignRe      = regexp.MustCompile(`(?m)(?:^|[^\S\n])(?:(?:my|our|state)\s+)?\$([A-Za-z_]\w*)\s*=\s*([A-Z][A-Za-z0-9_]*(?:::[A-Za-z_]\w*)*)\s*->[ \t]*new\b`)
	perlChainAssignRe     = regexp.MustCompile(`(?m)(?:^|[^\S\n])(?:(?:my|our|state)\s+)?\$([A-Za-z_]\w*)\s*=\s*\$([A-Za-z_]\w*)([^\n;]*)`)
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
		segments := perlReceiverSegmentRe.FindAllStringSubmatch(chain, -1)
		if len(segments) == 0 {
			continue
		}
		method := segments[len(segments)-1][1]
		if method == "" || method == "new" {
			continue
		}
		receiver := strings.TrimSpace(strings.SplitN(chain, "->", 2)[0])
		receiver = strings.TrimPrefix(receiver, "$")
		if receiver == "" {
			continue
		}
		key := receiver + "." + method
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, receiverCall{Receiver: receiver, Method: method})
	}
	return out
}

func perlLocalVarTypes(block string) map[string]string {
	stripped := stripPerlCodeLiteralsAndComments(block)
	out := map[string]string{}
	for _, m := range perlCtorAssignRe.FindAllStringSubmatch(stripped, -1) {
		if len(m) != 3 {
			continue
		}
		out[m[1]] = m[2]
	}
	for changed := true; changed; {
		changed = false
		for _, m := range perlChainAssignRe.FindAllStringSubmatch(stripped, -1) {
			if len(m) != 4 {
				continue
			}
			dst, receiver, chain := m[1], m[2], m[3]
			if _, exists := out[dst]; exists {
				continue
			}
			receiverType := out[receiver]
			if receiverType == "" {
				continue
			}
			// Treat multi-hop chains on a typed Perl receiver as fluent
			// same-object assignments. This covers idioms such as
			// `$base = $url->base(...)->base->userinfo(...)` without typing
			// single-hop value getters like `$path = $url->path`.
			if len(perlReceiverSegmentRe.FindAllStringSubmatch(chain, -1)) < 2 {
				continue
			}
			out[dst] = receiverType
			changed = true
		}
	}
	return out
}

func stripPerlCodeLiteralsAndComments(content string) string {
	bytes := []byte(stripCodeLiteralsAndComments(content))
	for i := 0; i < len(bytes); i++ {
		if bytes[i] != '#' || !perlHashStartsComment(bytes, i) {
			continue
		}
		j := i + 1
		for j < len(bytes) && bytes[j] != '\n' && bytes[j] != '\r' {
			j++
		}
		maskBytes(bytes, i, j)
		i = j
	}
	return string(bytes)
}

func perlHashStartsComment(bytes []byte, pos int) bool {
	if pos == 0 {
		return true
	}
	prev := bytes[pos-1]
	switch prev {
	case '\n', '\r', ' ', '\t', ';', '{', '}', '(', ')':
		return true
	default:
		return false
	}
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
	typePath := strings.ReplaceAll(strings.TrimSpace(typeName), "::", "/")
	if typePath == "" {
		return false
	}
	if strings.HasSuffix(filePath, typePath+".pm") {
		return true
	}
	if strings.Contains(typeName, "::") {
		return false
	}
	return filepath.Base(filePath) == typeName+".pm"
}
