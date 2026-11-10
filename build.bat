@echo off
setlocal enabledelayedexpansion
chcp 65001 >nul
title NekoGo - Build

REM ============================================================
REM  NekoGo - build script (Wails3 + Go, no Qt/CMake/MSVC)
REM  Produces:
REM    - gui\build\bin\nekogo.exe            (dev test)
REM    - gui\build\bin\nekobox_core.exe      (sing-box engine)
REM    - gui\build\bin\sing-box.exe          (official CLI, optional)
REM    - gui\build\bin\mihomo.exe            (Clash Meta core, optional)
REM    - gui\build\bin\xray.exe              (Xray-core)
REM    - dist\NekoGo-Portable-*.zip          (portable)
REM    - dist\NekoGo-Setup-*.exe             (installer, needs NSIS)
REM  A missing xray.exe / sing-box.exe / mihomo.exe / aether.exe is downloaded
REM  from its official GitHub release (SHA256-checked when the release
REM  publishes one) instead of failing / being skipped.
REM ============================================================

REM ---------- Resolve root ----------
REM Before the version read: VERSION lives next to this script, and a
REM cwd-relative "if exist VERSION" silently falls back to the hardcoded
REM tag when build.bat is invoked from anywhere else (wrong artifact names).
set "ROOT=%~dp0"
if "%ROOT:~-1%"=="\" set "ROOT=%ROOT:~0,-1%"

REM ---------- Version ----------
if exist "%ROOT%\VERSION" (
    set /p APP_VERSION=<"%ROOT%\VERSION"
) else (
    set "APP_VERSION=5.11.28.3-fork.1"
)
REM An empty/trailing-newline-only VERSION leaves APP_VERSION undefined.
if not defined APP_VERSION set "APP_VERSION=5.11.28.3-fork.1"
REM NSIS VIProductVersion needs 4 plain numbers (1.0.0.0), not a tag.
set "APP_VERSION_VI=1.0.0.0"
for /f "tokens=1,2,3 delims=.-" %%a in ("%APP_VERSION%") do set "APP_VERSION_VI=%%a.%%b.%%c.0"
echo %APP_VERSION_VI%| findstr /r /c:"^[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*$" >nul 2>nul || set "APP_VERSION_VI=1.0.0.0"

set "CORE=%ROOT%\core\server"
set "GUI=%ROOT%\gui"
set "FRONTEND=%ROOT%\gui\frontend"
set "BUILDBIN=%ROOT%\gui\build\bin"
set "RUNTIME=%ROOT%\runtime"
set "DIST=%ROOT%\dist"
REM Portable staging: dist\portable normally, dist\portable-staging when that
REM folder is stuck (see STEP 7). PORTABLE_STAGE is what STEP 7/8 read.
set "PORTABLE_DIR=%ROOT%\dist\portable"
set "PORTABLE_FALLBACK=%ROOT%\dist\portable-staging"
set "PORTABLE_STAGE=%PORTABLE_DIR%"
set "SETUP_BUILT="
set "EXE=%ROOT%\gui\build\bin\nekogo.exe"
set "CORE_EXE=%ROOT%\gui\build\bin\nekobox_core.exe"
set "XRAY_EXE=%ROOT%\gui\build\bin\xray.exe"
set "XRAY_SRC=%ROOT%\xray"

REM ---------- PATH ----------
set "PATH=%USERPROFILE%\.cargo\bin;D:\mingw64\bin;%USERPROFILE%\go\bin;%PATH%"
if exist "C:\mingw64\bin"          set "PATH=C:\mingw64\bin;%PATH%"
if exist "D:\msys64\mingw64\bin"   set "PATH=D:\msys64\mingw64\bin;%PATH%"
if exist "C:\Program Files\Go\bin" set "PATH=C:\Program Files\Go\bin;%PATH%"
if exist "C:\Program Files (x86)\NSIS" set "PATH=C:\Program Files (x86)\NSIS;%PATH%"
if exist "C:\Program Files\NSIS"       set "PATH=C:\Program Files\NSIS;%PATH%"
if exist "%LOCALAPPDATA%\Programs\NSIS" set "PATH=%LOCALAPPDATA%\Programs\NSIS;%PATH%"

echo.
echo ============================================================
echo   NekoGo - BUILD v%APP_VERSION%
echo   Root: %ROOT%
echo ============================================================
echo.

REM ---------- optional: --dry-run ----------
REM Prints the release assets the build WOULD download when a binary
REM is missing, then exits without touching anything.
if /i "%~1"=="--dry-run" (
    echo [dry-run] latest engine release assets this build would fetch:
    call :show_release XTLS/Xray-core "Xray-windows-64.zip"
    call :show_release SagerNet/sing-box "^sing-box-[0-9.]+-windows-amd64\.zip$"
    call :show_release MetaCubeX/mihomo "^mihomo-windows-amd64-v1-v[0-9.]+\.zip$"
    call :show_release CluvexStudio/Aether "aether-windows-x86_64.zip"
    echo.
    exit /b 0
)

REM ---------- Toolchain check ----------
echo [setup] checking toolchain...
where go     >nul 2>nul || (echo   [ERR] go not found & goto :fail)
echo   [ok] go
where node   >nul 2>nul || (echo   [ERR] node not found & goto :fail)
echo   [ok] node
where wails3 >nul 2>nul || (echo   [ERR] wails3 not found ^(go install github.com/wailsapp/wails/v3/cmd/wails3@latest^) & goto :fail)
echo   [ok] wails3
where gcc    >nul 2>nul || (echo   [warn] gcc not found - cgo builds may fail)

set "NSIS_AVAILABLE="
where makensis >nul 2>nul && set "NSIS_AVAILABLE=1"
if defined NSIS_AVAILABLE (
    echo   [ok] makensis
) else (
    echo   [skip] makensis not found - setup.exe will not be built
    echo          Install NSIS: https://nsis.sourceforge.io/Download
)

REM ============================================================
REM  STEP 1 - NekoBox core (sing-box engine, Go, no CMake)
REM ============================================================
echo.
echo [1/9] NekoBox core...
cd /d "%CORE%"
REM Stamp the sing-box version into the binary: without the -X below,
REM `nekobox_core sing-box version` prints "sing-box version unknown"
REM (constant.Version default) and the in-app update manager cannot
REM tell the core version. Stamped with the NekoGo build version so
REM the updater can compare it against release tags.
go build -trimpath -ldflags="-s -w -checklinkname=0 -X internal/godebug.defaultGODEBUG=multipathtcp=0 -X github.com/sagernet/sing-box/constant.Version=%APP_VERSION%" -tags "with_clash_api,with_gvisor,with_quic,with_wireguard,with_utls,with_dhcp,with_shadowtls,with_grpc,with_acme,with_awg,with_tailscale,with_naive,with_naive_outbound,with_purego" -o "%CORE_EXE%" .
if errorlevel 1 (echo   [ERR] nekobox_core build failed & goto :fail)
if not exist "%CORE_EXE%" (echo   [ERR] nekobox_core.exe not produced & goto :fail)
echo   [ok] nekobox_core.exe

REM ============================================================
REM  STEP 2 - Xray core (local source tree, else official release)
REM ============================================================
echo.
echo [2/9] Xray core...
if not exist "%XRAY_EXE%" (
    if exist "%XRAY_SRC%\go.mod" (
        echo   building Xray-core from the local source tree...
        cd /d "%XRAY_SRC%\main"
        go build -trimpath -ldflags="-s -w" -o "%XRAY_EXE%" .
        if errorlevel 1 (echo   [ERR] xray build failed & goto :fail)
    ) else (
        if not exist "%BUILDBIN%" mkdir "%BUILDBIN%" >nul 2>nul
        call :fetch_github_release XTLS/Xray-core "Xray-windows-64.zip" "%BUILDBIN%" "xray.exe"
        if errorlevel 1 (
            echo   [ERR] cannot obtain xray.exe: the XTLS/Xray-core release download failed.
            echo         Tried: !FETCH_URL!
            echo         Fix the network, or clone the source: git clone --depth 1 https://github.com/XTLS/Xray-core.git "%XRAY_SRC%"
            goto :fail
        )
    )
)
if not exist "%XRAY_EXE%" (echo   [ERR] xray.exe not produced & goto :fail)
echo   [ok] xray.exe

REM ============================================================
REM  STEP 2b - SNI core (Rust sni-spoof engine, out-of-process)
REM ============================================================
echo.
echo [2b/9] SNI core...
where cargo >nul 2>nul || (echo   [ERR] cargo not found - install Rust: https://rustup.rs/ & goto :fail)
if not exist "%BUILDBIN%" mkdir "%BUILDBIN%" >nul 2>nul
cd /d "%ROOT%\sni spoofing"
cargo build --release -p sni-spoof
if errorlevel 1 (echo   [ERR] sni-spoof build failed & goto :fail)
if not exist "target\release\sni-spoof.exe" (echo   [ERR] sni-spoof.exe not produced & goto :fail)
copy /Y "target\release\sni-spoof.exe" "%BUILDBIN%\sni-spoof.exe" >nul 2>nul
if errorlevel 1 (echo   [ERR] sni-spoof.exe copy failed & goto :fail)
if not exist "%BUILDBIN%\sni-spoof.exe" (echo   [ERR] sni-spoof.exe not staged & goto :fail)
echo   [ok] sni-spoof.exe

REM ============================================================
REM  STEP 2c - Aether core (optional binary + pt/ folder)
REM ============================================================
echo.
echo [2c/9] Aether core...
set "AETHER_SRC=%ROOT%\aether-reference\bin"
set "AETHER_EXE=%BUILDBIN%\aether.exe"
REM Aether is optional (the installer ships it /nonfatal, the portable zip
REM omits it when absent), so a failed download warns instead of stopping
REM the build - but the exact URL tried is printed for a manual fetch.
if not exist "%AETHER_SRC%\aether.exe" (
    call :fetch_github_release CluvexStudio/Aether "aether-windows-x86_64.zip" "%AETHER_SRC%" "aether.exe"
    if errorlevel 1 (
        echo   [warn] Aether download failed - Aether profiles will be unavailable.
        echo          Tried: !FETCH_URL!
        echo          Place aether.exe ^(+ pt\^) in "%AETHER_SRC%" and rebuild.
    )
)
if exist "%AETHER_SRC%\aether.exe" (
    if not exist "%BUILDBIN%" mkdir "%BUILDBIN%" >nul 2>nul
    copy /Y "%AETHER_SRC%\aether.exe" "%AETHER_EXE%" >nul
    if errorlevel 1 (echo   [ERR] aether.exe copy failed & goto :fail)
    if not exist "%AETHER_EXE%" (echo   [ERR] aether.exe not staged & goto :fail)
    echo   [ok] aether.exe

    if exist "%AETHER_SRC%\pt\" (
        if exist "%BUILDBIN%\pt\" rmdir /s /q "%BUILDBIN%\pt" >nul 2>nul
        robocopy "%AETHER_SRC%\pt" "%BUILDBIN%\pt" /E /NFL /NDL /NJH /NJS /NC /NS >nul
        if errorlevel 8 (echo   [ERR] aether pt\ copy failed ^(robocopy !errorlevel!^) & goto :fail)
        echo   [ok] aether pt/
    ) else (
        echo   [warn] aether-reference\bin\pt\ not found - Tor/Psiphon in Aether will not work
    )
) else (
    echo   [warn] aether.exe not found under aether-reference\bin - Aether profiles will fail until the user places aether.exe next to nekogo.exe
)

REM ============================================================
REM  STEP 2d - Official sing-box CLI (optional; Upstream engine)
REM ============================================================
echo.
echo [2d/9] Official sing-box CLI...
if not exist "%BUILDBIN%\sing-box.exe" (
    if not exist "%BUILDBIN%" mkdir "%BUILDBIN%" >nul 2>nul
    REM Upstream SagerNet CLI only: the custom core is the local build
    REM from STEP 1 and is never replaced by this binary.
    call :fetch_github_release SagerNet/sing-box "^sing-box-[0-9.]+-windows-amd64\.zip$" "%BUILDBIN%" "sing-box.exe"
    if errorlevel 1 (
        echo   [warn] sing-box.exe download failed - the sing-box ^(Upstream^) engine will be unavailable.
        echo          Tried: !FETCH_URL!
        echo          Place sing-box.exe in "%BUILDBIN%" and rebuild, or use Settings - Updates.
    )
)
if exist "%BUILDBIN%\sing-box.exe" (
    for %%A in ("%BUILDBIN%\sing-box.exe") do set "SINGBOX_SIZE=%%~zA"
    echo   [ok] sing-box.exe ^(!SINGBOX_SIZE! bytes^)
) else (
    echo   [warn] sing-box.exe not staged - the sing-box ^(Upstream^) engine stays unavailable
)

REM ============================================================
REM  STEP 2e - mihomo core (optional; Clash Meta engine)
REM ============================================================
echo.
echo [2e/9] mihomo core...
if not exist "%BUILDBIN%\mihomo.exe" (
    if not exist "%BUILDBIN%" mkdir "%BUILDBIN%" >nul 2>nul
    REM The GOAMD64=v1 asset is the baseline x86-64 build. The plain
    REM amd64 asset targets a newer instruction set and would not start
    REM on every machine NekoGo ships to, so it is deliberately not the
    REM one fetched here.
    call :fetch_github_release MetaCubeX/mihomo "^mihomo-windows-amd64-v1-v[0-9.]+\.zip$" "%BUILDBIN%" "mihomo.exe"
    if errorlevel 1 (
        echo   [warn] mihomo download failed - mihomo ^(Clash Meta^) profiles will be unavailable.
        echo          Tried: !FETCH_URL!
        echo          Place mihomo.exe in "%BUILDBIN%" and rebuild, or set mihomoPath in settings.
    )
    REM The fetch helper keeps the archive's own file name, so normalize
    REM it: the resolver prefers mihomo.exe and STEP 7/8 stage that name.
    if not exist "%BUILDBIN%\mihomo.exe" for %%F in ("%BUILDBIN%\mihomo-*.exe") do move /Y "%%F" "%BUILDBIN%\mihomo.exe" >nul 2>nul
)
if exist "%BUILDBIN%\mihomo.exe" (
    for %%A in ("%BUILDBIN%\mihomo.exe") do set "MIHOMO_SIZE=%%~zA"
    echo   [ok] mihomo.exe ^(!MIHOMO_SIZE! bytes^)
) else (
    echo   [warn] mihomo.exe not staged - the mihomo core stays unavailable
)

REM ============================================================
REM  STEP 3 - Wails bindings
REM ============================================================
echo.
echo [3/9] Wails bindings...
cd /d "%GUI%"
wails3 generate bindings -ts >nul 2>nul
if errorlevel 1 (
    echo   [warn] bindings generation failed - using existing
) else (
    echo   [ok] bindings regenerated
)

REM ============================================================
REM  STEP 4 - Icon
REM ============================================================
echo.
echo [4/9] Icon embed...
cd /d "%GUI%"

if exist "rsrc.syso" del "rsrc.syso" >nul 2>nul

REM Generate gui/app.ico from the frontend icon when missing.
if not exist "app.ico" (
    if exist "frontend\src\assets\app.png" (
        echo   generating app.ico from frontend\src\assets\app.png...
        cd /d "%ROOT%"
        go run ./script/png2ico.go -in gui/frontend/src/assets/app.png -out gui/app.ico >nul 2>nul
        cd /d "%GUI%"
    )
)

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
    echo   [warn] app.ico and frontend\src\assets\app.png both missing - default icon
)

REM ============================================================
REM  STEP 4b - GeoLite2 country DB (preparation for country detection)
REM ============================================================
echo.
echo [4b/9] GeoLite2 DB...
if exist "%GUI%\assets\GeoLite2-Country.mmdb" (
    if not exist "%BUILDBIN%" mkdir "%BUILDBIN%" >nul 2>nul
    copy /Y "%GUI%\assets\GeoLite2-Country.mmdb" "%BUILDBIN%\GeoLite2-Country.mmdb" >nul 2>nul
    if errorlevel 1 (
        echo   [warn] GeoLite2-Country.mmdb could not be copied into bin\ ^(file locked?^) - the existing copy stays.
    ) else (
        echo   [ok] GeoLite2-Country.mmdb staged
    )
) else (
    echo   [warn] GeoLite2-Country.mmdb not found in gui\assets\ - country detection will be disabled.
)

REM ============================================================
REM  STEP 5 - Frontend
REM ============================================================
echo.
echo [5/9] Frontend...
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
REM  STEP 6 - Go GUI binary
REM ============================================================
echo.
echo [6/9] Go GUI binary...
cd /d "%GUI%"
if not exist "build\bin" mkdir "build\bin" >nul 2>nul
if exist "%EXE%" del "%EXE%" >nul 2>nul

go build -trimpath -ldflags="-H windowsgui -s -w -X main.appVersion=%APP_VERSION%" -o "%EXE%" .
if errorlevel 1 (echo   [ERR] go build failed & goto :fail)
if not exist "%EXE%" (echo   [ERR] nekogo.exe not produced & goto :fail)
echo   [ok] nekogo.exe

REM ============================================================
REM  STEP 7 - Copy runtime files + stage portable ZIP
REM ============================================================
echo.
echo [7/9] Portable ZIP...

if not exist "%DIST%" mkdir "%DIST%" >nul 2>nul
REM Stage into dist\portable when it can be wiped - and it is not always
REM wipeable. A LOADED driver image stays open in the kernel even after the
REM app that installed it has exited, and the windivert service here points
REM straight at dist\portable\bin\WinDivert64.sys, so rmdir deletes every
REM other file and leaves that one. The old check read that as "a NekoGo
REM build is running" and failed the build with nothing running at all;
REM fall back to a second stage folder and name the file that is holding.
set "PORTABLE_STAGE=%PORTABLE_DIR%"
if not exist "%PORTABLE_DIR%" goto :stage_make
rmdir /s /q "%PORTABLE_DIR%" >nul 2>nul
if not exist "%PORTABLE_DIR%" goto :stage_make
echo   [warn] cannot delete %PORTABLE_DIR% - still held open:
for /r "%PORTABLE_DIR%" %%F in (*) do echo          %%F
echo        A loaded WinDivert64.sys stays open after its app exits; from an
echo        admin prompt "sc stop windivert" ^& "sc delete windivert" frees it
echo        ^(or reboot^). Staging into %PORTABLE_FALLBACK% - build unaffected.
set "PORTABLE_STAGE=%PORTABLE_FALLBACK%"
if not exist "%PORTABLE_STAGE%" goto :stage_make
rmdir /s /q "%PORTABLE_STAGE%" >nul 2>nul
if not exist "%PORTABLE_STAGE%" goto :stage_make
echo   [ERR] both portable stage folders are locked - close the NekoGo that
echo         runs from either one ^(or clear the driver^), then build again.
goto :fail

:stage_make
mkdir "%PORTABLE_STAGE%" >nul 2>nul
if not exist "%PORTABLE_STAGE%" (
    echo   [ERR] cannot create %PORTABLE_STAGE%
    goto :fail
)

REM Copy from runtime/ folder (if exists). WinDivert files must sit
REM next to the exes that load them (bin\, next to the engines).
if exist "%RUNTIME%\WinDivert.dll"   copy /Y "%RUNTIME%\WinDivert.dll"   "%BUILDBIN%\" >nul 2>nul
if exist "%RUNTIME%\WinDivert64.sys" copy /Y "%RUNTIME%\WinDivert64.sys" "%BUILDBIN%\" >nul 2>nul

REM PattN-style layout: nekogo.exe in the root, every engine binary in
REM bin\ (nekobox_core, sing-box, mihomo, xray, sni-spoof, aether + pt\,
REM and the WinDivert runtime the cores load).
set "PORTABLE_BIN=%PORTABLE_STAGE%\bin"
mkdir "%PORTABLE_BIN%" >nul 2>nul

copy /Y "%BUILDBIN%\nekogo.exe"         "%PORTABLE_STAGE%\" >nul 2>nul
copy /Y "%BUILDBIN%\nekobox_core.exe" "%PORTABLE_BIN%\" >nul 2>nul
REM The official sing-box CLI is a separate binary (Upstream core engine),
REM fetched by STEP 2d when missing and still optional at packaging.
if exist "%BUILDBIN%\sing-box.exe" (
    copy /Y "%BUILDBIN%\sing-box.exe" "%PORTABLE_BIN%\" >nul 2>nul
)
REM mihomo (Clash Meta) is a separate optional core, fetched by STEP 2e.
if exist "%BUILDBIN%\mihomo.exe" (
    copy /Y "%BUILDBIN%\mihomo.exe" "%PORTABLE_BIN%\" >nul 2>nul
)
copy /Y "%BUILDBIN%\xray.exe"         "%PORTABLE_BIN%\" >nul 2>nul
copy /Y "%BUILDBIN%\sni-spoof.exe"    "%PORTABLE_BIN%\" >nul 2>nul

if exist "%BUILDBIN%\aether.exe" (
    copy /Y "%BUILDBIN%\aether.exe" "%PORTABLE_BIN%\" >nul 2>nul
)
if exist "%BUILDBIN%\pt\" (
    robocopy "%BUILDBIN%\pt" "%PORTABLE_BIN%\pt" /E /NFL /NDL /NJH /NJS /NC /NS >nul 2>nul
    if errorlevel 8 (echo   [ERR] pt\ copy into the portable stage failed ^(robocopy !errorlevel!^) & goto :fail)
)

REM One copy only, next to the engines: the detector resolves bin\ first,
REM and the in-app GeoIP updater rewrites this exact file.
if exist "%BUILDBIN%\GeoLite2-Country.mmdb" copy /Y "%BUILDBIN%\GeoLite2-Country.mmdb" "%PORTABLE_BIN%\" >nul 2>nul

if exist "%BUILDBIN%\WinDivert.dll"   copy /Y "%BUILDBIN%\WinDivert.dll"   "%PORTABLE_BIN%\" >nul 2>nul
if exist "%BUILDBIN%\WinDivert64.sys" copy /Y "%BUILDBIN%\WinDivert64.sys" "%PORTABLE_BIN%\" >nul 2>nul

REM A running NekoGo or engine locks its exe: rmdir/copy above then fail
REM silently on that file and the zip + installer ship the PREVIOUS build.
REM Compare every staged file with the fresh one and fail loudly instead.
call :same "%BUILDBIN%\nekogo.exe" "%PORTABLE_STAGE%\nekogo.exe"
if errorlevel 1 goto :fail
call :same "%BUILDBIN%\nekobox_core.exe" "%PORTABLE_BIN%\nekobox_core.exe"
if errorlevel 1 goto :fail
call :same "%BUILDBIN%\xray.exe" "%PORTABLE_BIN%\xray.exe"
if errorlevel 1 goto :fail
call :same "%BUILDBIN%\sni-spoof.exe" "%PORTABLE_BIN%\sni-spoof.exe"
if errorlevel 1 goto :fail
if exist "%BUILDBIN%\aether.exe" (
    call :same "%BUILDBIN%\aether.exe" "%PORTABLE_BIN%\aether.exe"
    if errorlevel 1 goto :fail
)
if exist "%BUILDBIN%\sing-box.exe" (
    call :same "%BUILDBIN%\sing-box.exe" "%PORTABLE_BIN%\sing-box.exe"
    if errorlevel 1 goto :fail
)
if exist "%BUILDBIN%\mihomo.exe" (
    call :same "%BUILDBIN%\mihomo.exe" "%PORTABLE_BIN%\mihomo.exe"
    if errorlevel 1 goto :fail
)
if exist "%BUILDBIN%\pt\lyrebird.exe" (
    call :same "%BUILDBIN%\pt\lyrebird.exe" "%PORTABLE_BIN%\pt\lyrebird.exe"
    if errorlevel 1 goto :fail
)
if exist "%BUILDBIN%\pt\psiphon-tunnel-core.exe" (
    call :same "%BUILDBIN%\pt\psiphon-tunnel-core.exe" "%PORTABLE_BIN%\pt\psiphon-tunnel-core.exe"
    if errorlevel 1 goto :fail
)
if exist "%BUILDBIN%\WinDivert.dll" (
    call :same "%BUILDBIN%\WinDivert.dll" "%PORTABLE_BIN%\WinDivert.dll"
    if errorlevel 1 goto :fail
)
if exist "%BUILDBIN%\WinDivert64.sys" (
    call :same "%BUILDBIN%\WinDivert64.sys" "%PORTABLE_BIN%\WinDivert64.sys"
    if errorlevel 1 goto :fail
)

REM README
(
    echo NekoGo - Portable v%APP_VERSION%
    echo ============================================
    echo.
    echo CONTENTS:
    echo   nekogo.exe         - Wails3 GUI ^(sing-box + Xray^)
    echo   bin\nekobox_core.exe - custom sing-box core ^(Thrift RPC; the bundled engine^)
    echo   bin\sing-box.exe     - official sing-box CLI ^(Upstream core engine; optional^)
    echo   bin\mihomo.exe       - mihomo / Clash Meta core ^(mihomo profiles; optional^)
    echo   bin\xray.exe         - Xray-core engine ^(gRPC stats^)
    echo   bin\sni-spoof.exe    - SNI spoofing engine
    echo   bin\aether.exe       - Aether WARP engine ^(optional; requires bin\pt\ next to it for Tor/Psiphon^)
    echo   bin\GeoLite2-Country.mmdb - GeoIP database ^(optional; next to the engines^)
    echo.
    echo Every engine lives in bin\; the GUI finds them there. Updating a
    echo core from Settings - Updates installs the new binary into bin\.
    echo.
    echo HOW TO RUN:
    echo   1. Right-click "nekogo.exe"
    echo   2. Select "Run as administrator" ^(needed for TUN mode^)
    echo.
    echo REQUIREMENTS:
    echo   - Windows 10 or newer ^(x64^)
    echo   - WebView2 Runtime ^(usually preinstalled^)
    echo     Download: https://developer.microsoft.com/microsoft-edge/webview2/
    echo.
    echo CONFIG:
    echo   config.json is created under %%APPDATA%%\NekoGo on first launch.
    echo.
    echo UNINSTALL:
    echo   Just delete this folder. No registry changes.
    echo.
    echo LICENSE: GPL-3.0
    echo SOURCE:  https://github.com/xenonama/NekoGo
) > "%PORTABLE_STAGE%\README.txt"

set "ZIP_NAME=NekoGo-Portable-v%APP_VERSION%.zip"
if exist "%DIST%\%ZIP_NAME%" del "%DIST%\%ZIP_NAME%" >nul 2>nul

set "ZIP_BUILT="
powershell -NoProfile -Command ^
    "Compress-Archive -Path '%PORTABLE_STAGE%\*' -DestinationPath '%DIST%\%ZIP_NAME%' -Force" ^
    >nul 2>nul

if errorlevel 1 (
    REM Compress-Archive can leave a partial file behind; shipping it as
    REM "BUILD OK" is worse than reporting no portable ZIP at all.
    echo   [warn] Compress-Archive failed - no portable ZIP was produced
    if exist "%DIST%\%ZIP_NAME%" del "%DIST%\%ZIP_NAME%" >nul 2>nul
) else if exist "%DIST%\%ZIP_NAME%" (
    for %%A in ("%DIST%\%ZIP_NAME%") do set "ZIP_SIZE=%%~zA"
    set "ZIP_BUILT=1"
    echo   [ok] %ZIP_NAME% ^(!ZIP_SIZE! bytes^)
) else (
    echo   [warn] zip creation failed
)

REM ============================================================
REM  STEP 8 - Installer (setup.exe)
REM ============================================================
echo.
echo [8/9] Installer...
set "SETUP_NAME=NekoGo-Setup-v%APP_VERSION%.exe"
set "SETUP_FINAL=%DIST%\%SETUP_NAME%"
set "SETUP_TMP=%DIST%\NekoGo-Setup-v%APP_VERSION%-new.exe"
set "NSIS_LOG=%DIST%\installer-build.log"

if not defined NSIS_AVAILABLE (
    echo   [skip] makensis not found - setup.exe was NOT rebuilt
    echo          Install NSIS ^(https://nsis.sourceforge.io/Download^) and rebuild
    goto :done
)

cd /d "%ROOT%"
if not exist "installer.nsi" (
    echo   [ERR] installer.nsi not found
    goto :fail
)

REM Compile to a .new file and publish it only after makensis succeeds.
REM The old command sent every error to nul: a locked/failing setup left
REM the PREVIOUS installer in dist while the build still printed BUILD OK,
REM which is exactly how "the exe installer is not updated" looked.
REM Both scratch files are cleared first: a run killed between makensis and
REM the move leaves them behind, and they must not look like artifacts.
if exist "%NSIS_LOG%" del /f /q "%NSIS_LOG%" >nul 2>nul
if exist "%SETUP_TMP%" (
    del /f /q "%SETUP_TMP%" >nul 2>nul
    if exist "%SETUP_TMP%" (
        echo   [ERR] cannot delete the leftover -new.exe installer ^(file is open or locked^).
        echo         Close whatever holds %SETUP_TMP% and build again.
        goto :fail
    )
)
makensis /DAPP_VERSION="%APP_VERSION%" /DAPP_VERSION_VI="%APP_VERSION_VI%" /DPORTABLE_STAGE="%PORTABLE_STAGE%" /DDIST="%DIST%" /DOUTFILE="%SETUP_TMP%" installer.nsi >"%NSIS_LOG%" 2>&1
if errorlevel 1 (
    echo   [ERR] makensis failed - full log follows:
    type "%NSIS_LOG%"
    call :cleanup_installer
    goto :fail
)
if not exist "%SETUP_TMP%" (
    echo   [ERR] makensis returned success but produced no setup.exe
    type "%NSIS_LOG%"
    call :cleanup_installer
    goto :fail
)
if exist "%NSIS_LOG%" del /f /q "%NSIS_LOG%" >nul 2>nul

REM Moving onto the published name leaves the old installer intact if the
REM destination is open/locked, and then we fail loudly instead of
REM pretending it was rebuilt.
move /Y "%SETUP_TMP%" "%SETUP_FINAL%" >nul 2>nul
if errorlevel 1 (
    echo   [ERR] cannot replace %SETUP_NAME% ^(file is open or locked^).
    echo         Close the running setup/installer and build again.
    if exist "%SETUP_TMP%" del /f /q "%SETUP_TMP%" >nul 2>nul
    goto :fail
)
set "SETUP_BUILT=1"
for %%A in ("%SETUP_FINAL%") do echo   [ok] %SETUP_NAME% ^(%%~zA bytes, %%~tA^)

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
echo   Dev run:    %EXE%
if defined ZIP_BUILT (
    echo   Portable:   %DIST%\%ZIP_NAME%
) else (
    echo   Portable:   NOT BUILT - see [7/9] above
)
if defined SETUP_BUILT (
    echo   Installer:  %SETUP_FINAL%
) else (
    echo   Installer:  NOT BUILT - see [8/9] above
)
echo ============================================================
echo.

choice /C YN /M "Open dist folder now"
if errorlevel 2 goto :end
explorer "%DIST%"

:end
echo.
pause
exit /b 0

:same
fc /b "%~1" "%~2" >nul 2>nul
if not errorlevel 1 exit /b 0
echo   [ERR] staged copy is not the fresh build: %~2
echo         A running NekoGo or engine is holding it - close it and rebuild.
exit /b 1

REM ---------- helper: fetch a missing engine binary from GitHub ----------
REM call :fetch_github_release OWNER/REPO ASSET_REGEX DEST_DIR [EXE_NAME]
REM   Downloads the latest release asset whose name matches the regex
REM   (case-insensitive), verifies SHA256 when the release publishes one
REM   (asset .sha256, SHA256SUMS(.txt) or <asset>.dgst), extracts the
REM   archive and copies the matching exe - plus a sibling pt\ folder,
REM   for Aether - into DEST_DIR (created when missing). EXE_NAME is
REM   optional: without it the exe is derived from the asset name prefix.
REM   Sets FETCH_URL to the resolved asset URL for failure messages and
REM   returns errorlevel 1 on any failure. Never downloads when the caller
REM   already has the binary: every call site checks first.
:fetch_github_release
set "FGR_REPO=%~1"
set "FGR_ASSET=%~2"
set "FGR_DEST=%~3"
set "FGR_EXE=%~4"
set "FETCH_URL=https://github.com/%~1/releases/latest"
del /f /q "%TEMP%\nekogo-fetch-url.txt" >nul 2>nul
echo   fetching "%~2" from %~1 ...
REM One PowerShell script assembled below, so every batch line stays
REM readable. Double quotes and exclamation marks are avoided inside it
REM (cmd would eat them); PowerShell strings use single quotes only.
set "FGR_PS="
set "FGR_PS=!FGR_PS!$ErrorActionPreference = 'Stop';"
set "FGR_PS=!FGR_PS! [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12;"
set "FGR_PS=!FGR_PS! $repo = $env:FGR_REPO; $assetRe = $env:FGR_ASSET; $dest = $env:FGR_DEST; $wantExe = $env:FGR_EXE;"
set "FGR_PS=!FGR_PS! $hdr = @{ 'User-Agent' = 'NekoGo-build'; 'Accept' = 'application/vnd.github+json' };"
REM The API Accept header must NOT go to the asset host: with it,
REM Invoke-WebRequest dies mid-stream ("unexpected EOF or 0 bytes").
set "FGR_PS=!FGR_PS! $dlHdr = @{ 'User-Agent' = 'NekoGo-build' };"
REM GitHub resets large asset streams occasionally; retry before giving up.
set "FGR_PS=!FGR_PS! function Get-Asset($u, $o) { for ($i = 1; $i -le 3; $i++) { try { Invoke-WebRequest -UseBasicParsing -Headers $dlHdr -Uri $u -OutFile $o -TimeoutSec 300; return } catch { if ($i -eq 3) { throw }; Write-Host ('[warn] download attempt ' + $i + ' failed, retrying: ' + $_.Exception.Message); Start-Sleep -Seconds 2 } } };"
set "FGR_PS=!FGR_PS! if ($env:GITHUB_TOKEN) { $hdr['Authorization'] = 'Bearer ' + $env:GITHUB_TOKEN };"
set "FGR_PS=!FGR_PS! $urlFile = Join-Path $env:TEMP 'nekogo-fetch-url.txt';"
set "FGR_PS=!FGR_PS! $tmp = Join-Path $env:TEMP ('nekogo-fetch-' + [guid]::NewGuid().ToString('N'));"
set "FGR_PS=!FGR_PS! $code = 1;"
set "FGR_PS=!FGR_PS! try {"
set "FGR_PS=!FGR_PS! $rel = Invoke-RestMethod -Headers $hdr -Uri ('https://api.github.com/repos/' + $repo + '/releases/latest') -TimeoutSec 60;"
set "FGR_PS=!FGR_PS! $asset = @($rel.assets) | Where-Object { $_.name -match $assetRe } | Select-Object -First 1;"
set "FGR_PS=!FGR_PS! if (-not $asset) { throw ('no asset matching ' + $assetRe + ' in ' + $repo + ' ' + $rel.tag_name) };"
set "FGR_PS=!FGR_PS! Write-Host ('[fetch] ' + $repo + ' ' + $rel.tag_name + ' -> ' + $asset.name);"
set "FGR_PS=!FGR_PS! [IO.File]::WriteAllText($urlFile, $asset.browser_download_url);"
set "FGR_PS=!FGR_PS! New-Item -ItemType Directory -Force -Path $tmp | Out-Null;"
set "FGR_PS=!FGR_PS! $file = Join-Path $tmp $asset.name;"
set "FGR_PS=!FGR_PS! Get-Asset $asset.browser_download_url $file;"
set "FGR_PS=!FGR_PS! $sumAsset = $null; $sumIsList = $false;"
set "FGR_PS=!FGR_PS! $per = @($rel.assets) | Where-Object { $_.name -eq ($asset.name + '.sha256') } | Select-Object -First 1;"
set "FGR_PS=!FGR_PS! if ($per) { $sumAsset = $per } else {"
set "FGR_PS=!FGR_PS!   $list = @($rel.assets) | Where-Object { $_.name -match '(?i)^sha256sums(\.txt)?$' } | Select-Object -First 1;"
set "FGR_PS=!FGR_PS!   if ($list) { $sumAsset = $list; $sumIsList = $true }"
set "FGR_PS=!FGR_PS!   else { $dg = @($rel.assets) | Where-Object { $_.name -eq ($asset.name + '.dgst') } | Select-Object -First 1; if ($dg) { $sumAsset = $dg } } };"
set "FGR_PS=!FGR_PS! if ($sumAsset) {"
set "FGR_PS=!FGR_PS!   $sumFile = Join-Path $tmp $sumAsset.name;"
set "FGR_PS=!FGR_PS!   Get-Asset $sumAsset.browser_download_url $sumFile;"
set "FGR_PS=!FGR_PS!   $want = $null;"
set "FGR_PS=!FGR_PS!   if ($sumIsList) {"
set "FGR_PS=!FGR_PS!     foreach ($line in (Get-Content -LiteralPath $sumFile)) {"
set "FGR_PS=!FGR_PS!       $hex = [regex]::Match($line, '(?i)\b[0-9a-f]{64}\b').Value;"
set "FGR_PS=!FGR_PS!       if (-not $hex) { continue };"
set "FGR_PS=!FGR_PS!       $fname = ($line -replace '(?i)^\s*[0-9a-f]{64}\s+\*?', '').Trim();"
set "FGR_PS=!FGR_PS!       if ($fname -eq $asset.name) { $want = $hex; break } } }"
set "FGR_PS=!FGR_PS!   else { $want = [regex]::Match((Get-Content -Raw -LiteralPath $sumFile), '(?i)\b[0-9a-f]{64}\b').Value };"
set "FGR_PS=!FGR_PS!   if (-not $want) { Write-Host ('[warn] ' + $sumAsset.name + ' has no SHA256 for ' + $asset.name + ' - verification skipped') }"
set "FGR_PS=!FGR_PS!   else { $got = (Get-FileHash -Algorithm SHA256 -LiteralPath $file).Hash;"
set "FGR_PS=!FGR_PS!     if ($got -ne $want.ToUpperInvariant()) { throw ('SHA256 mismatch for ' + $asset.name) };"
set "FGR_PS=!FGR_PS!     Write-Host ('[fetch] SHA256 verified (' + $sumAsset.name + ')') } }"
set "FGR_PS=!FGR_PS! else { Write-Host '[warn] release publishes no checksum - verification skipped' };"
set "FGR_PS=!FGR_PS! $ex = Join-Path $tmp 'extract';"
set "FGR_PS=!FGR_PS! New-Item -ItemType Directory -Force -Path $ex | Out-Null;"
set "FGR_PS=!FGR_PS! if ($asset.name -match '(?i)\.zip$') { Expand-Archive -LiteralPath $file -DestinationPath $ex -Force }"
set "FGR_PS=!FGR_PS! elseif ($asset.name -match '(?i)\.(tar\.gz|tgz)$') { tar -xzf $file -C $ex; if ($LASTEXITCODE -ne 0) { throw 'tar extraction failed' } }"
set "FGR_PS=!FGR_PS! else { throw ('unsupported archive type: ' + $asset.name) };"
set "FGR_PS=!FGR_PS! $want = $wantExe;"
set "FGR_PS=!FGR_PS! if (-not $want) { $want = (($asset.name -split '-')[0] + '.exe') };"
set "FGR_PS=!FGR_PS! $exes = @(Get-ChildItem -LiteralPath $ex -Filter *.exe -Recurse);"
set "FGR_PS=!FGR_PS! $pick = $exes | Where-Object { $_.Name -ieq $want } | Select-Object -First 1;"
set "FGR_PS=!FGR_PS! if (-not $pick) {"
set "FGR_PS=!FGR_PS!   if ($exes.Count -eq 1) { $pick = $exes[0] }"
set "FGR_PS=!FGR_PS!   else { throw ('no ' + $want + ' inside ' + $asset.name + ' (' + $exes.Count + ' exe files)') } };"
set "FGR_PS=!FGR_PS! New-Item -ItemType Directory -Force -Path $dest | Out-Null;"
set "FGR_PS=!FGR_PS! Copy-Item -LiteralPath $pick.FullName -Destination (Join-Path $dest $pick.Name) -Force;"
set "FGR_PS=!FGR_PS! Write-Host ('[fetch] installed ' + $pick.Name + ' into ' + $dest);"
set "FGR_PS=!FGR_PS! $ptSrc = Join-Path $pick.Directory.FullName 'pt';"
set "FGR_PS=!FGR_PS! if (Test-Path -LiteralPath $ptSrc) {"
set "FGR_PS=!FGR_PS!   $ptDst = Join-Path $dest 'pt';"
set "FGR_PS=!FGR_PS!   if (Test-Path -LiteralPath $ptDst) { Remove-Item -LiteralPath $ptDst -Recurse -Force };"
set "FGR_PS=!FGR_PS!   Copy-Item -LiteralPath $ptSrc -Destination $ptDst -Recurse -Force;"
set "FGR_PS=!FGR_PS!   Write-Host ('[fetch] installed pt\ next to ' + $pick.Name) };"
set "FGR_PS=!FGR_PS! $code = 0 }"
set "FGR_PS=!FGR_PS! catch { Write-Host ('[ERR] ' + $_.Exception.Message);"
set "FGR_PS=!FGR_PS!   if ($_.Exception.Message -match '403|rate limit') { Write-Host '[hint] GitHub limits anonymous API calls; set GITHUB_TOKEN to raise the limit, or retry later.' } }"
set "FGR_PS=!FGR_PS! finally { if (Test-Path -LiteralPath $tmp) { Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue } }"
set "FGR_PS=!FGR_PS! exit $code"
powershell -NoProfile -ExecutionPolicy Bypass -Command "!FGR_PS!"
set "FGR_RC=!errorlevel!"
set "FGR_PS="
if exist "%TEMP%\nekogo-fetch-url.txt" for /f "usebackq delims=" %%U in ("%TEMP%\nekogo-fetch-url.txt") do set "FETCH_URL=%%U"
del /f /q "%TEMP%\nekogo-fetch-url.txt" >nul 2>nul
if not "!FGR_RC!"=="0" (
    echo   [ERR] could not fetch %~2 from %~1
    exit /b 1
)
exit /b 0

REM ---------- helper: --dry-run target printer ----------
REM call :show_release OWNER/REPO ASSET_REGEX
:show_release
set "FGR_PS=$h = @{ 'User-Agent' = 'NekoGo-build' };"
set "FGR_PS=!FGR_PS! [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12;"
set "FGR_PS=!FGR_PS! $r = Invoke-RestMethod -Headers $h -Uri ('https://api.github.com/repos/%~1/releases/latest') -TimeoutSec 60;"
set "FGR_PS=!FGR_PS! $a = @($r.assets) | Where-Object { $_.name -match '%~2' } | Select-Object -First 1;"
set "FGR_PS=!FGR_PS! if ($a) { Write-Host ('  %~1 ' + $r.tag_name + ' -> ' + $a.browser_download_url) }"
set "FGR_PS=!FGR_PS! else { Write-Host ('  [ERR] %~1 ' + $r.tag_name + ' has no asset matching %~2'); exit 1 }"
powershell -NoProfile -ExecutionPolicy Bypass -Command "!FGR_PS!"
set "FGR_RC=!errorlevel!"
set "FGR_PS="
exit /b !FGR_RC!

REM ---------- helper: drop unpublished installer scratch files ----------
REM A failed makensis leaves -new.exe and installer-build.log in dist\; both
REM are scratch (the log was already echoed above) and a leftover -new.exe
REM reads like a shipped installer in the dist listing.
:cleanup_installer
if defined SETUP_TMP if exist "%SETUP_TMP%" del /f /q "%SETUP_TMP%" >nul 2>nul
if defined NSIS_LOG if exist "%NSIS_LOG%" del /f /q "%NSIS_LOG%" >nul 2>nul
exit /b 0

:fail
echo.
echo ============================================================
echo   BUILD FAILED
echo ============================================================
echo.
pause
exit /b 1

