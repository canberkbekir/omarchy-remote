# winget manifest (draft)

These files are for [winget-pkgs](https://github.com/microsoft/winget-pkgs). They are filled in for v0.2.0 but not submitted yet.

The package installs the release zip `pc-remote-windows-amd64.zip` as a portable app. winget unpacks the whole zip, so `ViGEmClient.dll` stays next to `pc-remote-web.exe`, and it adds the `pc-remote-web` command to PATH.

## Before a submission

1. In all three files, replace `0.0.0` with the release version. The tag has a `v` in front (`v1.2.3`), but `PackageVersion` doesn't (`1.2.3`).
2. In the installer manifest, set `InstallerSha256` to the zip's line in the release's `checksums.txt`, in upper case, and set `ReleaseDate`.
3. Check the files: `winget validate --manifest packaging\winget`
4. Test the install in a Windows VM: `winget install --manifest packaging\winget`, then `pc-remote-web setup`.
5. Copy the files to `manifests/c/canberkbekir/OmarchyRemote/<version>/` in a fork of winget-pkgs and open a pull request. `wingetcreate update canberkbekir.OmarchyRemote --version <version> --urls <zip url>` does steps 1, 2 and 5 for later releases.

## Updates

`pc-remote-web update` won't update a winget copy. It tells you to run `winget upgrade canberkbekir.OmarchyRemote` instead, so winget's record of the installed version stays right.

Checked in the Windows 11 VM with v0.2.0 (winget 1.29):

- `winget validate` passes, and `winget install --manifest` installs to `%LOCALAPPDATA%\Microsoft\WinGet\Packages\canberkbekir.OmarchyRemote__DefaultSource\` with the DLL next to the exe and adds the `pc-remote-web` alias (in a new shell).
- `setup` from that copy works; the Task Scheduler task points at the exe in the package folder, whose name has no version in it.
- **winget can't upgrade or reinstall while the server runs**: it fails with `remove_all: Access is denied` and leaves the running copy in place. Stop the server first (`schtasks /End /TN "PC Remote"`, then end `pc-remote-web.exe` in Task Manager, since /End only stops the console), upgrade, then `schtasks /Run /TN "PC Remote"`.
