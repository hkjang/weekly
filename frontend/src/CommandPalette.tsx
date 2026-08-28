import { useEffect, useMemo, useRef, useState } from 'react'
import { api } from './api'
import { matchScore } from './router'
import type { PageName } from './router'
import type { ReportListItem, ReportListView, SearchResponse, SessionInfo } from './types'
import { keepFocusInside } from './components'

/**
 * Quick navigation. Ctrl+K (Cmd+K on macOS) opens a searchable list of screens,
 * period reports, recent weekly reports and actions. Matching understands
 * Korean leading consonants, so "ㄱㅇㅂ" finds 기간 업무보고.
 */

export interface Command {
  id: string
  label: string
  hint?: string
  group: string
  keywords?: string[]
  run: () => void
}

const recentKey = 'weekly.palette.recent'

function loadRecent(): string[] {
  try {
    const raw = window.localStorage.getItem(recentKey)
    const parsed = raw ? JSON.parse(raw) : []
    return Array.isArray(parsed) ? parsed.filter(item => typeof item === 'string').slice(0, 12) : []
  } catch { return [] }
}

function rememberRecent(id: string) {
  try {
    const next = [id, ...loadRecent().filter(item => item !== id)].slice(0, 12)
    window.localStorage.setItem(recentKey, JSON.stringify(next))
  } catch { /* a private window without storage still navigates fine */ }
}

function monthToken(date: Date) { return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}` }
function quarterToken(date: Date) { return `${date.getFullYear()}-Q${Math.floor(date.getMonth() / 3) + 1}` }

/** periodCommands offers the periods a reader actually asks for by name. */
export function periodCommands(today: Date, go: (page: PageName, params?: Record<string, string>) => void): Command[] {
  const thisMonth = new Date(today.getFullYear(), today.getMonth(), 1)
  const lastMonth = new Date(today.getFullYear(), today.getMonth() - 1, 1)
  const lastQuarter = new Date(today.getFullYear(), today.getMonth() - 3, 1)
  const half = today.getMonth() > 5 ? 2 : 1
  const entries: { label: string; hint: string; kind: string; period: string; keywords: string[] }[] = [
    { label: '이번 달 월간보고', hint: monthToken(thisMonth), kind: 'MONTH', period: monthToken(thisMonth), keywords: ['월간', 'month', 'monthly'] },
    { label: '지난 달 월간보고', hint: monthToken(lastMonth), kind: 'MONTH', period: monthToken(lastMonth), keywords: ['월간', 'last month'] },
    { label: '이번 분기 보고', hint: quarterToken(today), kind: 'QUARTER', period: quarterToken(today), keywords: ['분기', 'quarter'] },
    { label: '지난 분기 보고', hint: quarterToken(lastQuarter), kind: 'QUARTER', period: quarterToken(lastQuarter), keywords: ['분기', 'last quarter'] },
    { label: `${today.getFullYear()}년 ${half === 1 ? '상반기' : '하반기'} 보고`, hint: `${today.getFullYear()}-H${half}`, kind: 'HALF', period: `${today.getFullYear()}-H${half}`, keywords: ['반기', 'half'] },
    { label: `${today.getFullYear()}년 연간보고`, hint: `${today.getFullYear()}`, kind: 'YEAR', period: `${today.getFullYear()}`, keywords: ['연간', 'year', 'annual'] },
  ]
  return entries.map(entry => ({
    id: `period:${entry.kind}:${entry.period}`,
    label: entry.label,
    hint: entry.hint,
    group: '기간 보고',
    keywords: [...entry.keywords, entry.period],
    run: () => go('rollup', { kind: entry.kind, period: entry.period }),
  }))
}

export default function CommandPalette({ open, onClose, session, go, commands }: {
  open: boolean
  onClose: () => void
  session: SessionInfo
  go: (page: PageName, params?: Record<string, string>) => void
  commands: Command[]
}) {
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const [reports, setReports] = useState<ReportListItem[]>()
  const [search, setSearch] = useState<SearchResponse>()
  const [searching, setSearching] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLDivElement>(null)
  const panelRef = useRef<HTMLDivElement>(null)

  // Content search runs on the server, so it waits for a pause in typing and
  // drops any answer that arrives after the query has moved on.
  useEffect(() => {
    if (!open) return
    const trimmed = query.trim()
    if (trimmed.length < 2) { setSearch(undefined); setSearching(false); return }
    let stale = false
    setSearching(true)
    const timer = window.setTimeout(() => {
      api<SearchResponse>(`/api/v1/search?q=${encodeURIComponent(trimmed)}`)
        .then(value => { if (!stale) setSearch(value) })
        .catch(() => { if (!stale) setSearch(undefined) })
        .finally(() => { if (!stale) setSearching(false) })
    }, 220)
    return () => { stale = true; window.clearTimeout(timer); }
  }, [query, open])

  useEffect(() => {
    if (!open) return
    setQuery(''); setActive(0); setSearch(undefined)
    const release = keepFocusInside(panelRef, () => inputRef.current?.focus())
    // Recent reports are only worth fetching once the palette is actually used.
    if (reports === undefined) {
      api<ReportListView>('/api/v1/reports?limit=8').then(value => setReports(value.items)).catch(() => setReports([]))
    }
    return release
  }, [open])

  const reportCommands = useMemo<Command[]>(() => (reports ?? []).map(report => ({
    id: `report:${report.id}`,
    label: `${report.weekStart} 주간보고`,
    hint: report.summary ? report.summary.slice(0, 40) : statusLabel(report.status),
    group: '내 보고서',
    keywords: [report.weekStart.replace(/-/g, ''), report.summary, '주간보고', 'report'],
    run: () => go('history', { report: String(report.id) }),
  })), [reports, go])

  const contentCommands = useMemo<Command[]>(() => (search?.hits ?? []).map(hit => {
    const first = hit.matches[0]
    const where = first ? `${first.label}${first.title ? ` · ${first.title}` : ''}` : ''
    return {
      id: `content:${hit.reportId}`,
      label: first ? first.snippet : `${hit.weekStart} 주간보고`,
      hint: `${hit.weekStart} · ${hit.displayName}${where ? ` · ${where}` : ''}`,
      group: hit.semantic ? '의미가 비슷한 내용' : hit.approximate ? '비슷한 내용' : '보고서 내용',
      // The server already decided this hit matches, so it must never be
      // filtered out again by the local matcher.
      keywords: [query],
      run: () => go('history', { report: String(hit.reportId) }),
    }
  }), [search, query, go])

  const all = useMemo(() => [...commands, ...reportCommands], [commands, reportCommands])

  const results = useMemo(() => {
    const recent = loadRecent()
    return all
      .map(command => ({ command, score: matchScore(query, command.label, command.keywords ?? []) }))
      .filter(entry => entry.score > 0)
      .map(entry => {
        const position = recent.indexOf(entry.command.id)
        // A recently used entry wins ties, which makes repeat trips one keystroke.
        return { ...entry, score: entry.score + (position >= 0 ? 12 - position : 0) }
      })
      .sort((left, right) => right.score - left.score)
      .slice(0, 40)
      .map(entry => entry.command)
      .concat(contentCommands)
  }, [all, query, contentCommands])

  useEffect(() => { setActive(0) }, [query])
  useEffect(() => {
    listRef.current?.querySelector('.palette-item.active')?.scrollIntoView({ block: 'nearest' })
  }, [active, results.length])

  if (!open) return null

  const choose = (command: Command) => { rememberRecent(command.id); onClose(); command.run() }
  const onKeyDown = (event: React.KeyboardEvent) => {
    if (event.key === 'ArrowDown') { event.preventDefault(); setActive(current => Math.min(current + 1, results.length - 1)) }
    else if (event.key === 'ArrowUp') { event.preventDefault(); setActive(current => Math.max(current - 1, 0)) }
    else if (event.key === 'Home') { event.preventDefault(); setActive(0) }
    else if (event.key === 'End') { event.preventDefault(); setActive(results.length - 1) }
    else if (event.key === 'Enter') { event.preventDefault(); if (results[active]) choose(results[active]) }
    else if (event.key === 'Escape') { event.preventDefault(); onClose() }
  }

  let lastGroup = ''
  return <div className="palette-backdrop" onClick={onClose} role="presentation">
    <div className="palette" ref={panelRef} onClick={event => event.stopPropagation()} role="dialog" aria-modal="true" aria-label="빠른 이동">
      <div className="palette-input">
        <span aria-hidden="true">⌕</span>
        <input ref={inputRef} value={query} onChange={event => setQuery(event.target.value)} onKeyDown={onKeyDown}
          placeholder="화면, 기간, 보고서 검색 — 초성도 됩니다 (예: ㄱㅇㅂ)"
          aria-label="빠른 이동 검색" autoComplete="off" spellCheck={false} />
        <kbd>Esc</kbd>
      </div>
      <div className="palette-list" ref={listRef} role="listbox">
        {!results.length ? <p className="palette-empty">{searching ? '검색 중…' : query.trim().length === 1 ? '두 글자 이상 입력하면 보고서 내용도 함께 찾습니다.' : '일치하는 항목이 없습니다.'}</p> : results.map((command, index) => {
          const header = command.group !== lastGroup ? command.group : ''
          lastGroup = command.group
          return <div key={command.id}>
            {header && <span className="palette-group">{header}</span>}
            <button className={`palette-item ${index === active ? 'active' : ''}`} role="option"
              aria-selected={index === active}
              onMouseEnter={() => setActive(index)} onClick={() => choose(command)}>
              <span>{command.label}</span>
              {command.hint && <small>{command.hint}</small>}
            </button>
          </div>
        })}
      </div>
      <footer className="palette-foot">
        {searching && <span className="palette-busy">보고서 내용 검색 중…</span>}
        {search?.truncated && <span className="palette-busy">결과가 많아 일부만 표시합니다</span>}
        {(search?.fuzzy || search?.semantic) && <span className="palette-busy">
          정확히 일치하는 결과가 적어 {search.semantic ? '표기가 비슷하거나 의미가 가까운' : '비슷한'} 내용도 함께 찾았습니다</span>}
        {/* A short list is the reader's evidence that nothing was written about
            this. It is only evidence if the wider passes actually ran, so when
            one of them could not, say so here rather than let the silence
            stand for an answer. */}
        {search?.reason && <span className="palette-busy">{search.reason}</span>}
        <span><kbd>↑</kbd><kbd>↓</kbd> 이동</span>
        <span><kbd>Enter</kbd> 열기</span>
        <span><kbd>g</kbd> 다음 <kbd>d</kbd>·<kbd>c</kbd>·<kbd>h</kbd>·<kbd>r</kbd> 바로가기</span>
        <span className="palette-user">{session.user.displayName}</span>
      </footer>
    </div>
  </div>
}

function statusLabel(status: string) {
  return ({ DRAFT: '작성 중', SUBMITTED: '검토 대기', REVISION_REQUESTED: '반려', APPROVED: '승인', CLOSED: '확정' } as Record<string, string>)[status] ?? status
}
