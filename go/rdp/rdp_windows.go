//go:build windows

package rdp

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"vol20toglm/bootflag"
)

var (
	modkernel32 = windows.NewLazySystemDLL("kernel32.dll")
	modadvapi32 = windows.NewLazySystemDLL("advapi32.dll")
	modwtsapi32 = windows.NewLazySystemDLL("wtsapi32.dll")

	procProcessIdToSessionId  = modkernel32.NewProc("ProcessIdToSessionId")
	procCredReadW             = modadvapi32.NewProc("CredReadW")
	procCredFree              = modadvapi32.NewProc("CredFree")
	procWTSEnumerateSessionsW = modwtsapi32.NewProc("WTSEnumerateSessionsW")
	procWTSFreeMemory         = modwtsapi32.NewProc("WTSFreeMemory")
)

// WTS session states.
const (
	wtsDisconnected = 4
)

// wtsSessionInfo matches the Windows WTS_SESSION_INFOW structure.
type wtsSessionInfo struct {
	SessionID      uint32
	WinStationName *uint16
	State          int32
}

const (
	credTypeGeneric uint32 = 1
	flagFileName           = "rdp_primed.flag"
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

// EnsureSessionConnected checks if the current session is disconnected
// (e.g., after RDP disconnect) and reconnects it to the console via tscon.
// This must be called before any pixel/screen capture operations, as BitBlt
// fails when the session has no active display.
//
// Returns nil if already connected or successfully reconnected.
func EnsureSessionConnected(log *slog.Logger) error {
	// Get current process session ID.
	var sessionID uint32
	ret, _, _ := procProcessIdToSessionId.Call(
		uintptr(os.Getpid()),
		uintptr(unsafe.Pointer(&sessionID)),
	)
	if ret == 0 {
		log.Debug("EnsureSessionConnected: ProcessIdToSessionId failed, assuming connected")
		return nil
	}

	// Enumerate sessions to find ours and check its state.
	// pSessionInfo is stored as unsafe.Pointer (not uintptr) so that
	// subsequent pointer arithmetic satisfies go vet's unsafe.Pointer
	// rule 4: the conversion uintptr→unsafe.Pointer must happen in a
	// single expression, not from a stored uintptr variable.
	var pSessionInfo unsafe.Pointer
	var count uint32
	ret, _, _ = procWTSEnumerateSessionsW.Call(
		0, // WTS_CURRENT_SERVER_HANDLE
		0, // Reserved
		1, // Version
		uintptr(unsafe.Pointer(&pSessionInfo)),
		uintptr(unsafe.Pointer(&count)),
	)
	if ret == 0 {
		log.Debug("EnsureSessionConnected: WTSEnumerateSessionsW failed, assuming connected")
		return nil
	}
	defer procWTSFreeMemory.Call(uintptr(pSessionInfo))

	// Walk the session array to find our session's state.
	const infoSize = unsafe.Sizeof(wtsSessionInfo{})
	disconnected := false
	for i := uint32(0); i < count; i++ {
		info := (*wtsSessionInfo)(unsafe.Pointer(uintptr(pSessionInfo) + uintptr(i)*infoSize))
		if info.SessionID == sessionID {
			disconnected = info.State == wtsDisconnected
			break
		}
	}

	if !disconnected {
		return nil
	}

	// Session is disconnected — reconnect to console.
	log.Info("session disconnected, reconnecting to console", "session_id", sessionID)

	cmd := exec.Command("tscon", strconv.FormatUint(uint64(sessionID), 10), "/dest:console")
	output, err := cmd.CombinedOutput()
	if err == nil {
		log.Info("reconnected session to console", "session_id", sessionID)
		time.Sleep(500 * time.Millisecond) // let display driver settle
		return nil
	}

	// Direct tscon failed — try psexec for SYSTEM privileges.
	log.Debug("direct tscon failed, trying psexec", "err", err, "output", string(output))
	for _, name := range []string{"psexec", "psexec64"} {
		psexecPath, lookErr := exec.LookPath(name)
		if lookErr != nil {
			continue
		}
		cmd = exec.Command(psexecPath, "-s", "-accepteula",
			"tscon", strconv.FormatUint(uint64(sessionID), 10), "/dest:console")
		output, err = cmd.CombinedOutput()
		if err == nil {
			log.Info("reconnected session to console via psexec", "session_id", sessionID)
			time.Sleep(500 * time.Millisecond)
			return nil
		}
	}

	return fmt.Errorf("failed to reconnect session %d to console: %w (output: %s)", sessionID, err, string(output))
}

// WindowsPrimer implements RDP session priming on Windows to prevent
// high CPU in GLM after RDP session switches.
type WindowsPrimer struct {
	Log *slog.Logger
}

// NeedsPriming checks whether RDP priming is needed for this boot.
// Returns false if already primed this boot, or if the user is already
// connected via RDP (session is inherently primed — priming would disrupt it).
func (w *WindowsPrimer) NeedsPriming() bool {
	if !bootflag.NeedsRun(flagFileName, w.Log) {
		return false
	}
	// A stale rdp-tcp# session from a prior crashed run can linger in
	// Disc/Down state — we must only skip priming for a session that is
	// actually Active, otherwise the CPU bug we are trying to avoid can
	// re-emerge on this boot.
	if hasActiveSession("rdp-tcp#", w.Log) {
		w.Log.Info("user already connected via RDP, session inherently primed")
		return false
	}
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
//  1. Read credentials from Credential Manager
//  2. Snapshot existing rdp-tcp# sessions (stale / other users)
//  3. Launch FreeRDP to localhost
//  4. Poll until a NEW rdp-tcp# session appears (the one we just created)
//  5. Kill FreeRDP and reconnect our now-disconnected session to console
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

	// Step 2: Snapshot existing rdp-tcp# session IDs BEFORE launching FreeRDP.
	// Anything present now is pre-existing (crashed prior run, orphan) and
	// must be excluded when identifying our newly-created session. This
	// replaces the previous race-prone "first rdp-tcp# found wins" logic.
	staleSessionIDs := listSessionIDs("rdp-tcp#", w.Log)
	if len(staleSessionIDs) > 0 {
		preExisting := make([]string, 0, len(staleSessionIDs))
		for id := range staleSessionIDs {
			preExisting = append(preExisting, id)
		}
		w.Log.Debug("pre-existing rdp-tcp# sessions before priming", "ids", preExisting)
	}

	// Step 3: Launch FreeRDP
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

	// Step 4: Poll for OUR rdp-tcp# session — the first ID not in the snapshot.
	ourSessionID := ""
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for id := range listSessionIDs("rdp-tcp#", w.Log) {
			if _, stale := staleSessionIDs[id]; !stale {
				ourSessionID = id
				break
			}
		}
		if ourSessionID != "" {
			w.Log.Info("RDP session detected", "session_id", ourSessionID)
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	if ourSessionID == "" {
		w.Log.Warn("RDP session not detected within timeout — skipping tscon to avoid grabbing an unrelated session")
	}

	// Step 5: Wait 1s for Windows to fully register session
	time.Sleep(1 * time.Second)

	// Step 6: Kill FreeRDP
	if err := cmd.Process.Kill(); err != nil {
		w.Log.Warn("failed to kill FreeRDP process", "error", err)
	}
	_ = cmd.Wait() // reap the process
	w.Log.Info("killed FreeRDP process")

	// Step 7: Reconnect OUR now-disconnected session to console via tscon.
	// If we never identified our session we skip tscon entirely rather than
	// fall back to a guess — grabbing the wrong session is strictly worse
	// than a one-off priming failure.
	//
	// After killing FreeRDP the session transitions from Active → Disc, but
	// the transition is not instantaneous: a fixed 1s sleep was observed to
	// be too short on v0.12.4.1, causing tscon to fail with error 5023
	// ("group or resource not in the correct state"). Instead, poll for up
	// to 5s until the session is confirmed Disc, then run tscon.
	if ourSessionID != "" {
		discReady := false
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if isSessionDisc(ourSessionID, w.Log) {
				discReady = true
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if !discReady {
			w.Log.Warn("session did not reach Disc state before timeout, skipping tscon",
				"session_id", ourSessionID)
		} else {
			w.Log.Info("reconnecting session to console", "session_id", ourSessionID, "username", username)
			tsconCmd := exec.Command("tscon", ourSessionID, "/dest:console")
			if output, err := tsconCmd.CombinedOutput(); err != nil {
				w.Log.Warn("tscon failed", "error", err, "output", string(output), "session_id", ourSessionID)
			} else {
				w.Log.Info("reconnected console session via tscon", "session_id", ourSessionID)
			}
			time.Sleep(1 * time.Second)
		}
	}

	w.Log.Info("RDP session primed successfully")
	return nil
}

// listSessionIDs runs "query session" and returns the set of session IDs
// whose line contains keyword (case-insensitive substring). Returns an empty
// (non-nil) map if nothing matches. Used by Prime() to snapshot the set of
// rdp-tcp# sessions so a newly-created session can be identified by diff.
func listSessionIDs(keyword string, log *slog.Logger) map[string]struct{} {
	result := make(map[string]struct{})
	cmd := exec.Command("query", "session")
	output, _ := cmd.CombinedOutput()
	if len(output) == 0 {
		log.Debug("query session returned empty output")
		return result
	}

	keywordLower := strings.ToLower(keyword)
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.Contains(strings.ToLower(line), keywordLower) {
			continue
		}
		// Extract session ID: first numeric field in the valid session range.
		for _, f := range strings.Fields(line) {
			id, err := strconv.Atoi(f)
			if err != nil || id <= 0 || id >= 65536 {
				continue
			}
			result[strconv.Itoa(id)] = struct{}{}
			break
		}
	}
	return result
}

// isSessionDisc reports whether the session with the given ID is currently
// in the Disc (disconnected) state according to "query session". Used after
// killing FreeRDP to wait for the session to finish tearing down before
// running tscon — calling tscon on a session still mid-transition fails
// with error 5023 ("group or resource not in the correct state").
func isSessionDisc(sessionID string, log *slog.Logger) bool {
	if sessionID == "" {
		return false
	}
	cmd := exec.Command("query", "session")
	output, _ := cmd.CombinedOutput()
	if len(output) == 0 {
		return false
	}
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		for i, f := range fields {
			id, err := strconv.Atoi(f)
			if err != nil || id <= 0 || id >= 65536 {
				continue
			}
			if strconv.Itoa(id) != sessionID {
				break
			}
			// Found our session. State is the next field.
			if i+1 < len(fields) && strings.EqualFold(fields[i+1], "Disc") {
				log.Debug("session reached Disc state", "session_id", sessionID)
				return true
			}
			return false
		}
	}
	return false
}

// hasActiveSession returns true if "query session" contains any session whose
// line matches keyword (case-insensitive substring) AND whose STATE column is
// Active. Used by NeedsPriming() so a stale rdp-tcp# entry in Disc/Down state
// from a prior crashed run does not cause priming to be skipped.
func hasActiveSession(keyword string, log *slog.Logger) bool {
	cmd := exec.Command("query", "session")
	output, _ := cmd.CombinedOutput()
	if len(output) == 0 {
		return false
	}
	keywordLower := strings.ToLower(keyword)
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(strings.ToLower(line), keywordLower) {
			continue
		}
		// State is the field immediately after the numeric session ID.
		fields := strings.Fields(line)
		for i, f := range fields {
			id, err := strconv.Atoi(f)
			if err != nil || id <= 0 || id >= 65536 {
				continue
			}
			if i+1 < len(fields) && strings.EqualFold(fields[i+1], "Active") {
				log.Debug("active session matched", "keyword", keyword, "session_id", id)
				return true
			}
			break
		}
	}
	return false
}
