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
		topLevelChain := maskPerlParenthesizedArgumentContents(chain)
		segments := perlReceiverSegmentRe.FindAllStringSubmatch(topLevelChain, -1)
		if len(segments) == 0 {
			continue
		}
		method := segments[len(segments)-1][1]
		if method == "" || method == "new" {
			continue
		}
		receiver := strings.TrimSpace(strings.SplitN(topLevelChain, "->", 2)[0])
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
			if len(perlReceiverSegmentRe.FindAllStringSubmatch(maskPerlParenthesizedArgumentContents(chain), -1)) < 2 {
				continue
			}
			out[dst] = receiverType
			changed = true
		}
	}
	return out
}

func stripPerlCodeLiteralsAndComments(content string) string {
	bytes := []byte(content)
	for i := 0; i < len(bytes); i++ {
		switch bytes[i] {
		case '"', '\'', '`':
			quote := bytes[i]
			for j := i + 1; j < len(bytes); j++ {
				if bytes[j] == '\n' || bytes[j] == '\r' {
					i = j
					break
				}
				if bytes[j] == '\\' {
					j++
					continue
				}
				if bytes[j] == quote {
					maskBytes(bytes, i, j+1)
					i = j
					break
				}
			}
		case '#':
			if !perlHashStartsComment(bytes, i) {
				continue
			}
			j := i + 1
			for j < len(bytes) && bytes[j] != '\n' && bytes[j] != '\r' {
				j++
			}
			maskBytes(bytes, i, j)
			i = j
		}
	}
	return string(bytes)
}

func maskPerlParenthesizedArgumentContents(content string) string {
	bytes := []byte(content)
	depth := 0
	start := -1
	for i, ch := range bytes {
		switch ch {
		case '(':
			if depth == 0 {
				start = i + 1
			}
			depth++
		case ')':
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 && start >= 0 {
				maskBytes(bytes, start, i)
				start = -1
			}
		}
	}
	if depth > 0 && start >= 0 {
		maskBytes(bytes, start, len(bytes))
	}
	return string(bytes)
}

func perlHashStartsComment(bytes []byte, pos int) bool {
	if pos == 0 {
		return true
	}
	if bytes[pos-1] == '$' {
		return false
	}
	if perlHashInHashDelimitedRegex(bytes, pos) {
		return false
	}
	if perlHashStartsRegexLiteral(bytes, pos) {
		return false
	}
	return true
}

func perlHashInHashDelimitedRegex(bytes []byte, pos int) bool {
	lineStart := pos
	for lineStart > 0 && bytes[lineStart-1] != '\n' && bytes[lineStart-1] != '\r' {
		lineStart--
	}
	line := string(bytes[lineStart : pos+1])
	for i := 0; i < len(line); i++ {
		for _, operator := range []string{"qr", "tr", "s", "m", "y"} {
			if !strings.HasPrefix(line[i:], operator+"#") {
				continue
			}
			if i > 0 && perlIdentByte(line[i-1]) {
				continue
			}
			segment := line[i:]
			if semi := strings.IndexByte(segment, ';'); semi >= 0 && i+semi < pos-lineStart {
				continue
			}
			hashes := strings.Count(segment, "#")
			if hashes == 0 {
				continue
			}
			limit := 2
			if operator == "s" || operator == "tr" || operator == "y" {
				limit = 3
			}
			if hashes <= limit {
				return true
			}
		}
	}
	return false
}

func perlHashStartsRegexLiteral(bytes []byte, pos int) bool {
	if pos == 0 {
		return false
	}
	opener := bytes[pos-1]
	if !perlRegexOpeningDelimiter(opener) {
		return false
	}
	lineStart := pos - 1
	for lineStart > 0 && bytes[lineStart-1] != '\n' && bytes[lineStart-1] != '\r' {
		lineStart--
	}
	prefix := strings.TrimRight(string(bytes[lineStart:pos-1]), " \t")
	if strings.HasSuffix(prefix, "=~") || strings.HasSuffix(prefix, "!~") {
		return true
	}
	if opener == '/' && perlRegexAtExpressionStart(prefix) {
		return true
	}
	for _, operator := range []string{"qr", "tr", "s", "m", "y"} {
		if !strings.HasSuffix(prefix, operator) {
			continue
		}
		start := len(prefix) - len(operator)
		if start == 0 || !perlIdentByte(prefix[start-1]) {
			return true
		}
	}
	return false
}

func perlRegexAtExpressionStart(prefix string) bool {
	trimmed := strings.TrimSpace(prefix)
	if trimmed == "" {
		return true
	}
	last := trimmed[len(trimmed)-1]
	switch last {
	case '(', '[', '{', ',', ';', '=', '!', '~', '?', ':':
		return true
	default:
		return false
	}
}

func perlRegexOpeningDelimiter(delimiter byte) bool {
	switch delimiter {
	case '/', '{', '(', '[', '<':
		return true
	default:
		return false
	}
}

func perlIdentByte(ch byte) bool {
	return ch == '_' || ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
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
