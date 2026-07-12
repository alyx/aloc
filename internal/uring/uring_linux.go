//go:build linux

// Package uring is a minimal io_uring wrapper for the walker's batched file
// reads: open/read/close chains over direct descriptors, one submission
// syscall per batch. It is deliberately not a general-purpose binding — only
// the operations the walker needs are implemented, in pure Go (no cgo, no
// liburing).
package uring

import (
	"fmt"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// amd64/arm64 syscall numbers are identical for the io_uring family.
const (
	sysSetup    = 425
	sysEnter    = 426
	sysRegister = 427
)

// Opcodes (linux/io_uring.h).
const (
	opOpenat = 18
	opClose  = 19
	opRead   = 22
)

// SQE flags.
const (
	sqeFixedFile = 1 << 0
	sqeIOLink    = 1 << 2
	sqeHardlink  = 1 << 3
)

// io_uring_register opcodes.
const (
	regRegisterFiles    = 2
	regRegisterFilesUpd = 6
	regRegisterProbe    = 8
)

// Setup features we rely on / check.
const (
	featSingleMmap = 1 << 0
	featNoDrop     = 1 << 1
)

// Enter flags.
const enterGetevents = 1

// mmap offsets.
const (
	offSQRing = 0
	offSQEs   = 0x10000000
)

// io_uring_params, 120 bytes on 64-bit.
type params struct {
	sqEntries    uint32
	cqEntries    uint32
	flags        uint32
	sqThreadCPU  uint32
	sqThreadIdle uint32
	features     uint32
	wqFD         uint32
	resv         [3]uint32
	sqOff        sqringOffsets
	cqOff        cqringOffsets
}

type sqringOffsets struct {
	head        uint32
	tail        uint32
	ringMask    uint32
	ringEntries uint32
	flags       uint32
	dropped     uint32
	array       uint32
	resv1       uint32
	resv2       uint64
}

type cqringOffsets struct {
	head        uint32
	tail        uint32
	ringMask    uint32
	ringEntries uint32
	overflow    uint32
	cqes        uint32
	flags       uint32
	resv1       uint32
	resv2       uint64
}

// SQE mirrors struct io_uring_sqe (64 bytes). openFlags doubles as the
// rw_flags union member; fileIndex is the 5.15 direct-descriptor selector.
type sqe struct {
	opcode      uint8
	flags       uint8
	ioprio      uint16
	fd          int32
	off         uint64
	addr        uint64
	len         uint32
	openFlags   uint32
	userData    uint64
	bufIndex    uint16
	personality uint16
	fileIndex   uint32
	pad         [2]uint64
}

// CQE mirrors struct io_uring_cqe.
type CQE struct {
	UserData uint64
	Res      int32
	Flags    uint32
}

// Ring is one io_uring instance. Not safe for concurrent use; the walker
// gives each worker its own ring.
type Ring struct {
	fd int

	sqRing []byte // mmap of the SQ ring (shared with CQ under FEAT_SINGLE_MMAP)
	sqes   []byte // mmap of the SQE array

	sqHead    *uint32
	sqTail    *uint32
	sqMask    uint32
	sqArray   []uint32
	sqEntries uint32

	cqHead *uint32
	cqTail *uint32
	cqMask uint32
	cqes   *CQE // base of the CQE array

	toSubmit uint32 // SQEs staged since the last Enter
}

// New creates a ring with the given SQ depth and registers a sparse table of
// nfiles direct-descriptor slots (all -1). Kernel 5.15 accepts -1 entries in
// IORING_REGISTER_FILES, which is what direct opens require.
func New(entries, nfiles int) (*Ring, error) {
	var p params
	fd, _, errno := syscall.Syscall(sysSetup, uintptr(entries), uintptr(unsafe.Pointer(&p)), 0)
	if errno != 0 {
		return nil, fmt.Errorf("io_uring_setup: %w", errno)
	}
	r := &Ring{fd: int(fd)}
	if p.features&featSingleMmap == 0 {
		// Pre-5.4 layout; not worth supporting for this experiment.
		syscall.Close(r.fd)
		return nil, fmt.Errorf("kernel lacks IORING_FEAT_SINGLE_MMAP")
	}

	sqSize := int(p.sqOff.array + p.sqEntries*4)
	cqSize := int(p.cqOff.cqes + p.cqEntries*16)
	size := max(sqSize, cqSize)
	ring, err := syscall.Mmap(r.fd, offSQRing, size,
		syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED|syscall.MAP_POPULATE)
	if err != nil {
		syscall.Close(r.fd)
		return nil, fmt.Errorf("mmap sq ring: %w", err)
	}
	r.sqRing = ring

	sqes, err := syscall.Mmap(r.fd, offSQEs, int(p.sqEntries)*64,
		syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED|syscall.MAP_POPULATE)
	if err != nil {
		syscall.Munmap(ring)
		syscall.Close(r.fd)
		return nil, fmt.Errorf("mmap sqes: %w", err)
	}
	r.sqes = sqes

	base := unsafe.Pointer(&ring[0])
	r.sqHead = (*uint32)(unsafe.Add(base, p.sqOff.head))
	r.sqTail = (*uint32)(unsafe.Add(base, p.sqOff.tail))
	r.sqMask = *(*uint32)(unsafe.Add(base, p.sqOff.ringMask))
	r.sqEntries = p.sqEntries
	r.sqArray = unsafe.Slice((*uint32)(unsafe.Add(base, p.sqOff.array)), p.sqEntries)
	r.cqHead = (*uint32)(unsafe.Add(base, p.cqOff.head))
	r.cqTail = (*uint32)(unsafe.Add(base, p.cqOff.tail))
	r.cqMask = *(*uint32)(unsafe.Add(base, p.cqOff.ringMask))
	r.cqes = (*CQE)(unsafe.Add(base, p.cqOff.cqes))

	if nfiles > 0 {
		fds := make([]int32, nfiles)
		for i := range fds {
			fds[i] = -1
		}
		_, _, errno = syscall.Syscall6(sysRegister, uintptr(r.fd), regRegisterFiles,
			uintptr(unsafe.Pointer(&fds[0])), uintptr(nfiles), 0, 0)
		if errno != 0 {
			r.Close()
			return nil, fmt.Errorf("register sparse files: %w", errno)
		}
	}
	return r, nil
}

func (r *Ring) Close() {
	if r.sqes != nil {
		syscall.Munmap(r.sqes)
	}
	if r.sqRing != nil {
		syscall.Munmap(r.sqRing)
	}
	syscall.Close(r.fd)
}

// nextSQE claims the next SQE slot. The caller must not stage more than the
// ring holds between Enter calls; the walker's batch sizing guarantees that.
func (r *Ring) nextSQE() *sqe {
	tail := *r.sqTail // only this goroutine writes the tail
	idx := tail & r.sqMask
	e := (*sqe)(unsafe.Pointer(&r.sqes[idx*64]))
	*e = sqe{}
	r.sqArray[idx] = idx
	atomic.StoreUint32(r.sqTail, tail+1)
	r.toSubmit++
	return e
}

// PushOpenDirect stages an independent openat into direct-descriptor slot.
// path must be NUL-terminated and stay alive until Enter returns (the kernel
// copies it at submit). Not linked to the read: this kernel resolves
// IOSQE_FIXED_FILE at submission time, so a read of a slot populated by an
// earlier SQE in the same submission fails EBADF and cancels the chain —
// opens and reads must go in separate Enter calls. Direct opens reject
// O_CLOEXEC; direct descriptors are not real fds, so it is meaningless
// there anyway.
func (r *Ring) PushOpenDirect(path *byte, slot int, userData uint64) {
	e := r.nextSQE()
	e.opcode = opOpenat
	e.fd = -100 // AT_FDCWD
	e.addr = uint64(uintptr(unsafe.Pointer(path)))
	e.openFlags = syscall.O_RDONLY
	e.fileIndex = uint32(slot + 1)
	e.userData = userData
}

// PushReadFixed stages a read from direct-descriptor slot into buf at offset
// off. With hardlinkNext the following SQE only runs after this one — a hard
// link, because a short read (any file smaller than buf) severs a normal
// link, and a chained close must still run.
func (r *Ring) PushReadFixed(slot int, buf []byte, off uint64, hardlinkNext bool, userData uint64) {
	e := r.nextSQE()
	e.opcode = opRead
	e.flags = sqeFixedFile
	if hardlinkNext {
		e.flags |= sqeHardlink
	}
	e.fd = int32(slot)
	e.off = off
	e.addr = uint64(uintptr(unsafe.Pointer(&buf[0])))
	e.len = uint32(len(buf))
	e.userData = userData
}

// PushCloseDirect stages a close of direct-descriptor slot, ending its chain.
func (r *Ring) PushCloseDirect(slot int, userData uint64) {
	e := r.nextSQE()
	e.opcode = opClose
	e.fileIndex = uint32(slot + 1)
	e.userData = userData
}

// Enter submits everything staged and waits for waitFor completions, then
// reaps into out. Returns the CQEs reaped (a prefix of out). The Go runtime
// interrupts blocked syscalls with SIGURG for preemption, so EINTR/EAGAIN
// are retried; the kernel only returns an error when nothing was consumed.
func (r *Ring) Enter(waitFor int, out []CQE) ([]CQE, error) {
	submit := r.toSubmit
	r.toSubmit = 0
	reaped := 0
	for {
		reaped += r.reap(out[reaped:])
		if submit == 0 && reaped >= waitFor {
			return out[:reaped], nil
		}
		want := uint32(0)
		if reaped < waitFor {
			want = uint32(waitFor - reaped)
		}
		n, _, errno := syscall.Syscall6(sysEnter, uintptr(r.fd), uintptr(submit),
			uintptr(want), enterGetevents, 0, 0)
		if errno == syscall.EINTR || errno == syscall.EAGAIN || errno == syscall.EBUSY {
			continue
		}
		if errno != 0 {
			return out[:reaped], fmt.Errorf("io_uring_enter: %w", errno)
		}
		submit -= uint32(n)
	}
}

// reap drains available CQEs into out and returns how many were copied.
func (r *Ring) reap(out []CQE) int {
	n := 0
	head := *r.cqHead
	tail := atomic.LoadUint32(r.cqTail)
	for head != tail && n < len(out) {
		idx := head & r.cqMask
		out[n] = *(*CQE)(unsafe.Add(unsafe.Pointer(r.cqes), uintptr(idx)*16))
		n++
		head++
	}
	atomic.StoreUint32(r.cqHead, head)
	return n
}

// ClearSlot synchronously empties one direct-descriptor slot via
// IORING_REGISTER_FILES_UPDATE. Used to recover a slot whose close was
// cancelled, so slot state never leaks across batches.
func (r *Ring) ClearSlot(slot int) error {
	fd := int32(-1)
	// struct io_uring_files_update { u32 offset; u32 resv; u64 fds; }
	upd := struct {
		offset uint32
		resv   uint32
		fds    uint64
	}{offset: uint32(slot), fds: uint64(uintptr(unsafe.Pointer(&fd)))}
	_, _, errno := syscall.Syscall6(sysRegister, uintptr(r.fd), regRegisterFilesUpd,
		uintptr(unsafe.Pointer(&upd)), 1, 0, 0)
	if errno != 0 {
		return fmt.Errorf("files_update: %w", errno)
	}
	return nil
}

// DirectSupported reports whether the kernel supports the direct-descriptor
// open/read/close cycle the walker uses, by performing one against
// /dev/null. Opcode-level probing is not enough: kernels between 5.6 and
// 5.14 support IORING_OP_OPENAT but reject the file_index form this package
// depends on (added in 5.15). The probe also fails wherever io_uring is
// unavailable altogether — old kernels, seccomp profiles (Docker blocks it
// by default), or the io_uring_disabled sysctl.
func DirectSupported() error {
	r, err := New(4, 1)
	if err != nil {
		return err
	}
	defer r.Close()
	path := [...]byte{'/', 'd', 'e', 'v', '/', 'n', 'u', 'l', 'l', 0}
	out := make([]CQE, 4)
	r.PushOpenDirect(&path[0], 0, 0)
	cqes, err := r.Enter(1, out)
	if err != nil {
		return err
	}
	for _, c := range cqes {
		if c.Res < 0 {
			return fmt.Errorf("direct open: %w", syscall.Errno(-c.Res))
		}
	}
	var buf [8]byte
	r.PushReadFixed(0, buf[:], 0, true, 1)
	r.PushCloseDirect(0, 2)
	if cqes, err = r.Enter(2, out); err != nil {
		return err
	}
	for _, c := range cqes {
		if c.Res < 0 {
			return fmt.Errorf("fixed read/direct close: %w", syscall.Errno(-c.Res))
		}
	}
	return nil
}

// Probe reports whether the kernel supports every opcode the walker needs.
func Probe() error {
	r, err := New(4, 0)
	if err != nil {
		return err
	}
	defer r.Close()
	// struct io_uring_probe: 16-byte header + 256 8-byte op entries.
	var buf [16 + 256*8]byte
	_, _, errno := syscall.Syscall6(sysRegister, uintptr(r.fd), regRegisterProbe,
		uintptr(unsafe.Pointer(&buf[0])), 256, 0, 0)
	if errno != 0 {
		return fmt.Errorf("probe: %w", errno)
	}
	lastOp := buf[0]
	for _, op := range []uint8{opOpenat, opClose, opRead} {
		if op > lastOp {
			return fmt.Errorf("opcode %d not supported (last_op %d)", op, lastOp)
		}
		entry := buf[16+int(op)*8:]
		const supported = 1 << 0     // IO_URING_OP_SUPPORTED
		if entry[2]&supported == 0 { // flags is u16 at offset 2 of the entry
			return fmt.Errorf("opcode %d not supported", op)
		}
	}
	return nil
}
