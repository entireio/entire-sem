package sem

import (
	"go/build"
	"testing"
)

func TestGoFileMatchesDefaultBuild(t *testing.T) {
	host := build.Default.GOOS
	hostArch := build.Default.GOARCH
	// Pick a definitely-different OS/arch for the negative cases.
	otherOS := "linux"
	if host == "linux" {
		otherOS = "windows"
	}
	otherArch := "amd64"
	if hostArch == "amd64" {
		otherArch = "arm64"
	}

	cases := []struct {
		name    string
		path    string
		content string
		want    bool
	}{
		{"plain", "foo.go", "package foo", true},
		{"host os suffix", "foo_" + host + ".go", "package foo", true},
		{"other os suffix", "foo_" + otherOS + ".go", "package foo", false},
		{"host arch suffix", "foo_" + hostArch + ".go", "package foo", true},
		{"other arch suffix", "foo_" + otherArch + ".go", "package foo", false},
		{"host os+arch", "foo_" + host + "_" + hostArch + ".go", "package foo", true},
		{"other os+arch", "foo_" + otherOS + "_" + otherArch + ".go", "package foo", false},
		{"test suffix is not a platform", "foo_test.go", "package foo", true},
		{"non-platform suffix kept", "foo_helper.go", "package foo", true},
		{"gobuild custom tag excluded", "foo.go", "//go:build binary_log\n\npackage foo", false},
		{"gobuild negated custom tag included", "foo.go", "//go:build !binary_log\n\npackage foo", true},
		{"gobuild host os included", "foo.go", "//go:build " + host + "\n\npackage foo", true},
		{"gobuild other os excluded", "foo.go", "//go:build " + otherOS + "\n\npackage foo", false},
		{"plusbuild custom tag excluded", "foo.go", "// +build binary_log\n\npackage foo", false},
		{"gobuild wins over filename", "foo_" + otherOS + ".go", "//go:build " + host + "\n\npackage foo", false}, // filename still excludes
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := goFileMatchesDefaultBuild(c.path, c.content); got != c.want {
				t.Fatalf("goFileMatchesDefaultBuild(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}

	// unix tag is satisfied on unix hosts, not on Windows/Plan9.
	wantUnix := unixGOOS[host]
	if got := goBuildCommentSatisfied("//go:build unix\n\npackage foo"); got != wantUnix {
		t.Fatalf("unix tag on %s = %v, want %v", host, got, wantUnix)
	}
}
