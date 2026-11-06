; The Windows installer, PCRemoteSetup.exe. Build it with Inno Setup 6:
;
;   iscc /DAppVersion=0.2.1 /DSourceDir=<unzipped release folder> ^
;        /DViGEmBusSetup=<ViGEmBus_1.22.0_x64_x86_arm64.exe> packaging\windows\pc-remote.iss
;
; It installs for the current user only, into %LOCALAPPDATA%\Programs, so it
; needs no administrator rights and `pc-remote-web update` can still replace
; the files later. Only two steps ask Windows for permission: the optional
; ViGEmBus driver, and the firewall rule that setup adds.

#ifndef AppVersion
  #error Pass the version: /DAppVersion=0.2.1
#endif
#ifndef SourceDir
  #error Pass the unzipped release folder: /DSourceDir=...
#endif
#ifndef ViGEmBusSetup
  #error Pass the ViGEmBus installer: /DViGEmBusSetup=...
#endif

[Setup]
AppId={{1425C116-D859-4F09-8553-2F28D0910FAC}
AppName=PC Remote
AppVersion={#AppVersion}
AppVerName=PC Remote {#AppVersion}
AppPublisher=Canberk Bekiroglu
AppPublisherURL=https://github.com/canberkbekir/omarchy-remote
AppSupportURL=https://github.com/canberkbekir/omarchy-remote/issues
AppUpdatesURL=https://github.com/canberkbekir/omarchy-remote/releases
PrivilegesRequired=lowest
DefaultDirName={autopf}\PC Remote
DisableProgramGroupPage=yes
DisableDirPage=auto
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
WizardStyle=modern
SetupIconFile=pc-remote.ico
UninstallDisplayIcon={app}\pc-remote.ico
UninstallDisplayName=PC Remote
OutputBaseFilename=PCRemoteSetup
OutputDir=..\..\dist
Compression=lzma2
SolidCompression=yes
LicenseFile={#SourceDir}\LICENSE

[Tasks]
Name: vigembus; Description: "Install the game controller driver (ViGEmBus). Windows asks for permission once."; Check: not ViGEmBusInstalled
Name: desktopicon; Description: "Add a desktop shortcut"; Flags: unchecked

[Files]
Source: "{#SourceDir}\pc-remote-web.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\ViGEmClient.dll"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\ViGEmClient-LICENSE.txt"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\LICENSE"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\README.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "pc-remote.ico"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#ViGEmBusSetup}"; DestDir: "{tmp}"; DestName: "ViGEmBusSetup.exe"; Tasks: vigembus; Flags: deleteafterinstall

[Icons]
; Setup shows the QR code and changes settings; the server itself starts at
; sign-in and lives in the tray.
Name: "{autoprograms}\PC Remote"; Filename: "{app}\pc-remote-web.exe"; Parameters: "setup"; IconFilename: "{app}\pc-remote.ico"; Comment: "Set up PC Remote, change its settings, or show the QR code"
Name: "{autodesktop}\PC Remote"; Filename: "{app}\pc-remote-web.exe"; Parameters: "setup"; IconFilename: "{app}\pc-remote.ico"; Tasks: desktopicon

[Run]
; The driver's installer needs administrator rights, so it goes through the
; shell, which asks Windows for permission.
Filename: "{tmp}\ViGEmBusSetup.exe"; Tasks: vigembus; Flags: shellexec waituntilterminated; StatusMsg: "Installing the game controller driver..."
; An update keeps the settings and starts the new server.
Filename: "{app}\pc-remote-web.exe"; Parameters: "setup --yes"; Check: HasSettings; Flags: runhidden waituntilterminated; StatusMsg: "Starting PC Remote..."
; A new install walks through setup, which ends with the QR code.
Filename: "{app}\pc-remote-web.exe"; Parameters: "setup"; Description: "Set up PC Remote now"; Check: not HasSettings; Flags: postinstall nowait skipifsilent

[Code]
function ViGEmBusInstalled: Boolean;
begin
  Result := RegKeyExists(HKLM, 'SYSTEM\CurrentControlSet\Services\ViGEmBus');
end;

function HasSettings: Boolean;
begin
  Result := FileExists(ExpandConstant('{userappdata}\pc-remote\config.toml'));
end;

// The running server holds its program file open: stop it before the files
// are replaced. setup --yes starts it again afterwards.
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  Code: Integer;
begin
  Exec(ExpandConstant('{sys}\schtasks.exe'), '/End /TN "PC Remote"', '', SW_HIDE, ewWaitUntilTerminated, Code);
  Exec(ExpandConstant('{sys}\taskkill.exe'), '/IM pc-remote-web.exe /F', '', SW_HIDE, ewWaitUntilTerminated, Code);
  Result := '';
end;

// Uninstalling stops the server and removes the logon task and the firewall
// rules (Windows asks for permission for those), then asks whether the
// settings and passcode go too. A silent uninstall keeps them.
procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  Params: String;
  Code: Integer;
begin
  if CurUninstallStep <> usUninstall then
    Exit;
  Params := 'uninstall';
  if not UninstallSilent and
     (MsgBox('Also delete your PC Remote settings and passcode?', mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES) then
    Params := 'uninstall --purge';
  Exec(ExpandConstant('{app}\pc-remote-web.exe'), Params, '', SW_HIDE, ewWaitUntilTerminated, Code);
end;
