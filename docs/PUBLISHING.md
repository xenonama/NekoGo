# Publishing a NekoGo Release

The Update Manager (`gui/updater.go`) is v2rayN-style: every row installs
from the repo that publishes that binary.

| Row | Source | Asset it looks for |
| --- | --- | --- |
| `nekobox` | `github.com/xenonama/NekoGo` | `nekobox_core.exe`, else a `*.zip` named `*nekobox_core*` |
| `xray` | `github.com/xenonama/NekoGo` | `xray.exe`, else a `*.zip` named `*xray*` |
| `gui` | `github.com/xenonama/NekoGo` | `NekoGo-Setup-*.exe`, else `NekoGo-Portable-*.zip`, else any `*nekogo*.exe`/`.zip` |
| `singbox` | `SagerNet/sing-box` | the official Windows zip (Settings → Connection → Core engine = upstream) |
| `mihomo` | `MetaCubeX/mihomo` | the STABLE release's Windows zip |
| `geoip` / `geosite` | `Loyalsoldier/geoip`, `Loyalsoldier/v2ray-rules-dat` | `Country.mmdb`, `geosite.dat` |

Names are matched case-insensitively, and the GUI patterns are anchored to
an extension, so an asset like `NekoGo-Setup-notes.txt` is never offered
as an installer.

The fork's own `nekobox_core.exe` carries the NekoBox Thrift server, and
`xray.exe` must match the config format NekoBox writes, so those two rows
never fall back to an upstream release — publishing them is part of this
procedure. `singbox`, `mihomo`, `geoip` and `geosite` resolve from their
own projects and need nothing here.

Until you publish a release, the fork's rows report:

- `gui`: `UpdateAvail=false`, `Notes="No NekoGo release published yet."`
- `nekobox` / `xray`: `UpdateAvail=false`,
  `Notes="No core release published yet — manual update only (run build.bat)."`
- `geoip` / `geosite`: still work (independent sources).

## Steps

1. **Build everything:**
   ```bat
   build.bat
   ```
   This produces `gui/build/bin/nekobox_core.exe`,
   `gui/build/bin/xray.exe` and `gui/build/bin/nekogo.exe` (plus
   `sni-spoof.exe` and the optional engines), and
   `dist/NekoGo-Setup-*.exe` / `dist/NekoGo-Portable-*.zip`.

2. **Create a release** on `github.com/xenonama/NekoGo`
   (Releases → Draft a new release).

3. **Upload as release assets:**
   - `gui/build/bin/nekobox_core.exe` (or a `*.zip` containing it)
   - `gui/build/bin/xray.exe` (or a `*.zip` containing it)
   - `dist/NekoGo-Setup-*.exe` and/or `dist/NekoGo-Portable-*.zip`

   Keep the `NekoGo-Setup-` and `NekoGo-Portable-` prefixes exactly as
   `build.bat` writes them — the `gui` row matches on them.

4. **Tag with the version from `VERSION`:**
   The current version is the `VERSION` file at the repo root
   (e.g. `1.0.0-beta.3`). Tag the release with that version — a leading
   `v` is accepted, so `v1.0.0-beta.3` matches `1.0.0-beta.3`. The tag is
   what the GUI compares against its own link-time `appVersion`, and a
   local build that is ahead of the published tag gets
   `Notes="This build is newer than the published release"`.
   The `nekobox`, `xray` and `gui` rows all read the fork's newest
   release via `/releases?per_page=1`, so marking the release as a
   prerelease keeps it visible; `/releases/latest` would hide it.

5. **Verify:** open NekoGo → Updates → Check. Each row logs
   `[update:<id>] source=<repo> latest=<tag>` (e.g.
   `[update:nekobox] source=xenonama/NekoGo latest=1.0.0-beta.3`).
   `nekobox` / `xray` replace the engine binaries in place — the updater
   refuses while a core is running, so disconnect first — and the GUI
   installer downloads to `%APPDATA%\NekoGo\updates\`, where it is run
   only when you press its **Open** button, never automatically.
   GeoIP (`gui/assets/GeoLite2-Country.mmdb`) and GeoSite
   (`gui/assets/geosite.dat`) update from their independent sources.
