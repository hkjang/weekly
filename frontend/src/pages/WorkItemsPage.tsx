import { useEffect, useMemo, useState } from 'react'
import { errorText, api, post, put } from '../api'
import { Modal, Button, Card, Empty, PageHeader, Spinner } from '../components'
import { WeekTrack } from '../charts'
import DecisionPanel from '../DecisionPanel'
import DependencyPanel from '../DependencyPanel'
import type { DueOutlook, SessionInfo, WorkItem, WorkItemEditResult, WorkItemPage, WorkSearchResponse } from '../types'

/**
 * Work item tracking. Each row is a task followed across every week it was
 * reported in, which is what makes ageing visible: how long it has run, how
 * many weeks went unreported, and how long it has sat at the same progress.
 */

const filters = [
  { key: 'ALL', name: '전체' },
  { key: 'ATTENTION', name: '조치 필요' },
  { key: 'STALLED', name: '정체' },
  { key: 'RISK', name: '이슈 지속' },
  { key: 'ASK', name: '요청 있음' },
  { key: 'ACTIVE', name: '진행 중' },
  { key: 'DONE', name: '완료' },
] as const
type FilterKey = typeof filters[number]['key']

/**
 * The deadline, and what the reported pace says about reaching it.
 *
 * Until now a task had no deadline anywhere — the column existed and nothing
 * wrote it — so the only date the product held was a decision's follow-up, and
 * that was reported only once it had passed. Being told on the 16th that the
 * 15th was missed is a record, not a warning.
 *
 * The verdict never appears without the projected progress and the paces behind
 * it. A red chip nobody can check is one nobody should act on.
 */
const dueTone: Record<DueOutlook['kind'], string> = {
  NONE: '', FINISHED: 'done', UNKNOWN: '', ON_TRACK: 'done',
  SPLIT: 'stalled', AT_RISK: 'risk', OVERDUE: 'risk',
}
const dueLabel: Record<DueOutlook['kind'], string> = {
  NONE: '', FINISHED: '기한 내 완료', UNKNOWN: '마감 추정 불가', ON_TRACK: '기한 내 예상',
  SPLIT: '기한 불확실', AT_RISK: '기한 초과 예상', OVERDUE: '기한 초과',
}

function dueChip(item: WorkItem) {
  const kind = item.dueOutlook?.kind
  if (!kind || kind === 'NONE' || kind === 'UNKNOWN' || kind === 'FINISHED') return null
  return <span className={`state-chip ${dueTone[kind]}`} title={item.dueOutlook.note}>{dueLabel[kind]}</span>
}

function DuePanel({ item, editable, notify, onSaved }: {
  item: WorkItem
  editable: boolean
  notify: (message: string, kind?: 'success' | 'error') => void
  onSaved: () => Promise<void>
}) {
  const [value, setValue] = useState(item.dueDate ?? '')
  const [busy, setBusy] = useState(false)
  useEffect(() => { setValue(item.dueDate ?? '') }, [item.id, item.dueDate])
  const save = async (next: string) => {
    setBusy(true)
    try {
      await put(`/api/v1/work-items/${item.id}/due-date`, { dueDate: next })
      await onSaved()
      notify(next ? `마감일을 ${next}로 저장했습니다.` : '마감일을 지웠습니다.')
    } catch (error) {
      setValue(item.dueDate ?? '')
      notify(errorText(error, '마감일을 저장할 수 없습니다.'), 'error')
    } finally { setBusy(false) }
  }
  const outlook = item.dueOutlook
  return <div className="due-panel">
    <div className="due-row">
      <small>마감일</small>
      {editable
        ? <input type="date" value={value} disabled={busy}
            onChange={event => { setValue(event.target.value); void save(event.target.value) }} />
        : <strong>{item.dueDate || '설정되지 않음'}</strong>}
      {editable && value && <button className="link-button" disabled={busy}
        onClick={() => { setValue(''); void save('') }}>지우기</button>}
    </div>
    {outlook && outlook.kind !== 'NONE' && <>
      <p className={outlook.kind === 'AT_RISK' || outlook.kind === 'OVERDUE' ? 'warn-text' : ''}>{outlook.note}</p>
      {outlook.kind !== 'OVERDUE' && outlook.kind !== 'FINISHED' && outlook.kind !== 'UNKNOWN' &&
        <code>마감일 예상 진척 {outlook.projectedLow === outlook.projectedHigh
          ? `${outlook.projectedLow}%`
          : `${outlook.projectedLow}~${outlook.projectedHigh}%`} · 남은 {outlook.weeksLeft}주</code>}
    </>}
    {/* A deadline the meeting already agreed. Offered, not applied: the
        follow-up may be one step of the task rather than the whole of it, so
        the person who owns the work confirms what it means. */}
    {!item.dueDate && item.agreedDue && <div className="due-agreed">
      <div>
        <strong>{item.agreedDue.dueDate}</strong>
        <small>{item.agreedDue.decidedBy} 결정 · {item.agreedDue.decidedOn}</small>
        <p>{item.agreedDue.followUp || item.agreedDue.title}</p>
      </div>
      {editable && <button className="button secondary" disabled={busy}
        onClick={() => { const next = item.agreedDue!.dueDate; setValue(next); void save(next) }}>이 날짜를 마감일로</button>}
    </div>}
    {!item.dueDate && <p className="muted">{item.agreedDue
      ? '회의에서 정한 후속 기한이 있습니다. 업무 전체의 마감일과 같다면 그대로 지정하세요.'
      : '마감일을 설정하면 지금까지 보고된 속도로 그 날짜에 몇 %에 닿는지 함께 계산합니다.'}</p>}
  </div>
}

export default function WorkItemsPage({ session, notify }: {
  session: SessionInfo
  notify: (message: string, kind?: 'success' | 'error') => void
}) {
  const [scope, setScope] = useState<'SELF' | 'TEAM'>('SELF')
  const [items, setItems] = useState<WorkItem[]>()
  // What exists, beside what arrived. The list is capped, and a capped list
  // presented as the whole set is the failure this project fixed everywhere
  // else — a reader counting rows would undercount their own team.
  const [total, setTotal] = useState(0)
  const [filter, setFilter] = useState<FilterKey>('ALL')
  const [detail, setDetail] = useState<WorkItem>()
  // Natural language lookup over past work: "인증 연동하다 막혔던 사례".
  // Separate from the filters above because it answers a different question —
  // not "what needs attention now" but "has anyone done this before".
  const [query, setQuery] = useState('')
  // Corrections to what the title normalizer decided. Keyed by week because that
  // is what the timeline shows; the report item rows behind each week travel
  // with it.
  const [picked, setPicked] = useState<string[]>([])
  const [splitTitle, setSplitTitle] = useState('')
  const [mergeInto, setMergeInto] = useState('')
  const [editBusy, setEditBusy] = useState(false)
  const [found, setFound] = useState<WorkSearchResponse>()
  const [searching, setSearching] = useState(false)
  const canTeam = session.user.role !== 'USER'

  useEffect(() => {
    let stale = false
    setItems(undefined)
    api<WorkItemPage>(`/api/v1/work-items?scope=${scope}`)
      .then(value => { if (!stale) { setItems(value.items); setTotal(value.total) } })
      .catch(error => {
        if (stale) return
        setItems([])
        notify(errorText(error, '업무를 불러올 수 없습니다.'), 'error')
      })
    return () => { stale = true }
  }, [scope])

  const visible = useMemo(() => (items ?? []).filter(item => {
    switch (filter) {
      case 'ATTENTION': return item.stalled || item.atRisk || !!item.latestManagementAsk
      case 'STALLED': return item.stalled
      case 'RISK': return item.atRisk
      case 'ASK': return !!item.latestManagementAsk
      case 'ACTIVE': return !item.completed
      case 'DONE': return item.completed
      default: return true
    }
  }), [items, filter])

  const counts = useMemo(() => {
    const list = items ?? []
    return {
      total: list.length,
      active: list.filter(item => !item.completed).length,
      stalled: list.filter(item => item.stalled).length,
      risk: list.filter(item => item.atRisk).length,
      ask: list.filter(item => !!item.latestManagementAsk).length,
      oldest: list.reduce((max, item) => Math.max(max, item.ageWeeks), 0),
    }
  }, [items])

  // The list carries no weekly history — it is 80% of the payload and nothing
  // in the table reads it. The dialog needs it, so it is fetched for the one
  // task being opened.
  const openDetail = async (item: WorkItem) => {
    setPicked([]); setSplitTitle(''); setMergeInto('')
    setDetail(item)
    try {
      setDetail(await api<WorkItem>(`/api/v1/work-items/${item.id}`))
    } catch (error) {
      notify(errorText(error, '업무 상세를 불러올 수 없습니다.'), 'error')
    }
  }
  const closeDetail = () => setDetail(undefined)
  const canEdit = (item: WorkItem) => item.userId === session.user.id || session.user.role === 'ADMIN'
  const reload = async () => {
    const fresh = await api<WorkItemPage>(`/api/v1/work-items?scope=${scope}`)
    setItems(fresh.items)
    setTotal(fresh.total)
    return fresh.items
  }
  // A deadline is entered to see what it means, so the answer has to arrive
  // with the save. Patching only dueDate onto the open dialog left the outlook
  // showing the state from before the date existed, and the panel went blank
  // until the reader closed and reopened it.
  const refreshDetail = async () => {
    await reload()
    setDetail(current => current)
    const open = detail
    if (open) setDetail(await api<WorkItem>(`/api/v1/work-items/${open.id}`))
  }
  // Reports what happened, not what was asked for.
  //
  // The server answers with movedItems — how many weekly snapshots actually
  // changed hands — and its own comment calls that the only number that tells
  // the author whether the correction did what they meant. The screen was
  // throwing it away and announcing the requested count instead, so a split
  // that moved fewer rows than asked still said it had moved all of them.
  const runEdit = async (path: string, body: unknown, done: (moved: number) => string) => {
    setEditBusy(true)
    try {
      const result = await post<WorkItemEditResult>(path, body)
      closeDetail()
      await reload()
      notify(done(result.movedItems))
    } catch (error) {
      notify(errorText(error, '업무를 정리할 수 없습니다.'), 'error')
    } finally { setEditBusy(false) }
  }
  const splitOff = (item: WorkItem) => {
    const ids = (item.weeks ?? []).filter(week => picked.includes(week.weekStart)).flatMap(week => week.itemIds)
    return runEdit(`/api/v1/work-items/${item.id}/split`,
      { title: splitTitle, category: item.category, reportItemIds: ids },
      moved => moved === ids.length
        ? `${moved}개 주차를 '${splitTitle.trim()}' 업무로 분리했습니다.`
        : `${ids.length}개 주차 중 ${moved}개를 '${splitTitle.trim()}' 업무로 분리했습니다. 나머지는 옮겨지지 않았습니다.`)
  }
  const mergeWith = (item: WorkItem) => runEdit(`/api/v1/work-items/${item.id}/merge`,
    { intoId: Number(mergeInto) },
    moved => `두 업무를 하나로 합쳤습니다. 주차 기록 ${moved}건이 옮겨졌습니다.`)

  const runSearch = async (text: string) => {
    const trimmed = text.trim()
    if (trimmed.length < 2) { setFound(undefined); return }
    setSearching(true)
    try {
      setFound(await api<WorkSearchResponse>(`/api/v1/work-items/search?scope=${scope}&q=${encodeURIComponent(trimmed)}`))
    } catch (error) {
      notify(errorText(error, '업무를 검색할 수 없습니다.'), 'error')
    } finally { setSearching(false) }
  }

  return <>
    <PageHeader title="업무 추적"
      description="주차를 넘어 이어지는 업무를 하나의 흐름으로 따라갑니다. 얼마나 오래 진행됐고, 몇 주 보고가 빠졌고, 언제부터 진척이 멈췄는지 확인합니다."
      action={canTeam ? <label className="inline-select">범위
        <select value={scope} onChange={event => setScope(event.target.value as 'SELF' | 'TEAM')}>
          <option value="SELF">본인</option><option value="TEAM">소속 조직</option>
        </select></label> : undefined} />

    <Card title="과거 사례 찾기">
      <p className="muted">비슷한 업무나 장애를 겪은 기록을 문장으로 찾습니다. 이슈와 그 이후 진행이 함께 표시되므로 해결 경과를 확인할 수 있습니다.</p>
      <form className="work-search" onSubmit={event => { event.preventDefault(); void runSearch(query) }}>
        <input value={query} onChange={event => setQuery(event.target.value)}
          placeholder="예: 인증 연동하다 막혔던 사례, 결산 자동화 실패 원인" maxLength={200} />
        <button className="button" type="submit" disabled={searching || query.trim().length < 2}>
          {searching ? '찾는 중…' : '찾기'}</button>
        {found && <button className="button secondary" type="button"
          onClick={() => { setFound(undefined); setQuery('') }}>지우기</button>}
      </form>
      {/* "찾지 못했습니다" claims a search happened. When the meaning-based pass
          could not run, say which kind of nothing this is. */}
      {found && (found.hits.length === 0
        ? <Empty>{found.semanticReason
            ? `글자가 일치하는 업무가 없습니다. ${found.semanticReason}`
            : '비슷한 과거 업무를 찾지 못했습니다.'}</Empty>
        : <ul className="work-search-results">
            {found.semantic && <li className="muted work-search-note">표현이 달라도 내용이 가까운 업무를 함께 찾았습니다.</li>}
            {!found.semantic && found.semanticReason && <li className="muted work-search-note">{found.semanticReason}</li>}
            {found.hits.map(hit => <li key={hit.workItemId}>
              <div className="work-search-head">
                <strong>{hit.title}</strong>
                <span className="muted-chip">{hit.displayName}{hit.organizationName ? ` · ${hit.organizationName}` : ''}</span>
                <span className="muted-chip">{hit.lastWeek}</span>
                {hit.semantic && <span className="muted-chip">의미 유사</span>}
              </div>
              {hit.issue && <p className="work-search-issue"><strong>이슈</strong> {hit.issue}</p>}
              {hit.resolution && <p className="work-search-fix"><strong>이후 경과</strong> {hit.resolution}</p>}
              <p className="muted">{hit.why}</p>
            </li>)}
          </ul>)}
    </Card>

    {items === undefined ? <Spinner /> : !items.length ? <Empty>
      아직 추적할 업무가 없습니다. 주간보고를 저장하면 업무가 자동으로 만들어집니다.
    </Empty> : <>
      <div className="metric-grid">
        {/* The count says which set it counted. With the list capped, "전체
            2100건" beside 200 loaded rows would be a number nobody could
            reconcile with what is on screen. */}
        <Card><span className="metric-label">진행 중 업무</span><strong className="metric-value">{counts.active}</strong>
          <small>{total > counts.total ? `표시 ${counts.total}건 · 전체 ${total}건` : `전체 ${counts.total}건`}</small></Card>
        <Card><span className="metric-label">정체</span><strong className="metric-value">{counts.stalled}</strong><small>진척도 변화 없음</small></Card>
        <Card><span className="metric-label">이슈 지속</span><strong className="metric-value">{counts.risk}</strong><small>미완료 상태로 반복 보고</small></Card>
        <Card><span className="metric-label">최장 진행</span><strong className="metric-value">{counts.oldest}주</strong><small>가장 오래된 업무</small></Card>
      </div>

      {total > (items?.length ?? 0) && <p className="muted">
        업무가 {total}건이라 가장 먼저 봐야 할 {items?.length}건만 불러왔습니다. 이슈 지속·정체·오래된 순으로 정렬돼 있습니다.
      </p>}
      <Card title="업무 목록" action={<div className="tabs rollup-filter">
        {filters.map(item => <button key={item.key} className={filter === item.key ? 'active' : ''}
          onClick={() => setFilter(item.key)}>{item.name}</button>)}
      </div>}>
        {!visible.length ? <Empty>조건에 맞는 업무가 없습니다.</Empty> : <div className="table-wrap"><table>
          <thead><tr>
            <th>구분</th><th>업무</th><th>진척도</th><th>경과</th><th>보고</th><th>상태</th>
            {scope === 'TEAM' && <th>담당</th>}
          </tr></thead>
          <tbody>{visible.map(item => <tr key={item.id} onClick={() => openDetail(item)}>
            <td>{item.category || '미분류'}</td>
            <td><strong>{item.title}</strong>
              {item.latestManagementAsk && <small className="cell-sub ask-line">요청 · {item.latestManagementAsk}</small>}</td>
            <td><div className="cell-progress"><i style={{ width: `${item.progress}%` }} /></div>
              <small>{item.progress}%{item.progressGain !== 0 && <span className="muted"> ({item.progressGain > 0 ? '+' : ''}{item.progressGain})</span>}</small></td>
            <td>{item.ageWeeks}주<small className="cell-sub">{item.firstWeek} ~</small></td>
            <td>{item.reportedWeeks}회{item.silentWeeks > 0 && <small className="cell-sub warn-text">{item.silentWeeks}주 누락</small>}</td>
            <td><div className="state-chips">
              {item.completed && <span className="state-chip done">완료</span>}
              {item.atRisk && <span className="state-chip risk">이슈 {item.issueWeeks}주</span>}
              {item.stalled && <span className="state-chip stalled">{item.stalledWeeks}주 정체</span>}
              {item.latestManagementAsk && <span className="state-chip ask">요청</span>}
              {item.repeatedPlan >= 3 && <span className="state-chip past">계획 {item.repeatedPlan}회 반복</span>}
              {dueChip(item)}
              {!item.completed && !item.atRisk && !item.stalled && !item.latestManagementAsk && <span className="state-chip">진행</span>}
            </div></td>
            {scope === 'TEAM' && <td>{item.displayName}</td>}
          </tr>)}</tbody>
        </table></div>}
      </Card>
    </>}

    {detail && <Modal onClose={closeDetail} label={`${detail.title} 상세`} className="wide">
        <header>
          <div>
            <span className="week-label">{detail.category || '미분류'} · {detail.displayName}</span>
            <h2>{detail.title}</h2>
          </div>
          <button onClick={closeDetail}>×</button>
        </header>
        <div className="rollup-detail">
          <div className="rollup-detail-meta">
            <div><small>진척도</small><strong>{detail.startProgress}% → {detail.progress}%</strong></div>
            <div><small>경과</small><strong>{detail.ageWeeks}주</strong></div>
            <div><small>보고 주차</small><strong>{detail.reportedWeeks}회{detail.silentWeeks > 0 ? ` (${detail.silentWeeks} 누락)` : ''}</strong></div>
            <div><small>이슈 발생</small><strong>{detail.issueWeeks}주</strong></div>
          </div>
          {detail.latestManagementAsk && <div className="ask-panel">
            <strong>상위 조직 요청</strong><p>{detail.latestManagementAsk}</p>
          </div>}
          <DuePanel item={detail} editable={canEdit(detail)} notify={notify} onSaved={refreshDetail} />
          <h4 className="timeline-heading">선행·후행 업무</h4>
          {/* Above the decisions: a task waiting on another team is the most
              common reason work stops, and it is the fact the reader can act on
              without reading anything else. */}
          <DependencyPanel workItemId={detail.id} editable={canEdit(detail)} notify={notify} />

          <h4 className="timeline-heading">결정 기록</h4>
          {/* Above the weekly record on purpose: a week says what happened, a
              decision says why it was going to. The why is what someone opening
              this dialog months later came for. */}
          <DecisionPanel workItemId={detail.id} aiEnabled={session.aiEnabled} notify={notify} />

          <h4 className="timeline-heading">주차별 기록</h4>
          {/* The same record as the list below, in one strip. The list answers
              "what happened in week N"; the strip answers "which week should I
              be reading", which is the question someone opens this dialog with.

              The history arrives after the dialog does: the list does not carry
              it, so this is a spinner for one request rather than 19 MB of text
              downloaded for every reader who never opens a task. */}
          {!detail.weeks ? <Spinner/> : <>
          <WeekTrack firstWeek={detail.firstWeek} lastWeek={detail.lastWeek}
            track={detail.weeks.map(week => ({ week: week.weekStart, progress: week.progress, issue: !!week.issue.trim() }))} />
          <ol className="work-timeline">{detail.weeks.map(week => <li key={week.weekStart}>
            <div className="work-timeline-head">
              {canEdit(detail) && <label className="week-pick" title="이 주차를 다른 업무로 분리">
                <input type="checkbox" checked={picked.includes(week.weekStart)}
                  onChange={event => setPicked(event.target.checked ? [...picked, week.weekStart] : picked.filter(value => value !== week.weekStart))} />
              </label>}
              <strong>{week.weekStart}</strong>
              <span className="work-progress">{week.progress}%</span>
            </div>
            <div className="work-timeline-body">
              <div><b>실적</b><p>{week.currentResult || '-'}</p></div>
              <div><b>계획</b><p>{week.nextPlan || '-'}</p></div>
              {week.issue && <div><b>이슈</b><p className="warn-text">{week.issue}</p></div>}
              {week.managementAsk && <div><b>요청</b><p>{week.managementAsk}</p></div>}
            </div>
          </li>)}</ol>
          </>}
          {canEdit(detail) && detail.weeks && <div className="work-edit">
            <h4 className="timeline-heading">업무 정리</h4>
            <p className="muted">업무는 제목을 정규화해 자동으로 묶습니다. 잘못 묶였거나 잘못 나뉘었으면 여기서 바로잡으세요. 바로잡은 결과는 다음 보고서를 저장해도 유지됩니다.</p>
            <div className="work-edit-row">
              <div>
                <strong>선택한 주차를 다른 업무로 분리</strong>
                <small>위 목록에서 분리할 주차를 고른 뒤 새 업무 제목을 입력하세요. 전체 주차는 분리할 수 없습니다.</small>
                <div className="work-edit-controls">
                  <input value={splitTitle} onChange={event => setSplitTitle(event.target.value)}
                    placeholder="분리할 업무 제목" maxLength={240} />
                  <Button variant="secondary" disabled={editBusy || !splitTitle.trim() || !picked.length || picked.length >= detail.weeks.length}
                    onClick={() => splitOff(detail)}>{picked.length ? `${picked.length}개 주차 분리` : '주차를 선택하세요'}</Button>
                </div>
              </div>
              <div>
                <strong>이 업무를 다른 업무에 합치기</strong>
                <small>이 업무의 모든 주차가 대상 업무로 옮겨지고, 앞으로 같은 제목으로 보고해도 대상 업무로 이어집니다.</small>
                <div className="work-edit-controls">
                  <select value={mergeInto} onChange={event => setMergeInto(event.target.value)}>
                    <option value="">합칠 업무 선택</option>
                    {(items ?? []).filter(item => item.id !== detail.id && item.userId === detail.userId)
                      .map(item => <option key={item.id} value={item.id}>{item.title}</option>)}
                  </select>
                  <Button variant="secondary" disabled={editBusy || !mergeInto}
                    onClick={() => mergeWith(detail)}>합치기</Button>
                </div>
              </div>
            </div>
          </div>}
        </div>
    </Modal>}
  </>
}
