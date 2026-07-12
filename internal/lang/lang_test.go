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

func TestExpandedBuiltinsByPath(t *testing.T) {
	r := NewRegistry()
	tests := map[string]string{
		"grammar.abnf": "ABNF", "main.adb": "Ada", "guide.adoc": "AsciiDoc",
		"main.bicep": "Bicep", "recipe.bb": "BitBake", "login.csh": "C Shell",
		"pkg.cabal": "Cabal", "main.cob": "COBOL", "app.coffee": "CoffeeScript",
		"system.lisp": "Common Lisp", "main.cr": "Crystal", "kernel.cu": "CUDA",
		"schema.cue": "CUE", "module.pyx": "Cython", "graph.d2": "D2",
		"config.dhall": "Dhall", "pkg.ebuild": "Ebuild", "Main.elm": "Elm",
		"init.el": "Emacs Lisp", "macro.fnl": "Fennel", "prompt.fish": "Fish",
		"schema.fbs": "FlatBuffers", "words.fth": "Forth", "player.gd": "GDScript",
		"effect.gdshader": "GDShader", "main.gleam": "Gleam", "shader.frag": "GLSL",
		"Main.hx": "Haxe", "effect.hlsl": "HLSL", "config.jsonnet": "Jsonnet",
		"Justfile": "Just", "module.ll": "LLVM", "macros.m4": "M4",
		"meson.build": "Meson", "kernel.metal": "Metal", "plant.mo": "Modelica",
		"workflow.nf": "Nextflow", "flake.nix": "Nix", "script.nu": "Nushell",
		"main.odin": "Odin", "policy.rego": "Open Policy Agent", "kernel.cl": "OpenCL",
		"model.scad": "OpenSCAD", "notes.org": "Org", "diagram.puml": "PlantUML",
		"sketch.pde": "Processing", "facts.pro": "Prolog", "Main.purs": "PureScript",
		"view.qml": "QML", "main.rkt": "Racket", "main.raku": "Raku",
		"component.res": "ReScript", "main.roc": "Roc", "style.sass": "Sass",
		"main.scm": "Scheme", "surface.shader": "ShaderLab", "Snakefile": "Snakemake",
		"Token.sol": "Solidity", "model.stan": "Stan", "module.sml": "Standard ML",
		"analysis.do": "Stata", "design.sv": "SystemVerilog", "tool.tcl": "TCL",
		"service.thrift": "Thrift", "paper.typ": "Typst", "object.vala": "Vala",
		"form.frm": "VB6/VBA", "script.vbs": "VBScript", "module.v": "Verilog",
		"entity.vhdl": "VHDL", "Module.vb": "Visual Basic", "module.wat": "WebAssembly",
		"project.sln":     "Visual Studio Solution",
		"compute.wgsl":    "WGSL",
		"view.xaml":       "XAML",
		"main.BICEPPARAM": "Bicep",
	}
	for path, want := range tests {
		if got := r.ByPath(path); got == nil || got.Name != want {
			t.Errorf("ByPath(%q) = %v, want %s", path, got, want)
		}
	}
}

func TestBuiltinMatchersDoNotCollide(t *testing.T) {
	type owner struct{ kind, language string }
	seen := map[string]owner{}
	for _, set := range [][]Language{builtin, builtinExpanded} {
		for _, l := range set {
			for _, value := range l.Extensions {
				key := "ext:" + value
				if old, ok := seen[key]; ok {
					t.Errorf("extension %q belongs to both %s and %s", value, old.language, l.Name)
				}
				seen[key] = owner{"extension", l.Name}
			}
			for _, value := range l.Filenames {
				key := "name:" + value
				if old, ok := seen[key]; ok {
					t.Errorf("filename %q belongs to both %s and %s", value, old.language, l.Name)
				}
				seen[key] = owner{"filename", l.Name}
			}
			for _, value := range l.Shebangs {
				key := "shebang:" + value
				if old, ok := seen[key]; ok {
					t.Errorf("shebang %q belongs to both %s and %s", value, old.language, l.Name)
				}
				seen[key] = owner{"shebang", l.Name}
			}
		}
	}
}

func TestAllBuiltinMatchersResolve(t *testing.T) {
	r := NewRegistry()
	for _, set := range [][]Language{builtin, builtinExpanded} {
		for _, l := range set {
			for _, ext := range l.Extensions {
				if got := r.ByPath("file." + ext); got == nil || got.Name != l.Name {
					t.Errorf("extension %q resolves to %v, want %s", ext, got, l.Name)
				}
			}
			for _, name := range l.Filenames {
				if got := r.ByPath(name); got == nil || got.Name != l.Name {
					t.Errorf("filename %q resolves to %v, want %s", name, got, l.Name)
				}
			}
			for _, interpreter := range l.Shebangs {
				if got := r.ByShebang([]byte("#!/usr/bin/env " + interpreter + "\n")); got == nil || got.Name != l.Name {
					t.Errorf("shebang %q resolves to %v, want %s", interpreter, got, l.Name)
				}
			}
		}
	}
}

func TestAmbiguousExtensionsKeepExistingOwners(t *testing.T) {
	r := NewRegistry()
	for path, want := range map[string]string{
		"document.cls": "LaTeX",
		"header.inc":   "C",
		"unit.pp":      "Pascal",
		"device.rules": "udev Rules",
		"style.scss":   "SCSS",
	} {
		if got := r.ByPath(path); got == nil || got.Name != want {
			t.Errorf("ByPath(%q) = %v, want %s", path, got, want)
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
		{"#!/usr/bin/env fish\necho hi\n", "Fish"},
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
	// Long extensions use the fallback table and retain case-insensitive
	// override/removal semantics too.
	r.Add(&Language{Name: "MyLongLang", Extensions: []string{"bicepparam"}})
	if l := r.ByPath("main.BICEPPARAM"); l == nil || l.Name != "MyLongLang" {
		t.Errorf("long extension override failed: %v", l)
	}
	r.Add(&Language{Name: "MyLongLang", Extensions: []string{"longreplacement"}})
	if l := r.ByPath("main.bicepparam"); l != nil {
		t.Errorf("stale long extension mapping survived redefinition: %v", l.Name)
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
