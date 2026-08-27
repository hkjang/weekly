import { describe, expect, it } from 'vitest'
import { jobClick, panelState } from './importPanel'

// Reproduced on a running deployment: the list auto-selects the newest job,
// and clicking it left "불러오는 중…" on screen until the tab was reloaded.

describe('jobClick', () => {
  it('이미 보고 있는 작업을 다시 누르면 아무것도 버리지 않습니다', () => {
    expect(jobClick(7, 7)).toBe('ignore')
  })

  it('다른 작업을 누르면 바꿉니다', () => {
    expect(jobClick(7, 8)).toBe('switch')
  })

  it('아무것도 고르지 않은 상태에서 누르면 바꿉니다', () => {
    expect(jobClick(undefined, 7)).toBe('switch')
  })
})

describe('panelState', () => {
  it('고른 것이 없으면 안내를 보여 줍니다', () => {
    expect(panelState(undefined, false, false)).toBe('empty')
  })

  it('불러오는 중에는 기다리게 합니다', () => {
    expect(panelState(7, false, false)).toBe('loading')
  })

  it('불러왔으면 보여 줍니다', () => {
    expect(panelState(7, true, false)).toBe('detail')
  })

  it('실패하면 영영 기다리게 두지 않고 실패라고 말합니다', () => {
    expect(panelState(7, false, true)).toBe('failed')
  })
})
