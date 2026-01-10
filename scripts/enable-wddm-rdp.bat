@echo off
:: Re-enable WDDM Graphics Driver for RDP Sessions
:: This reverts to the default WDDM driver for RDP
::
:: Requires Administrator privileges
:: Reboot required after running

echo ============================================
echo  Re-enable WDDM for RDP Sessions
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

echo Removing registry fix...
REG DELETE "HKLM\SOFTWARE\Policies\Microsoft\Windows NT\Terminal Services" /v fEnableWddmDriver /f >nul 2>&1

if %errorlevel% equ 0 (
    echo.
    echo SUCCESS: WDDM re-enabled for RDP sessions.
    echo.
    echo IMPORTANT: You must REBOOT for the change to take effect.
    echo.
    echo After reboot, RDP will use the default WDDM driver.
) else (
    echo.
    echo NOTE: Registry key not found - WDDM is already enabled.
)

echo.
pause
