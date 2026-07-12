package walker

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
)

// trackedSet is the git index of one scan root: the tracked files (relative
// slash paths) and every directory that contains at least one of them, so
// untracked subtrees can be pruned without walking them.
type trackedSet struct {
	files map[string]bool
	dirs  map[string]bool
	oids  map[string]string
	blobs *gitBatch
}

// gitObjects returns the tracked set plus index blob IDs for files that are
// clean in the worktree. Modified and conflicted paths intentionally have no
// ID, which routes them through the normal filesystem reader.
func gitObjects(dir string) (*trackedSet, error) {
	out, err := gitOutput(dir, "ls-files", "-s", "-z")
	if err != nil {
		return nil, err
	}
	dirtyOut, err := gitOutput(dir, "ls-files", "-m", "-d", "-z")
	if err != nil {
		return nil, err
	}
	dirty := map[string]bool{}
	for _, f := range strings.Split(string(dirtyOut), "\x00") {
		if f != "" {
			dirty[f] = true
		}
	}
	ts := &trackedSet{
		files: map[string]bool{}, dirs: map[string]bool{}, oids: map[string]string{},
		blobs: &gitBatch{dir: dir},
	}
	for _, entry := range strings.Split(string(out), "\x00") {
		if entry == "" {
			continue
		}
		meta, f, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 {
			return nil, fmt.Errorf("unexpected git ls-files entry %q", entry)
		}
		ts.add(f)
		// Only regular files can be substituted with their index blobs.
		// A symlink blob contains the link target, while --follow-symlinks
		// expects the target file's contents from the filesystem.
		if fields[2] == "0" && !dirty[f] && strings.HasPrefix(fields[0], "100") {
			ts.oids[f] = fields[1]
		}
	}
	return ts, nil
}

func gitOutput(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("git %s in %s: %s", args[0], dir, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("git %s in %s: %w", args[0], dir, err)
	}
	return out, nil
}

func (ts *trackedSet) add(f string) {
	ts.files[f] = true
	for d := path.Dir(f); d != "." && !ts.dirs[d]; d = path.Dir(d) {
		ts.dirs[d] = true
	}
}

// gitTracked lists the files tracked by git under dir. It fails when dir is
// not inside a git work tree or git is not installed.
func gitTracked(dir string) (*trackedSet, error) {
	out, err := gitOutput(dir, "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	ts := &trackedSet{files: map[string]bool{}, dirs: map[string]bool{}}
	for _, f := range strings.Split(string(out), "\x00") {
		if f == "" {
			continue
		}
		ts.add(f)
	}
	return ts, nil
}

// gitBatch lazily owns one git cat-file --batch process. Read is serialized
// because the protocol is request/response ordered; counting remains parallel.
type gitBatch struct {
	dir       string
	startOnce sync.Once
	startErr  error
	writeMu   sync.Mutex
	cmd       *exec.Cmd
	in        io.WriteCloser
	out       *bufio.Reader
	order     chan *gitRequest
	readerWG  sync.WaitGroup
	stderr    bytes.Buffer
}

type gitRequest struct {
	oid  string
	buf  []byte
	done chan gitResult
}

type gitResult struct {
	content []byte
	err     error
}

func (b *gitBatch) start() {
	b.cmd = exec.Command("git", "-C", b.dir, "cat-file", "--batch")
	b.cmd.Stderr = &b.stderr
	b.in, b.startErr = b.cmd.StdinPipe()
	if b.startErr != nil {
		return
	}
	var stdout io.ReadCloser
	stdout, b.startErr = b.cmd.StdoutPipe()
	if b.startErr != nil {
		return
	}
	b.out = bufio.NewReader(stdout)
	b.startErr = b.cmd.Start()
	if b.startErr != nil {
		return
	}
	// Multiple workers can queue requests while this goroutine consumes
	// responses in protocol order. That keeps the pipe full instead of doing
	// one request/response round trip per blob.
	b.order = make(chan *gitRequest, 256)
	b.readerWG.Add(1)
	go b.readLoop()
}

func (b *gitBatch) Read(oid string, buf []byte) ([]byte, error) {
	b.startOnce.Do(b.start)
	if b.startErr != nil {
		return nil, b.startErr
	}
	req := &gitRequest{oid: oid, buf: buf, done: make(chan gitResult, 1)}
	// The write and order enqueue are one critical section: the reader must
	// consume responses in exactly the order git received their object IDs.
	b.writeMu.Lock()
	_, err := io.WriteString(b.in, oid+"\n")
	if err == nil {
		b.order <- req
	}
	b.writeMu.Unlock()
	if err != nil {
		return nil, err
	}
	res := <-req.done
	return res.content, res.err
}

func (b *gitBatch) readLoop() {
	defer b.readerWG.Done()
	for req := range b.order {
		content, err := b.readOne(req.buf)
		req.done <- gitResult{content: content, err: err}
	}
}

func (b *gitBatch) readOne(buf []byte) ([]byte, error) {
	header, err := b.out.ReadString('\n')
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(header)
	if len(fields) != 3 || fields[1] != "blob" {
		return nil, fmt.Errorf("unexpected cat-file response %q", strings.TrimSpace(header))
	}
	size, err := strconv.Atoi(fields[2])
	if err != nil {
		return nil, err
	}
	if cap(buf) < size {
		buf = make([]byte, size)
	} else {
		buf = buf[:size]
	}
	if _, err = io.ReadFull(b.out, buf); err != nil {
		return nil, err
	}
	term, err := b.out.ReadByte()
	if err != nil || term != '\n' {
		if err == nil {
			err = fmt.Errorf("missing cat-file record terminator")
		}
		return nil, err
	}
	return buf, nil
}

func (b *gitBatch) Close() {
	if b.cmd == nil {
		return
	}
	b.writeMu.Lock()
	if b.in != nil {
		_ = b.in.Close()
	}
	if b.order != nil {
		close(b.order)
	}
	b.writeMu.Unlock()
	b.readerWG.Wait()
	_ = b.cmd.Wait()
}
