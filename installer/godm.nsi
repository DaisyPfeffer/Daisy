Unicode true

!include "MUI2.nsh"

!define APP_NAME "GoDM Download Manager"
!define APP_VERSION "2.0.0"
!define APP_PUBLISHER "GoDM"
!define APP_EXE "GoDM.exe"
!define APP_CLI "godm-cli.exe"

Name "\${APP_NAME}"
OutFile "GoDM-Setup-\${APP_VERSION}.exe"
InstallDir "$PROGRAMFILES64\GoDM"
InstallDirRegKey HKLM "Software\GoDM" "InstallDir"
RequestExecutionLevel admin
ShowInstDetails show
ShowUnInstDetails show

!define MUI_ABORTWARNING
!define MUI_ICON "godm.ico"
!define MUI_UNICON "godm.ico"
!define MUI_FINISHPAGE_RUN "$INSTDIR\${APP_EXE}"
!define MUI_FINISHPAGE_RUN_TEXT "Launch GoDM"

VIProductVersion "2.0.0.0"
VIAddVersionKey /LANG=\${LANG_ENGLISH} "ProductName" "\${APP_NAME}"
VIAddVersionKey /LANG=\${LANG_ENGLISH} "CompanyName" "\${APP_PUBLISHER}"
VIAddVersionKey /LANG=\${LANG_ENGLISH} "FileDescription" "\${APP_NAME} Setup"
VIAddVersionKey /LANG=\${LANG_ENGLISH} "FileVersion" "\${APP_VERSION}"
VIAddVersionKey /LANG=\${LANG_ENGLISH} "ProductVersion" "\${APP_VERSION}"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

Section "Install"
    SetShellVarContext all
    SetOutPath "$INSTDIR"

    File "..\installer\GoDM.exe"
    File "..\installer\godm-cli.exe"

    SetOutPath "$INSTDIR\extension"
    File /r "..\extension\*.*"

    SetOutPath "$INSTDIR"
    WriteUninstaller "$INSTDIR\Uninstall.exe"

    WriteRegStr HKLM "Software\GoDM" "InstallDir" "$INSTDIR"
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\GoDM" "DisplayName" "\${APP_NAME}"
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\GoDM" "DisplayVersion" "\${APP_VERSION}"
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\GoDM" "Publisher" "\${APP_PUBLISHER}"
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\GoDM" "InstallLocation" "$INSTDIR"
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\GoDM" "UninstallString" '"$INSTDIR\Uninstall.exe"'
    WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\GoDM" "NoModify" 1
    WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\GoDM" "NoRepair" 1

    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\App Paths\GoDM.exe" "" "$INSTDIR\GoDM.exe"

    CreateDirectory "$SMPROGRAMS\GoDM"
    CreateShortcut "$SMPROGRAMS\GoDM\GoDM.lnk" "$INSTDIR\GoDM.exe" "" "$INSTDIR\GoDM.exe" 0
    CreateShortcut "$SMPROGRAMS\GoDM\Uninstall GoDM.lnk" "$INSTDIR\Uninstall.exe"

    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\GoDM" "DisplayIcon" "$INSTDIR\GoDM.exe"
SectionEnd

Section "Uninstall"
    SetShellVarContext all

    Delete "$SMPROGRAMS\GoDM\GoDM.lnk"
    Delete "$SMPROGRAMS\GoDM\Uninstall GoDM.lnk"
    RMDir "$SMPROGRAMS\GoDM"

    Delete "$INSTDIR\GoDM.exe"
    Delete "$INSTDIR\godm-cli.exe"
    Delete "$INSTDIR\Uninstall.exe"
    RMDir /r "$INSTDIR\extension"
    RMDir "$INSTDIR"

    DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\App Paths\GoDM.exe"
    DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\GoDM"
    DeleteRegKey HKLM "Software\GoDM"

    ; User downloads and settings in %APPDATA%\godm are intentionally preserved.
SectionEnd
