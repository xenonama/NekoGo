<!-- Custom window caption. The native Windows title bar is disabled
     (WebviewWindowOptions.Frameless), so this bar owns dragging and the
     caption buttons.

     `--wails-draggable: drag` hands a press on the bar to the runtime's
     native caption drag loop (WM_NCLBUTTONDOWN / HTCAPTION), which is what
     keeps Aero Snap and multi-monitor dragging working. That property
     inherits, so the button cluster resets it to `no-drag`: without the
     reset a click on Close would be treated as a drag handle and never
     reach the button.

     Close goes through Window.Close(), which emits Common.WindowClosing —
     the same event main.go hooks for close-to-tray, so that setting keeps
     working from the custom bar. -->
<template>
  <header class="nb-titlebar" @dblclick="toggleMaximise">
    <img class="nb-titlebar-icon" :src="appIcon" alt="" />
    <span class="nb-titlebar-title">NekoGo</span>
    <div class="nb-titlebar-controls" @dblclick.stop>
      <button class="nb-tb-btn" type="button" title="Minimise" aria-label="Minimise" @click="minimise">
        <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
          <line x1="0" y1="5" x2="10" y2="5" stroke="currentColor" stroke-width="1" />
        </svg>
      </button>
      <button
        class="nb-tb-btn"
        type="button"
        :title="maximised ? 'Restore' : 'Maximise'"
        :aria-label="maximised ? 'Restore' : 'Maximise'"
        @click="toggleMaximise"
      >
        <svg v-if="maximised" width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
          <rect x="0.5" y="2.5" width="7" height="7" fill="none" stroke="currentColor" stroke-width="1" />
          <path d="M2.5 2.5v-2h7v7h-2" fill="none" stroke="currentColor" stroke-width="1" />
        </svg>
        <svg v-else width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
          <rect x="0.5" y="0.5" width="9" height="9" fill="none" stroke="currentColor" stroke-width="1" />
        </svg>
      </button>
      <button class="nb-tb-btn nb-tb-btn--close" type="button" title="Close" aria-label="Close" @click="close">
        <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
          <path d="M0 0l10 10M10 0L0 10" stroke="currentColor" stroke-width="1" />
        </svg>
      </button>
    </div>
  </header>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { Events, Window } from '@wailsio/runtime'
import appIcon from '../assets/app.png'

// Drives the maximise/restore glyph. Read from the window rather than
// flipped locally: the user can also maximise with Win+Up, a snap layout
// or a caption double-click, none of which go through toggleMaximise().
const maximised = ref(false)

async function syncMaximised() {
  try {
    maximised.value = await Window.IsMaximised()
  } catch {
    /* `vite dev` in a plain browser: no window to ask. */
  }
}

function minimise() {
  Window.Minimise().catch(() => {})
}

function toggleMaximise() {
  Window.ToggleMaximise().catch(() => {})
}

function close() {
  Window.Close().catch(() => {})
}

let unsubscribes: Array<() => void> = []

onMounted(() => {
  void syncMaximised()
  // Both namespaces: the Windows backend emits the windows:* events, and
  // the common:* ones are the cross-platform aliases. Subscribing to both
  // keeps the glyph correct without guessing which build emits which.
  const names = [
    Events.Types.Windows.WindowMaximise,
    Events.Types.Windows.WindowUnMaximise,
    Events.Types.Windows.WindowRestore,
    Events.Types.Common.WindowMaximise,
    Events.Types.Common.WindowUnMaximise,
    Events.Types.Common.WindowRestore,
  ]
  for (const name of names) {
    try {
      unsubscribes.push(Events.On(name, () => void syncMaximised()))
    } catch {
      /* no runtime: nothing will ever fire */
    }
  }
})

onUnmounted(() => {
  for (const off of unsubscribes) off()
  unsubscribes = []
})
</script>
