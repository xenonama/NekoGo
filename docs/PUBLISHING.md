# Publishing a NekoGo Release

The Update Manager (`gui/updater.go`) is v2rayN-style: it only installs
NekoGo-specific binaries from the fork's own releases at
`github.com/xenonama/NekoGo`. Upstream `SagerNet/sing-box` and
`XTLS/Xray-core` releases are never used — `nekobox_core.exe` carries
the NekoBox Thrift server and `xray.exe` must match NekoBox's config
format, so upstream binaries would break Connect.

Until you publish a release, the updater reports:

- `singbox` / `xray`: `UpdateAvail=false`,
  `Notes="No NekoGo release published yet. Build from source with build.bat."`
- `gui`: `UpdateAvail=false`, `Notes="No NekoGo release published yet."`
- `geoip` / `geosite`: still work (independent sources:
  `Loyalsoldier/geoip`, `Loyalsoldier/v2ray-rules-dat`).

## Steps

1. **Build everything:**
   ```bat
   build.bat
   ```
   This produces `gui/build/bin/nekobox_core.exe`,
   `gui/build/bin/xray.exe`, `gui/build/bin/nekobox-gui.exe`, plus
   `dist/NekoGo-Setup-*.exe` and `dist/NekoGo-Portable-*.zip`.

2. **Create a release** on `github.com/xenonama/NekoGo`
   (Releases → Draft a new release).

3. **Upload as release assets:**
   - `gui/build/bin/nekobox_core.exe` (or a `*.zip` containing it)
   - `gui/build/bin/xray.exe` (or a `*.zip` containing it)
   - `dist/NekoGo-Setup-*.exe` and/or `dist/NekoGo-Portable-*.zip`

   Asset-name matching (case-insensitive):
   - sing-box: exact `nekobox_core.exe`, else `*.zip` containing
     `nekobox_core` in the name.
   - xray: exact `xray.exe`, else `*.zip` containing `xray` in the name.
   - GUI: name containing `NekoGo-Setup-`, `NekoGo-Portable-`,
     `nekogo`, or `setup`.

4. **Tag with the version from `VERSION`:**
   Current version is in the `VERSION` file at the repo root
   (e.g. `5.11.28.3-fork.1`). Tag the release with that version
   (a leading `v` is accepted — `v1.14.1` matches `1.14.1`).

5. **Verify:** open NekoGo → Updates → Check. Each row logs
   `[update:<id>] source=<repo> latest=<tag>` (e.g.
   `[update:singbox] source=xenonama/NekoGo latest=5.11.28.3-fork.1`).
   `singbox` / `xray` install to `gui/build/bin/` (next to the GUI
   exe, core must be disconnected first); the GUI installer downloads
   to `%APPDATA%\NekoGo\updates\` and is opened for you to run.
   GeoIP (`gui/assets/GeoLite2-Country.mmdb`) and GeoSite
   (`gui/assets/geosite.dat`) update from their independent sources.
