/**
 * A place for the API layer to say "the server no longer knows who this is",
 * and for the shell to hear it.
 *
 * Without this the SPA simply kept running against a dead session: screens that
 * catch their errors showed a toast over an empty page, and screens that do not
 * — the dashboard among them — sat on a spinner that never resolved, with no
 * hint that logging in again was the fix.
 */

type Listener = () => void

let listener: Listener | null = null

export function onSessionLost(callback: Listener | null) {
  listener = callback
}

export function reportSessionLost() {
  listener?.()
}
