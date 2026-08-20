import { useEffect, useState } from 'react'
import { errorText, api } from '../api'
import { Card, Empty, PageHeader, Spinner } from '../components'
import type { HandoverView, ReportListItem, SessionInfo } from '../types'

/**
 * Handover view. Not a status list — what a new owner needs to know that the
 * current status does not say: where the turning points were, which issues
 * disappeared without explanation, what is still waiting on someone else.
 *
 * It is assembled from the weekly record rather than written by hand, because
 * the handover document written on the last day is the one that omits the
 * awkward parts.
 */

export default function HandoverPage({ session, notify }: {
  session: SessionInfo
  notify: (message: string, kind?: 'success' | 'error') => void
}) {
  const canPickPerson = session.user.role !== 'USER'
  const [userId, setUserId] = useState<number>(session.user.id)
  const [people, setPeople] = useState<{ id: number; displayName: string }[]>([])
  const [view, setView] = useState<HandoverView>()

  // The people list is derived from the reports this leader can already read,
  // so the picker can never offer someone whose work they cannot open.
  useEffect(() => {
    if (!canPickPerson) return
    api<ReportListItem[]>('/api/v1/team/reports').then(reports => {
      const seen = new Map<number, string>()
      for (const report of reports) seen.set(report.userId, report.displayName)
      setPeople([...seen].map(([id, displayName]) => ({ id, displayName }))
        .sort((left, right) => left.displayName.localeCompare(right.displayName)))
    }).catch(() => setPeople([]))
  }, [canPickPerson])

  useEffect(() => {
    let stale = false
    setView(undefined)
    api<HandoverView>(`/api/v1/handover?userId=${userId}`)
      .then(value => { if (!stale) setView(value) })
      .catch(error => {
        if (stale) return
        setView({ userId, displayName: '', active: 0, completed: 0, items: [] })
        notify(errorText(error, '인수인계 자료를 불러올 수 없습니다.'), 'error')
      })
    return () => { stale = true }
  }, [userId])

  return <>
    <PageHeader title="업무 인수인계" description="담당자가 바뀔 때 필요한 경과·결정·미해결 사항을 기록에서 모아 정리합니다."
      action={canPickPerson && people.length > 0
        ? <select value={userId} onChange={event => setUserId(Number(event.target.value))}>
            <option value={session.user.id}>{session.user.displayName} (본인)</option>
            {people.filter(person => person.id !== session.user.id)
              .map(person => <option key={person.id} value={person.id}>{person.displayName}</option>)}
          </select>
        : undefined} />

    {view === undefined ? <Spinner/> : view.items.length === 0
      ? <Empty>선택한 담당자에게는 아직 인수인계할 업무 기록이 없습니다. 주간보고가 쌓이면 진행 경과와 미해결 이슈가 여기에 모입니다.</Empty>
      : <>
        <div className="meeting-summary">
          <span><strong>{view.displayName}</strong></span>
          <span>진행 중 <strong>{view.active}</strong>건</span>
          <span>완료 <strong>{view.completed}</strong>건</span>
        </div>
        {view.items.map(item => <Card key={item.workItemId} title={item.title}
          action={<span className="muted-chip">{item.completed ? '완료' : `진척 ${item.progress}%`}</span>}>
          <div className="handover-facts">
            <span>{item.firstWeek} ~ {item.lastWeek}</span>
            <span>{item.ageWeeks}주 경과 · {item.reportedWeeks}주 보고</span>
            {item.category && <span>{item.category}</span>}
            {item.stalled && <span className="delta-down">정체</span>}
          </div>
          {item.caution && <p className="handover-caution">{item.caution}</p>}

          {(item.openAsk || item.openIssue || item.nextPlan) && <div className="handover-open">
            {item.openAsk && <p><strong>대기 중인 요청</strong> {item.openAsk}</p>}
            {item.openIssue && <p><strong>미해결 이슈</strong> {item.openIssue}</p>}
            {item.nextPlan && <p><strong>다음 계획</strong> {item.nextPlan}</p>}
          </div>}

          {item.milestones.length > 0 && <div className="handover-block">
            <h4>진행 경과</h4>
            <ul className="handover-timeline">{item.milestones.map(line => <li key={line}>{line}</li>)}</ul>
          </div>}

          {item.issueHistory.length > 0 && <div className="handover-block">
            <h4>이슈 이력</h4>
            <ul className="handover-issues">{item.issueHistory.map(issue => <li key={`${issue.week}-${issue.text}`}>
              <span className={issue.resolved ? 'issue-resolved' : 'issue-open'}>{issue.resolved ? '해소' : '미해결'}</span>
              <span className="muted">{issue.week}</span> {issue.text}
            </li>)}</ul>
          </div>}

          {item.relatedWork.length > 0 && <div className="handover-block">
            <h4>관련 업무</h4>
            <ul className="handover-related">{item.relatedWork.map(related => <li key={related.workItemId}>
              <strong>{related.title}</strong>
              <small>{related.displayName} · {related.organizationName || '조직 미지정'}</small>
            </li>)}</ul>
          </div>}
        </Card>)}
      </>}
  </>
}
