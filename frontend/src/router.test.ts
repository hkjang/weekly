import { describe, expect, it } from 'vitest'
import { appHandlesClick, routeHash } from './router'

// Navigation used to be buttons, which cost every affordance a link gives for
// free — middle click, ctrl+click, "새 탭에서 열기", "링크 주소 복사". In a tool
// people keep open all day, comparing your own report against the team's in a
// second tab is an ordinary thing to want, and there was no way to do it.
//
// Anchors give that back, but only if a plain click still goes through the app:
// a full reload throws away in-flight state, and leaving a page with unsaved
// edits has to ask first.
const plain = { defaultPrevented: false, button: 0, metaKey: false, ctrlKey: false, shiftKey: false, altKey: false }

describe('appHandlesClick', () => {
  it('평범한 왼쪽 클릭은 앱이 처리한다', () => {
    expect(appHandlesClick(plain)).toBe(true)
  })

  it('보조 키를 누른 클릭은 브라우저에 맡긴다', () => {
    // Each of these means "open this somewhere else", and nothing is being left
    // behind in this tab — so the unsaved-changes question does not apply.
    for (const key of ['metaKey', 'ctrlKey', 'shiftKey', 'altKey'] as const) {
      expect(appHandlesClick({ ...plain, [key]: true })).toBe(false)
    }
  })

  it('가운데 버튼과 오른쪽 버튼은 브라우저에 맡긴다', () => {
    expect(appHandlesClick({ ...plain, button: 1 })).toBe(false)
    expect(appHandlesClick({ ...plain, button: 2 })).toBe(false)
  })

  it('이미 처리된 클릭은 다시 처리하지 않는다', () => {
    expect(appHandlesClick({ ...plain, defaultPrevented: true })).toBe(false)
  })
})

describe('routeHash', () => {
  it('링크에 넣을 수 있는 주소를 만든다', () => {
    // The href a navigation item carries: without it a new tab lands on the
    // dashboard instead of the screen the reader pointed at.
    expect(routeHash('team')).toBe('#/team')
    expect(routeHash('rollup', { kind: 'MONTH', period: '2026-08' })).toBe('#/rollup?kind=MONTH&period=2026-08')
  })

  it('빈 값은 주소를 어지럽히지 않는다', () => {
    expect(routeHash('history', { week: '', status: undefined })).toBe('#/history')
  })
})
