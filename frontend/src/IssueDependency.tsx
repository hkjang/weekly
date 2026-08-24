import { useEffect, useState } from 'react'
import { api } from './api'
import { Button } from './components'
import DependencyPanel from './DependencyPanel'
import type { WorkItemLinkView } from './types'

/**
 * Turning "we are blocked" into something the organisation can see.
 *
 * The issue field is where somebody says they are waiting on another team. The
 * dependency link is how the product learns who they are waiting on, and every
 * cross-team view built since — bottlenecks, 타 조직 대기 on the meeting agenda,
 * the blocked notes in the quality check — reads from those links.
 *
 * They were never connected. Declaring a blocker lived on the work tracking
 * screen; being blocked is written here. Measured on real data: twelve issues
 * written, zero links declared. Everything built on top of the links was
 * complete and unreachable.
 *
 * Asked once and only where it applies — an item with an issue this week. Once
 * a blocker is declared it says so and stops asking, because repeating a demand
 * at somebody who already acted on it is how a screen trains people to ignore
 * it.
 */
export default function IssueDependency({ workItemId, notify }: {
  workItemId: number
  notify: (message: string, kind?: 'success' | 'error') => void
}) {
  const [view, setView] = useState<WorkItemLinkView>()
  const [open, setOpen] = useState(false)

  const load = () => api<WorkItemLinkView>(`/api/v1/work-items/${workItemId}/links`)
    .then(setView)
    .catch(() => setView({ blockers: [], blocking: [] }))
  useEffect(() => { void load() }, [workItemId])

  // Nothing is said until the answer is known. A prompt that flashes "waiting
  // on nothing" while the request is in flight is worse than a moment of quiet.
  if (!view) return null

  // Only the blockers that are still in the way. A finished predecessor is
  // history, and offering to "fix" it would ask about a question already
  // settled.
  const waiting = view.blockers.filter(link => !link.completed)
  if (waiting.length > 0 && !open) {
    return <div className="issue-dependency declared">
      <span>
        <b>{waiting[0].title}</b>
        {waiting[0].organizationName ? ` · ${waiting[0].organizationName}` : ''}
        {waiting.length > 1 ? ` 외 ${waiting.length - 1}건` : ''}을 기다리는 중으로 등록돼 있습니다.
      </span>
      <button className="link-button" onClick={() => setOpen(true)}>고치기</button>
    </div>
  }

  if (!open) {
    return <div className="issue-dependency">
      <span>이 이슈가 <b>다른 업무가 끝나야 풀리는 것</b>이라면 선행 업무로 등록해 두십시오. 회의 안건과 병목 분석이 그 관계를 읽습니다.</span>
      <Button variant="ghost" onClick={() => setOpen(true)}>선행 업무 등록</Button>
    </div>
  }

  return <div className="issue-dependency open">
    <DependencyPanel workItemId={workItemId} editable notify={notify} startAdding={waiting.length === 0} />
    <button className="link-button" onClick={() => { setOpen(false); void load() }}>접기</button>
  </div>
}
