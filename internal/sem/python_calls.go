package sem

import (
	"regexp"
	"strings"
)

var pythonDottedCallRe = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*(?:\s*\.\s*[A-Za-z_][A-Za-z0-9_]*)+)\s*\(`)

func pythonDottedCallImportedNames(block string, importsByName map[string][]string) map[string][]string {
	stripped := stripPythonLiteralsAndComments(block)
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
		if len(tail) == 0 && !pythonSingleSelectorAliasCall(alias, imported) {
			continue
		}
		for _, module := range pythonDottedCallModules(alias, tail, imported) {
			out[name] = append(out[name], module)
		}
	}
	for name := range out {
		out[name] = uniqueStrings(out[name])
	}
	return out
}

func stripPythonLiteralsAndComments(content string) string {
	bytes := []byte(content)
	for i := 0; i < len(bytes); i++ {
		switch bytes[i] {
		case '"', '\'':
			quote := bytes[i]
			if i+2 < len(bytes) && bytes[i+1] == quote && bytes[i+2] == quote {
				j := i + 3
				for j+2 < len(bytes) && !(bytes[j] == quote && bytes[j+1] == quote && bytes[j+2] == quote) {
					j++
				}
				if j+2 < len(bytes) {
					j += 3
				}
				maskBytes(bytes, i, j)
				i = j - 1
				continue
			}
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

func pythonSingleSelectorAliasCall(alias string, imported []string) bool {
	for _, module := range imported {
		module = strings.TrimSpace(module)
		if module == "" {
			continue
		}
		if module != alias || pythonOSPathDottedCall(alias, nil, module) {
			return true
		}
	}
	return false
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
		if len(tail) > 0 && !strings.Contains(module, "/") {
			tailSpec := strings.Join(tail, ".")
			selectorModule := alias + "." + tailSpec
			switch {
			case module == alias:
				add(selectorModule)
			case module == selectorModule || strings.HasPrefix(module, selectorModule+"."):
				add(selectorModule)
			case strings.Split(module, ".")[0] != alias:
				if strings.HasSuffix(module, "."+tailSpec) {
					add(module)
				} else {
					add(module + "." + tailSpec)
				}
			}
		} else if module == "os" && alias == "path" && len(tail) == 0 {
			add("os.path")
		} else {
			add(module)
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
