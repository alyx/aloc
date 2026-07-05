package ignore

import "testing"

func TestGitIgnoreBasics(t *testing.T) {
	g := ParseGitIgnore([]byte(`
# comment
*.log
!keep.log
build/
/rooted.txt
docs/*.tmp
**/deep
`))
	tests := []struct {
		rel   string
		isDir bool
		want  bool
	}{
		{"app.log", false, true},
		{"sub/dir/app.log", false, true},
		{"keep.log", false, false},  // negated
		{"build", true, true},       // dir-only matches dirs
		{"build", false, false},     // ...but not files
		{"sub/build", true, true},   // unrooted dir pattern at any depth
		{"rooted.txt", false, true}, // anchored to this dir
		{"sub/rooted.txt", false, false},
		{"docs/x.tmp", false, true},
		{"docs/sub/x.tmp", false, false}, // * does not cross /
		{"a/b/deep", true, true},         // ** crosses directories
		{"deep", true, true},
		{"other.txt", false, false},
	}
	for _, tt := range tests {
		decided, ignored, _ := g.match(tt.rel, tt.isDir)
		got := decided && ignored
		if got != tt.want {
			t.Errorf("match(%q, dir=%v) = %v, want %v", tt.rel, tt.isDir, got, tt.want)
		}
	}
}

func TestGitIgnoreLastMatchWins(t *testing.T) {
	g := ParseGitIgnore([]byte("*.log\n!important.log\n"))
	if _, ignored, _ := g.match("important.log", false); ignored {
		t.Error("negation should win as the later rule")
	}
	g2 := ParseGitIgnore([]byte("!important.log\n*.log\n"))
	if _, ignored, _ := g2.match("important.log", false); !ignored {
		t.Error("later ignore rule should override earlier negation")
	}
}

func TestGitStackScoping(t *testing.T) {
	root := ParseGitIgnore([]byte("*.log\n"))
	sub := ParseGitIgnore([]byte("!special.log\n*.tmp\n"))

	var s *GitStack = &GitStack{}
	s = s.Push("", root)
	nested := s.Push("sub", sub)

	// Root rules apply everywhere.
	if !nested.Ignored("sub/app.log", false) {
		t.Error("root *.log should apply in sub/")
	}
	// Nested negation overrides the root rule inside its scope.
	if nested.Ignored("sub/special.log", false) {
		t.Error("nested !special.log should re-include")
	}
	// Nested rules do not leak to the parent scope.
	if s.Ignored("elsewhere/x.tmp", false) {
		t.Error("sub/.gitignore must not affect paths outside sub/")
	}
	if nested.Ignored("x.tmp", false) {
		t.Error("sub/.gitignore must not affect the root")
	}
	// A nil stack ignores nothing.
	var nilStack *GitStack
	if nilStack.Ignored("anything", false) {
		t.Error("nil stack should ignore nothing")
	}
}

func TestGitStackIgnoredByAttribution(t *testing.T) {
	s := (&GitStack{}).
		Push("", ParseGitIgnore([]byte("*.log\n"))).
		Push("sub", ParseGitIgnore([]byte("/build\n")))

	if ig, src := s.IgnoredBy("a/b/x.log", false); !ig || src != `.gitignore: "*.log"` {
		t.Errorf("got (%v, %q), want root attribution", ig, src)
	}
	if ig, src := s.IgnoredBy("sub/build", true); !ig || src != `sub/.gitignore: "/build"` {
		t.Errorf("got (%v, %q), want nested attribution", ig, src)
	}
	if ig, src := s.IgnoredBy("kept.txt", false); ig || src != "" {
		t.Errorf("got (%v, %q), want no match", ig, src)
	}
}
