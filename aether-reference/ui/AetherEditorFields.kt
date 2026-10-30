// Extracted from PattNG (https://github.com/patterniha/PattNG).
// Sources:
//   - V2rayNG/app/src/main/java/com/v2ray/ang/ui/server/ServerUiState.kt
//       constructor defaults: lines 89-118
//       Aether properties:  lines 173-214
//       toProfileItem:      lines 328-359 (Aether mapping) and 361-372 (command)
//       fromProfileItem:    lines 433-464 (normalization of persisted values)
//   - V2rayNG/app/src/main/java/com/v2ray/ang/ui/server/ServerAetherActivity.kt
//       field order / visibility: lines 195-596
//       validation mapping:       lines 657-703
//
// Compose/Android state (`mutableStateOf`, `Saver`, string resources) removed.
// What remains is the exact field list, defaults, visibility-relevant booleans
// and ProfileItem round-trip mapping used by the Aether editor.

package com.v2ray.ang.ui.server

import com.v2ray.ang.AetherDefaults
import com.v2ray.ang.core.AetherArgs
import com.v2ray.ang.core.AetherCore
import com.v2ray.ang.dto.entities.ProfileItem
import com.v2ray.ang.enums.AetherFingerprint
import com.v2ray.ang.enums.AetherIpVersion
import com.v2ray.ang.enums.AetherObfuscation
import com.v2ray.ang.enums.AetherProtocol
import com.v2ray.ang.enums.AetherPsiphon
import com.v2ray.ang.enums.AetherPsiphonCdnSet
import com.v2ray.ang.enums.AetherPsiphonMode
import com.v2ray.ang.enums.AetherScanMode
import com.v2ray.ang.enums.AetherTor
import com.v2ray.ang.enums.AetherTorBridges
import com.v2ray.ang.enums.AetherTorRelays
import com.v2ray.ang.enums.AetherTransport

/**
 * The Aether editor's durable state. Every default below is exactly the
 * ServerUiState constructor default, so a new Aether profile looks the same in
 * NekoGo as in PattNG.
 *
 * Fields are all `var` here for a direct port; in PattNG each one is a Compose
 * `mutableStateOf` property.
 */
data class AetherEditorFields(
    // Shared profile fields shown on the Aether screen
    var remarks: String = "",
    var address: String = "",
    var port: String = "",

    // Protocol / transport
    var aetherProtocol: String = AetherProtocol.WIREGUARD.type,
    var aetherTransport: String = AetherTransport.HTTP3.type,
    var aetherMasqueSni: String = AetherDefaults.AETHER_MASQUE_SNI,
    var aetherScanMode: String = AetherScanMode.BALANCED.type,
    var aetherObfuscation: String = AetherObfuscation.AUTO.type,
    var aetherFingerprint: String = AetherFingerprint.CHROME.type,
    var aetherIpVersion: String = AetherIpVersion.V4.type,

    // Two-hop endpoints
    var aetherWiwOuter: String = "",
    var aetherWiwInner: String = "",

    // HTTP/2 fragmentation
    var aetherFragment: Boolean = false,
    var aetherFragmentSize: String = "",
    var aetherFragmentDelay: String = "",

    // ECH
    var aetherEch: Boolean = false,
    var aetherEchDns: String = AetherDefaults.AETHER_ECH_DNS,
    var aetherEchDomain: String = AetherDefaults.AETHER_ECH_DOMAIN,

    // Tunnel DNS / exit rule
    var aetherDns: String = "",
    var aetherExitLoc: String = "",

    // Exit-node: freedom with finalMask/dialMode, or a profile's name
    var aetherExitNode: String = "",
    var finalMask: String = "",
    var dialMode: String = "",

    // Psiphon
    var aetherPsiphon: String = AetherPsiphon.OFF.type,
    var aetherPsiphonMode: String = AetherPsiphonMode.AUTO.type,
    var aetherPsiphonCdnIps: String = "",
    var aetherPsiphonCdnSni: String = "",
    var aetherPsiphonCdnSets: String = "",
    var aetherPsiphonRegion: String = "",
    var aetherPsiphonBundledList: Boolean = true,

    // Tor
    var aetherTor: String = AetherTor.OFF.type,
    var aetherTorBridges: String = AetherTorBridges.AUTO.type,
    var aetherTorBridgeLines: String = "",
    var aetherTorRelays: String = AetherTorRelays.AUTO.type,

    // Hand-written command
    var aetherCommand: String = "",

    // Shared with ordinary profiles; shown under "Other settings"
    var targetStrategy: String = "",
) {

    /** The CDN edge lists the Psiphon fronting scan tries, as chosen; none chosen means all of them. */
    val aetherPsiphonCdnSetChoice: Set<AetherPsiphonCdnSet>
        get() = AetherPsiphonCdnSet.parse(aetherPsiphonCdnSets).toSet()

    /** Chooses or drops one CDN edge list; the choice is kept in the order the core tries them. */
    fun setPsiphonCdnSet(set: AetherPsiphonCdnSet, chosen: Boolean) {
        val choice = aetherPsiphonCdnSetChoice.let { if (chosen) it + set else it - set }
        aetherPsiphonCdnSets = AetherPsiphonCdnSet.join(choice).orEmpty()
    }

    /**
     * Whether an Aether setting the editor keeps folded away holds a value of
     * its own, so that the folded section opens by itself and nothing set stays
     * out of sight.
     *
     * PattNG also opens it when targetStrategy differs from the profile default;
     * that comparison needs CoreOutboundBuilder.defaultTargetStrategy, which is
     * Xray outbound logic and is described in the README.
     */
    val hasOtherAetherSettings: Boolean
        get() = aetherDns.isNotBlank() || aetherExitLoc.isNotBlank() || targetStrategy.isNotBlank()

    /**
     * Builds the ProfileItem the editor holds, exactly as ServerUiState.toProfileItem
     * maps the Aether fields. `initial` supplies the non-Aether fields (GUID,
     * configType, subscription, etc.) unchanged.
     *
     * A command that says what the settings say is no command of its own: the
     * profile follows the settings. A different command is stored trimmed.
     */
    fun toProfileItem(initial: ProfileItem, listenPort: Int? = null): ProfileItem {
        val psiphonOn = aetherPsiphon != AetherPsiphon.OFF.type
        val torOn = aetherTor != AetherTor.OFF.type
        val profile = initial.copy(
            remarks = remarks,
            server = address,
            serverPort = port,
            finalMask = finalMask.nullIfBlank(),
            dialMode = dialMode.nullIfBlank(),
            aetherProtocol = aetherProtocol,
            aetherTransport = aetherTransport,
            // Kept whatever the protocol, as the transport is.
            aetherMasqueSni = aetherMasqueSni.nullIfBlank(),
            aetherScanMode = aetherScanMode,
            aetherObfuscation = aetherObfuscation,
            aetherFingerprint = aetherFingerprint,
            aetherIpVersion = aetherIpVersion,
            aetherWiwOuter = aetherWiwOuter.nullIfBlank(),
            aetherWiwInner = aetherWiwInner.nullIfBlank(),
            aetherFragment = aetherFragment,
            aetherFragmentSize = aetherFragmentSize.nullIfBlank(),
            aetherFragmentDelay = aetherFragmentDelay.nullIfBlank(),
            aetherEch = aetherEch,
            // Kept while ECH is off as well, as the WARP keys page keeps its own.
            aetherEchDns = aetherEchDns.nullIfBlank(),
            aetherEchDomain = aetherEchDomain.nullIfBlank(),
            aetherDns = aetherDns.nullIfBlank(),
            aetherExitLoc = aetherExitLoc.nullIfBlank(),
            aetherExitNode = aetherExitNode.nullIfBlank(),
            aetherPsiphon = if (psiphonOn) aetherPsiphon else null,
            aetherPsiphonMode = if (psiphonOn) aetherPsiphonMode else null,
            aetherPsiphonCdnIps = if (psiphonOn) aetherPsiphonCdnIps.nullIfBlank() else null,
            aetherPsiphonCdnSni = if (psiphonOn) aetherPsiphonCdnSni.nullIfBlank() else null,
            aetherPsiphonCdnSets = if (psiphonOn) aetherPsiphonCdnSets.nullIfBlank() else null,
            aetherPsiphonRegion = if (psiphonOn) aetherPsiphonRegion.nullIfBlank() else null,
            aetherPsiphonBundledList = if (psiphonOn && !aetherPsiphonBundledList) false else null,
            aetherTor = if (torOn) aetherTor else null,
            aetherTorBridges = if (torOn) aetherTorBridges else null,
            aetherTorBridgeLines = if (torOn) aetherTorBridgeLines.nullIfBlank() else null,
            aetherTorRelays = if (torOn) aetherTorRelays else null,
            aetherCommand = null,
            targetStrategy = targetStrategy.nullIfBlank(),
        )
        val command = aetherCommand.trim()
        val built = if (command.isEmpty() ||
            command == AetherCore.of(profile, listenPort ?: AetherArgs.socksPort).command
        ) {
            profile
        } else {
            profile.copy(aetherCommand = command)
        }
        return built
    }

    companion object {

        /**
         * The editor state for an existing ProfileItem. Values are normalized so
         * the dropdowns always hold one of their own values, whatever was
         * persisted; the MASQUE SNI and ECH fields are shown filled in with
         * their defaults so that what the core is told is in sight.
         */
        fun fromProfileItem(initial: ProfileItem): AetherEditorFields = AetherEditorFields(
            remarks = initial.remarks,
            address = initial.server ?: "",
            port = initial.serverPort ?: "",
            finalMask = initial.finalMask ?: "",
            dialMode = initial.dialMode ?: "",
            targetStrategy = initial.targetStrategy.orEmpty(),
            aetherProtocol = AetherProtocol.fromString(initial.aetherProtocol).type,
            aetherTransport = AetherTransport.fromString(initial.aetherTransport).type,
            aetherMasqueSni = initial.aetherMasqueSni.nullIfBlank() ?: AetherDefaults.AETHER_MASQUE_SNI,
            aetherScanMode = AetherScanMode.fromString(initial.aetherScanMode).type,
            aetherObfuscation = AetherObfuscation.fromString(initial.aetherObfuscation).type,
            aetherFingerprint = AetherFingerprint.fromString(initial.aetherFingerprint).type,
            aetherIpVersion = AetherIpVersion.fromString(initial.aetherIpVersion).type,
            aetherWiwOuter = initial.aetherWiwOuter ?: "",
            aetherWiwInner = initial.aetherWiwInner ?: "",
            aetherFragment = initial.aetherFragment ?: false,
            aetherFragmentSize = initial.aetherFragmentSize ?: "",
            aetherFragmentDelay = initial.aetherFragmentDelay ?: "",
            aetherEch = initial.aetherEch == true,
            // Shown filled in, so that what the core is told is in sight.
            aetherEchDns = initial.aetherEchDns.nullIfBlank() ?: AetherDefaults.AETHER_ECH_DNS,
            aetherEchDomain = initial.aetherEchDomain.nullIfBlank() ?: AetherDefaults.AETHER_ECH_DOMAIN,
            aetherDns = initial.aetherDns ?: "",
            aetherExitLoc = initial.aetherExitLoc ?: "",
            aetherExitNode = initial.aetherExitNode ?: "",
            aetherPsiphon = AetherPsiphon.fromString(initial.aetherPsiphon).type,
            aetherPsiphonMode = AetherPsiphonMode.fromString(initial.aetherPsiphonMode).type,
            aetherPsiphonCdnIps = initial.aetherPsiphonCdnIps ?: "",
            aetherPsiphonCdnSni = initial.aetherPsiphonCdnSni ?: "",
            aetherPsiphonCdnSets = initial.aetherPsiphonCdnSets ?: "",
            aetherPsiphonRegion = initial.aetherPsiphonRegion ?: "",
            aetherPsiphonBundledList = initial.aetherPsiphonBundledList != false,
            aetherTor = AetherTor.fromString(initial.aetherTor).type,
            aetherTorBridges = AetherTorBridges.fromString(initial.aetherTorBridges).type,
            aetherTorBridgeLines = initial.aetherTorBridgeLines ?: "",
            aetherTorRelays = AetherTorRelays.fromString(initial.aetherTorRelays).type,
            aetherCommand = initial.aetherCommand ?: "",
        )
    }
}

/** extension/_Ext.kt: nullIfBlank. */
private fun String?.nullIfBlank(): String? = this?.takeIf { it.isNotBlank() }
