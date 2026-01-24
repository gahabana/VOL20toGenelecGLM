@echo off
REM Move Mesa OpenGL files to Mesa subdirectory
REM Run this script from the GLM directory

echo Moving Mesa OpenGL files to Mesa subdirectory...

if not exist "Mesa" (
    echo ERROR: Mesa subdirectory does not exist. Please create it first.
    pause
    exit /b 1
)

REM Move Mesa DLLs and related files (dated 10/01/2026)
move "clon12compiler.dll" "Mesa\" 2>nul
move "d3d10warp.dll" "Mesa\" 2>nul
move "dxil.dll" "Mesa\" 2>nul
move "dzn_icd.x86_64.json" "Mesa\" 2>nul
move "libEGL.dll" "Mesa\" 2>nul
move "libgallium_wgl.dll" "Mesa\" 2>nul
move "libGLESv1_CM.dll" "Mesa\" 2>nul
move "libGLESv2.dll" "Mesa\" 2>nul
move "lvp_icd.x86_64.json" "Mesa\" 2>nul
move "msav1enchmft.dll" "Mesa\" 2>nul
move "msh264enchmft.dll" "Mesa\" 2>nul
move "msh265enchmft.dll" "Mesa\" 2>nul
move "openclon12.dll" "Mesa\" 2>nul
move "opengl32.dll" "Mesa\" 2>nul
move "spirv2dxil.exe" "Mesa\" 2>nul
move "spirv_to_dxil.dll" "Mesa\" 2>nul
move "va.dll" "Mesa\" 2>nul
move "vaon12_drv_video.dll" "Mesa\" 2>nul
move "va_win32.dll" "Mesa\" 2>nul
move "VkLayer_MESA_anti_lag.dll" "Mesa\" 2>nul
move "VkLayer_MESA_anti_lag.json" "Mesa\" 2>nul
move "vulkan_dzn.dll" "Mesa\" 2>nul
move "vulkan_lvp.dll" "Mesa\" 2>nul

echo Done! Mesa files moved to Mesa subdirectory.
echo.
echo To restore Mesa files later, run: move Mesa\*.* .
pause
