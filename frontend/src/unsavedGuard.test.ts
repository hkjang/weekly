import { afterEach, describe, expect, it, vi } from 'vitest'
import { confirmDiscard, hasUnsavedWork, registerUnsavedGuard } from './unsavedGuard'

// The browser dialog is the last thing standing between an author and an hour of
// retyping. Two ways it fails: never asking when there is work to lose, and
// asking when there is nothing — the second is worse than it sounds, because a
// prompt that is usually wrong is a prompt people learn to click through.

afterEach(() => {
  registerUnsavedGuard(null)
  vi.unstubAllGlobals()
})

// The tests run without a DOM, so they supply the one browser call the guard
// makes. That is the point: the guard's job is deciding *whether* to ask, and
// that decision should be checkable without a browser.
function watchConfirm(answer: boolean) {
  const asked: string[] = []
  vi.stubGlobal('window', { confirm: (message: string) => { asked.push(message); return answer } })
  return asked
}

describe('hasUnsavedWork', () => {
  it('아무 화면도 등록하지 않았으면 잃을 것이 없다', () => {
    expect(hasUnsavedWork()).toBe(false)
  })

  it('등록한 화면의 답을 그대로 쓴다', () => {
    let dirty = false
    registerUnsavedGuard(() => dirty)
    expect(hasUnsavedWork()).toBe(false)
    dirty = true
    expect(hasUnsavedWork()).toBe(true)
  })

  it('화면이 사라지면 그 화면의 답도 사라진다', () => {
    // Unmount without this and every later navigation asks about a screen that
    // is no longer there.
    registerUnsavedGuard(() => true)
    registerUnsavedGuard(null)
    expect(hasUnsavedWork()).toBe(false)
  })
})

describe('confirmDiscard', () => {
  it('잃을 것이 없으면 묻지 않고 보낸다', () => {
    const asked = watchConfirm(false)
    registerUnsavedGuard(() => false)
    expect(confirmDiscard()).toBe(true)
    expect(asked).toHaveLength(0)
  })

  it('잃을 것이 있으면 묻는다', () => {
    const asked = watchConfirm(true)
    registerUnsavedGuard(() => true)
    expect(confirmDiscard()).toBe(true)
    expect(asked).toHaveLength(1)
  })

  it('묻는 말이 결과를 말한다', () => {
    // An abstract "are you sure?" does not tell the reader what they are about
    // to lose, and the answer they give is only as good as the question.
    const asked = watchConfirm(true)
    registerUnsavedGuard(() => true)
    confirmDiscard()
    expect(asked[0]).toContain('저장하지 않은 변경')
    expect(asked[0]).toContain('사라집니다')
  })

  it('아니오는 아니오다', () => {
    // Verified in a browser first: dismissing the dialog leaves the editor and
    // its text exactly where they were. This is the rule that made it so.
    const asked = watchConfirm(false)
    registerUnsavedGuard(() => true)
    expect(confirmDiscard()).toBe(false)
    expect(asked).toHaveLength(1)
  })
})
