@echo off
setlocal enabledelayedexpansion
chcp 65001 >nul
title SNI Spoofer - Build

REM ============================================================
REM  SNI Spoofer — build script
REM  Produces:
REM    - gui\build\bin\sni-gui.exe        (dev test)
REM    - gui\build\bin\sni-spoof.exe      (standalone core)
REM    - dist\SNI-Spoofer-Portable-*.zip  (portable)
REM    - dist\SNI-Spoofer-Setup-*.exe     (installer)
REM ============================================================

REM ---------- Version ----------
set "APP_VERSION=2.0.1"

REM ---------- Resolve root ----------
set "ROOT=%~dp0"
if "%ROOT:~-1%"=="\" set "ROOT=%ROOT:~0,-1%"

set "ENGINE=%ROOT%\engine-ffi"
set "GUI=%ROOT%\gui"
set "FRONTEND=%ROOT%\gui\frontend"
set "BUILDBIN=%ROOT%\gui\build\bin"
set "RUNTIME=%ROOT%\runtime"
set "DIST=%ROOT%\dist"
set "PORTABLE_STAGE=%ROOT%\dist\portable"
set "DLL_SRC=%ROOT%\target\release\sni_engine.dll"
set "DLL_DST=%ROOT%\gui\build\bin\sni_engine.dll"
set "SPOOF_SRC=%ROOT%\target\release\sni-spoof.exe"
set "SPOOF_DST=%ROOT%\gui\build\bin\sni-spoof.exe"
set "EXE=%ROOT%\gui\build\bin\sni-gui.exe"

REM ---------- PATH ----------
set "PATH=%USERPROFILE%\.cargo\bin;D:\mingw64\bin;%USERPROFILE%\go\bin;%PATH%"
if exist "C:\mingw64\bin"          set "PATH=C:\mingw64\bin;%PATH%"
if exist "D:\msys64\mingw64\bin"   set "PATH=D:\msys64\mingw64\bin;%PATH%"
if exist "C:\Program Files\Go\bin" set "PATH=C:\Program Files\Go\bin;%PATH%"
if exist "C:\Program Files (x86)\NSIS" set "PATH=C:\Program Files (x86)\NSIS;%PATH%"

echo.
echo ============================================================
echo   SNI Spoofer - BUILD v%APP_VERSION%
echo   Root: %ROOT%
echo ============================================================
echo.

REM ---------- Toolchain check ----------
echo [setup] checking toolchain...
where cargo  >nul 2>nul || (echo   [ERR] cargo not found & goto :fail)
echo   [ok] cargo
where dlltool >nul 2>nul || (echo   [ERR] dlltool not found & goto :fail)
echo   [ok] dlltool
where go     >nul 2>nul || (echo   [ERR] go not found & goto :fail)
echo   [ok] go
where node   >nul 2>nul || (echo   [ERR] node not found & goto :fail)
echo   [ok] node
where wails3 >nul 2>nul || (echo   [ERR] wails3 not found & goto :fail)
echo   [ok] wails3

set "NSIS_AVAILABLE="
where makensis >nul 2>nul && set "NSIS_AVAILABLE=1"
if defined NSIS_AVAILABLE (
    echo   [ok] makensis
) else (
    echo   [skip] makensis not found - setup.exe will not be built
    echo          Install NSIS: https://nsis.sourceforge.io/Download
)

REM ============================================================
REM  STEP 1 — Rust engine dll + standalone core exe
REM ============================================================
echo.
echo [1/8] Rust engine + core exe...
cd /d "%ROOT%"
cargo build --release -p sni-engine -p sni-spoof
if errorlevel 1 (echo   [ERR] cargo build failed & goto :fail)
if not exist "%DLL_SRC%" (echo   [ERR] sni_engine.dll not produced & goto :fail)
echo   [ok] sni_engine.dll
if not exist "%SPOOF_SRC%" (echo   [ERR] sni-spoof.exe not produced & goto :fail)
echo   [ok] sni-spoof.exe

REM ============================================================
REM  STEP 2 — Wails bindings
REM ============================================================
echo.
echo [2/8] Wails bindings...
cd /d "%GUI%"
wails3 generate bindings -ts >nul 2>nul
if errorlevel 1 (
    echo   [warn] bindings generation failed - using existing
) else (
    echo   [ok] bindings regenerated
)

REM ============================================================
REM  STEP 3 — Icon
REM ============================================================
echo.
echo [3/8] Icon embed...
cd /d "%GUI%"

if exist "rsrc.syso" del "rsrc.syso" >nul 2>nul

if exist "app.ico" (
    go run github.com/akavel/rsrc@latest -ico app.ico -o rsrc.syso >nul 2>nul
    if errorlevel 1 (
        echo   [warn] rsrc failed - default icon
        if exist "rsrc.syso" del "rsrc.syso" >nul 2>nul
    ) else (
        for %%A in ("rsrc.syso") do set "RSRC_SIZE=%%~zA"
        echo   [ok] rsrc.syso !RSRC_SIZE! bytes
    )
) else (
    echo   [skip] app.ico not found
)

REM ============================================================
REM  STEP 4 — Frontend
REM ============================================================
echo.
echo [4/8] Frontend...
cd /d "%FRONTEND%"

if not exist "node_modules" (
    echo   npm install ^(first run^)...
    call npm install >nul 2>nul
    if errorlevel 1 (echo   [ERR] npm install failed & goto :fail)
)

call npm run build >nul 2>nul
if errorlevel 1 (echo   [ERR] npm run build failed & goto :fail)
if not exist "dist\index.html" (echo   [ERR] dist not built & goto :fail)
echo   [ok] frontend built

REM ============================================================
REM  STEP 5 — Go binary
REM ============================================================
echo.
echo [5/8] Go binary...
cd /d "%GUI%"
if not exist "build\bin" mkdir "build\bin" >nul 2>nul
if exist "%EXE%" del "%EXE%" >nul 2>nul

go build -ldflags="-H windowsgui -s -w" -o "%EXE%" .
if errorlevel 1 (echo   [ERR] go build failed & goto :fail)
if not exist "%EXE%" (echo   [ERR] sni-gui.exe not produced & goto :fail)
echo   [ok] sni-gui.exe

REM ============================================================
REM  STEP 6 — Copy runtime files (from runtime/ folder)
REM ============================================================
echo.
echo [6/8] Copy runtime files from runtime\ ...

copy /Y "%DLL_SRC%" "%DLL_DST%" >nul 2>nul
if errorlevel 1 (echo   [ERR] copy sni_engine.dll failed & goto :fail)
echo   [ok] sni_engine.dll

copy /Y "%SPOOF_SRC%" "%SPOOF_DST%" >nul 2>nul
if errorlevel 1 (echo   [ERR] copy sni-spoof.exe failed & goto :fail)
echo   [ok] sni-spoof.exe

if exist "%FRONTEND%\src\assets\app.png" (
    copy /Y "%FRONTEND%\src\assets\app.png" "%BUILDBIN%\app.png" >nul 2>nul
    echo   [ok] app.png
)

REM Copy from runtime/ folder (if exists)
if exist "%RUNTIME%" (
    if exist "%RUNTIME%\WinDivert.dll" (
        copy /Y "%RUNTIME%\WinDivert.dll" "%BUILDBIN%\" >nul 2>nul
        echo   [ok] WinDivert.dll
    )
    if exist "%RUNTIME%\WinDivert64.sys" (
        copy /Y "%RUNTIME%\WinDivert64.sys" "%BUILDBIN%\" >nul 2>nul
        echo   [ok] WinDivert64.sys
    )
) else (
    echo   [warn] runtime\ folder not found
    echo          Create it and place WinDivert.dll + WinDivert64.sys inside
)

REM ---- Final check ----
if not exist "%BUILDBIN%\WinDivert.dll" (
    echo.
    echo   [WARN] WinDivert.dll still missing in build\bin\
    echo          Download: https://github.com/basil00/Divert/releases
    echo          Place it in: %RUNTIME%\
    echo.
    set "WINDIVERT_MISSING=1"
) else (
    set "WINDIVERT_MISSING="
)

REM Refresh icon cache
cd /d "%BUILDBIN%"
if exist "sni-gui.exe" (
    ren "sni-gui.exe" "sni-gui.tmp.exe" >nul 2>nul
    ren "sni-gui.tmp.exe" "sni-gui.exe" >nul 2>nul
)

REM ============================================================
REM  STEP 7 — Portable ZIP
REM ============================================================
echo.
echo [7/8] Portable ZIP...

if not exist "%DIST%" mkdir "%DIST%" >nul 2>nul
if exist "%PORTABLE_STAGE%" rmdir /s /q "%PORTABLE_STAGE%" >nul 2>nul
mkdir "%PORTABLE_STAGE%" >nul 2>nul

REM Copy runtime files into stage
copy /Y "%BUILDBIN%\sni-gui.exe"     "%PORTABLE_STAGE%\" >nul 2>nul
copy /Y "%BUILDBIN%\sni_engine.dll"  "%PORTABLE_STAGE%\" >nul 2>nul
copy /Y "%BUILDBIN%\sni-spoof.exe"   "%PORTABLE_STAGE%\" >nul 2>nul

if exist "%BUILDBIN%\WinDivert.dll"    copy /Y "%BUILDBIN%\WinDivert.dll"    "%PORTABLE_STAGE%\" >nul 2>nul
if exist "%BUILDBIN%\WinDivert64.sys"  copy /Y "%BUILDBIN%\WinDivert64.sys"  "%PORTABLE_STAGE%\" >nul 2>nul
if exist "%BUILDBIN%\app.png"          copy /Y "%BUILDBIN%\app.png"          "%PORTABLE_STAGE%\" >nul 2>nul

if exist "%RUNTIME%\ip_list.txt"  copy /Y "%RUNTIME%\ip_list.txt"  "%PORTABLE_STAGE%\" >nul 2>nul
if exist "%RUNTIME%\sni_list.txt" copy /Y "%RUNTIME%\sni_list.txt" "%PORTABLE_STAGE%\" >nul 2>nul

REM README
(
    echo SNI Spoofer - Portable v%APP_VERSION%
    echo ============================================
    echo.
    echo HOW TO RUN:
    echo   1. Right-click "sni-gui.exe"
    echo   2. Select "Run as administrator"
    echo   3. WinDivert driver requires Administrator.
    echo.
    echo REQUIREMENTS:
    echo   - Windows 10 or newer ^(x64^)
    echo   - WebView2 Runtime ^(usually preinstalled^)
    echo     Download: https://developer.microsoft.com/microsoft-edge/webview2/
    echo.
    echo CONFIG:
    echo   config.json is created on first launch.
    echo   ip_list.txt / sni_list.txt seed the default lists.
    echo.
    echo ANTIVIRUS:
    echo   WinDivert may trigger a false positive. Add this folder
    echo   to your AV exclusion list if needed.
    echo.
    echo UNINSTALL:
    echo   Just delete this folder. No registry changes.
    echo.
    echo LICENSE: GPL-3.0
    echo SOURCE:  https://github.com/xenonama/SNI-Spoofing-rust
) > "%PORTABLE_STAGE%\README.txt"

set "ZIP_NAME=SNI-Spoofer-Portable-v%APP_VERSION%.zip"
if exist "%DIST%\%ZIP_NAME%" del "%DIST%\%ZIP_NAME%" >nul 2>nul

powershell -NoProfile -Command ^
    "Compress-Archive -Path '%PORTABLE_STAGE%\*' -DestinationPath '%DIST%\%ZIP_NAME%' -Force" ^
    >nul 2>nul

if exist "%DIST%\%ZIP_NAME%" (
    for %%A in ("%DIST%\%ZIP_NAME%") do set "ZIP_SIZE=%%~zA"
    echo   [ok] %ZIP_NAME% ^(!ZIP_SIZE! bytes^)
) else (
    echo   [warn] zip creation failed
)

REM ============================================================
REM  STEP 8 — Installer (setup.exe)
REM ============================================================
echo.
echo [8/8] Installer...
if not defined NSIS_AVAILABLE (
    echo   [skip] makensis not in PATH - skipping setup.exe
    goto :done
)

cd /d "%ROOT%"
if not exist "installer.nsi" (
    echo   [warn] installer.nsi not found - skipping setup.exe
    goto :done
)

makensis /DAPP_VERSION=%APP_VERSION% /DPORTABLE_STAGE=%PORTABLE_STAGE% /DDIST=%DIST% installer.nsi >nul 2>nul
if errorlevel 1 (
    echo   [warn] makensis failed - see installer.nsi manually
) else (
    set "SETUP_NAME=SNI-Spoofer-Setup-v%APP_VERSION%.exe"
    if exist "%DIST%\%SETUP_NAME%" (
        for %%A in ("%DIST%\%SETUP_NAME%") do set "SETUP_SIZE=%%~zA"
        echo   [ok] %SETUP_NAME% ^(!SETUP_SIZE! bytes^)
    ) else (
        echo   [warn] setup.exe not produced
    )
)

:done
REM ============================================================
REM  FINISHED
REM ============================================================
echo.
echo ---------- build\bin ----------
dir /b "%BUILDBIN%"
echo.
echo ---------- dist ----------
dir /b "%DIST%"
echo.

echo ============================================================
echo   BUILD OK
echo.
if defined WINDIVERT_MISSING (
    echo   [WARN] WinDivert files were missing.
    echo          Setup.exe and Portable ZIP will NOT work without them.
    echo          Download: https://github.com/basil00/Divert/releases
    echo          Place both in: %RUNTIME%\
    echo.
)
echo   Dev run:    %EXE%
echo   Portable:   %DIST%\%ZIP_NAME%
echo   Installer:  %DIST%\SNI-Spoofer-Setup-v%APP_VERSION%.exe
echo ============================================================
echo.

choice /C YN /M "Open dist folder now"
if errorlevel 2 goto :end
explorer "%DIST%"

:end
echo.
pause
exit /b 0

:fail
echo.
echo ============================================================
echo   BUILD FAILED
echo ============================================================
echo.
pause
exit /b 1