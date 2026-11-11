// Extracted from PattNG (https://github.com/patterniha/PattNG).
//
// Sources, all in V2rayNG/app/src/main/java/com/v2ray/ang/core/AetherCoreManager.kt:
//   - buildArguments               lines 280-429  (the authoritative CLI mapping)
//   - masqueOverHttp2              lines 248-249
//   - reachesWarpThroughCarrier    lines 252-253
//   - listenPortSource/socksPort   lines 192-207
//   - listenPortOf/listenPortProblem/enum lines 219-234
//   - scanPort                     lines 241-242
//   - coreLogLevel/withLogLevel/withInfoLines/showsInfo lines 435-452, 800-802
//   - argument introspection      lines 1030-1244
//
// Android-only code removed: Context, ProcessBuilder, /proc reaping, readiness
// probes, Psiphon server-list unpacking, log relay. None of that changes the
// argument strings. AppConfig constants were replaced with AetherDefaults.
//
// This is the exact translation of an Aether profile + listen port into
// `aether` CLI arguments. Keep the option order: it is the order PattNG shows
// in the editor's command preview and the order tests assert against.

package com.v2ray.ang.core

import com.v2ray.ang.AetherDefaults
import com.v2ray.ang.dto.AetherEndpoint
import com.v2ray.ang.dto.AetherRange
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
import com.v2ray.ang.fmt.AetherFmt
import java.net.ServerSocket
import java.util.Locale

object AetherArgs {

    // ------------------------------------------------------------------
    // Constants used by the argument builder
    // ------------------------------------------------------------------

    /** The option that names Psiphon's own listener. */
    const val PSIPHON_BIND = "--psiphon-bind"

    /** The option that names Tor's own listener. */
    const val TOR_BIND = "--tor-bind"

    /** The option that names the proxy the core dials out through. */
    const val UPSTREAM = "--upstream"

    /** The core's flag naming the file of server entries the Psiphon client starts with. */
    const val PSIPHON_SERVER_ENTRIES = "--psiphon-server-entries"

    /** The word a command names the app's own Psiphon list by; the real file takes its place when the core starts. */
    const val SHIPPED_LIST = "shipped-list"

    const val DEFAULT_LOG_LEVEL = "info"

    /** The inbound the core dials out through is three ports above the Aether listen port. */
    private const val SECONDARY_SOCKS_OFFSET = 3

    /** Ports below this one are the system's; an app cannot listen there. */
    private const val FIRST_LISTEN_PORT = 1024

    /** The highest Aether listen port: the three ports after it are the core's as well. */
    private const val LAST_LISTEN_PORT = 65535 - SECONDARY_SOCKS_OFFSET

    // ------------------------------------------------------------------
    // Listen-port settings
    // ------------------------------------------------------------------

    /**
     * Where the loopback port every Aether core listens on comes from: the
     * setting (PattNG: SettingsManager.getAetherListenPort). Until the
     * application sets this, the default port applies.
     */
    @Volatile
    var listenPortSource: () -> Int = { AetherDefaults.PORT_AETHER_SOCKS.toInt() }

    /**
     * The loopback port every Aether core listens on, whatever its profile,
     * and every Aether outbound dials, unless a hand-written command names
     * another. PattNG runs one core at a time on it.
     */
    val socksPort: Int get() = listenPortSource()

    /**
     * The port of the secondary-socks inbound, which the core of a session
     * dials out through: three above [socksPort], past the two after it that
     * Psiphon and Tor may take.
     */
    val secondarySocksPort: Int get() = socksPort + SECONDARY_SOCKS_OFFSET

    /**
     * The Aether listen port the setting [text] names: a port an app can
     * listen on, with the three ports after it ports as well; the default port
     * for anything else.
     */
    internal fun listenPortOf(text: String?): Int =
        text?.trim()?.toIntOrNull()?.takeIf { it in FIRST_LISTEN_PORT..LAST_LISTEN_PORT }
            ?: AetherDefaults.PORT_AETHER_SOCKS.toInt()

    /** Why [text] cannot be the Aether listen port beside a local proxy on [localPorts]; null when it can. */
    internal fun listenPortProblem(text: String, localPorts: Set<Int>): ListenPortProblem? {
        val port = text.trim().toIntOrNull()?.takeIf { it in FIRST_LISTEN_PORT..LAST_LISTEN_PORT }
            ?: return ListenPortProblem.NOT_A_PORT
        return ListenPortProblem.LOCAL_PROXY.takeIf { (port..port + SECONDARY_SOCKS_OFFSET).any { it in localPorts } }
    }

    internal enum class ListenPortProblem {
        /** No port an app can listen on, with the three ports after it ports as well. */
        NOT_A_PORT,

        /** The port, or one of the three after it, which the core takes as well, is the local proxy's. */
        LOCAL_PROXY,
    }

    /**
     * The port a scan of [profile] binds: none, since nothing dials it, unless
     * Tor around the tunnel comes along, whose own listener follows the
     * tunnel's and needs a real port, because the core dials the address Tor
     * was told to listen on.
     */
    fun scanPort(profile: ProfileItem): Int =
        if (AetherTor.fromString(profile.aetherTor) == AetherTor.REVERSE) findRandomFreePort() else 0

    private fun findRandomFreePort(): Int = ServerSocket(0).use { it.localPort }

    /**
     * Whether a tunnel over MASQUE runs over HTTP/2: chosen so, or dialled
     * through Tor or Psiphon around it, which carry TCP alone, so that the core
     * takes HTTP/2 whatever [transport] says.
     */
    fun masqueOverHttp2(
        protocol: AetherProtocol,
        transport: AetherTransport,
        tor: AetherTor,
        psiphon: AetherPsiphon,
    ): Boolean = protocol.overMasque &&
        (transport == AetherTransport.HTTP2 || tor == AetherTor.REVERSE || psiphon == AetherPsiphon.REVERSE)

    /** True when a core on [arguments] reaches WARP through Tor or Psiphon, which then has to come up first. */
    fun reachesWarpThroughCarrier(arguments: List<String>): Boolean =
        torModeOf(arguments) == AetherTor.REVERSE || psiphonModeOf(arguments) == AetherPsiphon.REVERSE

    // ------------------------------------------------------------------
    // THE ARGUMENT BUILDER
    // ------------------------------------------------------------------

    /**
     * The CLI arguments of the Aether core for [profile], on [port].
     *
     * [scan] builds the arguments of a one-off scan: the endpoint is not pinned,
     * carriers inside the tunnel are dropped, and quick reconnect is disabled.
     * A scan still keeps an outer carrier (Tor/Psiphon REVERSE) because the
     * session will reach WARP from its exit.
     *
     * Port allocation, exactly as PattNG:
     *   - The listener the app dials takes [port].
     *   - If Psiphon CHAIN is what the app dials, Psiphon gets [port]; the
     *     core's own `--bind` gets port+1.
     *   - Else if Tor CHAIN is what the app dials, Tor gets [port]; the core's
     *     own `--bind` gets port+1.
     *   - Otherwise the core gets [port]; additional listeners take port+1,
     *     port+2 in this order: core, Tor, Psiphon.
     *   - Psiphon REVERSE gets port 0 (ephemeral).
     *   - Tor REVERSE always gets a real port.
     */
    fun buildArguments(
        profile: ProfileItem,
        port: Int,
        scan: Boolean = false,
        logLevel: String = DEFAULT_LOG_LEVEL,
    ): List<String> {
        val protocol = AetherProtocol.fromString(profile.aetherProtocol)
        // A scan looks for WARP endpoints from where the session will look: a
        // carrier around the tunnel stays, since the session reaches WARP from
        // its exit, while one inside the tunnel has no part in it.
        val tor = AetherTor.fromString(profile.aetherTor).takeUnless { scan && it != AetherTor.REVERSE } ?: AetherTor.OFF
        val psiphon = AetherPsiphon.fromString(profile.aetherPsiphon).takeUnless { scan && it != AetherPsiphon.REVERSE } ?: AetherPsiphon.OFF

        // The listener the app dials takes [port]: Psiphon's or Tor's when one
        // of them runs inside the tunnel and is what the app reaches, the
        // tunnel's own otherwise. Every other listener takes the ports after
        // it, in this order: the tunnel's own, then Tor's, then Psiphon's.
        // Psiphon around the tunnel is the exception: nothing of the app dials
        // its listener and the core takes the port Psiphon reports, so an
        // ephemeral port keeps a scan core from colliding with the session's.
        // Tor around the tunnel gets a real port, since the core dials the
        // address Tor was told to listen on.
        val dialsPsiphon = psiphon == AetherPsiphon.CHAIN
        val dialsTor = tor == AetherTor.CHAIN && !dialsPsiphon
        var next = port + 1
        val own = if (dialsPsiphon || dialsTor) next++ else port
        val torBind = when (tor) {
            AetherTor.CHAIN -> if (dialsTor) port else next++
            AetherTor.REVERSE -> next++
            AetherTor.OFF, AetherTor.ONLY -> null
        }
        val psiphonBind = when (psiphon) {
            AetherPsiphon.CHAIN -> if (dialsPsiphon) port else next++
            AetherPsiphon.REVERSE -> 0
            AetherPsiphon.OFF, AetherPsiphon.ONLY -> null
        }
        return buildList {
            addAll(listOf("--bind", "${AetherDefaults.LOOPBACK}:$own"))
            if (psiphon != AetherPsiphon.ONLY && tor != AetherTor.ONLY) {
                addAll(listOf("--protocol", protocol.core))
                // gool has meant WireGuard over MASQUE since aether 2.3.0;
                // WARP-in-WARP is the classic gool, asked for by name rather
                // than left to the hop settings that select it as well.
                if (protocol == AetherProtocol.GOOL) add("--gool-classic")
                addAll(listOf("--scan", AetherScanMode.fromString(profile.aetherScanMode).type))
                // Automatic obfuscation is the core's own choice per protocol,
                // so nothing is said about it; MASQUE over HTTP/2 takes none at
                // all, since obfuscation shapes the UDP of WireGuard and
                // HTTP/3 alone.
                val transport = AetherTransport.fromString(profile.aetherTransport)
                AetherObfuscation.fromString(profile.aetherObfuscation)
                    .takeUnless { it == AetherObfuscation.AUTO || masqueOverHttp2(protocol, transport, tor, psiphon) }
                    ?.let { addAll(listOf("--noize", it.type)) }
                addAll(listOf("--ip", AetherIpVersion.fromString(profile.aetherIpVersion).type))
                settingValue(profile.aetherDns)?.let { addAll(listOf("--dns", it)) }
                // A scan keeps the exit rule as well, so that it ends on an
                // endpoint the session will accept.
                settingValue(profile.aetherExitLoc)?.let { addAll(listOf("--exit-loc", it)) }

                // The server name the MASQUE handshakes put in their
                // ClientHello, on either carrier and both hops, the default
                // named as well, so that the command shows what is sent; the
                // HTTP host stays the core's.
                if (protocol.overMasque) {
                    addAll(listOf("--masque-sni", settingValue(profile.aetherMasqueSni) ?: AetherDefaults.AETHER_MASQUE_SNI))
                }
                if (protocol.overMasque && transport == AetherTransport.HTTP2) {
                    add("--h2")
                    // The ClientHello of the MASQUE handshake, and of the calls
                    // to the WARP API the core makes for a key it does not
                    // have; without the flag the core sends both whole.
                    if (profile.aetherFragment == true) {
                        add("--fragment")
                        AetherRange.parse(profile.aetherFragmentSize, AetherRange.FRAGMENT_SIZE)
                            ?.let { addAll(listOf("--fragment-size", it.toString())) }
                        AetherRange.parse(profile.aetherFragmentDelay, AetherRange.FRAGMENT_DELAY)
                            ?.let { addAll(listOf("--fragment-delay", it.toString())) }
                    }
                }
                // Encrypted Client Hello hides the server name of the MASQUE
                // handshake, on either carrier and both hops, with the key of
                // the HTTPS record of the ECH domain, asked of the ECH resolver.
                if (protocol.overMasque && profile.aetherEch == true) {
                    addAll(listOf("--ech", "auto"))
                    addAll(listOf("--ech-dns", settingValue(profile.aetherEchDns) ?: AetherDefaults.AETHER_ECH_DNS))
                    addAll(listOf("--ech-domain", settingValue(profile.aetherEchDomain) ?: AetherDefaults.AETHER_ECH_DOMAIN))
                }
                // The ClientHello of the MASQUE handshakes, and of the WARP API
                // calls and the ECH key lookup the core makes: over HTTP/3,
                // which carries TLS 1.3 alone, only its GREASE shows.
                if (protocol.overMasque) addAll(AetherFingerprint.fromString(profile.aetherFingerprint).arguments)

                if (protocol == AetherProtocol.WG_OVER_MASQUE) {
                    // The outer hop is a MASQUE gateway, which a scan looks for
                    // afresh. The inner one is the WireGuard endpoint dialled
                    // inside the tunnel, the one WARP assigned the key unless
                    // one is named; no scan looks for it, so a scan keeps the
                    // profile's own and finds a gateway that works with it.
                    AetherEndpoint.parse(profile.aetherWiwOuter).takeUnless { scan }
                        ?.let { addAll(listOf("--peer", it.toString())) }
                    AetherEndpoint.parse(profile.aetherWiwInner)
                        ?.let { addAll(listOf("--gool-peer", it.toString())) }
                } else if (protocol.twoHops) {
                    val hop = if (protocol == AetherProtocol.MIM) "--mim" else "--wiw"
                    val outer = AetherEndpoint.parse(profile.aetherWiwOuter).takeUnless { scan }
                    val inner = AetherEndpoint.parse(profile.aetherWiwInner).takeUnless { scan }
                    outer?.let { addAll(listOf("$hop-outer", it.toString())) }
                    inner?.let { addAll(listOf("$hop-inner", it.toString())) }
                    if (outer == null && inner == null) add("$hop-scan")
                } else if (!scan) {
                    AetherEndpoint.of(profile.server, profile.serverPort)
                        ?.let { addAll(listOf("--peer", it.toString())) }
                }

                add(if (scan) "--no-quick-reconnect" else "--quick-reconnect")
            }

            when (tor) {
                AetherTor.OFF -> Unit
                AetherTor.CHAIN -> add("--tor")
                AetherTor.REVERSE -> add("--tor-reverse")
                AetherTor.ONLY -> add("--tor-only")
            }
            torBind?.let { addAll(listOf(TOR_BIND, "${AetherDefaults.LOOPBACK}:$it")) }
            if (tor != AetherTor.OFF) {
                // Told nothing, the core tries Tor plainly and turns to fetched
                // bridges where Tor is blocked.
                val bridges = AetherTorBridges.fromString(profile.aetherTorBridges)
                when (bridges) {
                    AetherTorBridges.AUTO -> Unit
                    AetherTorBridges.FIRST -> add("--tor-bridges")
                    AetherTorBridges.NEVER -> add("--no-tor-bridges")
                    AetherTorBridges.OWN -> AetherFmt.bridgeLines(profile.aetherTorBridgeLines)
                        .mapNotNull { settingValue(it) }
                        .forEach { addAll(listOf("--tor-bridge", it)) }
                }
                // Where fetched bridges come from; with the profile's own lines,
                // or none at all, nothing is fetched.
                if (bridges == AetherTorBridges.AUTO || bridges == AetherTorBridges.FIRST) {
                    AetherTorRelays.fromString(profile.aetherTorRelays).takeUnless { it == AetherTorRelays.AUTO }
                        ?.let { addAll(listOf("--tor-relays", it.type)) }
                }
            }

            when (psiphon) {
                AetherPsiphon.OFF -> Unit
                AetherPsiphon.CHAIN -> add("--psiphon")
                AetherPsiphon.REVERSE -> add("--psiphon-reverse")
                AetherPsiphon.ONLY -> add("--psiphon-only")
            }
            psiphonBind?.let { addAll(listOf(PSIPHON_BIND, "${AetherDefaults.LOOPBACK}:$it")) }
            if (psiphon != AetherPsiphon.OFF) {
                val shape = AetherPsiphonMode.fromString(profile.aetherPsiphonMode)
                addAll(listOf("--psiphon-mode", shape.type))
                // The CDN lists feed the fronted transports alone, which the
                // direct shape never uses; the server names count only beside
                // an IP list of one's own, since the built-in list comes whole.
                val cdnIps = settingValue(profile.aetherPsiphonCdnIps)?.takeIf { shape != AetherPsiphonMode.DIRECT }
                cdnIps?.let { addAll(listOf("--psiphon-cdn-ips", it)) }
                if (cdnIps != null) settingValue(profile.aetherPsiphonCdnSni)
                    ?.let { addAll(listOf("--psiphon-cdn-sni", it)) }
                // Which of the edge lists built into Psiphon the fronting scan
                // tries; beside addresses of one's own, after them.
                if (shape != AetherPsiphonMode.DIRECT) {
                    AetherPsiphonCdnSet.join(AetherPsiphonCdnSet.parse(profile.aetherPsiphonCdnSets))
                        ?.let { addAll(listOf("--psiphon-cdn-sets", it)) }
                }
                settingValue(profile.aetherPsiphonRegion)?.let { addAll(listOf("--psiphon-region", it)) }
                // The bundled list, unless the profile wants Psiphon to fetch a
                // fresh one before it dials anything.
                if (profile.aetherPsiphonBundledList != false) {
                    addAll(listOf(PSIPHON_SERVER_ENTRIES, SHIPPED_LIST))
                }
            }
            addAll(listOf("--log-level", logLevel))
        }
    }

    /**
     * The text of a setting as the value of an option of the core: trimmed, and
     * null when it is blank or starts with '-'. No value of these settings
     * starts so, and one that did would read as an option of its own wherever
     * the app looks options up word by word: a link carrying dns=--upstream
     * would leave the core without the upstream the app gives it, so that it
     * dialled out around Xray.
     */
    private fun settingValue(value: String?): String? =
        value?.trim()?.takeUnless { it.isEmpty() || it.startsWith('-') }

    // ------------------------------------------------------------------
    // Log level mapping
    // ------------------------------------------------------------------

    /**
     * Maps the app's core log level setting onto the levels the core accepts.
     * Only the session follows the setting: scans and key renewals keep the
     * default because they read info lines.
     */
    fun coreLogLevel(appLevel: String?): String = when (appLevel?.lowercase(Locale.US)) {
        "debug" -> "debug"
        "info" -> "info"
        "warning", "warn" -> "warn"
        "error", "none" -> "error"
        else -> DEFAULT_LOG_LEVEL
    }

    /** [arguments] at [logLevel], unless they name a level of their own, as a hand-written command may. */
    fun withLogLevel(arguments: List<String>, logLevel: String): List<String> =
        if ("--log-level" in arguments || "--verbose" in arguments) arguments else arguments + listOf("--log-level", logLevel)

    /**
     * [arguments] at a level that writes the core's info lines, which a run
     * that waits for one of them needs: as they are, unless they name a
     * quieter level, which gives way to the default.
     */
    fun withInfoLines(arguments: List<String>): List<String> =
        if (showsInfo(arguments)) arguments
        else withoutOption(arguments, "--log-level") + listOf("--log-level", DEFAULT_LOG_LEVEL)

    /** True when a core started with [arguments] writes its info lines, at the level named or at the default. */
    fun showsInfo(arguments: List<String>): Boolean =
        "--verbose" in arguments || (valueAfter(arguments, "--log-level") ?: DEFAULT_LOG_LEVEL) in setOf("info", "debug", "trace")

    // ------------------------------------------------------------------
    // Argument introspection (used to read live cores and custom commands)
    // ------------------------------------------------------------------

    /**
     * [arguments] without the listeners, the log level and the upstream proxy:
     * what tells one tunnel from another. The session's core dials out through
     * Xray and its profile does not say so.
     */
    internal fun tunnelArguments(arguments: List<String>): List<String> =
        listOf("--log-level", "--bind", TOR_BIND, PSIPHON_BIND, UPSTREAM).fold(arguments, ::withoutOption)

    /** [arguments] without every [flag] and the value after it. */
    internal fun withoutOption(arguments: List<String>, flag: String): List<String> {
        val kept = mutableListOf<String>()
        var index = 0
        while (index < arguments.size) {
            if (arguments[index] == flag) index += 2 else kept.add(arguments[index++])
        }
        return kept
    }

    /** [arguments] with the listener [flag] names on the loopback port [port], in place of whatever they named for it. */
    internal fun withListener(arguments: List<String>, flag: String, port: Int): List<String> =
        withoutOption(arguments, flag) + listOf(flag, "${AetherDefaults.LOOPBACK}:$port")

    /**
     * The option naming the listener the app dials: Psiphon's when Psiphon
     * runs inside the tunnel, Tor's when Tor does, the core's own otherwise.
     * Both inside at once is refused by the editor; a command written that way
     * is dialled on Psiphon's.
     */
    internal fun listenerFlagOf(arguments: List<String>): String = when {
        psiphonModeOf(arguments) == AetherPsiphon.CHAIN -> PSIPHON_BIND
        torModeOf(arguments) == AetherTor.CHAIN -> TOR_BIND
        else -> "--bind"
    }

    /** Where Tor stands in the tunnel [argv] runs, read the way the core reads it: the last mode flag wins. */
    internal fun torModeOf(argv: List<String>): AetherTor = argv.fold(AetherTor.OFF) { mode, word ->
        when (word) {
            "--tor" -> AetherTor.CHAIN
            "--tor-reverse" -> AetherTor.REVERSE
            "--tor-only" -> AetherTor.ONLY
            else -> mode
        }
    }

    /** Where Psiphon stands in the tunnel [argv] runs, read the way the core reads it: the last mode flag wins. */
    internal fun psiphonModeOf(argv: List<String>): AetherPsiphon = argv.fold(AetherPsiphon.OFF) { mode, word ->
        when (word) {
            "--psiphon" -> AetherPsiphon.CHAIN
            "--psiphon-reverse" -> AetherPsiphon.REVERSE
            "--psiphon-only" -> AetherPsiphon.ONLY
            else -> mode
        }
    }

    /**
     * The tunnel [argv] runs, as the names of its parts from the outside in: a
     * carrier around the tunnel, the WARP protocol, which [label] names, a
     * carrier inside it. A carrier alone is the whole tunnel, and Tor alone
     * comes before Psiphon alone, as the core runs it before it looks at Psiphon.
     */
    internal fun pathOf(argv: List<String>, label: (AetherProtocol) -> String = AetherProtocol::name): List<String> {
        val tor = torModeOf(argv)
        val psiphon = psiphonModeOf(argv)
        if (tor == AetherTor.ONLY) return listOf(TOR_NAME)
        if (psiphon == AetherPsiphon.ONLY) return listOf(PSIPHON_NAME)
        return buildList {
            if (tor == AetherTor.REVERSE) add(TOR_NAME)
            if (psiphon == AetherPsiphon.REVERSE) add(PSIPHON_NAME)
            add(label(protocolOf(argv)))
            if (psiphon == AetherPsiphon.CHAIN) add(PSIPHON_NAME)
            if (tor == AetherTor.CHAIN) add(TOR_NAME)
        }
    }

    /** The carriers as [pathOf] names them, beside the names of [AetherProtocol]. */
    private const val TOR_NAME = "TOR"
    private const val PSIPHON_NAME = "PSIPHON"

    /**
     * Whether what the app sends through the tunnel [argv] runs leaves it
     * through WARP, the last part of [pathOf]: not through Tor or Psiphon,
     * which come last when they run inside the tunnel or are the whole of it.
     */
    internal fun leavesThroughWarp(argv: List<String>): Boolean =
        leavesThroughWarp(torModeOf(argv), psiphonModeOf(argv))

    /** [leavesThroughWarp] with Tor standing at [tor] and Psiphon at [psiphon]. */
    internal fun leavesThroughWarp(tor: AetherTor, psiphon: AetherPsiphon): Boolean =
        (tor == AetherTor.OFF || tor == AetherTor.REVERSE) &&
            (psiphon == AetherPsiphon.OFF || psiphon == AetherPsiphon.REVERSE)

    internal fun bindAddressOf(argv: List<String>): String? = valueAfter(argv, "--bind")

    /** The port of the core's own listener, null when [argv] names none. */
    internal fun bindPortOf(argv: List<String>): Int? = portAfter(argv, "--bind")

    /** The address of the listener the app dials, see [listenerFlagOf]; null when [argv] names none. */
    internal fun listenerAddressOf(argv: List<String>): String? = valueAfter(argv, listenerFlagOf(argv))

    /** The port of the listener the app dials, null when [argv] names none. */
    internal fun listenerPortOf(argv: List<String>): Int? =
        listenerAddressOf(argv)?.substringAfterLast(':', "")?.toIntOrNull()

    /** The port of the address after [flag], null when there is none or it cannot be read. */
    internal fun portAfter(argv: List<String>, flag: String): Int? =
        valueAfter(argv, flag)?.substringAfterLast(':', "")?.toIntOrNull()

    /**
     * The protocol [argv] selects, read the way the core reads it: the last of
     * --protocol and the protocol flags wins, a hop named without any of them
     * selects the two-hop protocol it belongs to, warp-in-warp before
     * masque-in-masque, and nothing at all is masque. Gool is WireGuard over
     * MASQUE unless --gool-classic or a warp-in-warp hop setting, a scan of the
     * hops included, makes it WARP-in-WARP, the classic gool, wherever they stand.
     */
    internal fun protocolOf(argv: List<String>): AetherProtocol {
        var chosen: AetherProtocol? = null
        var classicMode = false
        // Each hop setting is a variable of the core's environment, so the
        // last value given counts, a blank one included.
        val hops = HashMap<HopSetting, String>()
        for ((index, word) in argv.withIndex()) {
            val value = argv.getOrNull(index + 1)
            when (word) {
                "--protocol" -> chosen = value?.let(::protocolNamed) ?: chosen
                "--masque" -> chosen = AetherProtocol.MASQUE
                "--wg", "--wireguard", "--warp" -> chosen = AetherProtocol.WIREGUARD
                "--gool", "--wiw", "--gool-peer" -> chosen = AetherProtocol.WG_OVER_MASQUE
                "--gool-classic" -> {
                    chosen = AetherProtocol.WG_OVER_MASQUE
                    classicMode = true
                }

                "--mim", "--masque-in-masque" -> chosen = AetherProtocol.MIM
                "--wiw-outer", "--gool-outer", "--outer-peer" -> value?.let { hops[HopSetting.WIW_OUTER] = it }
                "--wiw-inner", "--gool-inner", "--inner-peer" -> value?.let { hops[HopSetting.WIW_INNER] = it }
                "--wiw-peers", "--gool-peers" -> value?.let { hops[HopSetting.WIW_PEERS] = it }
                "--wiw-scan", "--gool-scan" -> hops[HopSetting.WIW_PEERS] = "auto"
                "--mim-outer" -> value?.let { hops[HopSetting.MIM_OUTER] = it }
                "--mim-inner" -> value?.let { hops[HopSetting.MIM_INNER] = it }
                "--mim-peers" -> value?.let { hops[HopSetting.MIM_PEERS] = it }
                "--mim-scan" -> hops[HopSetting.MIM_PEERS] = "auto"
            }
        }
        // A blank value sets nothing for the core; a list of peers names hops unless it asks for a scan.
        fun given(setting: HopSetting) = hops[setting]?.trim()?.takeIf { it.isNotEmpty() }
        fun pinned(outer: HopSetting, inner: HopSetting, peers: HopSetting) =
            given(outer) != null || given(inner) != null || namesHops(given(peers))

        val classicGool = classicMode ||
            given(HopSetting.WIW_OUTER) != null || given(HopSetting.WIW_INNER) != null || given(HopSetting.WIW_PEERS) != null
        return when (chosen) {
            AetherProtocol.WG_OVER_MASQUE -> if (classicGool) AetherProtocol.GOOL else AetherProtocol.WG_OVER_MASQUE
            null -> when {
                pinned(HopSetting.WIW_OUTER, HopSetting.WIW_INNER, HopSetting.WIW_PEERS) -> AetherProtocol.GOOL
                pinned(HopSetting.MIM_OUTER, HopSetting.MIM_INNER, HopSetting.MIM_PEERS) -> AetherProtocol.MIM
                else -> AetherProtocol.MASQUE
            }

            else -> chosen
        }
    }

    /** The hop settings [protocolOf] reads, each the variable of the core's environment its flags set. */
    private enum class HopSetting { WIW_OUTER, WIW_INNER, WIW_PEERS, MIM_OUTER, MIM_INNER, MIM_PEERS }

    /**
     * The protocol the core selects for [name] after --protocol, under any of
     * the names it accepts; gool is WireGuard over MASQUE until [protocolOf]
     * finds what makes it the classic one.
     */
    private fun protocolNamed(name: String): AetherProtocol = when (name.trim().lowercase(Locale.US)) {
        "wg", "wireguard" -> AetherProtocol.WIREGUARD
        "gool", "wiw", "warp-in-warp", "warpinwarp" -> AetherProtocol.WG_OVER_MASQUE
        "mim", "m2", "masque-in-masque", "masqueinmasque" -> AetherProtocol.MIM
        else -> AetherProtocol.MASQUE
    }

    /** Whether a value of --wiw-peers or --mim-peers names hops rather than asking for a scan, as the core reads it. */
    private fun namesHops(value: String?): Boolean =
        value != null && value.lowercase(Locale.US) !in scanKeywords

    /** The values of --wiw-peers and --mim-peers that ask for a scan instead of naming hops, as the core reads them. */
    private val scanKeywords = setOf("auto", "scan", "none", "off", "0")

    /** The value after the last [flag] in [argv]; the last one is the one the core keeps. */
    private fun valueAfter(argv: List<String>, flag: String): String? =
        argv.lastIndexOf(flag).takeIf { it >= 0 }?.let { argv.getOrNull(it + 1) }
}
