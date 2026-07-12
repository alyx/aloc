//go:build !darwin

package walker

func platformIOConcurrency(jobs int) int   { return jobs }
func platformWalkConcurrency(jobs int) int { return min(8, jobs) }
func platformSplitIO() bool                { return false }
