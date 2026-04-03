//go:build !windows

package bootflag

func bootTimestamp() float64 {
	return bootTimestampFallback()
}
