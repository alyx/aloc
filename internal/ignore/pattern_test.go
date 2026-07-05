package ignore

import "testing"

func TestPatternMatches(t *testing.T) {
	tests := []struct {
		pattern string
		rel     string
		want    bool
	}{
		// Broad name match: any path component, anywhere.
		{"foobar", "foobar", true},
		{"foobar", "project/thing/blah/foobar/file.js", true},
		{"foobar", "foobar/file.js", true},
		{"foobar", "notfoobar/file.js", false},
		{"foobar", "a/foobarbaz/b", false},
		{"*.min.js", "dist/app.min.js", true},
		{"*.min.js", "dist/app.js", false},
		{"test*", "a/testdata/b.go", true},

		// Explicit relative path: anchored at scan root, subtree semantics.
		{"./project/thing", "project/thing", true},
		{"./project/thing", "project/thing/deep/file.js", true},
		{"./project/thing", "foo/bar/project/thing", false},
		{"./project/thing", "project/thingamajig", false},
		{"./foobar", "a/b/foobar", false},
		{"./foobar", "foobar", true},

		// Path globs (a "/" anchors too).
		{"src/**/gen", "src/a/b/gen/x.go", true},
		{"src/**/gen", "src/gen/x.go", true},
		{"src/**/gen", "other/src/gen/x.go", false},
		{"src/*/gen", "src/a/gen/x.go", true},
		{"src/*/gen", "src/a/b/gen/x.go", false}, // * does not cross /
		{"docs/*.md", "docs/readme.md", true},
		{"docs/*.md", "docs/sub/readme.md", false},

		// Trailing slash is tolerated.
		{"vendor/", "vendor/x.go", true},
	}
	for _, tt := range tests {
		p, err := ParsePattern(tt.pattern)
		if err != nil {
			t.Fatalf("ParsePattern(%q): %v", tt.pattern, err)
		}
		if got := p.Matches(tt.rel); got != tt.want {
			t.Errorf("pattern %q vs %q = %v, want %v", tt.pattern, tt.rel, got, tt.want)
		}
	}
}

func TestParsePatternErrors(t *testing.T) {
	for _, bad := range []string{"", "./..", "../up", "./../up", "a/[unclosed"} {
		if _, err := ParsePattern(bad); err == nil {
			t.Errorf("ParsePattern(%q) should fail", bad)
		}
	}
}

func TestSet(t *testing.T) {
	s, err := ParseSet([]string{"vendor", "./gen"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Empty() {
		t.Error("set should not be empty")
	}
	if !s.Matches("a/vendor/b.go") || !s.Matches("gen/x.go") {
		t.Error("expected matches")
	}
	if s.Matches("src/main.go") {
		t.Error("unexpected match")
	}
	var nilSet *Set
	if !nilSet.Empty() || nilSet.Matches("x") {
		t.Error("nil set should be empty and match nothing")
	}
}
