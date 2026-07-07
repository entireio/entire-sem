package sem

import (
	"regexp"
	"strings"
)

var dartSetterAssignmentRe = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_$]*)\s*\.\s*([A-Za-z_][A-Za-z0-9_]*)\s*=`)

func dartSetterAssignmentCalls(block string) []receiverCall {
	stripped := stripCodeLiteralsAndComments(block)
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
		receiver := strings.TrimSpace(stripped[m[2]:m[3]])
		method := strings.TrimSpace(stripped[m[4]:m[5]])
		key := receiver + "." + method
		if receiver == "" || method == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, receiverCall{Receiver: receiver, Method: method})
	}
	return out
}
