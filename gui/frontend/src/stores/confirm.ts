import { defineStore } from 'pinia'
import { ref } from 'vue'

export interface ConfirmAsk {
  title: string
  body: string
  confirmLabel: string
  /** Styles the accept button as the destructive action it is. */
  danger?: boolean
}

/** The app's own yes/no dialog, so a destructive setting asks inside
 *  NekoGo's chrome instead of window.confirm — which WebView2 renders as
 *  a browser box, the same reason the elevation prompt stopped using it.
 *
 *  ask() resolves when the dialog answers, so the caller keeps its own
 *  follow-up instead of duplicating the dialog's state (the elevation
 *  store works the same way). */
export const useConfirmStore = defineStore('confirm', () => {
  const request = ref<ConfirmAsk | null>(null)
  let settle: (ok: boolean) => void = () => {}

  /** Shows the dialog and waits for the answer. A second request while
   *  one is open is refused rather than stacked: two promises waiting on
   *  one dialog cannot both be right. */
  function ask(what: ConfirmAsk): Promise<boolean> {
    if (request.value) return Promise.resolve(false)
    request.value = what
    return new Promise<boolean>((resolve) => {
      settle = resolve
    })
  }

  function close(ok: boolean) {
    request.value = null
    const done = settle
    settle = () => {}
    done(ok)
  }

  return { request, ask, close }
})
