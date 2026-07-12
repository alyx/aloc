//go:build darwin

package walker

// APFS throughput on many small files peaks well below the logical CPU count
// on heterogeneous Apple silicon. Keep metadata and file I/O pressure bounded;
// counting still uses every requested worker after each read releases its slot.
func platformIOConcurrency(jobs int) int   { return min(4, jobs) }
func platformWalkConcurrency(jobs int) int { return min(4, jobs) }
func platformSplitIO() bool                { return true }
