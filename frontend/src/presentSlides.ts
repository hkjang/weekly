import type { PresentSlide } from './PresentationMode'
import type { ReportAttachment } from './types'
import type { MeetingView, Report, Rollup } from './types'

/**
 * Slide builders. Every screen that can export a PPTX can also present, and the
 * deck shown on screen is built from the same report and the same figures as
 * the exported file — measured: both read one `insights` object, and an export
 * of a nine-item report with 1,300-character entries carried every title, every
 * result, every plan, every issue and the management ask.
 *
 * It does not carry the same *text*, and saying so was overstating it. A slide
 * is what someone can read from across a room, so long text is truncated with a
 * marker rather than shrunk to fit; the presenter's own screen gets every word
 * through presenterText, and the exported file is uncut. The two decks also
 * place captures differently, on purpose, for the reason captureSlides gives.
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

/**
 * The same fields, uncut, for the presenter's own screen.
 *
 * The slide is written to be read across a room and so it is shortened; the
 * presenter is the one person who needs every word, and until now the only way
 * to get it was to leave the deck.
 */
function presenterText(...parts: [string, string][]): string | undefined {
  const written = parts
    .map(([label, text]) => [label, (text ?? '').trim()] as const)
    .filter(([, text]) => text !== '')
    .map(([label, text]) => `${label}\n${text}`)
  return written.length ? written.join('\n\n') : undefined
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
  // A capture whose file is missing would present as an empty frame, which is
  // worse than not being there: the room waits for something to appear.
  const group = attachments.filter(item => item.placement === placement && item.available !== false)
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
  const usable = attachments.filter(item => item.available !== false).length
  // A missing report is itself material: the person was explicitly selected,
  // and silently dropping them would make a leader read "nothing selected"
  // where the actual state is "selected, not written yet".
  const included = report.includedMaterials ?? []
  const slides: PresentSlide[] = [{
    kind: 'cover',
    eyebrow: '주간 업무보고',
    title: `${report.weekStart} 주차`,
    subtitle: `${report.displayName} · ${reportStatusLabel[report.status] ?? report.status}`,
    meta: [`업무 ${report.items.length}건`, included.length ? `팀원 자료 ${included.length}명` : '', usable ? `캡처 ${usable}장` : ''].filter(Boolean),
    body: clamp(report.summary, SLIDE_TEXT_LIMIT),
    presenterText: presenterText(['주간 요약', report.summary]),
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
      presenterText: presenterText(
        ['금주 실적', item.currentResult], ['차주 계획', item.nextPlan],
        ['이슈', item.issue], ['상위 조직 요청', item.managementAsk ?? '']),
    })
  })

  if (included.length > 0) {
    slides.push({
      kind: 'section', eyebrow: `${included.length}명`, title: '팀원 주간보고 자료',
      subtitle: '개인 설정에서 선택한 팀원의 같은 주차 보고입니다.',
    })
    included.forEach((material, memberIndex) => {
      slides.push({
        kind: 'section',
        eyebrow: `팀원 ${memberIndex + 1}/${included.length} · ${material.reportId
          ? (material.status ? (reportStatusLabel[material.status] ?? material.status) : '상태 미확인')
          : '미작성'}`,
        title: material.displayName,
        subtitle: [material.organizationName, material.username ? `@${material.username}` : ''].filter(Boolean).join(' · ') || undefined,
        body: material.reportId ? clamp(material.summary, SLIDE_TEXT_LIMIT) : '해당 주차 보고서 미작성',
        presenterText: material.reportId ? presenterText(['주간 요약', material.summary]) : '해당 주차 보고서 미작성',
      })
      material.items.forEach((item, itemIndex) => slides.push({
        kind: 'entry',
        eyebrow: `${material.displayName} · 업무 ${itemIndex + 1}/${material.items.length}${item.category ? ` · ${item.category}` : ''}`,
        title: item.title || `업무 ${itemIndex + 1}`,
        meta: [`작성자 ${material.displayName}`, `진척 ${item.progress}%`],
        blocks: [
          ...block('금주 실적', item.currentResult),
          ...block('차주 계획', item.nextPlan, 'plan'),
          ...block('이슈', item.issue, 'issue'),
          ...block('상위 조직 요청', item.managementAsk ?? '', 'ask'),
        ],
        presenterText: presenterText(
          ['금주 실적', item.currentResult], ['차주 계획', item.nextPlan],
          ['이슈', item.issue], ['상위 조직 요청', item.managementAsk ?? '']),
      }))
    })
  }

  slides.push(...captureSlides(report.id, attachments, 'AFTER'))

  const hasManagementAsk = report.items.some(item => (item.managementAsk ?? '').trim())
    || included.some(material => material.items.some(item => (item.managementAsk ?? '').trim()))
  slides.push({
    kind: 'end',
    title: '보고 종료',
    subtitle: hasManagementAsk
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
      + `업무 ${insights.sourceItems}→${insights.totalItems}건 · 반복 문장 ${insights.duplicatesCut}줄 정리 · 이월 ${insights.carryoverItems}건 · 정체 ${insights.stalledItems}건`,
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
      presenterText: presenterText(['근거', highlight.detail]),
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
        presenterText: presenterText(
          ['실적', item.currentResult], ['계획', item.nextPlan],
          ['이슈', item.issue], ['상위 조직 요청', item.managementAsk]),
        note: item.mergedTitles.length > 0
          ? `유사 업무 ${item.mergedTitles.length}건을 합쳐 표시합니다: ${item.mergedTitles.slice(0, 3).join(', ')}`
          : undefined,
      })
    })
  }

  // v0.14 settled that exporting and presenting are the same content in two
  // media, so a slide the deck carries the room has to hear too. Outstanding
  // decisions first: one already carried out is history, one still owing
  // something is why this is in a briefing at all.
  if (view.decisions.length > 0) {
    const rank = (status: string) => status === 'OPEN' ? 0 : status === 'DONE' ? 1 : 2
    const ordered = [...view.decisions].sort((left, right) => rank(left.status) - rank(right.status))
    slides.push({
      kind: 'section',
      eyebrow: view.decisionTotal > ordered.length ? `${view.decisionTotal}건 중 ${ordered.length}건` : `${ordered.length}건`,
      title: '기간 내 결정',
      subtitle: view.openDecisions > 0
        ? `후속 조치가 남은 결정이 ${view.openDecisions}건입니다.`
        : '이 기간에 기록된 결정입니다.',
    })
    ordered.forEach((decision, index) => slides.push({
      kind: 'entry',
      eyebrow: `결정 ${index + 1}/${ordered.length}${decision.workTitle ? ` · ${decision.workTitle}` : ''}`,
      title: decision.title,
      meta: [
        `${decision.decidedBy} 결정`,
        decision.decidedOn,
        decision.dueDate ? `후속 기한 ${decision.dueDate}` : '',
        decision.status === 'OPEN' ? '후속 조치 중' : decision.status === 'DONE' ? '완료' : '대체됨',
      ].filter(Boolean),
      blocks: [
        ...block('근거', decision.rationale),
        ...block('후속 조치', decision.followUp, 'plan'),
      ],
      presenterText: presenterText(['근거', decision.rationale], ['후속 조치', decision.followUp]),
      tone: decision.status === 'SUPERSEDED' ? 'silent' : 'decision',
    }))
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
  // The cover counts what exists; the section slides count what is shown. A
  // deck that says 40건 for a heading holding 2,100 is a deck that lies to the
  // room it is being presented to.
  const agendaCount = view.sections.reduce((sum, section) => sum + section.total, 0)
  const slides: PresentSlide[] = [{
    kind: 'cover',
    eyebrow: view.scope === 'TEAM' ? '조직 주간 회의' : '개인 주간 점검',
    title: `${view.week} 주차 회의`,
    subtitle: `업무 ${view.workItems}건 · 담당자 ${view.people}명 · 안건 ${agendaCount}건`,
    body: '→ 진행 · O 슬라이드 목록 · P 발표자 화면 · B 화면 끄기 · F 전체화면 · Esc 종료',
  }]

  for (const section of view.sections) {
    if (!section.entries.length) continue
    slides.push({
      kind: 'section',
      eyebrow: section.total > section.entries.length
        ? `${section.total}건 중 ${section.entries.length}건`
        : `${section.entries.length}건`,
      title: section.title,
      subtitle: section.total > section.entries.length
        ? `${section.purpose} 변화가 큰 ${section.entries.length}건만 실었습니다.`
        : section.purpose,
      tone: section.key,
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
      presenterText: presenterText(['내용', entry.detail], ['판단 근거', entry.note]),
      note: entry.note,
    }))
  }

  slides.push({
    kind: 'end', title: '안건 종료',
    subtitle: '결정 사항과 후속 조치는 각 담당자의 다음 주 보고에 반영합니다.',
  })
  return slides
}
