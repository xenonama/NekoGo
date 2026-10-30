<!-- Advanced System workspace content: proxy, DNS, privileges,
     WireGuard keygen, protocol support, cache HTTP.
     Moved verbatim from ConsolePanel (re-hosted, not rewritten):
     template cards and script logic below are byte-identical to the
     old Console -> System section, minus the outer tab condition. -->
<template>
  <div class="net-stack">
    <!-- A) System Proxy (GUI-owned, works with any core) -->
    <div v-if="show('proxy')" id="adv-tool-proxy" class="stg-card">
      <div class="stg-head">
        <span class="stg-head-chip"><Icon name="globe" :size="22" /></span>
        <span class="stg-head-text">
          <span class="stg-head-title">System Proxy</span>
          <span class="stg-head-sub">Point the OS proxy at the running core</span>
        </span>
      </div>
      <div class="stg-row">
        <span class="stg-chip"><Icon name="power" :size="18" /></span>
        <span class="stg-meta">
          <span class="stg-title">Enable system proxy</span>
          <span class="stg-sub">{{ sysProxyNote }}</span>
        </span>
        <span class="stg-ctl">
          <button
            class="stg-toggle"
            :class="{ on: proxyOn }"
            role="switch"
            :aria-checked="proxyOn"
            aria-label="Enable system proxy"
            :disabled="!canUseProxy || proxyBusy"
            @click="flipProxy"
          >
            <span class="knob" />
          </button>
        </span>
      </div>
      <div v-if="proxyError" class="nb-json-error">{{ proxyError }}</div>
    </div>
    <!-- B) System DNS (sing-box only; the RPC takes no address) -->
    <div v-if="show('dns')" id="adv-tool-dns" class="stg-card">
      <div class="stg-head">
        <span class="stg-head-chip"><Icon name="shield" :size="22" /></span>
        <span class="stg-head-text">
          <span class="stg-head-title">System DNS</span>
          <span class="stg-head-sub">Apply or restore the core's local DNS</span>
        </span>
      </div>
      <div class="toolbar" style="padding: 0 0 8px">
        <button class="nb-act nb-act--primary" :disabled="!canUseSys || dnsBusy" @click="applyDNS">
          <Icon name="check" :size="14" />Apply
        </button>
        <button class="nb-act" :disabled="!canUseSys || dnsBusy" @click="restoreDNS">
          <Icon name="refresh" :size="14" />Restore
        </button>
        <span class="muted">{{ sysProxyNote }}</span>
      </div>
      <div v-if="dnsMsg" class="nb-json-valid">{{ dnsMsg }}</div>
      <div v-if="dnsError" class="nb-json-error">{{ dnsError }}</div>
    </div>
    <!-- C) Privileges -->
    <div v-if="show('priv')" id="adv-tool-priv" class="stg-card">
      <div class="stg-head">
        <span class="stg-head-chip"><Icon name="shield" :size="22" /></span>
        <span class="stg-head-text">
          <span class="stg-head-title">Privileges</span>
          <span class="stg-head-sub">Elevation of the running process</span>
        </span>
      </div>
      <div class="toolbar" style="padding: 0 0 8px">
        <span v-if="privState === null" class="muted">Unknown — press Refresh.</span>
        <span v-else :class="privState ? 'nb-json-valid' : 'muted'">{{ privState ? 'Administrator' : 'Standard user' }}</span>
        <button class="nb-act" :disabled="privBusy" @click="refreshPrivileges">
          <Icon name="refresh" :size="14" />Refresh
        </button>
      </div>
      <div v-if="privError" class="nb-json-error">{{ privError }}</div>
    </div>
    <!-- D) WireGuard keygen -->
    <div v-if="show('wg')" id="adv-tool-wg" class="stg-card">
      <div class="stg-head">
        <span class="stg-head-chip"><Icon name="bolt" :size="22" /></span>
        <span class="stg-head-text">
          <span class="stg-head-title">WireGuard Key Pair</span>
          <span class="stg-head-sub">Generate a fresh keypair</span>
        </span>
      </div>
      <div class="toolbar" style="padding: 0 0 8px">
        <button class="nb-act nb-act--primary" :disabled="wgBusy" @click="genKeys">
          <Icon name="bolt" :size="14" />Generate WireGuard key pair
        </button>
      </div>
      <div v-if="wgPair">
        <div class="stg-row">
          <span class="stg-chip"><Icon name="copy" :size="18" /></span>
          <span class="stg-meta">
            <span class="stg-title">Private key — do not share.</span>
            <span class="stg-sub nb-mono">{{ wgPair.privateKey }}</span>
          </span>
          <span class="stg-ctl">
            <button class="nb-act nb-act--sm nb-act--ghost" @click="copyField('priv', wgPair.privateKey)">
              <Icon name="copy" :size="13" />{{ copiedKey === 'priv' ? 'Copied!' : 'Copy' }}
            </button>
          </span>
        </div>
        <div class="stg-row">
          <span class="stg-chip"><Icon name="copy" :size="18" /></span>
          <span class="stg-meta">
            <span class="stg-title">Public key</span>
            <span class="stg-sub nb-mono">{{ wgPair.publicKey }}</span>
          </span>
          <span class="stg-ctl">
            <button class="nb-act nb-act--sm nb-act--ghost" @click="copyField('pub', wgPair.publicKey)">
              <Icon name="copy" :size="13" />{{ copiedKey === 'pub' ? 'Copied!' : 'Copy' }}
            </button>
          </span>
        </div>
      </div>
      <div v-if="wgError" class="nb-json-error">{{ wgError }}</div>
    </div>
    <!-- E) Protocol support -->
    <div v-if="show('support')" id="adv-tool-support" class="stg-card">
      <div class="stg-head">
        <span class="stg-head-chip"><Icon name="list" :size="22" /></span>
        <span class="stg-head-text">
          <span class="stg-head-title">Protocol Support</span>
          <span class="stg-head-sub">Capability probe per protocol</span>
        </span>
      </div>
      <div v-for="proto in supportKinds" :key="proto" class="stg-row">
        <span class="stg-chip"><Icon name="check" :size="18" /></span>
        <span class="stg-meta">
          <span class="stg-title nb-mono">{{ proto }}</span>
          <span class="stg-sub">{{ supportState[proto] === undefined ? 'Not checked yet' : supportState[proto] ? 'Supported' : 'Not supported' }}</span>
        </span>
        <span class="stg-ctl">
          <span v-if="supportState[proto] === undefined" class="muted">—</span>
          <Icon v-else-if="supportState[proto]" name="check" :size="18" class="nb-json-valid" />
          <Icon v-else name="x" :size="18" class="nb-json-error" />
        </span>
      </div>
      <div class="toolbar" style="padding: 8px 0 0">
        <button class="nb-act" :disabled="supportBusy" @click="refreshSupport">
          <Icon name="refresh" :size="14" />Refresh
        </button>
      </div>
      <div v-if="supportError" class="nb-json-error">{{ supportError }}</div>
    </div>
    <!-- F) Cache HTTP (sing-box only) -->
    <div v-if="show('cache')" id="adv-tool-cache" class="stg-card">
      <div class="stg-head">
        <span class="stg-head-chip"><Icon name="download" :size="22" /></span>
        <span class="stg-head-text">
          <span class="stg-head-title">Cache HTTP</span>
          <span class="stg-head-sub">Fetch a URL into the core ruleset cache</span>
        </span>
      </div>
      <div class="toolbar" style="padding: 0 0 8px">
        <input
          v-model="cacheInput"
          class="nb-mono nb-field"
          style="flex: 1; min-width: 180px"
          placeholder="https://example.com/ruleset.srs"
          aria-label="URL to cache"
        />
        <button class="nb-act nb-act--primary" :disabled="!canUseSys || cacheBusy || !cacheInput.trim()" @click="fetchCache">
          <Icon name="download" :size="14" />Fetch and cache
        </button>
      </div>
      <p v-if="!canUseSys" class="muted">{{ sysProxyNote }}</p>
      <div v-if="cachePath" class="nb-json-valid nb-mono">Cached at {{ cachePath }}</div>
      <div v-if="cacheError" class="nb-json-error">{{ cacheError }}</div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import Icon from '../Icon.vue'
import { backend } from '../../api'
import { useConnectionStore } from '../../stores/connection'
import type { WgKeyPair } from '../../types'

// Section filter for the Advanced System workspace: each Panel
// mounts one SystemTools with a single key (proxy/dns/priv/wg/
// support/cache). Null renders everything (legacy full list).
const props = withDefaults(
  defineProps<{ only?: string[] | null }>(),
  { only: null },
)

function show(...keys: string[]): boolean {
  return !props.only || keys.some((k) => props.only!.includes(k))
}

const connection = useConnectionStore()

// --- Phase D tool cards: system-level RPCs. ---
const isXray = computed(() => connection.status.core === 'xray')
const sysRunning = computed(() => connection.status.running)
/** sing-box-only controls need a RUNNING sing-box core. */
const canUseSys = computed(() => sysRunning.value && !isXray.value)
/** The system-proxy toggle works on any running core: the GUI owns the
 *  OS proxy and knows each core's ports (ActiveProxyPorts). */
const canUseProxy = computed(() => sysRunning.value)
const sysProxyNote = computed(() => {
  if (!sysRunning.value) return 'Connect first'
  return '127.0.0.1:mixed port via the GUI'
})

// A) System proxy toggle (requested state only; errors revert it).
const proxyOn = ref(false)
const proxyBusy = ref(false)
const proxyError = ref('')

async function flipProxy() {
  const next = !proxyOn.value
  proxyBusy.value = true
  proxyError.value = ''
  try {
    if (next) await backend.enableSystemProxyGUI()
    else await backend.disableSystemProxyGUI()
    proxyOn.value = next
  } catch (e) {
    proxyError.value = String(e)
  } finally {
    proxyBusy.value = false
  }
}

// B) System DNS apply/restore (the RPC takes no address).
// BUG-011: shared dnsBusy (mirrors proxyBusy) disables both buttons
// until settle — without it Apply+Restore double-clicks race and the
// last-to-resolve message can describe the opposite adapter state.
const dnsMsg = ref('')
const dnsError = ref('')
const dnsBusy = ref(false)

async function applyDNS() {
  if (dnsBusy.value) return
  dnsBusy.value = true
  dnsMsg.value = ''
  dnsError.value = ''
  try {
    await backend.setSystemDNS(false)
    dnsMsg.value = 'Core DNS applied.'
  } catch (e) {
    dnsError.value = String(e)
  } finally {
    dnsBusy.value = false
  }
}

async function restoreDNS() {
  if (dnsBusy.value) return
  dnsBusy.value = true
  dnsMsg.value = ''
  dnsError.value = ''
  try {
    await backend.setSystemDNS(true)
    dnsMsg.value = 'System DNS restored.'
  } catch (e) {
    dnsError.value = String(e)
  } finally {
    dnsBusy.value = false
  }
}

// C) Privileges (works offline: local leg when sing-box is not active).
const privState = ref<boolean | null>(null)
const privBusy = ref(false)
const privError = ref('')

async function refreshPrivileges() {
  privBusy.value = true
  privError.value = ''
  try {
    privState.value = await backend.isPrivileged()
  } catch (e) {
    privError.value = String(e)
  } finally {
    privBusy.value = false
  }
}

// D) WireGuard keygen.
const wgPair = ref<WgKeyPair | null>(null)
const wgBusy = ref(false)
const wgError = ref('')
const copiedKey = ref('')

async function genKeys() {
  wgBusy.value = true
  wgError.value = ''
  try {
    wgPair.value = await backend.genWgKeyPair()
  } catch (e) {
    wgError.value = String(e)
  } finally {
    wgBusy.value = false
  }
}

// BUG-021: clipboard write with the LogViewer textarea+execCommand
// fallback (insecure contexts / denied permissions); a total failure
// surfaces in wgError instead of failing silently.
async function copyTextWithFallback(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    /* fall through to the textarea path */
  }
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    return ok
  } catch {
    return false
  }
}

async function copyField(key: string, text: string) {
  wgError.value = ''
  if (!(await copyTextWithFallback(text))) {
    wgError.value = 'Copy failed: clipboard unavailable'
    return
  }
  copiedKey.value = key
  window.setTimeout(() => {
    if (copiedKey.value === key) copiedKey.value = ''
  }, 1500)
}

// E) Protocol support (fixed probe list).
const supportKinds = ['wireguard', 'naive', 'tailscale']
const supportState = ref<Record<string, boolean>>({})
const supportBusy = ref(false)
const supportError = ref('')

async function refreshSupport() {
  supportBusy.value = true
  supportError.value = ''
  try {
    const next: Record<string, boolean> = {}
    for (const k of supportKinds) next[k] = await backend.isSupported(k)
    supportState.value = next
  } catch (e) {
    supportError.value = String(e)
  } finally {
    supportBusy.value = false
  }
}

// F) Cache HTTP (sing-box only).
const cacheInput = ref('')
const cacheBusy = ref(false)
const cachePath = ref('')
const cacheError = ref('')

async function fetchCache() {
  cacheBusy.value = true
  cachePath.value = ''
  cacheError.value = ''
  try {
    cachePath.value = await backend.cacheHTTP(cacheInput.value.trim())
  } catch (e) {
    cacheError.value = String(e)
  } finally {
    cacheBusy.value = false
  }
}

// BUG-010: reconcile the proxy toggle with the ACTUAL OS switch on
// mount — a crash leftover, external change, or restart otherwise leaves
// it showing OFF while the OS proxy is ON (and the first click sends
// Enable instead of Disable).
async function refreshProxyState() {
  try {
    proxyOn.value = await backend.getSystemProxyState()
  } catch {
    /* keep local state when unreadable */
  }
}

// Connecting/disconnecting is the other way the OS switch moves under this
// card: proxy mode turns it on, TUN mode leaves autoProxyOn to revert it.
// Mount alone leaves the toggle stale (and the next click sends the wrong
// verb). App.vue owns the poller, so the core state arrives without an
// acquire here.
watch(() => connection.status.running, () => void refreshProxyState())

onMounted(() => {
  void refreshProxyState()
  void refreshPrivileges()
  void refreshSupport()
})
</script>
