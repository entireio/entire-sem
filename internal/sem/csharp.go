package sem

// C#-specific call-site extraction. The generic scanners miss the dominant
// C# call idioms (evidence: on dotnet/efcore the focus method
// HistoryRepository.GetCreateCommands kept 1 of 4 outbound edges):
//
//   - Property receivers (`Dependencies.ModelDiffer.GetDifferences(...)`) are
//     matched by receiverCallRe as `ModelDiffer.GetDifferences(`, a receiver no
//     var-type source can type; the receiver's type is declared at the class
//     level (`protected virtual HistoryRepositoryDependencies Dependencies
//     { get; }`) and the chain hops through *another* class's typed property.
//   - Extension methods (`model.GetRelationalModel()`) live as static methods
//     with a `this` first parameter on an unrelated static class, so looking
//     the method up on the receiver's own type can never find them.
//   - Multi-line verbatim (@"...") and raw ("""...""") string bodies are not
//     masked by the generic stripper (which stops string masking at a line
//     end), so SQL/text blocks feed the call scanners.
//
// Everything in this file is gated to Language == "C#" by its callers so the
// other languages keep their existing behavior. Receiver typing follows the
// same conservative straight-line rules as the PHP port (php.go): declared
// member types only, single hop, conflicting or generic types dropped.
// Interface-typed receivers resolve to the interface's method symbol — that is
// how C# dispatch is declared, and the concrete implementation is a separate
// IMPLEMENTS/OVERRIDES hop.

import (
	"regexp"
	"strings"
)

// csharpChainCallRe matches a one-hop member chain call `A.B.Method(...)`,
// optionally spelled `this.A.B.Method(...)`. Deeper chains and call-result
// receivers are rejected by the prefix guard in csharpMemberChainCalls.
var csharpChainCallRe = regexp.MustCompile(`(?:\bthis\s*\.\s*)?\b([A-Za-z_]\w*)\s*\.\s*([A-Za-z_]\w*)\s*\.\s*([A-Za-z_]\w*)\s*\(`)

// csharpChainCall is an `A.B.Method(...)` call site: receiver A (typed by a
// class-level property/field), property B on A's type, and the called method.
type csharpChainCall struct {
	Receiver string
	Property string
	Method   string
	Detail   string
}

// maskCSharpTextBlocks blanks the C# string forms the generic stripper cannot
// contain: raw string literals ("""...""", possibly multi-line, closed by the
// same number of quotes) and verbatim strings (@"..." / $@"..." / @$"...",
// possibly multi-line, where `""` is the only escape and backslash is literal).
// Length and line structure are preserved. Ordinary and interpolated
// single-line strings are left for stripCodeLiteralsAndComments.
func maskCSharpTextBlocks(s string) string {
	bytes := []byte(s)
	for i := 0; i < len(bytes); i++ {
		switch bytes[i] {
		case '/':
			// Skip comments so prose like `// @"...` cannot open a phantom
			// string mask that swallows following code.
			if i+1 < len(bytes) && bytes[i+1] == '/' {
				for i < len(bytes) && bytes[i] != '\n' && bytes[i] != '\r' {
					i++
				}
			} else if i+1 < len(bytes) && bytes[i+1] == '*' {
				for i+1 < len(bytes) && !(bytes[i] == '*' && bytes[i+1] == '/') {
					i++
				}
				i++
			}
		case '\'':
			// Skip char literals so '"' cannot desynchronize the quote scan.
			j := i + 1
			for j < len(bytes) && bytes[j] != '\n' && bytes[j] != '\r' && bytes[j] != '\'' {
				if bytes[j] == '\\' {
					j++
				}
				j++
			}
			if j < len(bytes) {
				i = j
			} else {
				i = len(bytes) - 1
			}
		case '"':
			if quotes := runLength(bytes, i, '"'); quotes >= 3 {
				// Raw string literal: masked up to a closing run of at least as
				// many quotes (an unterminated literal masks to the end).
				end := indexOfQuoteRun(bytes, i+quotes, quotes)
				maskBytes(bytes, i, end)
				i = end - 1
				continue
			}
			// Ordinary or interpolated string: the generic stripper masks it
			// (single line, backslash escapes). Skip to its end here only so a
			// quote inside it cannot be misread as a verbatim/raw opener.
			j := i + 1
			for j < len(bytes) && bytes[j] != '\n' && bytes[j] != '\r' && bytes[j] != '"' {
				if bytes[j] == '\\' {
					j++
				}
				j++
			}
			if j < len(bytes) {
				i = j
			} else {
				i = len(bytes) - 1
			}
		case '@':
			// Verbatim string opener: @" with an optional interpolation `$` on
			// either side (@$" / $@"). `@identifier` keyword escapes have no
			// quote and fall through.
			j := i + 1
			if j < len(bytes) && bytes[j] == '$' {
				j++
			}
			if j >= len(bytes) || bytes[j] != '"' {
				continue
			}
			end := len(bytes)
			for k := j + 1; k < len(bytes); k++ {
				if bytes[k] == '"' {
					if k+1 < len(bytes) && bytes[k+1] == '"' {
						k++ // "" escape
						continue
					}
					end = k + 1
					break
				}
			}
			maskBytes(bytes, i, end)
			i = end - 1
		}
	}
	return string(bytes)
}

// runLength counts consecutive occurrences of b starting at i.
func runLength(bytes []byte, i int, b byte) int {
	n := 0
	for i+n < len(bytes) && bytes[i+n] == b {
		n++
	}
	return n
}

// indexOfQuoteRun returns the position just past the first run of at least
// `count` double quotes at or after i, or len(bytes) if none exists.
func indexOfQuoteRun(bytes []byte, i, count int) int {
	for i < len(bytes) {
		if bytes[i] != '"' {
			i++
			continue
		}
		n := runLength(bytes, i, '"')
		if n >= count {
			return i + n
		}
		i += n
	}
	return len(bytes)
}

// csharpMemberChainCalls extracts one-hop member chain call sites
// `A.B.Method(...)` (or `this.A.B.Method(...)`). Matches whose receiver is
// itself preceded by `.`, `)`, `]` or another identifier character are skipped:
// those are tails of deeper chains or call results, which straight-line member
// typing cannot resolve.
func csharpMemberChainCalls(block string) []csharpChainCall {
	stripped := stripCodeLiteralsAndComments(maskCSharpTextBlocks(block))
	var out []csharpChainCall
	seen := map[string]bool{}
	for _, m := range csharpChainCallRe.FindAllStringSubmatchIndex(stripped, -1) {
		if prev := lastNonSpaceByte(stripped, m[0]); prev == '.' || prev == ')' || prev == ']' || prev == '?' {
			continue
		}
		receiver := stripped[m[2]:m[3]]
		property := stripped[m[4]:m[5]]
		method := stripped[m[6]:m[7]]
		key := receiver + "." + property + "." + method
		if receiver == "" || property == "" || method == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, csharpChainCall{Receiver: receiver, Property: property, Method: method, Detail: key})
	}
	return out
}

// csharpNonTailReceiverCalls extracts receiver.method() call sites like
// receiverCalls but drops matches whose receiver token is itself preceded by
// `.`, `?`, `)` or `]`: those are tails of member chains
// (`Dependencies.MigrationsSqlGenerator.Generate(`) or call results, where the
// "receiver" is a property name. Feeding them to the typed tiers misresolves
// them — EF Core names properties after their concrete type, so the
// static-call tier reads the tail as a static call on that class. A pair that
// also occurs in clean receiver position elsewhere in the block is kept.
func csharpNonTailReceiverCalls(block string) []receiverCall {
	stripped := stripCodeLiteralsAndComments(block)
	var out []receiverCall
	seen := map[string]bool{}
	for _, m := range receiverCallRe.FindAllStringSubmatchIndex(stripped, -1) {
		if prev := lastNonSpaceByte(stripped, m[2]); prev == '.' || prev == '?' || prev == ')' || prev == ']' {
			continue
		}
		receiver := stripped[m[2]:m[3]]
		method := stripped[m[4]:m[5]]
		key := receiver + "." + method
		if receiver == "" || seen[key] {
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

// lastNonSpaceByte returns the last non-whitespace byte before position i, or
// 0 when the text starts there.
func lastNonSpaceByte(s string, i int) byte {
	for j := i - 1; j >= 0; j-- {
		switch s[j] {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			return s[j]
		}
	}
	return 0
}

// csharpReceiverMemberType types a receiver through the enclosing class's
// declared members, gated to C# callers so the switch branch in
// receiverCallRelations stays inert for every other language.
func csharpReceiverMemberType(from SymbolRecord, receiver string, fieldsByContainer map[string]map[string]SymbolRecord, superContainerByID map[string]string) (string, bool) {
	if from.Language != "C#" || from.ContainerID == "" {
		return "", false
	}
	return csharpMemberType(fieldsByContainer, superContainerByID, from.ContainerID, receiver)
}

// csharpMemberType resolves a class member's declared type by name, walking up
// the inheritance chain like lookupMethodUpChain (a subclass method calling
// `Dependencies.X()` types the property declared on the base class). The type
// comes from the field symbol's `name Type` signature; nullable markers are
// stripped, and generic or qualified types are dropped (a method lookup on
// `IReadOnlyList<T>` has no workspace target).
func csharpMemberType(fieldsByContainer map[string]map[string]SymbolRecord, superContainerByID map[string]string, container, name string) (string, bool) {
	seen := map[string]bool{}
	for c, hops := container, 0; c != "" && !seen[c] && hops < 32; c, hops = superContainerByID[c], hops+1 {
		seen[c] = true
		field, ok := fieldsByContainer[c][name]
		if !ok {
			continue
		}
		typeName, ok := csharpFieldSignatureType(field.Name, field.Signature)
		return typeName, ok
	}
	return "", false
}

// csharpFieldSignatureType extracts the declared type from a field symbol's
// `name Type` signature. Only plain capitalized identifiers qualify (nullable
// `?` stripped); generic, tuple, qualified, and builtin lowercase types return
// false rather than a guess.
func csharpFieldSignatureType(name, signature string) (string, bool) {
	typeText := strings.TrimSpace(strings.TrimPrefix(signature, name))
	typeText = strings.TrimSuffix(typeText, "?")
	if typeText == "" || !isCapitalized(typeText) {
		return "", false
	}
	for i := 0; i < len(typeText); i++ {
		if !isPHPWordByte(typeText[i]) {
			return "", false
		}
	}
	return typeText, true
}

// csharpExtensionMethodSignature reports whether a method signature declares a
// C# extension method: a static method whose first parameter carries the
// `this` modifier.
func csharpExtensionMethodSignature(signature string) bool {
	open := strings.Index(signature, "(")
	if open < 0 {
		return false
	}
	before := " " + signature[:open] + " "
	if !strings.Contains(before, " static ") {
		return false
	}
	rest := strings.TrimSpace(signature[open+1:])
	return strings.HasPrefix(rest, "this ") || strings.HasPrefix(rest, "this\t")
}

// csharpUniqueExtensionMethod resolves a receiver call by unique extension
// method name: exactly one method with this short name exists in the workspace
// and it is extension-shaped. This is the same "globally unique name"
// heuristic as the Go fallback tier, further gated by the `this`-parameter
// shape so ordinary same-named instance methods never match, and overloaded
// extension groups (which the shape alone cannot disambiguate) are dropped.
func csharpUniqueExtensionMethod(candidates []SymbolRecord) (SymbolRecord, bool) {
	method, ok := uniqueMethodByShortName(candidates)
	if !ok || method.Language != "C#" || !csharpExtensionMethodSignature(method.Signature) {
		return SymbolRecord{}, false
	}
	return method, true
}
