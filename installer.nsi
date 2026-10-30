; NekoGo NSIS installer
; Built by build.bat:
;   makensis /DAPP_VERSION=... /DAPP_VERSION_VI=... /DPORTABLE_STAGE=... \
;            /DDIST=... /DOUTFILE=... installer.nsi
; Installs GUI + both cores. Requires admin (WinDivert/TUN).

Unicode true

!ifndef APP_VERSION
  !define APP_VERSION "1.0.0-beta.1"
!endif
; VIProductVersion only accepts 4 plain numbers; build.bat derives this
; from the version tag ("1.0.0-beta.1" -> "1.0.0.0").
!ifndef APP_VERSION_VI
  !define APP_VERSION_VI "1.0.0.0"
!endif
!ifndef PORTABLE_STAGE
  !define PORTABLE_STAGE "dist\portable"
!endif
!ifndef DIST
  !define DIST "dist"
!endif
; build.bat compiles to a temporary name and only publishes the setup
; after makensis succeeded; a bare makensis run uses the normal name.
!ifndef OUTFILE
  !define OUTFILE "${DIST}\NekoGo-Setup-v${APP_VERSION}.exe"
!endif

Name "NekoGo ${APP_VERSION}"
OutFile "${OUTFILE}"
InstallDir "$PROGRAMFILES64\NekoGo"
RequestExecutionLevel admin
SetCompressor /SOLID lzma

!include "x64.nsh"

; Real version resource, so Explorer -> Properties and Apps & features
; show the build instead of a blank file version.
VIProductVersion "${APP_VERSION_VI}"
VIAddVersionKey /LANG=1033 "ProductName" "NekoGo"
VIAddVersionKey /LANG=1033 "ProductVersion" "${APP_VERSION}"
VIAddVersionKey /LANG=1033 "FileVersion" "${APP_VERSION}"
VIAddVersionKey /LANG=1033 "FileDescription" "NekoGo Setup"
VIAddVersionKey /LANG=1033 "LegalCopyright" "GPL-3.0"

!include "MUI2.nsh"
!define MUI_ABORTWARNING
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

; App + engines are x64 only, and the uninstall entry must live in the
; native registry view. Older builds wrote it from the 32-bit view
; (WOW6432Node); the upgrade path checks both so it never lands in a
; second folder. InstallDirRegKey is not used: its view depended on
; where SetRegView ran, which made upgrades read the wrong key.
Function .onInit
  ${IfNot} ${RunningX64}
    MessageBox MB_ICONSTOP|MB_OK "NekoGo requires 64-bit Windows." /SD IDOK
    Abort
  ${EndIf}
  SetRegView 64
  ; $INSTDIR still holds the compile-time default unless the user passed /D=.
  StrCmp $INSTDIR "$PROGRAMFILES64\NekoGo" 0 checkDone
  ReadRegStr $0 HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\NekoGo" "InstallLocation"
  ${If} $0 == ""
    SetRegView 32
    ReadRegStr $0 HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\NekoGo" "InstallLocation"
    SetRegView 64
  ${EndIf}
  StrCmp $0 "" checkDone
  StrCpy $INSTDIR $0
checkDone:
FunctionEnd

; Windows refuses to replace a running exe, and NSIS then offers "Ignore",
; which silently leaves the PREVIOUS build installed - an update that looks
; like it did nothing. Stop and ask before touching any file. Engine names
; here must stay unambiguous: xray/sing-box/aether are generic image names
; and would trip on unrelated software.
; Shared body: installer and uninstaller are separate code sections, so
; each needs its own function around the same macro.
!macro AbortIfRunningBody
retry:
  ; /C: patterns must stay unquoted: findstr keeps quotes as part of the
  ; pattern, so /C:"nekogo.exe" never matches. Full path avoids PATH shadowing.
  nsExec::ExecToStack 'cmd /c tasklist /NH | %SystemRoot%\System32\findstr.exe /I /C:nekogo.exe /C:nekobox_core.exe /C:sni-spoof.exe /C:psiphon-tunnel-core.exe /C:lyrebird.exe'
  Pop $0 ; exit code (0 = a process matched)
  Pop $1 ; matching tasklist lines
  StrCmp $0 "0" 0 done
  MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION \
    "NekoGo is still running:$\r$\n$\r$\n$1$\r$\nClose NekoGo completely (tray icon too), then click Retry.$\r$\nCancel leaves the installed version untouched." \
    /SD IDCANCEL IDRETRY retry
  Abort
done:
!macroend

Function AbortIfRunning
  !insertmacro AbortIfRunningBody
FunctionEnd

Function un.AbortIfRunning
  !insertmacro AbortIfRunningBody
FunctionEnd

Section "Install"
  Call AbortIfRunning
  SetOutPath "$INSTDIR"
  File "${PORTABLE_STAGE}\nekogo.exe"
  File "${PORTABLE_STAGE}\README.txt"
  ; Every engine lives in bin\ (PattN-style layout) so the GUI resolves
  ; them from one place and updates install there too.
  SetOutPath "$INSTDIR\bin"
  File "${PORTABLE_STAGE}\bin\nekobox_core.exe"
  File "${PORTABLE_STAGE}\bin\xray.exe"
  ; SNI-spoof engine (required for CoreSpoof profiles)
  File /nonfatal "${PORTABLE_STAGE}\bin\sni-spoof.exe"
  ; Aether engine (optional; pt\ required for Tor/Psiphon)
  File /nonfatal "${PORTABLE_STAGE}\bin\aether.exe"
  ; Official sing-box CLI (optional; Upstream core engine)
  File /nonfatal "${PORTABLE_STAGE}\bin\sing-box.exe"
  ; WinDivert is loaded by the engines, so it lives in bin\ next to them.
  File /nonfatal "${PORTABLE_STAGE}\bin\WinDivert.dll"
  File /nonfatal "${PORTABLE_STAGE}\bin\WinDivert64.sys"
  ; One GeoIP copy, next to the engines (the GUI resolves bin\ first and
  ; the in-app updater rewrites this file). Older installs had it in the
  ; install root; that copy is removed below.
  File /nonfatal "${PORTABLE_STAGE}\bin\GeoLite2-Country.mmdb"
  SetOutPath "$INSTDIR\bin\pt"
  File /nonfatal "${PORTABLE_STAGE}\bin\pt\lyrebird.exe"
  File /nonfatal "${PORTABLE_STAGE}\bin\pt\psiphon-tunnel-core.exe"
  SetOutPath "$INSTDIR"
  ; Drop the pre-bin copy so an upgrade cannot leave two databases.
  Delete "$INSTDIR\GeoLite2-Country.mmdb"

  WriteUninstaller "$INSTDIR\uninstall.exe"
  ; Upgrades from builds that registered from the 32-bit view would
  ; otherwise leave a second, dead entry in Apps & features.
  SetRegView 32
  DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\NekoGo"
  SetRegView 64
  CreateDirectory "$SMPROGRAMS\NekoGo"
  CreateShortcut "$SMPROGRAMS\NekoGo\NekoGo.lnk" "$INSTDIR\nekogo.exe"
  CreateShortcut "$DESKTOP\NekoGo.lnk" "$INSTDIR\nekogo.exe"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\NekoGo" \
    "DisplayName" "NekoGo ${APP_VERSION}"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\NekoGo" \
    "DisplayIcon" "$INSTDIR\nekogo.exe"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\NekoGo" \
    "UninstallString" "$INSTDIR\uninstall.exe"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\NekoGo" \
    "InstallLocation" "$INSTDIR"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\NekoGo" \
    "DisplayVersion" "${APP_VERSION}"
  WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\NekoGo" \
    "NoModify" 1
  WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\NekoGo" \
    "NoRepair" 1
SectionEnd

Section "Uninstall"
  ; A running NekoGo holds its exes open; refuse to "uninstall" nothing.
  Call un.AbortIfRunning
  ; The uninstaller is a new process: pick the native view (older builds
  ; registered from the 32-bit view; remove both keys).
  SetRegView 64
  ; Config lives in %APPDATA%\NekoGo; keep it (do not delete user data).
  Delete "$INSTDIR\nekogo.exe"
  Delete "$INSTDIR\bin\nekobox_core.exe"
  Delete "$INSTDIR\bin\sing-box.exe"
  Delete "$INSTDIR\bin\xray.exe"
  Delete "$INSTDIR\bin\sni-spoof.exe"
  Delete "$INSTDIR\bin\aether.exe"
  Delete "$INSTDIR\bin\pt\lyrebird.exe"
  Delete "$INSTDIR\bin\pt\psiphon-tunnel-core.exe"
  RMDir  "$INSTDIR\bin\pt"
  Delete "$INSTDIR\bin\WinDivert.dll"
  Delete "$INSTDIR\bin\WinDivert64.sys"
  Delete "$INSTDIR\bin\GeoLite2-Country.mmdb"
  RMDir  "$INSTDIR\bin"
  ; Legacy (pre-bin) engine layout, in case this install upgraded one.
  Delete "$INSTDIR\nekobox_core.exe"
  Delete "$INSTDIR\sing-box.exe"
  Delete "$INSTDIR\xray.exe"
  Delete "$INSTDIR\sni-spoof.exe"
  Delete "$INSTDIR\aether.exe"
  Delete "$INSTDIR\pt\lyrebird.exe"
  Delete "$INSTDIR\pt\psiphon-tunnel-core.exe"
  RMDir  "$INSTDIR\pt"
  Delete "$INSTDIR\README.txt"
  Delete "$INSTDIR\GeoLite2-Country.mmdb"
  Delete "$INSTDIR\WinDivert.dll"
  Delete "$INSTDIR\WinDivert64.sys"
  Delete "$INSTDIR\uninstall.exe"
  Delete "$SMPROGRAMS\NekoGo\NekoGo.lnk"
  Delete "$DESKTOP\NekoGo.lnk"
  RMDir "$SMPROGRAMS\NekoGo"
  RMDir "$INSTDIR"
  DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\NekoGo"
  SetRegView 32
  DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\NekoGo"
SectionEnd
