import { useEffect, useState } from 'react'
import { errorText, api } from '../api'
import { Card, Empty, PageHeader, Spinner } from '../components'
import { WeekTrack } from '../charts'
import type { HandoverView, SessionInfo, TeamMember } from '../types'

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
  const [people, setPeople] = useState<TeamMember[]>([])
  const [view, setView] = useState<HandoverView>()

  // The picker used to be built from a page of 팀 주간보고, which meant it only
  // ever offered people who had reported recently. Someone who stopped six
  // weeks ago was missing — and that is precisely who a handover is for. It now
  // comes from the people this leader can open, inactive accounts included.
  useEffect(() => {
    if (!canPickPerson) return
    api<TeamMember[]>('/api/v1/team/members').then(setPeople).catch(() => { setPeople([]); notify('담당자 목록을 불러오지 못했습니다. 화면을 다시 열어 보십시오.', 'error') })
  }, [canPickPerson])

  useEffect(() => {
    let stale = false
    setView(undefined)
    api<HandoverView>(`/api/v1/handover?userId=${userId}`)
      .then(value => { if (!stale) setView(value) })
      .catch(error => {
        if (stale) return
        setView({ userId, displayName: '', active: 0, completed: 0, openDecisions: 0, overdueDecisions: 0, items: [] })
        notify(errorText(error, '인수인계 자료를 불러올 수 없습니다.'), 'error')
      })
    return () => { stale = true }
  }, [userId])

  return <>
    <PageHeader title="업무 인수인계" description="담당자가 바뀔 때 필요한 경과·결정·미해결 사항을 기록에서 모아 정리합니다."
      action={canPickPerson && people.length > 0
        ? <select aria-label="인수인계 대상자" value={userId} onChange={event => setUserId(Number(event.target.value))}>
            <option value={session.user.id}>{session.user.displayName} (본인)</option>
            {people.filter(person => person.id !== session.user.id)
              .map(person => <option key={person.id} value={person.id}>
                {person.displayName}{person.active ? '' : ' (비활성)'}
                {person.lastWeek ? ` · 최근 ${person.lastWeek}` : ' · 보고 없음'}
              </option>)}
          </select>
        : undefined} />

    {view === undefined ? <Spinner/> : view.items.length === 0
      ? <Empty>선택한 담당자에게는 아직 인수인계할 업무 기록이 없습니다. 주간보고가 쌓이면 진행 경과와 미해결 이슈가 여기에 모입니다.</Empty>
      : <>
        <div className="meeting-summary">
          <span><strong>{view.displayName}</strong></span>
          <span>진행 중 <strong>{view.active}</strong>건</span>
          <span>완료 <strong>{view.completed}</strong>건</span>
          {view.openDecisions > 0 && <span>미해결 결정 <strong>{view.openDecisions}</strong>건</span>}
          {view.overdueDecisions > 0 && <span className="state-chip stalled">기한 지난 후속 조치 {view.overdueDecisions}건</span>}
        </div>
        {view.items.map(item => <Card key={item.workItemId} title={item.title}
          action={<span className="muted-chip">{item.completed ? '완료' : `진척 ${item.progress}%`}</span>}>
          <div className="handover-facts">
            <span>{item.firstWeek} ~ {item.lastWeek}</span>
            <span>{item.ageWeeks}주 경과 · {item.reportedWeeks}주 보고</span>
            {item.category && <span>{item.category}</span>}
            {item.stalled && <span className="state-chip stalled">정체</span>}
          </div>
          {item.caution && <p className="handover-caution">{item.caution}</p>}

          {(item.openAsk || item.openIssue || item.nextPlan) && <div className="handover-open">
            {item.openAsk && <p><strong>대기 중인 요청</strong> {item.openAsk}</p>}
            {item.openIssue && <p><strong>미해결 이슈</strong> {item.openIssue}</p>}
            {item.nextPlan && <p><strong>다음 계획</strong> {item.nextPlan}</p>}
          </div>}

          {item.decisions.length > 0 && <div className="handover-block">
            <h4>결정 기록</h4>
            {/* Read-only here. The handover is a document to be read, not a
                screen to work in; recording and correcting belong in 업무 추적
                where the task itself is managed. */}
            <ul className="decision-list">{item.decisions.map(decision => <li key={decision.id}
              className={`decision ${decision.status.toLowerCase()}`}>
              <div className="decision-head">
                <strong>{decision.title}</strong>
                <span className={`decision-status ${decision.status.toLowerCase()}`}>
                  {decision.status === 'OPEN' ? '후속 조치 중' : decision.status === 'DONE' ? '완료' : '대체됨'}</span>
              </div>
              <div className="decision-facts">
                <span><b>{decision.decidedBy}</b> 결정</span>
                <span>{decision.decidedOn}</span>
                {decision.dueDate && <span>후속 기한 {decision.dueDate}</span>}
                {decision.recordedByName && <span className="muted">{decision.recordedByName} 기록</span>}
              </div>
              {decision.rationale && <p className="decision-rationale"><b>근거</b> {decision.rationale}</p>}
              {decision.followUp && <p className="decision-followup"><b>후속 조치</b> {decision.followUp}</p>}
            </li>)}</ul>
          </div>}

          {item.track?.length > 0 && <div className="handover-block">
            <h4>주차별 기록</h4>
            <WeekTrack firstWeek={item.firstWeek} lastWeek={item.lastWeek} track={item.track} />
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
