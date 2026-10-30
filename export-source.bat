@echo off
setlocal enabledelayedexpansion
chcp 65001 >nul
title NekoGo - Export Source

REM ============================================================
REM  NekoGo - export source script
REM  Copies ONLY GitHub-ready source files into source\
REM  Usage:
REM    export-source.bat        (folder only)
REM    export-source.bat zip    (folder + zip in dist\)
REM
REM  Included: root build files + architecture docs (NEKOGO_PATTERNS.md,
REM    PATTN_*.md), core/, gui/ (no build outputs, no frontend
REM    node_modules/dist), third_party/ (local sing-box fork, needed to
REM    build), aether-reference/ (CLI docs + Kotlin reference; binaries
REM    excluded - see below), runtime/, docs/, script/, pages/,
REM    .github/, plus the sibling projects (sni spoofing).
REM  Skipped: xray/ (build.bat clones Xray-core when missing),
REM    aether-reference/bin binaries (aether.exe, pt/, zip - add them
REM    manually if you need Aether/Psiphon at runtime),
REM    PattN/ (audited third-party tree, docs already included),
REM    dist/, gui/build/, node_modules, frontend dist/, Rust target/
REM    dirs, scratch files (prompt.txt, chat.txt, go_files.txt,
REM    source*/), AGENT_LOG.md (internal development log), and all
REM    binaries/logs.
REM ============================================================

REM ---------- Resolve root ----------
set "ROOT=%~dp0"
if "%ROOT:~-1%"=="\" set "ROOT=%ROOT:~0,-1%"
set "SRC=%ROOT%\source"
set "DIST=%ROOT%\dist"

echo.
echo ============================================================
echo   NekoGo - EXPORT SOURCE
echo   Root:   %ROOT%
echo   Output: %SRC%
echo ============================================================
echo.

REM ---------- 1. Clean ----------
echo [1/7] Cleaning source\ ...
if exist "%SRC%" rmdir /s /q "%SRC%"
mkdir "%SRC%" >nul 2>nul
if not exist "%SRC%" (echo   [ERR] cannot create source\ folder & goto :fail)
echo   [ok] source\ is empty

REM ---------- 2. Root files ----------
echo.
echo [2/7] Copying root files ...
for %%F in (build.bat export-source.bat installer.nsi README.md LICENSE VERSION BUILD_NOTES.md NEKOGO_PATTERNS.md PATTN_BACKEND_PATTERNS.md PATTN_DEEP_DIVE_PATTERNS.md PATTN_VS_NEKOGO_GAP_ANALYSIS.md .gitignore .gitmodules) do (
    if exist "%ROOT%\%%F" (
        copy /Y "%ROOT%\%%F" "%SRC%\%%F" >nul
        echo   [ok] %%F
    ) else (
        echo   [skip] %%F not found
    )
)

REM ---------- 3. Go sources (core + gui) ----------
echo.
echo [3/7] Copying Go sources ...
if exist "%ROOT%\core" (
    robocopy "%ROOT%\core" "%SRC%\core" /E /XD bin dist /XF *.exe *.dll *.log *.tmp >nul
    echo   [ok] core
) else (
    echo   [skip] core not found
)
if exist "%ROOT%\gui" (
    robocopy "%ROOT%\gui" "%SRC%\gui" /E /XD build node_modules dist .vite .cache source /XF *.exe *.dll *.sys *.syso *.log *.tmp build_out.txt build_err.txt >nul
    echo   [ok] gui ^(no build outputs^)
) else (
    echo   [skip] gui not found
)

REM ---------- 4. Frontend (no node_modules / dist / tmp) ----------
echo.
echo [4/7] Trimming frontend artifacts ...
if exist "%SRC%\gui\frontend\node_modules" (
    rmdir /s /q "%SRC%\gui\frontend\node_modules" >nul 2>nul
    echo   [ok] dropped node_modules ^(npm install recreates it^)
)
if exist "%SRC%\gui\frontend\dist" (
    rmdir /s /q "%SRC%\gui\frontend\dist" >nul 2>nul
    echo   [ok] dropped frontend dist ^(vite build recreates it^)
)
for /d %%D in ("%SRC%\gui\frontend\.bindings-tmp-*") do (
    rmdir /s /q "%%D" >nul 2>nul
    echo   [ok] dropped %%~nxD
)

REM ---------- 5. Vendored deps + support dirs ----------
echo.
echo [5/7] Copying vendored deps and support dirs ...
if exist "%ROOT%\third_party" (
    robocopy "%ROOT%\third_party" "%SRC%\third_party" /E /XD .git /XF *.exe *.dll *.log >nul
    echo   [ok] third_party
) else (
    echo   [skip] third_party not found
)
if exist "%ROOT%\aether-reference" (
    robocopy "%ROOT%\aether-reference" "%SRC%\aether-reference" /E /XD .git pt aether.toml-psiphon /XF *.exe *.zip *.log >nul
    echo   [ok] aether-reference ^(docs + Kotlin, no binaries^)
    if exist "%ROOT%\aether-reference\bin\aether.exe" (
        echo   [skip] aether-reference\bin binaries ^(copy aether.exe + pt\ manually^)
    )
) else (
    echo   [skip] aether-reference not found
)
for %%D in (runtime docs script pages .github) do (
    if exist "%ROOT%\%%D" (
        robocopy "%ROOT%\%%D" "%SRC%\%%D" /E /XD .git /XF *.log *.tmp >nul
        echo   [ok] %%D
    ) else (
        echo   [skip] %%D not found
    )
)
if exist "%ROOT%\xray\go.mod" (
    echo   [skip] xray ^(build.bat clones Xray-core when missing^)
)

REM ---------- 6. Sibling projects (source only) ----------
echo.
echo [6/7] Copying sibling projects ...
call :copy_sibling "sni spoofing"

REM ---------- 7. Summary ----------
echo.
echo [7/7] Summary ...
set "COUNT=0"
for /f %%A in ('dir /s /b /a-d "%SRC%" ^| "%SystemRoot%\System32\find.exe" /c /v ""') do set "COUNT=%%A"
echo   Files: !COUNT!

echo.
echo ---------- source\ ----------
dir /b /a "%SRC%"

REM ---------- Optional ZIP ----------
if /i not "%~1"=="zip" goto :done

echo.
echo ---------- zip ----------
if not exist "%DIST%" mkdir "%DIST%" >nul 2>nul

set "VERSION="
for /f "usebackq tokens=1" %%A in ("%ROOT%\VERSION") do (
    if not defined VERSION set "VERSION=%%A"
)
if not defined VERSION set "VERSION=0.0.0"

set "ZIP_NAME=NekoGo-Source-v!VERSION!.zip"
if exist "%DIST%\!ZIP_NAME!" del "%DIST%\!ZIP_NAME!" >nul 2>nul

powershell -NoProfile -Command "Compress-Archive -Path '%SRC%\*' -DestinationPath '%DIST%\!ZIP_NAME!' -Force" >nul 2>nul
if exist "%DIST%\!ZIP_NAME!" (
    echo   [ok] !ZIP_NAME!
) else (
    echo   [warn] zip failed
)

:done
echo.
echo ============================================================
echo   EXPORT OK
echo   Folder: %SRC%
echo ============================================================
echo.
echo Rebuild from the export with:
echo   1. cd source\gui\frontend ^&^& npm install ^&^& npm run build
echo   2. drop aether.exe + pt\ into aether-reference\bin\ for Aether/Psiphon ^(not in this export^)
echo   3. cd source\ ^&^& build.bat   (Go + Rust + NSIS; clones xray)
echo.

choice /C YN /M "Open source folder now"
if errorlevel 2 goto :end
explorer "%SRC%"

:end
echo.
pause
exit /b 0

:fail
echo.
echo EXPORT FAILED
echo.
pause
exit /b 1

REM ---------- helper: sibling copy (handles names with spaces) ----------
:copy_sibling
if not exist "%ROOT%\%~1" (
    echo   [skip] %~1 not found
    exit /b 0
)
robocopy "%ROOT%\%~1" "%SRC%\%~1" /E /XD target dist build bin obj source node_modules .vite .cache .vs .vscode .idea .git /XF *.exe *.dll *.sys *.pdb *.syso *.zip *.map *.log *.tmp config.json chat.txt prompt.txt go_files.txt >nul
if errorlevel 8 (
    echo   [warn] %~1 copy had errors ^(robocopy !errorlevel!^)
) else (
    echo   [ok] %~1
)
exit /b 0