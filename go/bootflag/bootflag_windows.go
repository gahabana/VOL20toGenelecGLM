//go:build windows

package bootflag

import (
	"time"

	"golang.org/x/sys/windows"
)

var procGetTickCount64 = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetTickCount64")

// bootTimestamp uses GetTickCount64 to compute (now - uptime).
func bootTimestamp() float64 {
	ret, _, _ := procGetTickCount64.Call()
	uptimeMs := uint64(ret)
	nowMs := uint64(time.Now().UnixMilli())
	return float64(nowMs-uptimeMs) / 1000.0
}
