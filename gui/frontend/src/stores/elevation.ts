import { defineStore } from 'pinia'
import { ref } from 'vue'

/** The single ask behind relaunching NekoGo elevated, so the prompt is
 *  the app's own dialog instead of a browser confirm() box.
 *
 *  ask() resolves when the dialog answers, which keeps each caller's own
 *  follow-up: the connect path reports a backend refusal and hands off to
 *  RelaunchAsAdmin, the SNI tab refreshes its privilege readout after a
 *  failed attempt. Both would have had to duplicate the dialog's state to
 *  do that themselves. */
export const useElevationStore = defineStore('elevation', () => {
  const prompt = ref('')
  let settle: (ok: boolean) => void = () => {}

  /** Shows the dialog and waits for the answer. A second request while
   *  one is open is refused rather than stacked — it would be the same
   *  ask, and two promises waiting on one dialog cannot both be right. */
  function ask(message: string): Promise<boolean> {
    if (prompt.value) return Promise.resolve(false)
    prompt.value = message
    return new Promise<boolean>((resolve) => {
      settle = resolve
    })
  }

  /** Ends the prompt: true when the user accepted the relaunch. */
  function close(ok: boolean) {
    prompt.value = ''
    const done = settle
    settle = () => {}
    done(ok)
  }

  return { prompt, ask, close }
})
