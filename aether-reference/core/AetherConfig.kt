// Extracted from PattNG (https://github.com/patterniha/PattNG).
// Source: V2rayNG/app/src/main/java/com/v2ray/ang/dto/entities/ProfileItem.kt lines 7-166.
// Android-only imports and non-Aether fields removed.
//
// IMPORTANT: PattNG has no nested AetherConfig class. The Aether settings are
// top-level `aether*` properties of ProfileItem. This file distils exactly those
// properties into one struct-shaped reference for the NekoGo port. The field
// names, types and nullability match ProfileItem, and comments are the PattNG
// comments, so the Go port can keep the same meaning for each field.
//
// `server`, `serverPort`, `finalMask`, `dialMode` and `targetStrategy` are shared
// with ordinary profiles but also drive the Aether core (endpoint / exit-node /
// routing), so they are included here.

package com.v2ray.ang.dto.entities

data class AetherConfig(
    // ---------------------------------------------------------------------
    // Link / profile identity
    // ---------------------------------------------------------------------
    var remarks: String = "",

    /** Single endpoint of a one-hop protocol (`--peer`). Null for two-hop protocols. */
    var server: String? = null,
    var serverPort: String? = null,

    // ---------------------------------------------------------------------
    // Protocol, transport, scan
    // ---------------------------------------------------------------------
    /** AetherProtocol.type: "masque", "wg", "gool", "mim", "wg-over-masque". */
    var aetherProtocol: String? = null,

    /** AetherTransport.type: "h3" or "h2". */
    var aetherTransport: String? = null,

    /**
     * The server name the MASQUE handshakes put in their ClientHello, the core's
     * `--masque-sni`; null means AetherDefaults.AETHER_MASQUE_SNI. Only a MASQUE
     * tunnel takes it.
     */
    var aetherMasqueSni: String? = null,

    /** AetherScanMode.type: "turbo", "balanced", "thorough", "verified", "ironclad". */
    var aetherScanMode: String? = null,

    /** AetherObfuscation.type: "auto", "off", "light", "firewall", "balanced", "gfw", "aggressive". */
    var aetherObfuscation: String? = null,

    /**
     * AetherFingerprint.type: "chrome", "firefox", "semi-python", "go".
     * The ClientHello of the core's TLS handshakes: its TLS 1.2 cipher suites
     * and whether it carries GREASE; null means Chrome's. Only a MASQUE tunnel
     * takes it.
     */
    var aetherFingerprint: String? = null,

    /** AetherIpVersion.type: "v4", "v6", "both". */
    var aetherIpVersion: String? = null,

    // ---------------------------------------------------------------------
    // Two-hop endpoints (gool / mim / wg-over-masque)
    // ---------------------------------------------------------------------
    var aetherWiwOuter: String? = null,
    var aetherWiwInner: String? = null,

    // ---------------------------------------------------------------------
    // HTTP/2 fragmentation (MASQUE over HTTP/2 only)
    // ---------------------------------------------------------------------
    var aetherFragment: Boolean? = null,
    var aetherFragmentSize: String? = null,
    var aetherFragmentDelay: String? = null,

    // ---------------------------------------------------------------------
    // Encrypted Client Hello (MASQUE only)
    // ---------------------------------------------------------------------
    /** Whether the MASQUE handshake hides its server name with ECH; null means it does not. */
    var aetherEch: Boolean? = null,

    /** Resolver for the ECH key: udp://, tcp:// or https://; null means AetherDefaults.AETHER_ECH_DNS. */
    var aetherEchDns: String? = null,

    /** Domain whose HTTPS record gives the ECH key; null means AetherDefaults.AETHER_ECH_DOMAIN. */
    var aetherEchDomain: String? = null,

    // ---------------------------------------------------------------------
    // Tunnel DNS / exit rule
    // ---------------------------------------------------------------------
    /** Resolvers used inside the tunnel, comma-separated; null means the core's own. */
    var aetherDns: String? = null,

    /** Country codes to allow, or with a leading ! to refuse; null means any exit. */
    var aetherExitLoc: String? = null,

    // ---------------------------------------------------------------------
    // Exit-node selection
    // ---------------------------------------------------------------------
    /**
     * Name of the profile the core dials out through: that profile's outbound
     * becomes the Xray outbound tagged exit-node. Null means freedom, with the
     * finalMask and dialMode of this profile. No link carries it.
     */
    var aetherExitNode: String? = null,

    /**
     * Legacy loopback port from before every core listened on the Aether listen
     * port of the settings. Nothing reads it any more; kept so stored profiles
     * round-trip unchanged.
     */
    var aetherListenPort: String? = null,

    // ---------------------------------------------------------------------
    // Psiphon carrier
    // ---------------------------------------------------------------------
    /** AetherPsiphon.type: "off" (stored as null), "chain", "reverse", "only". */
    var aetherPsiphon: String? = null,
    var aetherPsiphonMode: String? = null,
    var aetherPsiphonCdnIps: String? = null,
    var aetherPsiphonCdnSni: String? = null,

    /** AetherPsiphonCdnSet types, comma-separated; null means all built-in edge lists. */
    var aetherPsiphonCdnSets: String? = null,
    var aetherPsiphonRegion: String? = null,

    /** Whether Psiphon starts from the bundled server list; null means yes, false means fetch a fresh list first. */
    var aetherPsiphonBundledList: Boolean? = null,

    // ---------------------------------------------------------------------
    // Tor carrier
    // ---------------------------------------------------------------------
    /** AetherTor.type: "off" (stored as null), "chain", "reverse", "only". */
    var aetherTor: String? = null,

    /** AetherTorBridges.type: "auto", "first", "never", "own". */
    var aetherTorBridges: String? = null,

    /** Bridge lines, one per line as torrc writes them; used when aetherTorBridges == "own". */
    var aetherTorBridgeLines: String? = null,

    /** AetherTorRelays.type: "auto", "only", "off". */
    var aetherTorRelays: String? = null,

    // ---------------------------------------------------------------------
    // Hand-written command line
    // ---------------------------------------------------------------------
    /** Custom command line, run verbatim; null follows the settings above. */
    var aetherCommand: String? = null,

    // ---------------------------------------------------------------------
    // Ordinary-profile fields that also configure an Aether profile
    // ---------------------------------------------------------------------
    /** Raw JSON object used by the freedom exit-node (`--fm` in a link). */
    var finalMask: String? = null,

    /** Xray dial mode of the freedom exit-node (`--dialMode` in a link). */
    var dialMode: String? = null,

    /** Xray targetStrategy of the outbound; null means the profile's default. */
    var targetStrategy: String? = null,
)
