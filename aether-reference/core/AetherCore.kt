// Extracted from PattNG (https://github.com/patterniha/PattNG).
// Source: V2rayNG/app/src/main/java/com/v2ray/ang/core/AetherCore.kt lines 20-175.
// Android-only imports and Xray outbound construction removed.
//
// The original data class also carries `exit: AetherExit`, the Xray outbound
// the core dials out through, plus helpers that build that outbound. Those are
// Xray-core integration, not Aether CLI/profile logic, so they are left out of
// this reference. Everything below still matches the original exactly.
//
// AetherCore is the bridge between a ProfileItem and a command line:
//   - of(profile)       -> either the profile's custom command or
//                          AetherArgs.buildArguments(profile) minus --log-level
//   - ofCommand(text)   -> parse the editor's "Aether command" field back
//   - command           -> render `aether <args>` for the editor
//   - ports/protocol    -> read the resulting arguments back

package com.v2ray.ang.core

import com.v2ray.ang.dto.entities.ProfileItem
import com.v2ray.ang.enums.AetherProtocol

data class AetherCore(val arguments: List<String>) {

    /**
     * The loopback port the app dials: the listener [AetherArgs.listenerFlagOf]
     * names (Psiphon's or Tor's when one of them runs inside the tunnel), the
     * core's own otherwise.
     */
    val port: Int get() = AetherArgs.listenerPortOf(arguments) ?: AetherArgs.socksPort

    /** Every loopback port the core is told to listen on; an inbound of the configuration cannot share one. */
    val ports: List<Int>
        get() = LISTENERS
            .mapNotNull { AetherArgs.portAfter(arguments, it) }
            .filter { it != 0 }
            .distinct()

    /** The protocol, which tells whose identity files the core uses. */
    val protocol: AetherProtocol get() = AetherArgs.protocolOf(arguments)

    /** The tunnel as the names of its parts from the outside in, carriers included; see [AetherArgs.pathOf]. */
    val path: List<String> get() = AetherArgs.pathOf(arguments)

    /** [path], with the WARP protocol named by [label], as a screen names it. */
    fun path(label: (AetherProtocol) -> String): List<String> = AetherArgs.pathOf(arguments, label)

    /** The command line a profile or a custom configuration carries for this core; [ofCommand] reads it back. */
    val command: String get() = (listOf(COMMAND_NAME) + arguments).joinToString(" ", transform = ::quoted)

    /** True when a process started with [processArguments] runs this core, on whatever ports and at whatever log level. */
    fun runsAs(processArguments: List<String>): Boolean =
        AetherArgs.tunnelArguments(processArguments) == AetherArgs.tunnelArguments(arguments)

    /** True when the core is told which proxy to dial out through, as a command written by hand may be. */
    val hasUpstream: Boolean get() = AetherArgs.UPSTREAM in arguments

    companion object {

        /** The name a command line starts with; the app runs its own copy of the core whatever the name says. */
        const val COMMAND_NAME = "aether"

        /** The listeners a core may be told to bind: the core's own, Tor's, Psiphon's. */
        private val LISTENERS = listOf("--bind", AetherArgs.TOR_BIND, AetherArgs.PSIPHON_BIND)

        /**
         * The core of [profile]: the command line it carries, or its settings
         * as arguments on [listenPort], the Aether listen port of the app. The
         * log level is the session's to add. A command the app cannot read is
         * left aside for the settings; the profile editor refuses to store one.
         * A screen passes the port it holds, since the setting is read from
         * storage; a command naming no listener gets one there as well.
         */
        fun of(profile: ProfileItem, listenPort: Int = AetherArgs.socksPort): AetherCore {
            val core = profile.aetherCommand?.takeIf { it.isNotBlank() }?.let { ofCommand(it, listenPort) }
                ?: AetherCore(
                    AetherArgs.withoutOption(
                        AetherArgs.buildArguments(profile, listenPort),
                        "--log-level",
                    )
                )
            return core
        }

        /**
         * The core [command] describes, or null when it names nothing the app
         * can run: no argument at all, or a listener whose port cannot be read.
         * Words are split on whitespace, quotes keep a word together, and a
         * program name in front is dropped. A command that names no listener
         * for the app to dial gets one on [listenPort], [AetherArgs.socksPort]
         * unless a screen passes the port it holds, the port the Aether
         * outbounds of the app dial unless told otherwise; the core's own
         * defaults are other ports, which nothing in the app dials.
         */
        fun ofCommand(command: String, listenPort: Int = AetherArgs.socksPort): AetherCore? {
            val arguments = readableArguments(command) ?: return null
            val listener = AetherArgs.listenerFlagOf(arguments)
            if (listener !in arguments) return AetherCore(AetherArgs.withListener(arguments, listener, listenPort))
            return AetherCore(arguments)
        }

        /**
         * The arguments of [command] when it names something the app can run,
         * see [ofCommand]: not no argument at all, nor a listener whose port
         * cannot be read. Null otherwise.
         */
        private fun readableArguments(command: String): List<String>? {
            val arguments = argumentsOf(command)
            if (arguments.isEmpty()) return null
            val listener = AetherArgs.listenerFlagOf(arguments)
            return arguments.takeIf { listener !in arguments || AetherArgs.portAfter(arguments, listener) != null }
        }

        /** The arguments of [command]: its [words], without a program name in front. */
        internal fun argumentsOf(command: String): List<String> {
            val words = words(command)
            return if (words.firstOrNull()?.startsWith("-") == false) words.drop(1) else words
        }

        /** The words of a command line: split on whitespace, with single or double quotes keeping a word together. */
        internal fun words(command: String): List<String> {
            val words = mutableListOf<String>()
            val word = StringBuilder()
            var quote: Char? = null
            var open = false
            for (c in command) {
                when {
                    quote != null -> if (c == quote) quote = null else word.append(c)
                    c == '"' || c == '\'' -> {
                        quote = c
                        open = true
                    }

                    c.isWhitespace() -> if (open) {
                        words.add(word.toString())
                        word.setLength(0)
                        open = false
                    }

                    else -> {
                        word.append(c)
                        open = true
                    }
                }
            }
            if (open) words.add(word.toString())
            return words
        }

        /** [word] as a command line carries it: quoted when whitespace would split it. */
        private fun quoted(word: String): String =
            if (word.isEmpty() || word.any(Char::isWhitespace)) "\"$word\"" else word
    }
}
