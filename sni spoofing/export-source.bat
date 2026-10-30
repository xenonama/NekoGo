@echo off
setlocal enabledelayedexpansion
chcp 65001 >nul
title SNI Spoofer - Export Source

REM ============================================================
REM  SNI Spoofer - export source script
REM  Copies ONLY GitHub-ready source files into source\
REM  Usage:
REM    export-source.bat        (folder only)
REM    export-source.bat zip    (folder + zip in dist\)
REM ============================================================

REM ---------- Resolve root ----------
set "ROOT=%~dp0"
if "%ROOT:~-1%"=="\" set "ROOT=%ROOT:~0,-1%"
set "SRC=%ROOT%\source"
set "DIST=%ROOT%\dist"

echo.
echo ============================================================
echo   SNI Spoofer - EXPORT SOURCE
echo   Root:   %ROOT%
echo   Output: %SRC%
echo ============================================================
echo.

REM ---------- 1. Clean ----------
echo [1/5] Cleaning source\ ...
if exist "%SRC%" rmdir /s /q "%SRC%"
mkdir "%SRC%" >nul 2>nul
if not exist "%SRC%" (echo   [ERR] cannot create source\ folder & goto :fail)
echo   [ok] source\ is empty

REM ---------- 2. Root files ----------
echo.
echo [2/5] Copying root files ...
for %%F in (Cargo.toml Cargo.lock build.bat export-source.bat installer.nsi README.md LICENSE .gitignore ip_list.txt sni_list.txt) do (
    if exist "%ROOT%\%%F" (
        copy /Y "%ROOT%\%%F" "%SRC%\%%F" >nul
        echo   [ok] %%F
    ) else (
        echo   [skip] %%F not found
    )
)

REM ---------- 3. Rust crates ----------
echo.
echo [3/5] Copying Rust crates ...
for %%C in (core windivert engine-ffi) do (
    if exist "%ROOT%\%%C" (
        robocopy "%ROOT%\%%C" "%SRC%\%%C" /E /XD target dist source .vscode .idea /XF *.syso *.pdb *.exe *.dll *.sys config.json log.txt >nul
        echo   [ok] %%C
    ) else (
        echo   [skip] %%C not found
    )
)

REM ---------- 4. GUI ----------
echo.
echo [4/5] Copying GUI ...
if not exist "%SRC%\gui" mkdir "%SRC%\gui" >nul 2>nul

for %%F in (main.go sni_engine.go rust_engine.go rust_engine_unix.go rust_engine_windows.go singleinstance_unix.go singleinstance_windows.go assets.go go.mod go.sum Taskfile.yml config.yml config.example.json app.ico app.png) do (
    if exist "%ROOT%\gui\%%F" (
        copy /Y "%ROOT%\gui\%%F" "%SRC%\gui\%%F" >nul
        echo   [ok] gui\%%F
    )
)

if exist "%ROOT%\gui\frontend" (
    robocopy "%ROOT%\gui\frontend" "%SRC%\gui\frontend" /E /XD node_modules dist .vite .cache wailsjs bindings /XF *.tsbuildinfo .DS_Store Thumbs.db >nul
    echo   [ok] gui\frontend
) else (
    echo   [skip] gui\frontend not found
)

REM ---------- 5. Runtime (optional) ----------
echo.
echo [5/5] Copying runtime ...
set "RUNTIME_COPIED="
if not exist "%SRC%\runtime" mkdir "%SRC%\runtime" >nul 2>nul
for %%F in (WinDivert.dll WinDivert64.sys ip_list.txt sni_list.txt) do (
    if exist "%ROOT%\runtime\%%F" (
        copy /Y "%ROOT%\runtime\%%F" "%SRC%\runtime\%%F" >nul
        echo   [ok] runtime\%%F
        set "RUNTIME_COPIED=1"
    )
)
if not defined RUNTIME_COPIED (
    rmdir /s /q "%SRC%\runtime" >nul 2>nul
    echo   [skip] no runtime files
)

REM ---------- Summary ----------
echo.
echo ---------- summary ----------
set "COUNT=0"
for /f %%A in ('dir /s /b "%SRC%" ^| find /c /v ""') do set "COUNT=%%A"
echo   Files: !COUNT!

echo.
echo ---------- source\ ----------
dir /b "%SRC%"

REM ---------- Optional ZIP ----------
if /i not "%~1"=="zip" goto :done

echo.
echo ---------- zip ----------
if not exist "%DIST%" mkdir "%DIST%" >nul 2>nul

set "VERSION="
for /f "tokens=2 delims== " %%A in ('findstr /R /C:"^version *=" "%ROOT%\engine-ffi\Cargo.toml" 2^>nul') do (
    if not defined VERSION set "VERSION=%%A"
)
set "VERSION=!VERSION:"=!"
set "VERSION=!VERSION: =!"
if not defined VERSION set "VERSION=0.0.0"

set "ZIP_NAME=SNI-Spoofer-Source-v!VERSION!.zip"
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