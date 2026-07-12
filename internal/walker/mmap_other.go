//go:build !darwin && !linux

package walker

func mapRaw(rawFile, int) ([]byte, int) { return nil, 0 }
func unmapRaw([]byte)                   {}
