// Extracted from PattNG (https://github.com/patterniha/PattNG).
// Source: V2rayNG/app/src/main/java/com/v2ray/ang/AppConfig.kt
// Android-only imports and unrelated constants removed; values are verbatim.
//
// These are the defaults the Aether link parser, validator and argument builder fall back to.

package com.v2ray.ang

object AetherDefaults {

    /** Link scheme; EConfigType.AETHER (value 500) owns it. */
    const val AETHER = "aether://"

    /** The loopback port every Aether core listens on unless the settings say otherwise. */
    const val PORT_AETHER_SOCKS = "10819"

    /** The loopback address every core binds and every app-side outbound dials. */
    const val LOOPBACK = "127.0.0.1"

    /** `--ech-dns` default: where the core asks for the ECH key. */
    const val AETHER_ECH_DNS = "udp://1.1.1.1"

    /** `--ech-domain` default: whose HTTPS record holds the ECH key. */
    const val AETHER_ECH_DOMAIN = "cloudflare-ech.com"

    /** `--masque-sni` default: the server name MASQUE handshakes put in their ClientHello. */
    const val AETHER_MASQUE_SNI = "www.cloudflare.com"

    /** `--api-address` default: where the calls to the WARP API go. */
    const val AETHER_ENROLL_ADDRESS = "api.cloudflareclient.com"

    /** The Xray outbound tag the core dials out through. */
    const val TAG_EXIT_NODE = "exit-node"
}
