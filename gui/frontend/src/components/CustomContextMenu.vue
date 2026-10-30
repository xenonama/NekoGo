<!-- Custom edit context menu. The native WebView2 menu (Inspect/Reload/Back)
     is blocked everywhere (see the capture-phase listener below); inside
     editable fields this component shows Cut/Copy/Paste/Select All instead,
     so no native menu item is ever reachable. Elements carrying
     data-rowmenu (proxy cards) are exempt: their own @contextmenu
     handler opens the row menu. -->
<template>
  <div
    v-if="visible"
    ref="menu"
    class="nb-ctxmenu"
    :style="{ left: x + 'px', top: y + 'px' }"
  >
    <button class="nb-ctxmenu-item" :disabled="!canCutCopy" @click="cut">Cut</button>
    <button class="nb-ctxmenu-item" :disabled="!canCutCopy" @click="copy">Copy</button>
    <button class="nb-ctxmenu-item" :disabled="!editable" @click="paste">Paste</button>
    <button class="nb-ctxmenu-item" :disabled="!editable" @click="selectAll">Select All</button>
  </div>
</template>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'

const visible = ref(false)
const x = ref(0)
const y = ref(0)
const editable = ref(false)
const canCutCopy = ref(false)
const menu = ref<HTMLElement | null>(null)
let target: (HTMLInputElement | HTMLTextAreaElement) | null = null

function findEditable(e: Event): (HTMLInputElement | HTMLTextAreaElement) | null {
  const el = (e.target as HTMLElement | null)?.closest?.('input, textarea')
  if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) return el
  return null
}

function selectionText(): string {
  if (!target) return ''
  try {
    return target.value.substring(target.selectionStart ?? 0, target.selectionEnd ?? 0)
  } catch {
    return ''
  }
}

function notifyInput() {
  // Keep v-model in sync after programmatic edits.
  target?.dispatchEvent(new Event('input', { bubbles: true }))
}

function onContextMenu(e: MouseEvent) {
  // Row menus (proxy cards carry data-rowmenu) own their zone:
  // let the row's own @contextmenu handler open its menu instead of
  // the edit menu or the native one.
  const zone = (e.target as HTMLElement | null)?.closest?.('[data-rowmenu]')
  if (zone) return
  // Block the native menu unconditionally first.
  e.preventDefault()
  e.stopPropagation()
  target = findEditable(e)
  if (!target || target.disabled || target.readOnly) {
    close()
    return
  }
  target.focus()
  editable.value = true
  canCutCopy.value = selectionText().length > 0
  visible.value = true
  // Clamp into the viewport once rendered.
  const px = e.clientX
  const py = e.clientY
  void nextTick(() => {
    const w = menu.value?.offsetWidth ?? 180
    const h = menu.value?.offsetHeight ?? 150
    x.value = Math.max(4, Math.min(px, window.innerWidth - w - 4))
    y.value = Math.max(4, Math.min(py, window.innerHeight - h - 4))
  })
}

async function copyText(s: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(s)
    return true
  } catch {
    try {
      return document.execCommand('copy')
    } catch {
      return false
    }
  }
}

async function copy() {
  if (target) await copyText(selectionText())
  close()
}

async function cut() {
  if (!target) {
    close()
    return
  }
  const s = target.selectionStart ?? 0
  const epos = target.selectionEnd ?? 0
  if (epos > s) {
    await copyText(selectionText())
    try {
      target.setRangeText('', s, epos, 'end')
      notifyInput()
    } catch {
      /* best effort */
    }
  }
  close()
}

async function paste() {
  if (target) {
    try {
      const text = await navigator.clipboard.readText()
      const s = target.selectionStart ?? target.value.length
      const epos = target.selectionEnd ?? s
      try {
        target.setRangeText(text, s, epos, 'end')
        notifyInput()
      } catch {
        /* best effort */
      }
    } catch {
      try {
        target.focus()
        document.execCommand('paste')
        notifyInput()
      } catch {
        /* clipboard unavailable */
      }
    }
  }
  close()
}

function selectAll() {
  try {
    target?.select()
  } catch {
    try {
      document.execCommand('selectAll')
    } catch {
      /* best effort */
    }
  }
  close()
}

function close() {
  visible.value = false
  editable.value = false
  canCutCopy.value = false
  target = null
}

function onDocClick(e: MouseEvent) {
  if (menu.value && !menu.value.contains(e.target as Node)) close()
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') close()
}

onMounted(() => {
  document.addEventListener('contextmenu', onContextMenu, true)
  document.addEventListener('click', onDocClick)
  document.addEventListener('keydown', onKey)
})
onBeforeUnmount(() => {
  document.removeEventListener('contextmenu', onContextMenu, true)
  document.removeEventListener('click', onDocClick)
  document.removeEventListener('keydown', onKey)
})
</script>
