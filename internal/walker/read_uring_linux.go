//go:build linux

package walker

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/alyx/aloc/internal/lang"
	"github.com/alyx/aloc/internal/uring"
)

// readContinuation preserves a prefix already read by io_uring and fills the
// remainder through a normal descriptor. Fstat replaces geometric growth and
// pread avoids copying the prefix from the page cache a second time.
func readContinuation(path string, prefix, buf []byte) ([]byte, []byte, error) {
	f, err := openRaw(path)
	if err != nil {
		return nil, buf, err
	}
	defer f.close()

	var st syscall.Stat_t
	if err := syscall.Fstat(f.fd, &st); err != nil || st.Size < int64(len(prefix)) || uint64(st.Size) > uint64(^uint(0)>>1) {
		// A concurrent shrink or an unusable size falls back to the fully
		// defensive read loop on the still-unadvanced descriptor.
		total, grown, readErr := readFull(f, buf, 0)
		return grown[:total], grown, readErr
	}
	size := int(st.Size)
	if cap(buf) < size {
		buf = make([]byte, size)
	} else {
		buf = buf[:size]
	}
	copy(buf, prefix)
	for total := len(prefix); total < size; {
		n, readErr := syscall.Pread(f.fd, buf[total:size], int64(total))
		if readErr == syscall.EINTR {
			continue
		}
		if readErr != nil {
			return nil, buf, &fs.PathError{Op: "read", Path: path, Err: readErr}
		}
		if n == 0 {
			return nil, buf, io.ErrUnexpectedEOF
		}
		total += n
	}
	return buf[:size], buf, nil
}

const (
	// uringBatchSize files are gathered per submission round. Each round is
	// three io_uring_enter calls (opens; reads; EOF confirms and closes) in
	// place of the standard path's ~4 syscalls per file.
	uringBatchSize = uringBatchHint
	// uringBufSize is the per-slot read buffer. A file that fills it exactly
	// continues from this prefix through positional reads on a normal
	// descriptor; nearly all source files are far smaller.
	uringBufSize = 512 << 10
	// The busiest round stages two SQEs (EOF confirm, close) per file.
	uringEntries = 2 * uringBatchSize
)

// uringAvailable memoizes the kernel feature probe (one direct-descriptor
// open/read/close cycle against /dev/null) for the life of the process.
var uringAvailable = sync.OnceValues(func() (bool, error) {
	err := uring.DirectSupported()
	return err == nil, err
})

// selectUring decides the read path. Unset ALOC_IO means auto: io_uring
// when the probe passes, silently standard otherwise — io_uring is commonly
// blocked (container seccomp profiles, io_uring_disabled) and a default
// must not warn about an environment being its normal self. An explicit
// ALOC_IO=uring does warn when it cannot be honored.
func (w *walker) selectUring() bool {
	switch os.Getenv("ALOC_IO") {
	case "std":
		return false
	case "uring":
		ok, err := uringAvailable()
		if !ok {
			w.warnf("io_uring requested but unavailable (%v); using standard reads", err)
		}
		return ok
	default:
		ok, _ := uringAvailable()
		return ok
	}
}

// user_data tags: slot<<8 | tag.
const (
	udOpen = iota
	udRead
	udClose
	udConfirm
)

// uringReap dispatches completion results to fn and handles closes: a failed
// close would leave the slot occupied and poison the next batch's open, so
// it is cleared synchronously. Cannot happen in practice (the preceding read
// holds the slot populated).
func (w *walker) uringReap(r *uring.Ring, cqes []uring.CQE, fn func(slot int, tag uint64, res int32)) {
	for _, c := range cqes {
		slot, tag := int(c.UserData>>8), c.UserData&0xff
		if tag == udClose {
			if c.Res < 0 && syscall.Errno(-c.Res) != syscall.ECANCELED {
				r.ClearSlot(slot)
			}
			continue
		}
		fn(slot, tag, c.Res)
	}
}

// uringWorker drains w.jobs in batches through an io_uring, mirroring
// countFile's per-file decisions exactly. Returns false if the ring cannot
// be set up, in which case the caller falls back to the standard read loop
// without having consumed any jobs.
func (w *walker) uringWorker() bool {
	r, err := uring.New(uringEntries, uringBatchSize)
	if err != nil {
		// The probe passed, so per-worker setup failing is unexpected
		// (resource limits?); say so once, not once per worker.
		w.uringWarn.Do(func() {
			w.warnf("io_uring setup failed (%v); using standard reads", err)
		})
		return false
	}
	defer r.Close()

	type slotState struct {
		j       job
		l       *lang.Language // nil = extensionless, sniff after read
		pathBuf []byte         // NUL-terminated abs path, kept alive across Enter
		readN   int
		opened  bool
		reread  bool // content untrusted (buffer full, or confirm saw more data)
	}
	slots := make([]slotState, uringBatchSize)
	bufs := make([][]byte, uringBatchSize)
	// Allocate the full pool before processing: doing this lazily in the first
	// hot batch regresses sustained many-file throughput by roughly 10%.
	for i := range bufs {
		bufs[i] = make([]byte, uringBufSize)
	}
	// Scratch for the rare file that overflows its slot buffer.
	fallback := make([]byte, initialBufSize)
	cq := make([]uring.CQE, 3*uringBatchSize)
	batch := make([]job, 0, uringBatchSize)

	for {
		j, ok := <-w.jobs
		if !ok {
			return true
		}
		batch = append(batch[:0], j)
	drain:
		// Top up the batch without blocking: a stalled walker must not
		// stall counting, and a partial batch still amortizes well.
		for len(batch) < uringBatchSize {
			select {
			case j2, ok2 := <-w.jobs:
				if !ok2 {
					break drain
				}
				batch = append(batch, j2)
			default:
				break drain
			}
		}

		// Pre-open decisions, exactly as countFile makes them: unknown
		// extensions are skipped unopened; a by-path language not selected
		// by --lang is skipped before I/O.
		n := 0
		for _, bj := range batch {
			l := w.opts.Registry.ByPath(bj.abs)
			if l == nil && !sniffable(filepath.Base(bj.abs)) {
				if w.trace {
					w.tracef("skip %s (unknown language)", bj.display())
				}
				continue
			}
			if l != nil && len(w.opts.Languages) > 0 && !w.opts.Languages[strings.ToLower(l.Name)] {
				if w.trace {
					w.tracef("skip %s (language %s not selected by --lang)", bj.display(), l.Name)
				}
				continue
			}
			s := &slots[n]
			s.j, s.l, s.opened = bj, l, false
			s.readN, s.reread = 0, false
			s.pathBuf = append(s.pathBuf[:0], bj.abs...)
			s.pathBuf = append(s.pathBuf, 0)
			n++
		}
		if n == 0 {
			continue
		}
		// Phase 1: open every file into its direct-descriptor slot.
		for i := 0; i < n; i++ {
			r.PushOpenDirect(&slots[i].pathBuf[0], i, uint64(i)<<8|udOpen)
		}
		cqes, err := r.Enter(n, cq)
		if err != nil {
			w.warnf("io_uring submit failed: %v", err)
			continue
		}
		for _, c := range cqes {
			s := &slots[c.UserData>>8]
			if c.Res < 0 {
				w.warnf("cannot read %s: %v", s.j.display(),
					&fs.PathError{Op: "open", Path: s.j.abs, Err: syscall.Errno(-c.Res)})
			} else {
				s.opened = true
			}
		}

		// Phase 2: reads on the slots that opened. Closes wait for phase 3,
		// after the EOF confirmation.
		k := 0
		for i := 0; i < n; i++ {
			if !slots[i].opened {
				continue
			}
			r.PushReadFixed(i, bufs[i], 0, false, uint64(i)<<8|udRead)
			k++
		}
		if k == 0 {
			continue
		}
		cqes, err = r.Enter(k, cq)
		if err != nil {
			w.warnf("io_uring submit failed: %v", err)
			continue
		}
		w.uringReap(r, cqes, func(slot int, tag uint64, res int32) {
			s := &slots[slot]
			if tag == udRead {
				if res < 0 {
					s.readN = -1
					w.warnf("cannot read %s: %v", s.j.display(),
						&fs.PathError{Op: "read", Path: s.j.abs, Err: syscall.Errno(-res)})
				} else {
					s.readN = int(res)
				}
			}
		})

		// Phase 3: re-read at the short-read offset, expecting 0 bytes,
		// exactly as readFull's loop does — a filesystem that legally
		// short-reads mid-file (FUSE and friends) must not silently
		// truncate content — then close. A confirm that returns data means
		// the first read really was short (or the file grew); those files
		// are reread synchronously below. Costs ~1-2% over closing straight
		// after the read: the confirm is an inline i_size check, no I/O.
		wait := 0
		for i := 0; i < n; i++ {
			s := &slots[i]
			if !s.opened {
				continue
			}
			if s.readN >= 0 && s.readN < len(bufs[i]) {
				r.PushReadFixed(i, bufs[i][s.readN:], uint64(s.readN), true, uint64(i)<<8|udConfirm)
				wait++
			}
			r.PushCloseDirect(i, uint64(i)<<8|udClose)
			wait++
		}
		if wait > 0 {
			cqes, err = r.Enter(wait, cq)
			if err != nil {
				w.warnf("io_uring submit failed: %v", err)
				continue
			}
			w.uringReap(r, cqes, func(slot int, tag uint64, res int32) {
				s := &slots[slot]
				if tag == udConfirm {
					if res < 0 {
						s.readN = -1
						w.warnf("cannot read %s: %v", s.j.display(),
							&fs.PathError{Op: "read", Path: s.j.abs, Err: syscall.Errno(-res)})
					} else if res > 0 {
						s.reread = true
					}
				}
			})
		}

		for i := 0; i < n; i++ {
			s := &slots[i]
			if !s.opened || s.readN < 0 {
				continue
			}
			content := bufs[i][:s.readN]
			if s.reread || s.readN == len(bufs[i]) {
				// Preserve the ring-read prefix, reopen only for a normal descriptor,
				// and continue with pread from the known offset. This avoids both the
				// old reread-from-zero and extra io_uring_enter rounds on large files.
				var err error
				content, fallback, err = readContinuation(s.j.abs, content, fallback)
				if err != nil {
					w.warnf("cannot read %s: %v", s.j.display(), err)
					continue
				}
			}
			l := s.l
			if l == nil {
				l = w.opts.Registry.ByShebang(content[:min(256, len(content))])
				if l == nil {
					if w.trace {
						w.tracef("skip %s (unknown language)", s.j.display())
					}
					continue
				}
				if len(w.opts.Languages) > 0 && !w.opts.Languages[strings.ToLower(l.Name)] {
					if w.trace {
						w.tracef("skip %s (language %s not selected by --lang)", s.j.display(), l.Name)
					}
					continue
				}
			}
			w.emitCounted(s.j, l, content)
			if len(fallback) > maxKeepBufSize {
				fallback = make([]byte, initialBufSize)
			}
		}
	}
}
