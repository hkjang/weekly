import { useEffect, useMemo, useState } from 'react'
import { api, errorText, put } from './api'
import { Button, Card, Empty, Spinner } from './components'
import type { ReportInclusionMember, ReportInclusionPreference, SessionInfo } from './types'

export function filterInclusionMembers(members: ReportInclusionMember[], query: string): ReportInclusionMember[] {
  const needle = query.trim().toLocaleLowerCase('ko-KR').replace(/\s+/g, '')
  if (!needle) return members
  return members.filter(member => [member.displayName, member.username ?? '', member.organizationName]
    .some(value => value.toLocaleLowerCase('ko-KR').replace(/\s+/g, '').includes(needle)))
}

export function inclusionMemberIDsWithinLimit(members: ReportInclusionMember[], maximum: number): number[] {
  return members.slice(0, Math.max(0, maximum)).map(member => member.id)
}

export default function ReportInclusionSettings({ session, notify }: {
  session: SessionInfo
  notify: (message: string, kind?: 'success' | 'error') => void
}) {
  const canManage = session.user.role !== 'USER'
  const [preference, setPreference] = useState<ReportInclusionPreference>()
  const [selectedMemberIds, setSelectedMemberIds] = useState<number[]>([])
  const [query, setQuery] = useState('')
  const [failed, setFailed] = useState('')
  const [busy, setBusy] = useState(false)

  const load = async () => {
    setFailed('')
    // A failed read is not an empty or previously signed-in person's choice.
    // Clear the old claim while retrying and render a durable failure if this
    // account's answer never arrives.
    setPreference(undefined)
    try {
      const value = await api<ReportInclusionPreference>('/api/v1/me/report-inclusions')
      setPreference(value)
      setSelectedMemberIds(value.selectedMemberIds)
    } catch (error) {
      setFailed(errorText(error, '팀원 포함 설정을 불러올 수 없습니다.'))
    }
  }

  useEffect(() => { if (canManage) void load() }, [canManage, session.user.id])

  const visibleMembers = useMemo(() => filterInclusionMembers(preference?.members ?? [], query), [preference?.members, query])
  const selected = useMemo(() => new Set(selectedMemberIds), [selectedMemberIds])
  const toggle = (id: number, checked: boolean) => setSelectedMemberIds(current => {
    if (!checked) return current.filter(memberId => memberId !== id)
    if (current.includes(id)) return current
    if (preference && current.length >= preference.maxMembers) {
      notify(`팀원은 최대 ${preference.maxMembers}명까지 선택할 수 있습니다.`, 'error')
      return current
    }
    return [...current, id]
  })
  const selectAll = () => {
    if (!preference) return
    setSelectedMemberIds(inclusionMemberIDsWithinLimit(preference.members, preference.maxMembers))
    if (preference.members.length > preference.maxMembers) {
      notify(`전체 ${preference.members.length}명 중 최대 ${preference.maxMembers}명을 선택했습니다.`)
    }
  }
  const save = async () => {
    setBusy(true)
    try {
      const value = await put<ReportInclusionPreference>('/api/v1/me/report-inclusions', { memberIds: selectedMemberIds })
      setPreference(value)
      setSelectedMemberIds(value.selectedMemberIds)
      notify(`내 주간보고 자료에 포함할 팀원 ${value.selectedMemberIds.length}명을 저장했습니다.`)
    } catch (error) {
      notify(errorText(error, '팀원 포함 설정을 저장할 수 없습니다.'), 'error')
    } finally { setBusy(false) }
  }

  if (!canManage) return null
  if (preference && !preference.available) return null
  return <Card title="팀원 주간보고 자료" action={preference && <span className="muted-chip">{selectedMemberIds.length}/{preference.maxMembers}명 선택</span>}>
    <p className="muted">선택한 팀원의 같은 주차 보고를 내 주간보고 화면·발표·PPTX와 제출 메일에 읽기 전용으로 함께 표시합니다. 작성 중인 보고서도 포함하며, 팀원의 원본 보고서와 내 업무 항목은 변경하지 않습니다.</p>
    {failed ? <><Empty>{failed}</Empty><div className="inclusion-actions"><Button variant="secondary" onClick={() => { void load() }}>다시 시도</Button></div></>
      : !preference ? <Spinner/>
      : !preference.members.length ? <Empty>현재 담당 범위에 선택할 팀원이 없습니다.</Empty>
      : <>
        <div className="inclusion-toolbar">
          <label>팀원 검색<input type="search" value={query} onChange={event => setQuery(event.target.value)} placeholder="이름·아이디·조직 검색"/></label>
          <div><Button variant="ghost" onClick={selectAll}>전체 선택</Button>
            <Button variant="ghost" onClick={() => setSelectedMemberIds([])}>전체 해제</Button></div>
        </div>
        <p className="inclusion-limit">최대 {preference.maxMembers}명까지 선택할 수 있습니다.</p>
        {visibleMembers.length ? <fieldset className="inclusion-member-list"><legend className="sr-only">내 주간보고 자료에 포함할 팀원</legend>
          {visibleMembers.map(member => <label className="inclusion-member" key={member.id}>
            <input type="checkbox" checked={selected.has(member.id)} onChange={event => toggle(member.id, event.target.checked)}/>
            <span><strong>{member.displayName}</strong><small>{[
              member.organizationName,
              member.username ? `@${member.username}` : '',
            ].filter(Boolean).join(' · ') || '조직 정보 없음'}</small></span>
          </label>)}
        </fieldset> : <Empty>검색 조건에 맞는 팀원이 없습니다.</Empty>}
        <div className="inclusion-actions"><Button onClick={save} disabled={busy}>{busy ? '저장 중…' : '포함 설정 저장'}</Button></div>
      </>}
  </Card>
}
