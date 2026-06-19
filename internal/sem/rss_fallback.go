//go:build !darwin && !linux

package sem

func maxRSSBytes() uint64 {
	return 0
}
