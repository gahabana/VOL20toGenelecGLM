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
//  2. Snapshot existing rdp-tcp# sessions AND pre-existing disc sessions
//     for the user (both needed later for set-diff identification)
//  3. Launch FreeRDP to localhost
//  4. Poll until a NEW rdp-tcp# session appears (FreeRDP is ready)
//  5. Kill FreeRDP
//  6. Poll until a NEW disc session owned by the user appears — this is
//     the user's original console session which FreeRDP bumped off the
//     console when it connected. Reconnect it to console via tscon.
//
// Note: the FreeRDP-created session ("ours") is NOT the tscon target —
// it typically gets destroyed on kill rather than transitioning to Disc,
// and even if it didn't, reconnecting it would evict the real user. The
// correct target is whichever session was bumped off the console, which
// we identify by looking for a new disc session owned by our user.
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
	// query session shows bare usernames — strip .\ for matching.
	sessionUser := strings.TrimPrefix(username, `.\`)

	// Step 2: Take two snapshots BEFORE launching FreeRDP:
	//  - rdp-tcp# sessions → excludes stale RDP sessions from FreeRDP-ready
	//    detection (first-seen-wins would otherwise race against leftovers).
	//  - disc sessions owned by the user → excludes stale disconnected
	//    sessions from the post-kill restore-to-console lookup (a stale
	//    disc session from a prior crashed run must not be picked up as
	//    "the session FreeRDP just bumped").
	staleRDPSessionIDs := listSessionIDs("rdp-tcp#", "", w.Log)
	staleUserDiscIDs := listSessionIDs(sessionUser, "Disc", w.Log)
	if len(staleRDPSessionIDs) > 0 {
		ids := make([]string, 0, len(staleRDPSessionIDs))
		for id := range staleRDPSessionIDs {
			ids = append(ids, id)
		}
		w.Log.Debug("pre-existing rdp-tcp# sessions before priming", "ids", ids)
	}
	if len(staleUserDiscIDs) > 0 {
		ids := make([]string, 0, len(staleUserDiscIDs))
		for id := range staleUserDiscIDs {
			ids = append(ids, id)
		}
		w.Log.Debug("pre-existing disc sessions for user before priming",
			"username", sessionUser, "ids", ids)
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

	// Step 4: Poll for the FIRST new rdp-tcp# session — our signal that
	// FreeRDP has finished handshaking. Used only for readiness detection;
	// this session is NOT a tscon target.
	rdpSessionID := ""
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for id := range listSessionIDs("rdp-tcp#", "", w.Log) {
			if _, stale := staleRDPSessionIDs[id]; !stale {
				rdpSessionID = id
				break
			}
		}
		if rdpSessionID != "" {
			w.Log.Info("RDP session detected", "session_id", rdpSessionID)
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if rdpSessionID == "" {
		w.Log.Warn("RDP session not detected within timeout, proceeding to kill anyway")
	}

	// Step 5: Let Windows fully register the session, then kill FreeRDP.
	time.Sleep(1 * time.Second)
	if err := cmd.Process.Kill(); err != nil {
		w.Log.Warn("failed to kill FreeRDP process", "error", err)
	}
	_ = cmd.Wait() // reap the process
	w.Log.Info("killed FreeRDP process")

	// Step 6: Poll for a NEW disc session owned by our user — this is the
	// user's original console session, which FreeRDP bumped off when it
	// connected and is now disconnected after FreeRDP was killed. Using
	// set-diff against the snapshot ensures we don't pick up an unrelated
	// stale disc session from a prior crash.
	userDiscSessionID := ""
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for id := range listSessionIDs(sessionUser, "Disc", w.Log) {
			if _, stale := staleUserDiscIDs[id]; !stale {
				userDiscSessionID = id
				break
			}
		}
		if userDiscSessionID != "" {
			w.Log.Debug("user session reached Disc state after FreeRDP kill",
				"session_id", userDiscSessionID)
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Step 7: Reconnect the user's now-disconnected session to console.
	// If no new disc session was observed, skip tscon rather than guess.
	if userDiscSessionID == "" {
		w.Log.Warn("no new disc session for user found, skipping tscon",
			"username", sessionUser)
	} else {
		w.Log.Info("reconnecting session to console",
			"session_id", userDiscSessionID, "username", username)
		tsconCmd := exec.Command("tscon", userDiscSessionID, "/dest:console")
		if output, err := tsconCmd.CombinedOutput(); err != nil {
			w.Log.Warn("tscon failed",
				"error", err, "output", string(output), "session_id", userDiscSessionID)
		} else {
			w.Log.Info("reconnected console session via tscon",
				"session_id", userDiscSessionID)
		}
		time.Sleep(1 * time.Second)
	}

	w.Log.Info("RDP session primed successfully")
	return nil
}

// listSessionIDs runs "query session" and returns the set of session IDs
// whose line contains keyword (case-insensitive substring). If stateFilter
// is non-empty, only sessions whose STATE column equals stateFilter
// (case-insensitive) are included. Returns an empty (non-nil) map if
// nothing matches. Used by Prime() to snapshot both rdp-tcp# sessions (for
// FreeRDP-ready detection) and disc+user sessions (for post-kill restore
// of the user's bumped console session), in each case via set-diff.
func listSessionIDs(keyword, stateFilter string, log *slog.Logger) map[string]struct{} {
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
		fields := strings.Fields(line)
		for i, f := range fields {
			id, err := strconv.Atoi(f)
			if err != nil || id <= 0 || id >= 65536 {
				continue
			}
			if stateFilter != "" {
				if i+1 >= len(fields) || !strings.EqualFold(fields[i+1], stateFilter) {
					break
				}
			}
			result[strconv.Itoa(id)] = struct{}{}
			break
		}
	}
	return result
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
