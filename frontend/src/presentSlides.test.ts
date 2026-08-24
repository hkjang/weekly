import { describe, expect, it } from 'vitest'
import { reportSlides } from './presentSlides'
import type { Report, ReportAttachment, ReportItem } from './types'

// The frontend had no executable verification of any kind, and this file
// decides what a presenter puts on a screen in a meeting — the same deliverable
// the PPTX export is, and equally unchecked. What it cuts, what it leaves out
// and what it says at the end are all decisions somebody stands behind in a
// room.

function item(overrides: Partial<ReportItem> = {}): ReportItem {
  return {
    category: '인프라', title: '회선 이설', currentResult: '3개 지사 완료',
    nextPlan: '잔여 2개 지사', issue: '', managementAsk: '', progress: 60,
    ...overrides,
  } as ReportItem
}

function report(overrides: Partial<Report> = {}): Report {
  return {
    id: 1, userId: 1, username: 'author', displayName: '작성자',
    weekStart: '2026-08-24', status: 'CLOSED', sourceType: 'MANUAL',
    summary: '주간 요약', version: 1, createdAt: '', updatedAt: '',
    items: [item()], comments: [],
    ...overrides,
  } as Report
}

describe('reportSlides', () => {
  it('배포 순서대로 표지, 업무, 종료를 만든다', () => {
    const slides = reportSlides(report())
    expect(slides.map(slide => slide.kind)).toEqual(['cover', 'entry', 'end'])
    expect(slides[0].title).toContain('2026-08-24')
    expect(slides[1].title).toBe('회선 이설')
  })

  it('빈 항목은 슬라이드에 자리를 차지하지 않는다', () => {
    // A labelled block with nothing under it is a line the room reads and
    // learns nothing from.
    const [, entry] = reportSlides(report({ items: [item({ issue: '', managementAsk: '' })] }))
    const labels = (entry.blocks ?? []).map(block => block.label)
    expect(labels).toEqual(['금주 실적', '차주 계획'])
  })

  it('이슈와 상위 조직 요청이 있으면 각각 자기 톤으로 실린다', () => {
    const [, entry] = reportSlides(report({
      items: [item({ issue: '임대 일정 지연', managementAsk: '예산 승인 필요' })],
    }))
    const blocks = entry.blocks ?? []
    expect(blocks.find(block => block.label === '이슈')?.tone).toBe('issue')
    expect(blocks.find(block => block.label === '상위 조직 요청')?.tone).toBe('ask')
    expect(blocks.find(block => block.label === '상위 조직 요청')?.text).toBe('예산 승인 필요')
  })

  it('길면 자르고 잘랐다는 표시를 남긴다', () => {
    // Shrinking text to fit is how a slide becomes unreadable from the back of
    // the room; cutting it and saying so is the choice this file makes.
    const long = '가'.repeat(900)
    const [, entry] = reportSlides(report({ items: [item({ currentResult: long })] }))
    const text = (entry.blocks ?? []).find(block => block.label === '금주 실적')?.text ?? ''
    expect(text.length).toBeLessThan(long.length)
    expect(text.endsWith('…')).toBe(true)
  })

  it('발표자 화면에는 자르지 않은 원문이 그대로 남는다', () => {
    // The one person who needs every word is the presenter.
    const long = '나'.repeat(900)
    const [, entry] = reportSlides(report({ items: [item({ currentResult: long })] }))
    expect(entry.presenterText ?? '').toContain(long)
  })

  it('요청이 있으면 종료 화면이 결정을 확인하라고 말한다', () => {
    const withAsk = reportSlides(report({ items: [item({ managementAsk: '예산 승인 필요' })] }))
    expect(withAsk[withAsk.length - 1].subtitle).toContain('결정')

    const without = reportSlides(report({ items: [item({ managementAsk: '   ' })] }))
    expect(without[without.length - 1].subtitle).not.toContain('결정')
  })

  it('파일이 없는 캡처는 빈 화면으로 띄우지 않는다', () => {
    // An empty frame is worse than no frame: the room waits for something.
    const attachments = [
      { id: 1, filename: '있음.png', caption: '', placement: 'BEFORE', available: true, width: 10, height: 10 },
      { id: 2, filename: '없음.png', caption: '', placement: 'BEFORE', available: false, width: 10, height: 10 },
    ] as unknown as ReportAttachment[]
    const slides = reportSlides(report(), attachments)
    const images = slides.filter(slide => slide.kind === 'image')
    expect(images).toHaveLength(1)
    expect(images[0].eyebrow).toContain('1/1')
  })

  it('캡처는 표지 다음에 오고 표지 앞에 오지 않는다', () => {
    // Opening a meeting on an unlabelled screenshot tells the room nothing.
    const attachments = [
      { id: 1, filename: '앞.png', caption: '', placement: 'BEFORE', available: true, width: 10, height: 10 },
    ] as unknown as ReportAttachment[]
    const slides = reportSlides(report(), attachments)
    expect(slides[0].kind).toBe('cover')
    expect(slides[1].kind).toBe('image')
  })
})
