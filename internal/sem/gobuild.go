package sem

import (
	"go/build"
	"go/build/constraint"
	"path"
	"strings"
)

// Go build-constraint evaluation. entire-graph parses with tree-sitter, which is
// build-tag-blind: it would parse every .go file regardless of //go:build lines
// or _GOOS/_GOARCH filename suffixes. The compiler (and GraphMark's go/types
// oracle) compiles only the files in the default build for the host. Parsing the
// excluded files poisons type inference — e.g. a package var declared with
// conflicting types under mutually exclusive build tags (zerolog's `enc` is
// json.Encoder under !binary_log and cbor.Encoder under binary_log) becomes
// ambiguous and every call on it is dropped. Excluding non-matching files aligns
// the heuristic with the compiler's view.

var knownGOOS = map[string]bool{
	"aix": true, "android": true, "darwin": true, "dragonfly": true,
	"freebsd": true, "illumos": true, "ios": true, "js": true, "linux": true,
	"netbsd": true, "openbsd": true, "plan9": true, "solaris": true,
	"wasip1": true, "windows": true,
}

var knownGOARCH = map[string]bool{
	"386": true, "amd64": true, "arm": true, "arm64": true, "loong64": true,
	"mips": true, "mips64": true, "mips64le": true, "mipsle": true,
	"ppc64": true, "ppc64le": true, "riscv64": true, "s390x": true, "wasm": true,
}

// unixGOOS mirrors the set Go treats as satisfying the "unix" build tag.
var unixGOOS = map[string]bool{
	"aix": true, "android": true, "darwin": true, "dragonfly": true,
	"freebsd": true, "hurd": true, "illumos": true, "ios": true, "linux": true,
	"netbsd": true, "openbsd": true, "solaris": true,
}

// goFileMatchesDefaultBuild reports whether a .go file is part of the default
// build for the host (build.Default GOOS/GOARCH, no custom tags) — what the
// compiler actually compiles. Non-matching files are excluded from the snapshot.
func goFileMatchesDefaultBuild(filePath, content string) bool {
	return goFilenameMatchesDefaultBuild(filePath) && goBuildCommentSatisfied(content)
}

// goFilenameMatchesDefaultBuild applies the implicit _GOOS / _GOARCH /
// _GOOS_GOARCH filename constraints (go/build's name-based rule).
func goFilenameMatchesDefaultBuild(filePath string) bool {
	name := path.Base(filePath)
	name = strings.TrimSuffix(name, ".go")
	name = strings.TrimSuffix(name, "_test") // _test is not a platform suffix
	parts := strings.Split(name, "_")
	if len(parts) < 2 {
		return true
	}
	last := parts[len(parts)-1]
	// _GOOS_GOARCH: the final two segments are a known OS then a known arch.
	if len(parts) >= 3 {
		if os2 := parts[len(parts)-2]; knownGOOS[os2] && knownGOARCH[last] {
			return os2 == build.Default.GOOS && last == build.Default.GOARCH
		}
	}
	if knownGOOS[last] {
		return last == build.Default.GOOS
	}
	if knownGOARCH[last] {
		return last == build.Default.GOARCH
	}
	return true
}

// goBuildCommentSatisfied evaluates //go:build (preferred) or legacy // +build
// constraints in the file header against the default build configuration.
func goBuildCommentSatisfied(content string) bool {
	var goBuildExpr constraint.Expr
	var plusBuild []constraint.Expr
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue // blank lines are allowed in the header
		}
		if !strings.HasPrefix(t, "//") {
			break // first line of code (e.g. the package clause) ends the header
		}
		if constraint.IsGoBuild(t) {
			if e, err := constraint.Parse(t); err == nil {
				goBuildExpr = e
			}
		} else if constraint.IsPlusBuild(t) {
			if e, err := constraint.Parse(t); err == nil {
				plusBuild = append(plusBuild, e)
			}
		}
	}
	// //go:build takes precedence; legacy // +build lines are ignored when present.
	if goBuildExpr != nil {
		return goBuildExpr.Eval(defaultBuildTagSatisfied)
	}
	for _, e := range plusBuild {
		if !e.Eval(defaultBuildTagSatisfied) {
			return false
		}
	}
	return true
}

// defaultBuildTagSatisfied reports whether a build tag holds in the default build
// for the host. Custom tags (binary_log, integration, …) are unsatisfied, which
// is exactly what excludes the conflicting alternate-tag files.
func defaultBuildTagSatisfied(tag string) bool {
	switch tag {
	case build.Default.GOOS, build.Default.GOARCH:
		return true
	case "unix":
		return unixGOOS[build.Default.GOOS]
	case "cgo":
		return build.Default.CgoEnabled
	case "gc":
		return true
	case "gccgo", "boringcrypto":
		return false
	}
	for _, r := range build.Default.ReleaseTags { // go1.1 .. go1.N
		if tag == r {
			return true
		}
	}
	return false
}
