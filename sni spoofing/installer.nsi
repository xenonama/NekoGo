; ============================================================
;  SNI Spoofer — NSIS installer script
;  Built automatically by build.bat
; ============================================================

!ifndef APP_VERSION
    !define APP_VERSION "2.0.1"
!endif

!ifndef PORTABLE_STAGE
    !error "PORTABLE_STAGE must be defined by build.bat"
!endif

!ifndef DIST
    !error "DIST must be defined by build.bat"
!endif

!define APP_NAME        "SNI Spoofer"
!define APP_PUBLISHER   "xenonama"
!define APP_URL         "https://github.com/xenonama/SNI-Spoofing-rust"
!define APP_EXE         "sni-gui.exe"
!define UNINST_KEY      "Software\Microsoft\Windows\CurrentVersion\Uninstall\SNISpoofer"

Name "${APP_NAME} ${APP_VERSION}"
OutFile "${DIST}\SNI-Spoofer-Setup-v${APP_VERSION}.exe"
InstallDir "$PROGRAMFILES64\${APP_NAME}"
InstallDirRegKey HKLM "Software\${APP_NAME}" "InstallDir"
RequestExecutionLevel admin
Unicode true
SetCompressor /SOLID lzma

; ---------- UI ----------
!include "MUI2.nsh"
!define MUI_ABORTWARNING
!define MUI_ICON   "gui\app.ico"
!define MUI_UNICON "gui\app.ico"
!define MUI_FINISHPAGE_RUN "$INSTDIR\${APP_EXE}"
!define MUI_FINISHPAGE_RUN_TEXT "Run SNI Spoofer now (as Administrator)"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

; ---------- Install ----------
Section "Main" SecMain
    SetOutPath "$INSTDIR"

    File "${PORTABLE_STAGE}\sni-gui.exe"
    File "${PORTABLE_STAGE}\sni_engine.dll"
    File "${PORTABLE_STAGE}\sni-spoof.exe"

    IfFileExists "${PORTABLE_STAGE}\WinDivert.dll" 0 +2
        File "${PORTABLE_STAGE}\WinDivert.dll"
    IfFileExists "${PORTABLE_STAGE}\WinDivert64.sys" 0 +2
        File "${PORTABLE_STAGE}\WinDivert64.sys"

    IfFileExists "${PORTABLE_STAGE}\app.png" 0 +2
        File "${PORTABLE_STAGE}\app.png"

    IfFileExists "${PORTABLE_STAGE}\ip_list.txt" 0 +2
        File "${PORTABLE_STAGE}\ip_list.txt"
    IfFileExists "${PORTABLE_STAGE}\sni_list.txt" 0 +2
        File "${PORTABLE_STAGE}\sni_list.txt"

    IfFileExists "${PORTABLE_STAGE}\README.txt" 0 +2
        File "${PORTABLE_STAGE}\README.txt"

    CreateDirectory "$SMPROGRAMS\${APP_NAME}"
    CreateShortcut "$SMPROGRAMS\${APP_NAME}\${APP_NAME}.lnk" "$INSTDIR\${APP_EXE}" "" "$INSTDIR\${APP_EXE}" 0 SW_SHOWNORMAL "" "Run SNI Spoofer"
    CreateShortcut "$SMPROGRAMS\${APP_NAME}\Uninstall.lnk" "$INSTDIR\uninstall.exe"
    CreateShortcut "$DESKTOP\${APP_NAME}.lnk" "$INSTDIR\${APP_EXE}" "" "$INSTDIR\${APP_EXE}" 0 SW_SHOWNORMAL "" "Run SNI Spoofer"

    WriteRegStr HKLM "Software\${APP_NAME}" "InstallDir" "$INSTDIR"
    WriteRegStr HKLM "${UNINST_KEY}" "DisplayName"     "${APP_NAME}"
    WriteRegStr HKLM "${UNINST_KEY}" "DisplayVersion"  "${APP_VERSION}"
    WriteRegStr HKLM "${UNINST_KEY}" "Publisher"       "${APP_PUBLISHER}"
    WriteRegStr HKLM "${UNINST_KEY}" "URLInfoAbout"    "${APP_URL}"
    WriteRegStr HKLM "${UNINST_KEY}" "UninstallString" "$INSTDIR\uninstall.exe"
    WriteRegStr HKLM "${UNINST_KEY}" "QuietUninstallString" "$INSTDIR\uninstall.exe /S"
    WriteRegDWORD HKLM "${UNINST_KEY}" "NoModify" 1
    WriteRegDWORD HKLM "${UNINST_KEY}" "NoRepair" 1

    WriteUninstaller "$INSTDIR\uninstall.exe"
SectionEnd

; ---------- Uninstall ----------
Section "Uninstall"
    Delete "$INSTDIR\sni-gui.exe"
    Delete "$INSTDIR\sni_engine.dll"
    Delete "$INSTDIR\sni-spoof.exe"
    Delete "$INSTDIR\WinDivert.dll"
    Delete "$INSTDIR\WinDivert64.sys"
    Delete "$INSTDIR\app.png"
    Delete "$INSTDIR\ip_list.txt"
    Delete "$INSTDIR\sni_list.txt"
    Delete "$INSTDIR\README.txt"
    Delete "$INSTDIR\config.json"
    Delete "$INSTDIR\uninstall.exe"

    Delete "$SMPROGRAMS\${APP_NAME}\${APP_NAME}.lnk"
    Delete "$SMPROGRAMS\${APP_NAME}\Uninstall.lnk"
    RMDir  "$SMPROGRAMS\${APP_NAME}"
    Delete "$DESKTOP\${APP_NAME}.lnk"

    RMDir "$INSTDIR"

    DeleteRegKey HKLM "${UNINST_KEY}"
    DeleteRegKey HKLM "Software\${APP_NAME}"
SectionEnd