# Publishing a NekoGo Release

The in-app Updates panel has exactly eight rows. Each row installs from the
project that publishes its binary or data.

| Row | Source | Asset or data it uses |
| --- | --- | --- |
| GeoIP Database (`geoip`) | `Loyalsoldier/geoip`, `release` branch via jsDelivr | `Country.mmdb` |
| GeoSite Rule-set (`geosite`) | `Loyalsoldier/v2ray-rules-dat` | `geosite.dat` |
| official sing-box (`singbox`) | `SagerNet/sing-box` | `sing-box-<version>-windows-<arch>.zip` |
| Xray (`xray`) | `XTLS/Xray-core` | `Xray-windows-<arch>.zip` |
| mihomo (`mihomo`) | `MetaCubeX/mihomo` | the STABLE release's exact baseline Windows zip |
| Aether (`psiphon`) | `CluvexStudio/Aether` | `aether-windows-x86_64.zip` |
| Psiphon Servers (`psiphon-servers`) | Psiphon's signed server list | `server_list_compressed`, stored as `psiphon_servers.dat` |
| NekoGo (`nekogo`) | `github.com/xenonama/NekoGo` | exactly `nekogo.exe` |

Names are matched case-insensitively. The NekoGo row accepts only the raw
`nekogo.exe` asset; setup executables, portable archives, and similarly named
files are not updater assets. There is no NekoBox core updater row and no
legacy GUI-installer row. Tor is runtime/diagnostics only and has no updater
row.

All rows except NekoGo resolve from their own upstream or data sources and
need no assets in the NekoGo release.

## Update behavior

Core and data rows download while the current connection stays up. Applying a
core update disconnects and reconnects only when the exact target image is in
use; an update to an unused image does not interrupt the active profile.

The NekoGo row downloads `nekogo.exe`, verifies the download (against GitHub's
published SHA-256 digest when available), and stages a post-exit helper. After
NekoGo exits, the helper rechecks the staged image's size and hash, replaces the
running executable with a `.bak` backup, and rolls back if replacement or
verification fails. It then relaunches NekoGo, passing `--connect <profile-id>`
to reconnect the prior profile when one was active.

Before a NekoGo release is published, its row reports `UpdateAvail=false` and
`Notes="No NekoGo release published yet."` If the newest release lacks the
exact `nekogo.exe` asset, it reports `UpdateAvail=false` and
`Notes="nekogo.exe is not published in this release"`. The other seven rows
continue to use their independent sources.

## Steps

1. **Build the release artifacts:**
   ```bat
   build.bat
   ```
   This produces `gui\build\bin\nekogo.exe` and publishes the raw self-update
   asset as `dist\nekogo.exe`. For version `1.0.0-beta.4`, that file sits beside
   `dist\NekoGo-Setup-v1.0.0-beta.4.exe`; the optional portable ZIP may also be
   produced. Neither package is consumed by the Updates panel.

2. **Create a release** on `github.com/xenonama/NekoGo`
   (Releases → Draft a new release).

3. **Upload the required release asset:**
   - `dist\nekogo.exe`

   Keep the filename exactly `nekogo.exe`. No `nekobox_core.exe`,
   `NekoGo-Setup-*.exe`, or `NekoGo-Portable-*.zip` upload is needed by the
   Updates panel.

4. **Tag with the version from `VERSION`:**
   The tag must match the version embedded by `build.bat` (for example,
   `1.0.0-beta.4`). A leading `v` is accepted, so `v1.0.0-beta.4` matches
   `1.0.0-beta.4`. The NekoGo row compares the tag with its link-time
   `appVersion`; unstamped builds fall back to the repository's `VERSION`
   file. A local build ahead of the published tag gets
   `Notes="This build is newer than the published release"`.
   The row reads the newest release via `/releases?per_page=1`, so prereleases
   remain visible; `/releases/latest` would hide them.

5. **Verify:** open NekoGo → Updates → Check. Confirm the eight rows listed
   above and inspect the per-row logs, which use
   `[update:<id>] source=<repo> latest=<tag>` (for example,
   `[update:nekogo] source=xenonama/NekoGo latest=1.0.0-beta.4`).
   A NekoGo update restarts the app to install the raw executable and then
   reconnects the prior profile when applicable.
