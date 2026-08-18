import type { PresentSlide } from './PresentationMode'
import type { ReportAttachment } from './types'
import type { MeetingView, Report, Rollup } from './types'

/**
 * Slide builders. Every screen that can export a PPTX can also present, and the
 * deck shown on screen carries the same content as the exported file.
 *
 * The rule throughout: a slide is what someone can read from across a room, so
 * long text is truncated with a marker rather than shrunk to fit. Anyone who
 * needs the full wording has the report or the exported file.
 */

const SLIDE_TEXT_LIMIT = 700
const SLIDE_BLOCK_LIMIT = 420

function clamp(value: string, limit: number): string {
  const text = (value ?? '').trim()
  if (text.length <= limit) return text
  return `${text.slice(0, limit).trimEnd()}…`
}

/** Only include a block when it has content; empty labels waste the slide. */
function block(label: string, text: string, tone?: 'issue' | 'ask' | 'plan') {
  const trimmed = clamp(text, SLIDE_BLOCK_LIMIT)
  return trimmed ? [{ label, text: trimmed, tone }] : []
}

// ---------------------------------------------------------------------------
// Weekly report
// ---------------------------------------------------------------------------

const reportStatusLabel: Record<string, string> = {
  DRAFT: '작성 중', SUBMITTED: '검토 요청', REVISION_REQUESTED: '보완 요청',
  APPROVED: '승인', CLOSED: '확정',
}

/**
 * captureSlides turns attached screen captures into slides, one image each,
 * exactly as the PPTX export does.
 *
 * Placement follows the panel's own labels: BEFORE is "본문 앞", so the captures
 * sit between the cover and the work items rather than ahead of the cover. The
 * export grafts them onto the very start of the file; on screen the presenter
 * needs the cover for context first, and starting a meeting on an unlabelled
 * screenshot tells the room nothing.
 */
function captureSlides(reportId: number, attachments: ReportAttachment[], placement: 'BEFORE' | 'AFTER'): PresentSlide[] {
  const group = attachments.filter(item => item.placement === placement)
  return group.map((item, index) => ({
    kind: 'image' as const,
    eyebrow: `${placement === 'BEFORE' ? '본문 앞 캡처' : '본문 뒤 캡처'} ${index + 1}/${group.length}`,
    title: item.caption || item.filename,
    image: {
      url: `/api/v1/reports/${reportId}/attachments/${item.id}`,
      caption: item.caption && item.caption !== item.filename ? item.filename : '',
      width: item.width, height: item.height,
    },
  }))
}

export function reportSlides(report: Report, attachments: ReportAttachment[] = []): PresentSlide[] {
  const slides: PresentSlide[] = [{
    kind: 'cover',
    eyebrow: '주간 업무보고',
    title: `${report.weekStart} 주차`,
    subtitle: `${report.displayName} · ${reportStatusLabel[report.status] ?? report.status}`,
    meta: [`업무 ${report.items.length}건`, attachments.length ? `캡처 ${attachments.length}장` : ''].filter(Boolean),
    body: clamp(report.summary, SLIDE_TEXT_LIMIT),
  }]

  slides.push(...captureSlides(report.id, attachments, 'BEFORE'))

  report.items.forEach((item, index) => {
    slides.push({
      kind: 'entry',
      eyebrow: `업무 ${index + 1}/${report.items.length}${item.category ? ` · ${item.category}` : ''}`,
      title: item.title || `업무 ${index + 1}`,
      meta: [`진척 ${item.progress}%`],
      blocks: [
        ...block('금주 실적', item.currentResult),
        ...block('차주 계획', item.nextPlan, 'plan'),
        ...block('이슈', item.issue, 'issue'),
        ...block('상위 조직 요청', item.managementAsk ?? '', 'ask'),
      ],
    })
  })

  slides.push(...captureSlides(report.id, attachments, 'AFTER'))

  slides.push({
    kind: 'end',
    title: '보고 종료',
    subtitle: report.items.some(item => (item.managementAsk ?? '').trim())
      ? '상위 조직 요청 사항의 결정을 확인해 주세요.'
      : '질의 사항을 확인해 주세요.',
  })
  return slides
}

// ---------------------------------------------------------------------------
// Period rollup
// ---------------------------------------------------------------------------

const highlightTone: Record<string, string> = { RISK: 'long_issue', WARNING: 'new_issue', INFO: 'change', GOOD: 'progress' }

export function rollupSlides(view: Rollup): PresentSlide[] {
  const insights = view.insights
  const slides: PresentSlide[] = [{
    kind: 'cover',
    eyebrow: '기간 업무보고',
    title: view.label,
    subtitle: `${view.scopeLabel} · ${view.start} ~ ${view.end}`,
    meta: [`업무 ${insights.totalItems}건`, `완료율 ${insights.completionRate}%`, `평균 진척 ${insights.averageProgress}%`],
    body: clamp(view.summary, SLIDE_TEXT_LIMIT),
  }, {
    kind: 'section',
    eyebrow: '요약',
    title: '기간 성과 요약',
    meta: [
      `완료 ${insights.completedItems}건`,
      `진행 ${insights.inProgressItems}건`,
      `미착수 ${insights.notStartedItems}건`,
      `이슈 ${insights.issueItems}건`,
      `요청 ${insights.askItems}건`,
    ],
    body: `보고 커버리지 ${insights.reportCoverage}% (${insights.reportedWeeks}/${insights.expectedWeeks}주) · `
      + `중복 제거 ${insights.duplicatesCut}건 · 이월 ${insights.carryoverItems}건 · 정체 ${insights.stalledItems}건`,
  }]

  // Executive judgement comes before the item list: a room that runs out of
  // time should have heard the risks, not the first five tasks alphabetically.
  if (view.highlights.length > 0) {
    slides.push({ kind: 'section', eyebrow: `${view.highlights.length}건`, title: '경영 인사이트' })
    view.highlights.forEach((highlight, index) => slides.push({
      kind: 'entry',
      eyebrow: `경영 인사이트 ${index + 1}/${view.highlights.length} · ${highlight.severity}`,
      title: highlight.title,
      body: clamp(highlight.detail, SLIDE_TEXT_LIMIT),
      tone: highlightTone[highlight.severity] ?? 'change',
    }))
  }

  const attention = view.items.filter(item => item.atRisk || item.stalled || item.managementAsk.trim())
  const rest = view.items.filter(item => !attention.includes(item))
  const ordered = [...attention, ...rest]

  if (ordered.length > 0) {
    slides.push({
      kind: 'section', eyebrow: `${ordered.length}건`, title: '업무별 실적',
      subtitle: attention.length > 0 ? `조치가 필요한 ${attention.length}건을 먼저 봅니다.` : undefined,
    })
    ordered.forEach((item, index) => {
      const flags = [
        item.completed ? '완료' : '', item.stalled ? '정체' : '', item.atRisk ? '이슈 지속' : '',
        item.carryover ? '이월' : '',
      ].filter(Boolean)
      slides.push({
        kind: 'entry',
        eyebrow: `업무 ${index + 1}/${ordered.length}${item.category ? ` · ${item.category}` : ''}`,
        title: item.title,
        meta: [
          `진척 ${item.startProgress}% → ${item.progress}%`,
          `${item.weekCount}주 보고`,
          item.owners.length > 1 ? `담당 ${item.owners.length}명` : item.owners[0] ?? '',
          ...flags,
        ].filter(Boolean),
        blocks: [
          ...block('실적', item.currentResult),
          ...block('계획', item.nextPlan, 'plan'),
          ...block('이슈', item.issue, 'issue'),
          ...block('상위 조직 요청', item.managementAsk, 'ask'),
        ],
        note: item.mergedTitles.length > 0
          ? `유사 업무 ${item.mergedTitles.length}건을 합쳐 표시합니다: ${item.mergedTitles.slice(0, 3).join(', ')}`
          : undefined,
      })
    })
  }

  slides.push({
    kind: 'end', title: '보고 종료',
    subtitle: insights.askItems > 0
      ? `상위 조직 결정이 필요한 ${insights.askItems}건을 확인해 주세요.`
      : '질의 사항을 확인해 주세요.',
  })
  return slides
}

// ---------------------------------------------------------------------------
// Meeting agenda
// ---------------------------------------------------------------------------

export const meetingNotes = [
  '결정 필요 항목은 이 자리에서 결론을 남기고 넘어갑니다.',
  '지속 이슈는 상태 확인이 아니라 접근 방식을 바꿀지 정합니다.',
  '보고 누락은 담당자에게 현황을 직접 확인합니다.',
]

export function meetingSlides(view: MeetingView): PresentSlide[] {
  const agendaCount = view.sections.reduce((sum, section) => sum + section.entries.length, 0)
  const slides: PresentSlide[] = [{
    kind: 'cover',
    eyebrow: view.scope === 'TEAM' ? '조직 주간 회의' : '개인 주간 점검',
    title: `${view.week} 주차 회의`,
    subtitle: `업무 ${view.workItems}건 · 담당자 ${view.people}명 · 안건 ${agendaCount}건`,
    body: '→ 또는 스페이스로 진행 · F 전체화면 · N 진행 안내 · Esc 종료',
  }]

  for (const section of view.sections) {
    if (!section.entries.length) continue
    slides.push({
      kind: 'section', eyebrow: `${section.entries.length}건`,
      title: section.title, subtitle: section.purpose, tone: section.key,
    })
    section.entries.forEach((entry, index) => slides.push({
      kind: 'entry',
      eyebrow: `${section.title} ${index + 1}/${section.entries.length}`,
      title: entry.title,
      tone: section.key,
      meta: [
        entry.displayName, entry.organizationName, entry.category,
        `진척 ${entry.progress}%`,
        entry.progressDelta !== 0 ? `${entry.progressDelta > 0 ? '+' : ''}${entry.progressDelta}%` : '',
        `${entry.weeks}주 보고`,
      ].filter(Boolean),
      body: clamp(entry.detail, SLIDE_TEXT_LIMIT),
      note: entry.note,
    }))
  }

  slides.push({
    kind: 'end', title: '안건 종료',
    subtitle: '결정 사항과 후속 조치는 각 담당자의 다음 주 보고에 반영합니다.',
  })
  return slides
}
