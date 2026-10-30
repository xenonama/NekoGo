<!-- Chip-style selection button: a pill trigger with a leading icon and
     a themed popover list, replacing the bare <select> look in toolbars.
     Focus stays on the trigger, so Arrow/Enter/Escape and click-outside
     are handled there (native popups cannot be themed, this one is). -->
<template>
  <div
    ref="root"
    class="nb-pick"
    :class="{ 'is-set': isSet, 'is-open': open, 'is-up': up, 'is-flip': flip, 'is-disabled': disabled }"
    @focusout="onFocusOut"
  >
    <button
      ref="trigger"
      type="button"
      class="nb-pick-btn"
      :disabled="disabled"
      :title="title || ariaLabel"
      :aria-label="ariaLabel || title"
      aria-haspopup="listbox"
      :aria-expanded="open"
      @click="toggle"
      @keydown="onKey"
    >
      <Icon v-if="icon" :name="icon" :size="14" class="nb-pick-ico" />
      <span class="nb-pick-label">{{ currentLabel }}</span>
      <Icon name="chevron" :size="13" class="nb-pick-chev" />
    </button>
    <ul v-if="open" class="nb-pick-menu" role="listbox" :aria-label="ariaLabel || title">
      <li v-for="(o, i) in options" :key="o.value" role="presentation">
        <button
          type="button"
          class="nb-pick-opt"
          :class="{ active: o.value === modelValue, focused: i === activeIdx }"
          role="option"
          :aria-selected="o.value === modelValue"
          tabindex="-1"
          @click="choose(o.value)"
          @mouseenter="activeIdx = i"
        >
          <Icon v-if="o.icon" :name="o.icon" :size="14" />
          <span class="nb-pick-opt-label">{{ o.label }}</span>
          <Icon v-if="o.value === modelValue" name="check" :size="13" class="nb-pick-check" />
        </button>
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import Icon, { type IconName } from './Icon.vue'

export interface PickOption {
  value: string
  label: string
  icon?: IconName
}

const props = withDefaults(
  defineProps<{
    /** Optional: fields backed by optional settings render the first
     *  option's label while unset (every such field carries a default). */
    modelValue?: string
    options: PickOption[]
    /** Leading icon on the trigger (category hint: layers, tag, …). */
    icon?: IconName
    title?: string
    ariaLabel?: string
    disabled?: boolean
    /** Value meaning "no choice" — renders as a quiet chip instead of
     *  the accent-tinted one. */
    neutral?: string
    /** Form-field style: never tint, even when the value is non-neutral
     *  (a settings value is not an "active filter"). */
    quiet?: boolean
  }>(),
  { neutral: '', quiet: false },
)

const emit = defineEmits<{ (e: 'update:modelValue', v: string): void }>()

const root = ref<HTMLElement | null>(null)
const trigger = ref<HTMLButtonElement | null>(null)
const open = ref(false)
const activeIdx = ref(0)
// Popover placement: flip above when the list would run past the bottom
// edge, and right-align when it would run past the right edge.
const up = ref(false)
const flip = ref(false)

const isSet = computed(() => !props.quiet && props.modelValue !== props.neutral)
const currentLabel = computed(() => {
  const hit = props.options.find((o) => o.value === props.modelValue)
  return hit?.label ?? props.options[0]?.label ?? ''
})

function place() {
  const el = trigger.value
  if (!el) return
  const r = el.getBoundingClientRect()
  const h = Math.min(300, props.options.length * 34 + 12)
  const w = Math.max(170, r.width)
  // Measure against the box that actually CLIPS the popover, not the window:
  // the app scrolls .nb-center (overflow-y:auto ⇒ overflow-x computes to
  // auto too), so a menu reaching past that edge is cut off and spawns a
  // horizontal scrollbar — the page then shifts right-to-left under the
  // pointer. Every clip edge is the tighter of the container and the window.
  const clip = clipBox(el)
  const right = Math.min(clip.right, window.innerWidth)
  const left = Math.max(clip.left, 0)
  const bottom = Math.min(clip.bottom, window.innerHeight)
  const top = Math.max(clip.top, 0)
  up.value = r.bottom + h + 8 > bottom && r.top - h - 8 > top
  flip.value = r.left + w > right - 8 && r.right - w > left + 8
}

/** The nearest ancestor that clips its content (overflow ≠ visible), in
 *  viewport coordinates; the window when there is none. */
function clipBox(el: HTMLElement): { left: number; right: number; top: number; bottom: number } {
  for (let p = el.parentElement; p; p = p.parentElement) {
    const cs = getComputedStyle(p)
    if (cs.overflowX !== 'visible' || cs.overflowY !== 'visible') {
      const r = p.getBoundingClientRect()
      return { left: r.left, right: r.right, top: r.top, bottom: r.bottom }
    }
  }
  return { left: 0, right: window.innerWidth, top: 0, bottom: window.innerHeight }
}

function openMenu() {
  place()
  open.value = true
  const idx = props.options.findIndex((o) => o.value === props.modelValue)
  activeIdx.value = idx >= 0 ? idx : 0
}

function toggle() {
  if (props.disabled) return
  if (open.value) open.value = false
  else openMenu()
}

function choose(value: string) {
  open.value = false
  trigger.value?.focus()
  if (value !== props.modelValue) emit('update:modelValue', value)
}

/** Trigger keeps focus while the list is open: arrows move the highlight,
 *  Enter commits, Escape closes without changing the value. */
function onKey(e: KeyboardEvent) {
  if (props.disabled) return
  if (e.key === 'Escape' && open.value) {
    e.preventDefault()
    open.value = false
    return
  }
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    e.preventDefault()
    if (!open.value) {
      openMenu()
      return
    }
    const n = props.options.length
    if (n === 0) return
    const step = e.key === 'ArrowDown' ? 1 : -1
    activeIdx.value = (activeIdx.value + step + n) % n
    return
  }
  if (e.key === 'Enter' || e.key === ' ') {
    e.preventDefault()
    if (!open.value) {
      openMenu()
      return
    }
    const hit = props.options[activeIdx.value]
    if (hit) choose(hit.value)
  }
}

function onFocusOut(e: FocusEvent) {
  if (!root.value?.contains(e.relatedTarget as Node | null)) open.value = false
}

function onDocPointer(e: MouseEvent) {
  if (open.value && root.value && !root.value.contains(e.target as Node)) open.value = false
}

// The anchor scrolls with its container (the app scrolls .nb-center, not
// the document), so a scroll while open would leave the list behind.
// Scrolling the popover itself is the one scroll that must not close it:
// long lists (SNI bypass methods) are taller than the 300px cap.
function onDocScroll(e: Event) {
  if (!open.value) return
  if (root.value && e.target instanceof Node && root.value.contains(e.target)) return
  open.value = false
}

onMounted(() => {
  document.addEventListener('mousedown', onDocPointer)
  document.addEventListener('scroll', onDocScroll, true)
})
onBeforeUnmount(() => {
  document.removeEventListener('mousedown', onDocPointer)
  document.removeEventListener('scroll', onDocScroll, true)
})
</script>
