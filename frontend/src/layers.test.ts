import { describe, expect, it } from 'vitest'
import { isTopLayer, popLayer, pushLayer } from './layers'

// This module exists because one Escape used to close everything at once: a
// reviewer leaving a presentation deck also lost the report they were reading
// underneath it. The rule it enforces is small enough to state in a sentence,
// which is exactly why it is worth writing down twice.

// Layers are module state and there is no reset to call, so every test here
// closes what it opens. An earlier version of this file had a beforeEach that
// looked like it emptied the stack and did nothing at all: isTopLayer is true
// for whatever was pushed last, so its probe always reported success on the
// first pass. A setup that pretends to isolate is worse than none.

describe('열려 있는 것이 하나도 없을 때', () => {
  // isTopLayer guards this case twice: it checks the stack is not empty, and
  // then compares against its last entry. The second check alone would do —
  // stack[-1] on an empty array is undefined and equals no symbol — so removing
  // the first is an equivalent change, not an untested one. Worth knowing when
  // a mutation survives here: the answer is dead code, not a missing test.
  it('아무것도 맨 위가 아니다', () => {
    const opened = pushLayer()
    popLayer(opened)
    expect(isTopLayer(opened)).toBe(false)
  })
})

describe('겹쳐 열렸을 때', () => {
  it('마지막에 연 것만 맨 위다', () => {
    const report = pushLayer()
    const deck = pushLayer()
    expect([isTopLayer(report), isTopLayer(deck)]).toEqual([false, true])
    popLayer(deck); popLayer(report)
  })

  it('위를 닫으면 그 아래가 맨 위가 된다', () => {
    // The whole point: Escape leaves the deck and the report stays open.
    const report = pushLayer()
    const deck = pushLayer()
    popLayer(deck)
    expect([isTopLayer(report), isTopLayer(deck)]).toEqual([true, false])
    popLayer(report)
  })

  it('셋이 열려도 한 번에 하나씩 벗겨진다', () => {
    const first = pushLayer(), second = pushLayer(), third = pushLayer()
    expect(isTopLayer(third)).toBe(true)
    popLayer(third)
    expect(isTopLayer(second)).toBe(true)
    popLayer(second)
    expect(isTopLayer(first)).toBe(true)
    popLayer(first)
    expect(isTopLayer(first)).toBe(false)
  })

  it('가운데 것이 먼저 닫혀도 맨 위는 그대로다', () => {
    // Overlays do not always close in the order they opened — a modal can be
    // dismissed by something other than the key that would close the deck above
    // it. Removing it must not hand the top to the wrong layer.
    const bottom = pushLayer(), middle = pushLayer(), top = pushLayer()
    popLayer(middle)
    expect([isTopLayer(top), isTopLayer(bottom)]).toEqual([true, false])
    popLayer(top)
    expect(isTopLayer(bottom)).toBe(true)
    popLayer(bottom)
  })
})

describe('같은 층을 두 번 닫아도', () => {
  it('아래에 있는 것을 건드리지 않는다', () => {
    const report = pushLayer()
    const deck = pushLayer()
    popLayer(deck)
    popLayer(deck)
    expect(isTopLayer(report)).toBe(true)
    popLayer(report)
  })
})

describe('층마다 다른 표식을 받는다', () => {
  it('두 번 열면 서로 다른 것이다', () => {
    // Two identical dialogs open at once must not be mistaken for one another,
    // or closing the second would hand the top to nothing.
    const first = pushLayer()
    const second = pushLayer()
    expect(first).not.toBe(second)
    popLayer(second); popLayer(first)
  })
})
