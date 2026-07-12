package walker

import (
	"errors"
	"fmt"
	"os/exec"
	"path"
	"strings"
)

// trackedSet is the git index of one scan root: the tracked files (relative
// slash paths) and every directory that contains at least one of them, so
// untracked subtrees can be pruned without walking them.
type trackedSet struct {
	files map[string]bool
	dirs  map[string]bool
}

// gitTracked lists the files tracked by git under dir. It fails when dir is
// not inside a git work tree or git is not installed.
func gitTracked(dir string) (*trackedSet, error) {
	out, err := exec.Command("git", "-C", dir, "ls-files", "-z").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("git ls-files in %s: %s", dir, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("git ls-files in %s: %w", dir, err)
	}
	ts := &trackedSet{files: map[string]bool{}, dirs: map[string]bool{}}
	for _, f := range strings.Split(string(out), "\x00") {
		if f == "" {
			continue
		}
		ts.files[f] = true
		// Ancestors of an already-recorded directory are recorded too.
		for d := path.Dir(f); d != "." && !ts.dirs[d]; d = path.Dir(d) {
			ts.dirs[d] = true
		}
	}
	return ts, nil
}
