# =========================
# CONFIG
# =========================

# Path to GLM executable
$glmPath = "C:\Program Files (x86)\Genelec\GLMv5\GLMv5.exe"
$processName = "GLMv5"

# CPU gating: run ONCE at script start (not on each restart)
$cpuThreshold     = 2      # % CPU considered "idle enough"
$cpuCheckInterval = 5      # seconds
$cpuMaxChecks     = 60     # 60 * 5s = 5 minutes max wait

# Start/minimize behavior
$postStartSleepSeconds = 5
$enforcePollSeconds    = 1
$enforceMaxSeconds     = 60   # best-effort minimize window

# Stop condition for stabilization/minimize (your preferred "enough" threshold)
# This yields 2 minimizations on the stable handle in normal cases:
# - stop once same handle seen twice AND at least one minimize attempt on it has happened.
$stableNeeded    = 2
$minimizeNeeded  = 1

# Watchdog behavior (only restart logic)
$watchdogCheckInterval = 5     # seconds
$maxNonRespChecks      = 6     # 6*5 = ~30 seconds non-responsive => restart
$restartDelaySeconds   = 5

# =========================
# LOGGING
# =========================
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$logFile   = Join-Path $scriptDir "minimize-glm.log"

function Write-Log {
    param([string]$message)
    $timestamp = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
    "$timestamp`t$message" | Out-File -FilePath $logFile -Append -Encoding UTF8
}

# =========================
# WIN32 (NON-BLOCKING MINIMIZE)
# =========================
Add-Type @"
using System;
using System.Runtime.InteropServices;

public static class Win32 {
    public const UInt32 WM_SYSCOMMAND = 0x0112;
    public static readonly IntPtr SC_MINIMIZE = (IntPtr)0xF020;

    [DllImport("user32.dll", SetLastError=true)]
    public static extern bool PostMessage(IntPtr hWnd, UInt32 Msg, IntPtr wParam, IntPtr lParam);

    [DllImport("user32.dll")]
    public static extern bool IsWindow(IntPtr hWnd);
}
"@

function Invoke-NonBlockingMinimize {
    param([IntPtr]$hWnd)

    # PostMessage is non-blocking; it may fail or may not be processed if app is wedged,
    # but it will not hang this script.
    if ($hWnd -eq [IntPtr]::Zero) { return $false }
    if (-not [Win32]::IsWindow($hWnd)) { return $false }

    return [Win32]::PostMessage($hWnd, [Win32]::WM_SYSCOMMAND, [Win32]::SC_MINIMIZE, [IntPtr]::Zero)
}

# =========================
# CPU GATING (ONCE AT START)
# =========================
function Wait-ForCpuCalm {
    Write-Log "CPU pre-launch check (once): threshold=${cpuThreshold}%, interval=${cpuCheckInterval}s, maxChecks=$cpuMaxChecks"

    for ($check = 1; $check -le $cpuMaxChecks; $check++) {
        try {
            $cpu = (Get-Counter '\Processor(_Total)\% Processor Time').CounterSamples.CookedValue
            $cpuRounded = [math]::Round($cpu, 1)

            if ($cpu -lt $cpuThreshold) {
                Write-Log "CPU ${cpuRounded}% < threshold ${cpuThreshold}%. Proceeding."
                return $true
            }

            Write-Log "CPU ${cpuRounded}% >= threshold ${cpuThreshold}%. Waiting ${cpuCheckInterval}s... (check $check/$cpuMaxChecks)"
        }
        catch {
            Write-Log "WARNING: Get-Counter failed: $($_.Exception.Message). Proceeding without CPU gating."
            return $true
        }

        Start-Sleep -Seconds $cpuCheckInterval
    }

    Write-Log "CPU did not drop below threshold in allotted time; proceeding anyway."
    return $true
}

# =========================
# START + BEST-EFFORT MINIMIZE
# Returns: Process object if started (or already running), else $null
# NOTE: This function never blocks on minimize and will return the process if alive.
# =========================
function Start-GlmSmart {
    Write-Log "=== Start-GlmSmart BEGIN ==="

    if (-not (Test-Path -Path $glmPath -PathType Leaf)) {
        Write-Log "ERROR: GLM executable not found at '$glmPath'."
        Write-Log "=== Start-GlmSmart END (failure: missing EXE) ==="
        return $null
    }

    # Start or reuse process
    $process = Get-Process -Name $processName -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $process) {
        try {
            Write-Log "Starting $processName from '$glmPath' ..."
            $process = Start-Process -FilePath $glmPath -PassThru
            Write-Log "$processName started. PID=$($process.Id)"
        }
        catch {
            Write-Log "ERROR: Failed to start $processName: $($_.Exception.Message)"
            Write-Log "=== Start-GlmSmart END (failure: Start-Process) ==="
            return $null
        }
    }
    else {
        Write-Log "Existing $processName detected. PID=$($process.Id). Reusing."
    }

    # Short post-start delay + priority bump
    Write-Log "Sleeping ${postStartSleepSeconds}s after start before priority/minimize."
    Start-Sleep -Seconds $postStartSleepSeconds

    try {
        $process.Refresh()
        if (-not $process.HasExited) {
            $process.PriorityClass = [System.Diagnostics.ProcessPriorityClass]::AboveNormal
            Write-Log "Priority set to AboveNormal for PID $($process.Id)"
        }
    }
    catch {
        Write-Log "WARNING: Failed to set priority: $($_.Exception.Message)"
    }

    # Best-effort minimize/stabilization (non-blocking minimize)
    $deadline      = (Get-Date).AddSeconds($enforceMaxSeconds)
    $lastHandle    = [IntPtr]::Zero
    $stableCount   = 0
    $minimizeCount = 0

    Write-Log "Entering enforce-minimized loop: poll every ${enforcePollSeconds}s, for up to ${enforceMaxSeconds}s."

    while ((Get-Date) -lt $deadline) {
        try {
            $process.Refresh()
            if ($process.HasExited) {
                Write-Log "$processName exited during enforce-minimized loop. Returning null."
                Write-Log "=== Start-GlmSmart END (failure: exited early) ==="
                return $null
            }

            $hwnd = $process.MainWindowHandle

            # Logging (avoid "$var:" parsing issues by using -f formatting)
            if ($hwnd -eq [IntPtr]::Zero) {
                Write-Log ("Current main window: PID={0} SessionId={1} Handle=0" -f $process.Id, $process.SessionId)
                # Reset if we lose handle
                $lastHandle    = [IntPtr]::Zero
                $stableCount   = 0
                $minimizeCount = 0
                Start-Sleep -Seconds $enforcePollSeconds
                continue
            }
            else {
                Write-Log ("Current main window: PID={0} SessionId={1} Handle={2}" -f $process.Id, $process.SessionId, $hwnd)
            }

            # Handle stability tracking
            if ($hwnd -eq $lastHandle) {
                $stableCount++
            }
            else {
                $lastHandle    = $hwnd
                $stableCount   = 1
                $minimizeCount = 0
                Write-Log ("New window handle detected. Resetting counters. Handle={0}" -f $hwnd)
            }

            Write-Log ("DEBUG: StableCount={0} MinimizeCount={1} Handle={2}" -f $stableCount, $minimizeCount, $hwnd)

            # Stop condition BEFORE extra minimize (so we don't do a redundant extra call)
            if ($stableCount -ge $stableNeeded -and $minimizeCount -ge $minimizeNeeded) {
                Write-Log ("Handle {0} considered stable (StableCount={1}, MinimizeCount={2}). Stopping enforce-minimized loop." -f $hwnd, $stableCount, $minimizeCount)
                break
            }

            # Non-blocking minimize attempt
            $ok = Invoke-NonBlockingMinimize -hWnd $hwnd
            $minimizeCount++

            Write-Log ("Minimize posted (non-blocking). ok={0} Handle={1} MinimizeCount={2}" -f $ok, $hwnd, $minimizeCount)
        }
        catch {
            Write-Log "WARNING in enforce-minimized loop: $($_.Exception.Message)"
        }

        Start-Sleep -Seconds $enforcePollSeconds
    }

    Write-Log "Leaving enforce-minimized loop."

    # IMPORTANT: Always return the process if it's alive, even if minimization didn't fully stabilize.
    try {
        $process.Refresh()
        if (-not $process.HasExited) {
            Write-Log ("=== Start-GlmSmart END (success-ish: returning live process PID={0}) ===" -f $process.Id)
            return $process
        }
    }
    catch { }

    Write-Log "=== Start-GlmSmart END (failure: process not alive at return point) ==="
    return $null
}

# =========================
# MAIN
# =========================
Write-Log "========== minimize-glm.ps1 START =========="
try {
    $me = Get-Process -Id $PID
    Write-Log ("Script running in SessionId = {0}" -f $me.SessionId)
} catch {
    Write-Log "WARNING: Unable to determine script SessionId."
}

# One-time CPU gating at script start
[void](Wait-ForCpuCalm)

while ($true) {
    $glm = Start-GlmSmart

    if (-not $glm) {
        # If EXE is missing, this will never recover; exit to avoid tight loops.
        if (-not (Test-Path -Path $glmPath -PathType Leaf)) {
            Write-Log "FATAL: GLM executable missing. Exiting."
            break
        }

        Write-Log "Start-GlmSmart returned null. Waiting and retrying."
        Start-Sleep -Seconds $restartDelaySeconds
        continue
    }

    Write-Log ("Entering watchdog loop for PID {0}." -f $glm.Id)
    $nonRespCount = 0

    while ($true) {
        try {
            $glm.Refresh()
        }
        catch {
            Write-Log "Watchdog: process refresh failed (likely exited). Restarting."
            break
        }

        if ($glm.HasExited) {
            Write-Log ("Watchdog: GLM PID {0} has exited. Restarting." -f $glm.Id)
            break
        }

        # Responding is the key "GUI hung" signal; watchdog is sole restart policy.
        if ($glm.Responding) {
            if ($nonRespCount -gt 0) {
                Write-Log ("Watchdog: GLM responsive again. Resetting non-responsive streak (was {0})." -f $nonRespCount)
            }
            $nonRespCount = 0
        }
        else {
            $nonRespCount++
            Write-Log ("Watchdog: GLM (PID={0}) NOT responding. Streak={1}/{2}." -f $glm.Id, $nonRespCount, $maxNonRespChecks)

            if ($nonRespCount -ge $maxNonRespChecks) {
                Write-Log ("Watchdog: GLM hung for ~{0}s. Killing and restarting." -f ($watchdogCheckInterval * $maxNonRespChecks))
                try {
                    $glm.Kill()
                    Write-Log ("Watchdog: Killed GLM PID={0}." -f $glm.Id)
                }
                catch {
                    Write-Log ("Watchdog: ERROR killing GLM: {0}" -f $_.Exception.Message)
                }

                Start-Sleep -Seconds $restartDelaySeconds
                break
            }
        }

        Start-Sleep -Seconds $watchdogCheckInterval
    }

    Write-Log ("Watchdog: restarting start/minimize sequence after {0}s." -f $restartDelaySeconds)
    Start-Sleep -Seconds $restartDelaySeconds
}

Write-Log "========== minimize-glm.ps1 END =========="
