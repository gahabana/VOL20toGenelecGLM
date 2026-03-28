//go:build windows

package rdp

import (
	"bufio"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modkernel32 = windows.NewLazySystemDLL("kernel32.dll")
	modadvapi32 = windows.NewLazySystemDLL("advapi32.dll")

	procGetTickCount64 = modkernel32.NewProc("GetTickCount64")
	procCredReadW      = modadvapi32.NewProc("CredReadW")
	procCredFree       = modadvapi32.NewProc("CredFree")
)

const (
	credTypeGeneric uint32 = 1
	flagFileName           = "rdp_primed.flag"
	bootTolerance          = 60.0 // seconds
)

// credentialW mirrors the Windows CREDENTIALW struct layout.
type credentialW struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        uint64
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

// WindowsPrimer implements RDP session priming on Windows to prevent
// high CPU in GLM after RDP session switches.
type WindowsPrimer struct {
	Log *slog.Logger
}

// flagPath returns the full path to the boot flag file in %TEMP%.
func flagPath() string {
	return filepath.Join(os.TempDir(), flagFileName)
}

// bootTimestamp returns the approximate boot time as seconds since epoch.
// It uses GetTickCount64 to compute (now - uptime).
func bootTimestamp() float64 {
	ret, _, _ := procGetTickCount64.Call()
	uptimeMs := uint64(ret)
	nowMs := uint64(time.Now().UnixMilli())
	return float64(nowMs-uptimeMs) / 1000.0
}

// NeedsPriming checks whether RDP priming is needed for this boot.
// It compares the stored boot timestamp in the flag file with the current
// boot timestamp. If they differ by more than 60 seconds, priming is needed.
func (w *WindowsPrimer) NeedsPriming() bool {
	currentBoot := bootTimestamp()

	data, err := os.ReadFile(flagPath())
	if err == nil {
		storedBoot, parseErr := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
		if parseErr == nil && math.Abs(storedBoot-currentBoot) < bootTolerance {
			w.Log.Info("RDP priming already done this boot", "boot_time", currentBoot)
			return false
		}
	}

	// Write the flag file for this boot
	writeErr := os.WriteFile(flagPath(), []byte(fmt.Sprintf("%.1f", currentBoot)), 0644)
	if writeErr != nil {
		w.Log.Warn("failed to write RDP priming flag file", "error", writeErr)
	}

	w.Log.Info("RDP priming needed", "boot_time", currentBoot)
	return true
}

// readCredential attempts to read a generic credential from Windows Credential Manager.
func readCredential(target string) (username, password string, err error) {
	targetUTF16, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return "", "", fmt.Errorf("UTF16 conversion for target %q: %w", target, err)
	}

	var pcred *credentialW
	ret, _, callErr := procCredReadW.Call(
		uintptr(unsafe.Pointer(targetUTF16)),
		uintptr(credTypeGeneric),
		0, // reserved
		uintptr(unsafe.Pointer(&pcred)),
	)
	if ret == 0 {
		return "", "", fmt.Errorf("CredReadW(%q): %w", target, callErr)
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(pcred)))

	// Extract username
	if pcred.UserName != nil {
		username = windows.UTF16PtrToString(pcred.UserName)
	}

	// Extract password from UTF-16LE credential blob
	if pcred.CredentialBlobSize > 0 && pcred.CredentialBlob != nil {
		blob := unsafe.Slice(pcred.CredentialBlob, pcred.CredentialBlobSize)
		u16 := make([]uint16, pcred.CredentialBlobSize/2)
		for i := range u16 {
			u16[i] = uint16(blob[i*2]) | uint16(blob[i*2+1])<<8
		}
		password = windows.UTF16ToString(u16)
	}

	return username, password, nil
}

// getCredentials tries to read RDP credentials from Credential Manager,
// attempting "localhost" first, then "TERMSRV/localhost".
func getCredentials() (username, password string, err error) {
	targets := []string{"localhost", "TERMSRV/localhost"}
	for _, target := range targets {
		username, password, err = readCredential(target)
		if err == nil {
			return username, password, nil
		}
	}
	return "", "", fmt.Errorf("no credentials found for targets %v: %w", targets, err)
}

// Prime performs the full RDP priming sequence:
// 1. Read credentials from Credential Manager
// 2. Launch FreeRDP to localhost
// 3. Wait for RDP session to appear
// 4. Kill FreeRDP and reconnect console via tscon
func (w *WindowsPrimer) Prime() error {
	// Step 1: Read credentials
	username, password, err := getCredentials()
	if err != nil {
		return fmt.Errorf("reading credentials: %w", err)
	}
	w.Log.Info("read credentials from Credential Manager", "username", username)

	// Prepend .\ to username if no backslash present (required for NLA)
	if !strings.Contains(username, `\`) {
		username = `.\` + username
	}

	// Step 2: Launch FreeRDP
	freerdpPath, err := exec.LookPath("wfreerdp")
	if err != nil {
		freerdpPath, err = exec.LookPath("wfreerdp.exe")
		if err != nil {
			return fmt.Errorf("wfreerdp not found in PATH: %w", err)
		}
	}

	cmd := exec.Command(freerdpPath,
		"/v:localhost",
		"/u:"+username,
		"/p:"+password,
		"/cert:ignore",
		"/sec:nla",
	)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting wfreerdp: %w", err)
	}
	w.Log.Info("launched FreeRDP", "pid", cmd.Process.Pid)

	// Step 3: Poll for RDP session (up to 10s, every 500ms)
	rdpDetected := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if findSessionID("rdp-tcp#", "", w.Log) != "" {
			rdpDetected = true
			w.Log.Info("RDP session detected")
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	if !rdpDetected {
		w.Log.Warn("RDP session not detected within timeout, proceeding anyway")
	}

	// Step 4: Wait 1s for Windows to fully register session
	time.Sleep(1 * time.Second)

	// Step 5: Kill FreeRDP
	if err := cmd.Process.Kill(); err != nil {
		w.Log.Warn("failed to kill FreeRDP process", "error", err)
	}
	_ = cmd.Wait() // reap the process
	w.Log.Info("killed FreeRDP process")

	// Step 6: Wait 1s then find the disconnected session to reconnect
	time.Sleep(1 * time.Second)
	discSessionID := findSessionID("disc", username, w.Log)
	if discSessionID == "" {
		w.Log.Warn("no disconnected session found for user, trying fallback", "username", username)
		discSessionID = "1"
	}

	// Step 7: Reconnect disconnected session to console via tscon
	w.Log.Info("reconnecting session to console", "session_id", discSessionID, "username", username)
	tsconCmd := exec.Command("tscon", discSessionID, "/dest:console")
	if output, err := tsconCmd.CombinedOutput(); err != nil {
		w.Log.Warn("tscon failed", "error", err, "output", string(output), "session_id", discSessionID)
	} else {
		w.Log.Info("reconnected console session via tscon", "session_id", discSessionID)
	}

	// Step 7: Final settle time
	time.Sleep(1 * time.Second)

	w.Log.Info("RDP session primed successfully")
	return nil
}

// findSessionID runs "query session" and finds a session matching the given
// keyword (e.g. "rdp-tcp#" or "disc") and optionally a username. Returns
// the session ID as a string, or "" if not found. Logs all sessions at DEBUG level.
func findSessionID(keyword, username string, log *slog.Logger) string {
	cmd := exec.Command("query", "session")
	output, _ := cmd.CombinedOutput()
	if len(output) == 0 {
		log.Debug("query session returned empty output")
		return ""
	}

	keyword = strings.ToLower(keyword)
	username = strings.ToLower(username)
	matchedID := ""

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(line)

		// Log every session line for diagnostics
		log.Debug("query session", "line", trimmed)

		if !strings.Contains(lower, keyword) {
			continue
		}
		if username != "" && !strings.Contains(lower, username) {
			continue
		}
		// Extract session ID: first numeric field in the line
		fields := strings.Fields(line)
		for _, f := range fields {
			if id, err := strconv.Atoi(f); err == nil && id > 0 && id < 65536 {
				matchedID = strconv.Itoa(id)
				log.Debug("session matched", "keyword", keyword, "username", username, "session_id", matchedID)
				return matchedID
			}
		}
	}
	log.Debug("no session matched", "keyword", keyword, "username", username)
	return ""
}
