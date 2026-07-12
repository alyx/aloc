//go:build linux

// uring-smoke validates the exact io_uring semantics the walker's uring
// backend relies on, against the running kernel. It prints every CQE so
// link/cancel behavior on this kernel version is visible, not assumed.
//
// Kernel 5.15 resolves IOSQE_FIXED_FILE at submission time, so an
// open-direct linked to a read of the same slot cancels the whole chain
// (EBADF at prep). The walker therefore uses two submits per batch:
// opens first, then read+close chains on the populated slots.
//
// Usage: uring-smoke <existing-file> <missing-path>
package main

import (
	"fmt"
	"os"
	"syscall"

	"github.com/alyx/aloc/internal/uring"
)

func cqeName(ud uint64) string {
	switch ud & 0xff {
	case 0:
		return "open "
	case 1:
		return "read "
	case 2:
		return "close"
	}
	return "?"
}

func report(cqes []uring.CQE, err error) {
	if err != nil {
		fmt.Printf("  enter error: %v\n", err)
		return
	}
	for _, c := range cqes {
		slot := c.UserData >> 8
		if c.Res < 0 {
			fmt.Printf("  slot %d %s res=%d (%v)\n", slot, cqeName(c.UserData), c.Res, syscall.Errno(-c.Res))
		} else {
			fmt.Printf("  slot %d %s res=%d\n", slot, cqeName(c.UserData), c.Res)
		}
	}
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: uring-smoke <existing-file> <missing-path>")
		os.Exit(2)
	}
	exist, missing := os.Args[1], os.Args[2]

	if err := uring.Probe(); err != nil {
		fmt.Printf("probe: FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("probe: openat/read/close supported")

	r, err := uring.New(128, 32)
	if err != nil {
		fmt.Printf("ring setup with sparse file table: FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("ring setup + sparse REGISTER_FILES(32 x -1): ok")
	defer r.Close()

	buf := make([]byte, 4096)
	buf2 := make([]byte, 4096)
	tiny := make([]byte, 16)
	pe := append([]byte(exist), 0)
	pm := append([]byte(missing), 0)
	out := make([]uring.CQE, 64)

	ud := func(slot, op int) uint64 { return uint64(slot)<<8 | uint64(op) }

	fmt.Println("--- phase 1: batch of 3 open-directs (slots 0,1,2; slot 1 missing path)")
	r.PushOpenDirect(&pe[0], 0, ud(0, 0))
	r.PushOpenDirect(&pm[0], 1, ud(1, 0))
	r.PushOpenDirect(&pe[0], 2, ud(2, 0))
	cqes, err := r.Enter(3, out)
	report(cqes, err)

	fmt.Println("--- phase 2: read+close chains on slots 0,2 (slot 2 tiny buf = full read)")
	r.PushReadFixed(0, buf, 0, true, ud(0, 1))
	r.PushCloseDirect(0, ud(0, 2))
	r.PushReadFixed(2, tiny, 0, true, ud(2, 1))
	r.PushCloseDirect(2, ud(2, 2))
	cqes, err = r.Enter(4, out)
	report(cqes, err)

	fmt.Println("--- phase 3: slot reuse — open slot 0 again, read+close")
	r.PushOpenDirect(&pe[0], 0, ud(0, 0))
	cqes, err = r.Enter(1, out)
	report(cqes, err)
	r.PushReadFixed(0, buf2, 0, true, ud(0, 1))
	r.PushCloseDirect(0, ud(0, 2))
	cqes, err = r.Enter(2, out)
	report(cqes, err)

	fmt.Println("--- phase 4: read on an empty slot chained to close (prep-fail isolation)")
	r.PushReadFixed(5, buf, 0, true, ud(5, 1))
	r.PushCloseDirect(5, ud(5, 2))
	r.PushReadFixed(0, buf, 0, true, ud(0, 1)) // slot 0 also empty now; independent chain
	r.PushCloseDirect(0, ud(0, 2))
	cqes, err = r.Enter(4, out)
	report(cqes, err)

	fmt.Println("--- phase 5: open into occupied slot (expect EBUSY or replace)")
	r.PushOpenDirect(&pe[0], 3, ud(3, 0))
	cqes, err = r.Enter(1, out)
	report(cqes, err)
	r.PushOpenDirect(&pe[0], 3, ud(3, 0)) // slot 3 still holds a file
	cqes, err = r.Enter(1, out)
	report(cqes, err)

	if err := r.ClearSlot(3); err != nil {
		fmt.Printf("ClearSlot(occupied): %v\n", err)
	} else {
		fmt.Println("ClearSlot(occupied): ok")
	}
	fmt.Println("smoke done")
}
