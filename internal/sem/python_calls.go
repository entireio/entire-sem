package sem

import (
	"regexp"
	"strings"
)

var pythonDottedCallRe = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*(?:\s*\.\s*[A-Za-z_][A-Za-z0-9_]*)+)\s*\(`)

func pythonDottedCallImportedNames(block string, importsByName map[string][]string) map[string][]string {
	stripped := stripCodeLiteralsAndComments(block)
	out := map[string][]string{}
	for _, m := range pythonDottedCallRe.FindAllStringSubmatchIndex(stripped, -1) {
		if len(m) < 4 {
			continue
		}
		parts := pythonDottedPathParts(stripped[m[2]:m[3]])
		if len(parts) < 2 {
			continue
		}
		alias := parts[0]
		imported := importsByName[alias]
		if len(imported) == 0 {
			continue
		}
		name := parts[len(parts)-1]
		tail := parts[1 : len(parts)-1]
		for _, module := range pythonDottedCallModules(alias, tail, imported) {
			out[name] = append(out[name], module)
		}
		out[name] = uniqueStrings(out[name])
	}
	return out
}

func cloneStringSliceMap(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func pythonDottedPathParts(path string) []string {
	raw := strings.Split(path, ".")
	parts := make([]string, 0, len(raw))
	for _, part := range raw {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil
		}
		parts = append(parts, part)
	}
	return parts
}

func pythonDottedCallModules(alias string, tail []string, imported []string) []string {
	var modules []string
	add := func(module string) {
		module = strings.TrimSpace(module)
		if module != "" {
			modules = append(modules, module)
		}
	}
	for _, module := range imported {
		module = strings.TrimSpace(module)
		if module == "" {
			continue
		}
		add(module)
		if len(tail) > 0 && !strings.Contains(module, "/") {
			tailSpec := strings.Join(tail, ".")
			add(module + "." + tailSpec)
			if module == alias || strings.HasSuffix(module, "."+tailSpec) {
				add(module)
			}
		}
		if pythonOSPathDottedCall(alias, tail, module) {
			add("genericpath")
			add("posixpath")
			add("ntpath")
		}
	}
	return uniqueStrings(modules)
}

func pythonOSPathDottedCall(alias string, tail []string, module string) bool {
	module = strings.TrimSpace(module)
	switch {
	case module == "os.path":
		return true
	case module == "os" && alias == "os" && len(tail) > 0 && tail[0] == "path":
		return true
	case module == "os" && alias == "path" && len(tail) == 0:
		return true
	default:
		return false
	}
}
