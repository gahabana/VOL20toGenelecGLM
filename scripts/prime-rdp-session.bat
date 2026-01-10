@echo off
:: Prime RDP Session - Test Script
::
:: Run this BEFORE starting bridge2glm to prime the session.
:: This opens an RDP connection to localhost, then you manually disconnect.
::
:: Prerequisites:
:: 1. First, save credentials (run once in an interactive session):
::    cmdkey /add:localhost /user:zh /pass:YOUR_PASSWORD
::
:: 2. Then this script can auto-connect
::

echo ============================================
echo  RDP Session Priming (Test)
echo ============================================
echo.

echo Step 1: Current session state:
qwinsta
echo.

echo Step 2: Starting RDP to localhost...
echo          (This will open a new RDP window)
echo.
start mstsc /v:localhost

echo Step 3: Wait for RDP window to connect...
timeout /t 5 /nobreak >nul

echo.
echo Step 4: Now MANUALLY close/disconnect the RDP window
echo         (Click X or disconnect from Start menu)
echo.
pause

echo.
echo Step 5: Session state after priming:
qwinsta
echo.

echo ============================================
echo  Priming complete! Now start bridge2glm.py
echo ============================================
pause
