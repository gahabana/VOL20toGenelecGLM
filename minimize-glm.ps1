# ==========================================
# CONFIG
# ==========================================
# Path to GLM executable
$glmPath = "C:\Program Files (x86)\Genelec\GLMv5\GLMv5.exe"

# CPU gating (avoid starting GLM while system is slammed)
$cpuThreshold     = 5      # % CPU considered "idle enough"
$cpuCheckInterval = 5       # seconds between CPU checks
$cpuMaxChecks     = 60      # 60 * 5s = 5 minutes max wait

# Long-poll window detection / minimization
$pollSeconds = 10           # how often to poll GLM window
$maxMinutes  = 10           # total time to keep trying
$maxLoops    = [int](($maxMinutes * 60) / $pollSeconds)

# ShowWindow flag:
# 2 = SW_SHOWMINIMIZED, 6 = SW_MINIMIZE
# For our use case we want "behave like clicking Minimize"
$nCmdShow = 6

# ==========================================
# LOGGING
# ==========================================
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$logFile   = Join-Path $scriptDir "minimize-glm.log"

function Write-Log {
    param([string]$message)
    $timestamp = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
    "$timestamp`t$message" | Out-File -FilePath $logFile -Append -Encoding UTF8
}

Write-Log "========== minimize-glm.ps1 START =========="

# Log script session for debugging
try {
    $currentProc = Get-Process -Id $PID
    Write-Log "Script running in SessionId = $($currentProc.SessionId)"
} catch {
    Write-Log "WARNING: Cannot get script SessionId: $($_.Exception.Message)"
}

# ==========================================
# WIN32 API: ShowWindow
# ==========================================
Add-Type @"
using System;
using System.Runtime.InteropServices;
public class Window {
    [DllImport("user32.dll")]
    public static extern bool ShowWindow(IntPtr hWnd, int nCmdShow);
}
"@

# ==========================================
# STEP 1 – CPU-AWARE PRE-LAUNCH
# ==========================================
Write-Log "CPU pre-launch check: threshold=${cpuThreshold}%, interval=${cpuCheckInterval}s, maxChecks=${cpuMaxChecks}"

$cpuChecks = 0
while ($cpuChecks -lt $cpuMaxChecks) {
    try {
        $cpu = (Get-Counter '\Processor(_Total)\% Processor Time').CounterSamples.CookedValue
        $cpuRounded = [math]::Round($cpu, 1)

        if ($cpu -lt $cpuThreshold) {
            Write-Log "CPU ${cpuRounded}% < threshold ${cpuThreshold}%. Proceeding to start GLM."
            break
        } else {
            Write-Log "CPU ${cpuRounded}% >= threshold ${cpuThreshold}%. Waiting ${cpuCheckInterval}s..."
        }
    } catch {
        Write-Log "WARNING: Get-Counter failed: $($_.Exception.Message). Skipping CPU gating."
        break
    }

    Start-Sleep -Seconds $cpuCheckInterval
    $cpuChecks++
}

if ($cpuChecks -ge $cpuMaxChecks) {
    Write-Log "CPU never dropped below threshold in time; starting GLM anyway."
}

# ==========================================
# STEP 2 – START GLM PROCESS
# ==========================================
if (-not (Test-Path -Path $glmPath -PathType Leaf)) {
    Write-Log "ERROR: GLM executable not found at '$glmPath'. Aborting."
    Write-Log "========== minimize-glm.ps1 END (error) =========="
    exit 1
}

Write-Log "Starting GLM from '$glmPath' ..."
try {
    # WindowStyle Minimized is a hint; some apps ignore it, but it's free to try
    $process = Start-Process -FilePath $glmPath -WindowStyle Minimized -PassThru
    Write-Log "GLM started. PID = $($process.Id)"
} catch {
    Write-Log "ERROR: Failed to start GLM: $($_.Exception.Message)"
    Write-Log "========== minimize-glm.ps1 END (error) =========="
    exit 1
}

# Give GLM a little time to do initial startup
Write-Log "Sleeping after start GLM for 5 seconds prior to elevation of Priority and Minimization "
Start-Sleep -Seconds 5

# Try to bump priority slightly (optional)
try {
    $process.Refresh()
    $process.PriorityClass = [System.Diagnostics.ProcessPriorityClass]::AboveNormal
    Write-Log "Priority set to AboveNormal for PID $($process.Id)"
} catch {
    Write-Log "WARNING: Failed to set priority: $($_.Exception.Message)"
}

# ==========================================
# STEP 3 – ENFORCE MINIMIZED UNTIL HANDLE IS STABLE
# ==========================================
$pollSeconds    = 2          # how often to poll
$maxSeconds     = 60        # total time budget as safety
$deadline       = (Get-Date).AddSeconds($maxSeconds)

$lastHandle     = [IntPtr]::Zero
$stableCount    = 0          # how many times we've seen the same handle in a row
$minimizeCount  = 0          # how many times we've called ShowWindow on the current handle

Write-Log "Entering enforce-minimized loop: poll every $pollSeconds s, for up to $maxSeconds seconds."

while ((Get-Date) -lt $deadline) {
    try {
        if (-not $process -or $process.HasExited) {
            Write-Log "GLM process has exited; stopping enforce-minimized loop."
            break
        }

        $process.Refresh()

        # 1) Get the best guess for the current main window handle
        $hwnd = $process.MainWindowHandle

        # Optional: fallback search for any GLM* process with a top-level window
        if ($hwnd -eq [IntPtr]::Zero) {
            $candidate = Get-Process | Where-Object {
                $_.ProcessName -like 'GLM*' -and $_.MainWindowHandle -ne 0
            } | Select-Object -First 1

            if ($candidate) {
                $candidate.Refresh()
                $hwnd = $candidate.MainWindowHandle
                Write-Log "Fallback candidate: PID=$($candidate.Id) SessionId=$($candidate.SessionId) Handle=$hwnd"
            } else {
                Write-Log "No GLM main window yet (MainWindowHandle == 0)."
            }
        } else {
            Write-Log "Current main window from primary process: PID=$($process.Id) SessionId=$($process.SessionId) Handle=$hwnd"
        }

        # 2) If we still don't have a handle, just wait and try again
        if ($hwnd -eq [IntPtr]::Zero) {
            $lastHandle    = [IntPtr]::Zero
            $stableCount   = 0
            $minimizeCount = 0
            Start-Sleep -Seconds $pollSeconds
            continue
        }

        # 3) Track stability of the handle
        if ($hwnd -eq $lastHandle) {
            $stableCount++
        } else {
            $lastHandle    = $hwnd
            $stableCount   = 1
            $minimizeCount = 0
            Write-Log "New window handle detected. Resetting counters. Handle=$hwnd"
        }

        # Write-Log ("DEBUG: StableCount={0} MinimizeCount={1} Handle={2}" -f $stableCount, $minimizeCount, $hwnd)

        # 4) Send MINIMIZE to this handle
        [Window]::ShowWindow($hwnd, $nCmdShow) | Out-Null
        $minimizeCount++
        Write-Log "Enforced minimized on handle $hwnd at $(Get-Date -Format 'HH:mm:ss'). MinimizeCount=$minimizeCount"

        # 5) If we've seen the same handle at least 3 times and minimized it at least twice, we're done
        if ($stableCount -ge 3 -and $minimizeCount -ge 2) {
            Write-Log "Handle $hwnd considered stable (StableCount=$stableCount, MinimizeCount=$minimizeCount). Stopping loop."
            break
        }
    }
    catch {
        Write-Log "WARNING in enforce-minimized loop: $($_.Exception.Message)"
    }

    Start-Sleep -Seconds $pollSeconds
}

Write-Log "Leaving enforce-minimized loop."
Write-Log "========== minimize-glm.ps1 END =========="

exit 1
