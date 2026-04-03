// Package bootflag provides a once-per-boot guard using a flag file in %TEMP%.
// The flag stores the boot timestamp; if the stored value matches the current
// boot (within tolerance), the task is skipped.
package bootflag

import (
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const bootTolerance = 60.0 // seconds

// NeedsRun checks whether a once-per-boot task should run.
// It compares the stored boot timestamp in flagFile (under os.TempDir()) with
// the current boot timestamp. Returns true if the task hasn't run this boot,
// and writes the flag file to record that it has.
func NeedsRun(flagFile string, log *slog.Logger) bool {
	flagPath := filepath.Join(os.TempDir(), flagFile)
	currentBoot := BootTimestamp()

	data, err := os.ReadFile(flagPath)
	if err == nil {
		storedBoot, parseErr := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
		if parseErr == nil && math.Abs(storedBoot-currentBoot) < bootTolerance {
			log.Info("already done this boot, skipping", "flag", flagFile)
			return false
		}
	}

	if writeErr := os.WriteFile(flagPath, []byte(fmt.Sprintf("%.1f", currentBoot)), 0644); writeErr != nil {
		log.Warn("failed to write boot flag", "flag", flagFile, "error", writeErr)
	}
	return true
}

// BootTimestamp returns the approximate boot time as seconds since epoch.
// On Windows it uses GetTickCount64; on other platforms it falls back to
// a process-start approximation.
func BootTimestamp() float64 {
	return bootTimestamp()
}

// bootTimestampFallback is used on non-Windows platforms.
// It approximates boot time as "now", which means the flag file will always
// appear stale across process restarts. This is acceptable because boot-once
// tasks only run on Windows.
func bootTimestampFallback() float64 {
	return float64(time.Now().UnixMilli()) / 1000.0
}
