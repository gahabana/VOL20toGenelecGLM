@echo off
:: Disable WDDM Graphics Driver for RDP Sessions
:: This forces RDP to use the legacy XDDM driver instead of WDDM
:: Fixes high CPU issue after RDP disconnect + tscon on headless VMs
::
:: Requires Administrator privileges
:: Reboot required after running

echo ============================================
echo  Disable WDDM for RDP Sessions
echo ============================================
echo.

:: Check for admin privileges
net session >nul 2>&1
if %errorlevel% neq 0 (
    echo ERROR: This script requires Administrator privileges.
    echo Right-click and select "Run as administrator"
    pause
    exit /b 1
)

echo Applying registry fix...
REG ADD "HKLM\SOFTWARE\Policies\Microsoft\Windows NT\Terminal Services" /v "fEnableWddmDriver" /t REG_DWORD /d 0 /f

if %errorlevel% equ 0 (
    echo.
    echo SUCCESS: WDDM disabled for RDP sessions.
    echo.
    echo IMPORTANT: You must REBOOT for the change to take effect.
    echo.
    echo After reboot, RDP will use the legacy XDDM driver.
    echo This should fix the high CPU issue after RDP disconnect.
    echo.
    echo To revert this change, run: enable-wddm-rdp.bat
) else (
    echo.
    echo ERROR: Failed to apply registry fix.
)

echo.
pause
