package detect

import "testing"

func engine(t *testing.T, disabled ...string) *Engine {
	t.Helper()
	e, err := NewEngine(nil, disabled)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestScopeExcludes(t *testing.T) {
	e := engine(t)

	// A composer project at the root: vendor excluded anywhere below.
	scope := e.Extend(nil, "", []string{"composer.json", "src"})
	if got := scope.ExcludedBy("vendor"); got != "composer" {
		t.Errorf("vendor: excluded by %q, want composer", got)
	}
	if got := scope.ExcludedBy("src/deep/vendor"); got != "composer" {
		t.Errorf("nested vendor: excluded by %q, want composer", got)
	}
	if got := scope.ExcludedBy("src"); got != "" {
		t.Errorf("src should not be excluded, got %q", got)
	}

	// Monorepo: a node project under services/web only affects that subtree.
	scope2 := e.Extend(nil, "services/web", []string{"package.json"})
	if got := scope2.ExcludedBy("services/web/node_modules"); got != "node" {
		t.Errorf("scoped node_modules: got %q", got)
	}
	if got := scope2.ExcludedBy("services/api/node_modules"); got != "" {
		t.Errorf("node_modules outside the scope should not match, got %q", got)
	}
	// Without any marker, nothing is excluded.
	if got := (*Scope)(nil).ExcludedBy("node_modules"); got != "" {
		t.Errorf("nil scope excluded %q", got)
	}
}

func TestScopeChaining(t *testing.T) {
	e := engine(t)
	root := e.Extend(nil, "", []string{"Cargo.toml"})
	child := e.Extend(root, "web", []string{"package.json"})

	// Parent rules still apply under the child scope.
	if got := child.ExcludedBy("web/target"); got != "rust" {
		t.Errorf("parent rust rule should reach web/target, got %q", got)
	}
	if got := child.ExcludedBy("web/node_modules"); got != "node" {
		t.Errorf("child node rule missing, got %q", got)
	}
}

func TestRelativePathRule(t *testing.T) {
	e := engine(t)
	scope := e.Extend(nil, "", []string{"Gemfile"})
	if got := scope.ExcludedBy("vendor/bundle"); got != "ruby" {
		t.Errorf("vendor/bundle: got %q, want ruby", got)
	}
	// "vendor" alone is not excluded by ruby (only vendor/bundle is).
	if got := scope.ExcludedBy("vendor"); got != "" {
		t.Errorf("vendor alone should not match ruby rule, got %q", got)
	}
}

func TestGlobMarkersAndRules(t *testing.T) {
	e := engine(t)
	scope := e.Extend(nil, "", []string{"MyApp.sln"})
	if got := scope.ExcludedBy("bin"); got != "dotnet" {
		t.Errorf("dotnet bin: got %q", got)
	}
	pyScope := e.Extend(nil, "", []string{"pyproject.toml"})
	if got := pyScope.ExcludedBy("src/aloc.egg-info"); got != "python" {
		t.Errorf("egg-info glob: got %q", got)
	}
}

func TestSelfMarkers(t *testing.T) {
	e := engine(t)
	if got := e.SelfExcludedBy("my-weird-env", []string{"pyvenv.cfg", "bin", "lib"}); got != "venv" {
		t.Errorf("venv self-marker: got %q", got)
	}
	if got := e.SelfExcludedBy("build", []string{"CMakeCache.txt", "CMakeFiles"}); got != "cmake" {
		t.Errorf("cmake self-marker: got %q", got)
	}
	// Go vendoring requires the directory to be named vendor.
	if got := e.SelfExcludedBy("vendor", []string{"modules.txt", "github.com"}); got != "go" {
		t.Errorf("go vendor: got %q", got)
	}
	if got := e.SelfExcludedBy("notvendor", []string{"modules.txt"}); got != "" {
		t.Errorf("modules.txt outside vendor/ should not match, got %q", got)
	}
}

func TestDisableAndCustom(t *testing.T) {
	e := engine(t, "node")
	scope := e.Extend(nil, "", []string{"package.json"})
	if got := scope.ExcludedBy("node_modules"); got != "" {
		t.Errorf("disabled detector still fired: %q", got)
	}

	if _, err := NewEngine(nil, []string{"nope"}); err == nil {
		t.Error("unknown detector name should be an error")
	}

	custom, err := NewEngine([]Detector{{Name: "mytool", Markers: []string{"mytool.lock"}, ExcludeDirs: []string{".mytool-cache"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope = custom.Extend(nil, "", []string{"mytool.lock"})
	if got := scope.ExcludedBy(".mytool-cache"); got != "mytool" {
		t.Errorf("custom detector: got %q", got)
	}
}
