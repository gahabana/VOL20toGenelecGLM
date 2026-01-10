# Prime RDP Session Script
#
# This script "primes" the Windows session by doing an RDP connect/disconnect cycle.
# This fixes the high CPU issue that occurs after the first RDP disconnect on headless VMs.
#
# Prerequisites:
# 1. Save RDP credentials first (run once manually):
#    cmdkey /add:localhost /user:YOUR_USERNAME /pass:YOUR_PASSWORD
#
# 2. Or create an .rdp file with saved credentials
#
# Usage:
#    .\prime-rdp-session.ps1
#

param(
    [int]$WaitSeconds = 5,
    [switch]$Force
)

$ErrorActionPreference = "Stop"

Write-Host "=== RDP Session Priming Script ===" -ForegroundColor Cyan

# Check if we're already in a "primed" state by looking at session history
# For now, we'll just do the priming unconditionally if -Force is specified

function Get-CurrentSessionId {
    $processId = [System.Diagnostics.Process]::GetCurrentProcess().Id
    $session = (Get-Process -Id $processId).SessionId
    return $session
}

function Test-SessionDisconnected {
    param([int]$SessionId)

    $qwinsta = qwinsta 2>$null | Where-Object { $_ -match "^\s*\S+\s+\S+\s+$SessionId\s+" }
    if ($qwinsta -match "Disc") {
        return $true
    }
    return $false
}

function Start-RdpConnection {
    Write-Host "Starting RDP connection to localhost..." -ForegroundColor Yellow

    # Start mstsc in the background
    $process = Start-Process -FilePath "mstsc" -ArgumentList "/v:localhost" -PassThru

    return $process
}

function Stop-RdpSession {
    param([int]$SessionId)

    Write-Host "Disconnecting RDP session $SessionId..." -ForegroundColor Yellow

    # Use tscon to disconnect (sends session back to console)
    # Or we can just kill mstsc
    $mstscProcesses = Get-Process -Name "mstsc" -ErrorAction SilentlyContinue
    if ($mstscProcesses) {
        $mstscProcesses | Stop-Process -Force
        Write-Host "Killed mstsc process(es)" -ForegroundColor Green
    }
}

# Main logic
try {
    $sessionId = Get-CurrentSessionId
    Write-Host "Current session ID: $sessionId"

    # Check current session state
    Write-Host ""
    Write-Host "Current session state:" -ForegroundColor Cyan
    qwinsta | Where-Object { $_ -match $sessionId -or $_ -match "console" -or $_ -match "rdp" }
    Write-Host ""

    # Start RDP connection
    $rdpProcess = Start-RdpConnection

    Write-Host "Waiting $WaitSeconds seconds for RDP to establish..." -ForegroundColor Yellow
    Start-Sleep -Seconds $WaitSeconds

    # Check if mstsc is still running (connection successful)
    $mstscRunning = Get-Process -Name "mstsc" -ErrorAction SilentlyContinue
    if ($mstscRunning) {
        Write-Host "RDP connection appears successful" -ForegroundColor Green

        # Now disconnect
        Stop-RdpSession -SessionId $sessionId

        Write-Host "Waiting 2 seconds for disconnect to complete..." -ForegroundColor Yellow
        Start-Sleep -Seconds 2

        Write-Host ""
        Write-Host "Session state after priming:" -ForegroundColor Cyan
        qwinsta | Where-Object { $_ -match $sessionId -or $_ -match "console" -or $_ -match "rdp" }

        Write-Host ""
        Write-Host "=== Priming complete ===" -ForegroundColor Green
        Write-Host "The session should now be primed. Subsequent RDP disconnect cycles should not cause high CPU."
    }
    else {
        Write-Host "ERROR: mstsc did not stay running. RDP connection may have failed." -ForegroundColor Red
        Write-Host "Make sure credentials are saved: cmdkey /add:localhost /user:USERNAME /pass:PASSWORD" -ForegroundColor Yellow
        exit 1
    }
}
catch {
    Write-Host "ERROR: $($_.Exception.Message)" -ForegroundColor Red
    exit 1
}
