package lang

import "testing"

func TestByPath(t *testing.T) {
	r := NewRegistry()
	tests := []struct {
		path string
		want string // "" = unknown
	}{
		{"main.go", "Go"},
		{"src/app.PY", "Python"}, // extension case-insensitive
		{"a/b/c.tar.gz", ""},
		{"Makefile", "Makefile"},
		{"sub/dir/Makefile", "Makefile"},
		{"Dockerfile", "Dockerfile"},
		{"deploy.dockerfile", "Dockerfile"},
		{"CMakeLists.txt", "CMake"},
		{"notes.txt", "Plain Text"},
		{"Gemfile", "Ruby"},
		{"script", ""},
		{"style.CSS", "CSS"},
		{"app.test.tsx", "TypeScript"},
		{"Documentation/core-api/index.rst", "reStructuredText"},
		{"arch/arm64/boot/dts/vendor/board.dts", "Device Tree"},
		{"arch/arm64/boot/dts/vendor/soc.dtsi", "Device Tree"},
		{"Kbuild.in", "Autoconf"},
		{"scripts/Makefile.am", "Automake"},
		{"scripts/check.awk", "AWK"},
		{"tools/testing/BUILD.bazel", "Bazel"},
		{"tools/perf/Build", "Makefile"},
		{"net/example.asn1", "ASN.1"},
		{"arch/x/kernel/vmlinux.lds", "Linker Script"},
		{"scripts/parser.l", "Lex"},
		{"scripts/parser.y", "Yacc"},
		{"po/messages.po", "Gettext Catalog"},
		{"drivers/example.rules", "udev Rules"},
		{"features/example.feature", "Gherkin"},
		{"firmware/image.hex", "Intel HEX"},
		{"scripts/package/kernel.spec", "RPM Spec"},
	}
	for _, tt := range tests {
		var got string
		if l := r.ByPath(tt.path); l != nil {
			got = l.Name
		}
		if got != tt.want {
			t.Errorf("ByPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestByShebang(t *testing.T) {
	r := NewRegistry()
	tests := []struct {
		content string
		want    string
	}{
		{"#!/usr/bin/python\nprint(1)\n", "Python"},
		{"#!/usr/bin/env python3\nprint(1)\n", "Python"},
		{"#!/usr/bin/env python3.11\nprint(1)\n", "Python"},
		{"#!/usr/bin/env -S node --experimental\nx\n", "JavaScript"},
		{"#!/bin/bash\necho hi\n", "Shell"},
		{"#!/usr/bin/env ruby\nputs 1\n", "Ruby"},
		{"# not a shebang\n", ""},
		{"", ""},
		{"#!\n", ""},
	}
	for _, tt := range tests {
		var got string
		if l := r.ByShebang([]byte(tt.content)); l != nil {
			got = l.Name
		}
		if got != tt.want {
			t.Errorf("ByShebang(%q) = %q, want %q", tt.content, got, tt.want)
		}
	}
}

func TestDetectPrefersPath(t *testing.T) {
	r := NewRegistry()
	// A .py file with a node shebang is still Python: path wins.
	if l := r.Detect("weird.py", []byte("#!/usr/bin/env node\n")); l == nil || l.Name != "Python" {
		t.Errorf("Detect = %v, want Python", l)
	}
	// No extension: shebang decides.
	if l := r.Detect("bin/tool", []byte("#!/usr/bin/env bash\n")); l == nil || l.Name != "Shell" {
		t.Errorf("Detect = %v, want Shell", l)
	}
}

func TestAddOverride(t *testing.T) {
	r := NewRegistry()
	// A user definition can steal an extension from a builtin.
	r.Add(&Language{Name: "MyLang", Extensions: []string{"go"}, LineComments: []string{";"}})
	if l := r.ByPath("x.go"); l == nil || l.Name != "MyLang" {
		t.Errorf("override failed: ByPath(x.go) = %v", l)
	}
	// Redefining the same name replaces cleanly.
	r.Add(&Language{Name: "MyLang", Extensions: []string{"ml2"}})
	if l := r.ByPath("x.go"); l != nil {
		t.Errorf("stale extension mapping survived redefinition: %v", l.Name)
	}
}

func TestValidate(t *testing.T) {
	if err := (&Language{Name: "X"}).Validate(); err == nil {
		t.Error("language without matchers should not validate")
	}
	if err := (&Language{Name: "", Extensions: []string{"x"}}).Validate(); err == nil {
		t.Error("unnamed language should not validate")
	}
	if err := (&Language{Name: "X", Extensions: []string{"x"}, BlockComments: [][2]string{{"", "*/"}}}).Validate(); err == nil {
		t.Error("empty block delimiter should not validate")
	}
}
