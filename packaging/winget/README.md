# winget manifest (draft)

These files are a draft for [winget-pkgs](https://github.com/microsoft/winget-pkgs). They aren't submitted yet.

The package installs the release zip `pc-remote-windows-amd64.zip` as a portable app. winget unpacks the whole zip, so `ViGEmClient.dll` stays next to `pc-remote-web.exe`, and it adds the `pc-remote-web` command to PATH.

## Before a submission

1. In all three files, replace `0.0.0` with the release version. The tag has a `v` in front (`v1.2.3`), but `PackageVersion` doesn't (`1.2.3`).
2. In the installer manifest, set `InstallerSha256` to the zip's line in the release's `checksums.txt`, in upper case, and set `ReleaseDate`.
3. Check the files: `winget validate --manifest packaging\winget`
4. Test the install in a Windows VM: `winget install --manifest packaging\winget`, then `pc-remote-web setup`.
5. Copy the files to `manifests/c/canberkbekir/OmarchyRemote/<version>/` in a fork of winget-pkgs and open a pull request. `wingetcreate update canberkbekir.OmarchyRemote --version <version> --urls <zip url>` does steps 1, 2 and 5 for later releases.

## Updates

`pc-remote-web update` won't update a winget copy. It tells you to run `winget upgrade canberkbekir.OmarchyRemote` instead, so winget's record of the installed version stays right.

Not checked yet, so test both in the VM before submitting:

- The Task Scheduler task from `pc-remote-web setup` points at the exe in winget's package folder. That folder is named after the package, not the version, so the task should keep working after an upgrade.
- winget can't replace the exe while the server runs from it. The upgrade may fail until the server is stopped.
