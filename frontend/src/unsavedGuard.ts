/**
 * One place that knows whether the screen is holding work that has not been
 * saved, so that leaving takes a deliberate answer instead of happening by
 * accident.
 *
 * A module-level registration rather than context because the two places that
 * have to ask are not React children of the screen that knows: the hash router
 * listens on `window`, and `beforeunload` fires outside React entirely.
 */

let isDirty: (() => boolean) | null = null

/** Screens call this on mount with a check, and with null on unmount. */
export function registerUnsavedGuard(check: (() => boolean) | null) {
  isDirty = check
}

export function hasUnsavedWork() {
  return !!isDirty && isDirty()
}

/**
 * Returns true when it is safe to proceed. The wording names the consequence
 * rather than asking an abstract question, because the browser dialog is the
 * last thing standing between the author and an hour of retyping.
 */
export function confirmDiscard() {
  if (!hasUnsavedWork()) return true
  return window.confirm('저장하지 않은 변경이 있습니다.\n이 화면을 벗어나면 작성 중인 내용이 사라집니다. 계속하시겠습니까?')
}
