import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import AutoResizeTextarea, { AUTO_RESIZE_TEXTAREA_CLASS, resizeTextarea } from './AutoResizeTextarea'

afterEach(() => vi.unstubAllGlobals())

function textareaWith(scrollHeight: number, borderHeight = 0) {
  const style = { height: '140px' } as CSSStyleDeclaration
  let heightWhenMeasured = ''
  const textarea = { style } as HTMLTextAreaElement
  Object.defineProperty(textarea, 'scrollHeight', {
    get: () => {
      heightWhenMeasured = style.height
      return scrollHeight
    },
  })
  Object.defineProperty(textarea, 'offsetHeight', { value: 140 + borderHeight })
  Object.defineProperty(textarea, 'clientHeight', { value: 140 })
  return { textarea, measuredAfterReset: () => heightWhenMeasured }
}

describe('보고서 textarea 자동 높이', () => {
  it('기존 높이를 먼저 해제하고 border-box 테두리까지 더해 내부 스크롤을 없앤다', () => {
    const { textarea, measuredAfterReset } = textareaWith(187.2, 3)
    vi.stubGlobal('getComputedStyle', () => ({
      boxSizing: 'border-box',
    }))

    expect(resizeTextarea(textarea)).toBe(191)
    expect(measuredAfterReset()).toBe('auto')
    expect(textarea.style.height).toBe('191px')
  })

  it('content-box에서는 scrollHeight를 그대로 쓰고 테두리 높이를 섞지 않는다', () => {
    const { textarea } = textareaWith(80, 4)
    vi.stubGlobal('getComputedStyle', () => ({ boxSizing: 'content-box' }))

    expect(resizeTextarea(textarea)).toBe(80)
    expect(textarea.style.height).toBe('80px')
  })

  it('내용이 줄어든 뒤 다시 호출하면 이전의 큰 높이를 유지하지 않는다', () => {
    const style = { height: '240px' } as CSSStyleDeclaration
    let scrollHeight = 220
    const textarea = { style, offsetHeight: 142, clientHeight: 140 } as HTMLTextAreaElement
    Object.defineProperty(textarea, 'scrollHeight', { get: () => scrollHeight })
    vi.stubGlobal('getComputedStyle', () => ({ boxSizing: 'border-box' }))

    expect(resizeTextarea(textarea)).toBe(222)
    scrollHeight = 64
    expect(resizeTextarea(textarea)).toBe(66)
    expect(textarea.style.height).toBe('66px')
  })

  it('기존 className을 보존하면서 자동 높이 전용 클래스를 붙인다', () => {
    const markup = renderToStaticMarkup(<AutoResizeTextarea className="summary-input" aria-label="주간 요약" value={'첫 줄\n둘째 줄'} readOnly/>)
    expect(markup).toContain(`class="${AUTO_RESIZE_TEXTAREA_CLASS} summary-input"`)
    expect(markup).toContain('aria-label="주간 요약"')
    expect(markup).toContain('첫 줄\n둘째 줄')
  })
})
