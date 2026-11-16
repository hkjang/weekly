import { useEffect, useState } from 'react'
import { api, download, errorText, post, put } from '../api'
import { Modal, Button, Card, Spinner, formatDate } from '../components'
import type { AdminUser, AdminUserPage, DepartmentOverview, DepartmentRow, DepartmentSyncMode, Organization } from '../types'

type Notify = (message: string, kind?: 'success' | 'error') => void

const roleNames: Record<string, string> = { USER: '사용자', TEAM_LEADER: '팀장', ORG_MANAGER: '조직장', ADMIN: '관리자' }
const roleOptions = (['USER', 'TEAM_LEADER', 'ORG_MANAGER', 'ADMIN'] as const).map(value => <option key={value} value={value}>{roleNames[value]}</option>)
const syncModeNames: Record<DepartmentSyncMode, string> = {
  OFF: '자동 지정 꺼짐 — 부서 값만 기록',
  FILL: '조직이 없거나 기본 조직인 계정만 자동 지정',
  ALWAYS: '로그인할 때마다 부서에 맞춰 자동 지정',
}
const matchNames: Record<DepartmentRow['via'], string> = {
  alias: '수동 매핑', code: '코드 일치', name: '이름 일치', ambiguous: '같은 이름의 조직이 여럿', unmatched: '일치하는 조직 없음',
}

/**
 * Keycloak 부서 → 조직.
 *
 * The sign-in places people by their department one at a time and says nothing
 * about the ones it could not place. This is where those are visible: every
 * value Keycloak has sent, what it resolves to, and how many people are not
 * where it says — with the two decisions only an administrator can make (which
 * of two same-named teams, and what an unknown value means) one select away.
 */
function DepartmentsCard({ organizations, notify, reload, version }: { organizations: Organization[]; notify: Notify; reload: () => Promise<unknown>; version: number }) {
  const [overview, setOverview] = useState<DepartmentOverview>()
  const [failed, setFailed] = useState('')
  const [busy, setBusy] = useState(false)
  const load = () => api<DepartmentOverview>('/api/v1/admin/departments').then(data => { setOverview(data); setFailed('') }).catch(error => setFailed(errorText(error, '부서 현황을 불러올 수 없습니다.')))
  useEffect(() => { void load() }, [version])
  const run = async (work: () => Promise<string>) => {
    setBusy(true)
    try { const message = await work(); await Promise.all([load(), reload()]); notify(message) } catch (error) { notify(errorText(error, '처리할 수 없습니다.'), 'error') } finally { setBusy(false) }
  }
  const map = (row: DepartmentRow, value: string) => run(async () => {
    await put('/api/v1/admin/departments/mapping', { department: row.department, organizationId: value ? Number(value) : null })
    return value ? `'${row.department}' 부서를 조직에 연결했습니다. '적용'을 누르면 해당 사용자가 옮겨집니다.` : `'${row.department}' 수동 매핑을 지웠습니다.`
  })
  const apply = (row?: DepartmentRow) => {
    const count = row ? row.pending : overview?.pending ?? 0
    if (!confirm(`${row ? `'${row.department}' 부서 ` : '모든 부서의 '}사용자 ${count.toLocaleString()}명을 부서 기준 조직으로 옮깁니다. 조직을 고정한 사용자는 옮기지 않습니다. 계속하시겠습니까?`)) return
    void run(async () => { const result = await post<{ moved: number }>('/api/v1/admin/departments/apply', { department: row?.department ?? '' }); return `${result.moved.toLocaleString()}명의 조직을 옮겼습니다.` })
  }
  const createOrganization = (row: DepartmentRow) => {
    if (!confirm(`'${row.department}' 이름으로 최상위 조직을 만듭니다(코드 ${row.suggestedCode}). 상위 조직은 조직 탭에서 바꿀 수 있습니다. 계속하시겠습니까?`)) return
    void run(async () => { await post('/api/v1/admin/organizations', { name: row.department.slice(0, 120), code: row.suggestedCode, parentId: null }); return `'${row.department}' 조직을 만들었습니다.` })
  }
  if (failed) return <Card title="Keycloak 부서 → 조직"><p className="capture-warning">{failed} <button className="text-button" onClick={() => void load()}>다시 시도</button></p></Card>
  if (!overview) return <Card title="Keycloak 부서 → 조직"><Spinner/></Card>
  const problems = overview.items.filter(row => row.via === 'unmatched' || row.via === 'ambiguous').length
  return <Card title="Keycloak 부서 → 조직" action={overview.pending > 0 ? <Button variant="secondary" disabled={busy} onClick={() => apply()}>전체 적용 ({overview.pending.toLocaleString()}명)</Button> : undefined}>
    <p className="setting-help"><strong>{syncModeNames[overview.mode]}</strong> · Claim <code>{overview.claim || '(지정 안 됨)'}</code>{overview.create ? ' · 일치하는 조직이 없으면 새로 만듦' : ''}. 방식은 <em>서비스 설정 → 인증 · Keycloak OIDC</em>에서 바꿉니다. 연결 순서는 수동 매핑 → 조직 코드 → 조직 이름(하나뿐일 때)입니다.
      {overview.withoutDepartment > 0 && <> SSO 계정 중 <strong>{overview.withoutDepartment.toLocaleString()}명</strong>은 아직 부서 값을 받은 적이 없습니다 — Keycloak 클라이언트에 User Attribute 매퍼(<code>department</code>)가 있는지 확인하세요.</>}</p>
    {overview.items.length === 0
      ? <p className="muted">아직 부서 값을 받은 사용자가 없습니다. Keycloak 클라이언트에 User Attribute 매퍼를 추가한 뒤 사용자가 한 번 로그인하면 여기에 나타납니다.</p>
      : <>{problems > 0 && <p className="capture-warning">조직에 연결되지 않은 부서 값이 <strong>{problems}개</strong> 있습니다. 해당 사용자는 자동 지정되지 않으므로 아래에서 조직을 골라 연결하세요.</p>}
        <div className="table-wrap department-table"><table><thead><tr><th>Keycloak 부서 값</th><th>사용자</th><th>연결된 조직</th><th>현황</th><th/></tr></thead><tbody>{overview.items.map(row => <tr key={row.department}>
          <td><strong>{row.department}</strong><small className="cell-sub">최근 수신 {formatDate(row.lastSeenAt ?? undefined)}</small></td>
          <td>{row.users.toLocaleString()}명</td>
          <td><select aria-label={`${row.department} 부서를 연결할 조직`} disabled={busy} value={row.via === 'alias' && row.organizationId ? row.organizationId : ''} onChange={event => void map(row, event.target.value)}>
            <option value="">{row.via === 'alias' ? '수동 매핑 지우기 (자동)' : row.organizationId ? `자동 — ${row.organizationName}` : '— 조직 선택 —'}</option>
            {organizations.map(o => <option key={o.id} value={o.id}>{o.name} ({o.code})</option>)}</select>
            <small className={`cell-sub ${row.organizationId ? '' : 'danger-text'}`}>{matchNames[row.via]}{row.via === 'ambiguous' && row.candidates ? ` (${row.candidates.length}곳)` : ''}</small></td>
          <td>{row.organizationId ? <>일치 {row.inOrganization.toLocaleString()}{row.pending > 0 && <> · <strong>이동 대기 {row.pending.toLocaleString()}</strong></>}{row.locked > 0 && <> · 고정 {row.locked.toLocaleString()}</>}</> : <span className="muted">연결 필요</span>}</td>
          <td className="row-actions">{row.pending > 0 && <button className="text-button" disabled={busy} onClick={() => apply(row)}>적용</button>}{row.via === 'unmatched' && <button className="text-button" disabled={busy} onClick={() => createOrganization(row)}>조직 만들기</button>}</td>
        </tr>)}</tbody></table></div></>}
  </Card>
}

export default function UsersTab({ notify }: { notify: Notify }) {
  const [users, setUsers] = useState<AdminUser[]>(); const [organizations, setOrganizations] = useState<Organization[]>([])
  const [editing, setEditing] = useState<(AdminUser & { password: string })>()
  // Escape closes this dialog now, so a half-finished account edit needs a
  // question before it is thrown away.
  const userChanged = () => {
    if (!editing) return false
    const original = users?.find(user => user.id === editing.id) ?? reviewers.find(user => user.id === editing.id)
    if (!original) return true
    return editing.password.trim() !== ''
      || editing.displayName !== original.displayName
      || editing.email !== original.email
      || editing.role !== original.role
      || editing.organizationId !== original.organizationId
      || editing.managerId !== original.managerId
      || editing.active !== original.active
      || !!editing.organizationLocked !== !!original.organizationLocked
  }
  // Every way out of this dialog asks the same question. The × was the one that
  // did not, and it is the one people reach for.
  const confirmDiscardUser = () => !userChanged() || confirm('수정한 내용을 저장하지 않고 닫으시겠습니까?')
  const [form, setForm] = useState({ username: '', displayName: '', email: '', password: '', role: 'USER', organizationId: '' })
  const [creating, setCreating] = useState(false)
  // userPageDefault on the server, sent explicitly so the pager's arithmetic
  // and the page it is describing cannot drift apart.
  const userLimit = 100
  const [total, setTotal] = useState(0)
  const [query, setQuery] = useState('')
  // The filters an administrator actually reads a directory by. 'none' in the
  // organisation filter is the accounts no team leader can see.
  const [filters, setFilters] = useState({ role: '', organization: '', status: '', activity: '', sort: '' })
  const [syncMode, setSyncMode] = useState<DepartmentSyncMode>('OFF')
  // The list is a page now, so the 검토 책임자 picker cannot be built from it —
  // a reviewer on page three would simply not be offered. Reviewers are asked
  // for by role instead, which is a small set however large the directory.
  const [reviewers, setReviewers] = useState<AdminUser[]>([])
  // Accounts with no organisation are invisible to every team leader: their
  // reports never reach a review queue.
  const [unassigned, setUnassigned] = useState(0)
  const [offset, setOffset] = useState(0)
  const [selected, setSelected] = useState<number[]>([])
  const [bulk, setBulk] = useState({ organization: '', role: '' })
  // Bumped after anything that can change where people sit, so the department
  // card re-reads its counts.
  const [version, setVersion] = useState(0)
  const filterQuery = (search = query, current = filters) => `q=${encodeURIComponent(search)}`
    + (current.role ? `&role=${current.role}` : '') + (current.organization ? `&organization=${current.organization}` : '')
    + (current.status ? `&status=${current.status}` : '') + (current.activity ? `&activity=${current.activity}` : '')
    + (current.sort ? `&sort=${current.sort}` : '')
  const load = (search = query, current = filters, from = offset) => Promise.all([
    api<AdminUserPage>(`/api/v1/admin/users?${filterQuery(search, current)}&limit=${userLimit}&offset=${from}`),
    api<Organization[]>('/api/v1/admin/organizations'),
    api<AdminUserPage>('/api/v1/admin/users?role=TEAM_LEADER,ORG_MANAGER,ADMIN&limit=500'),
  ]).then(([page,o,leads]) => { setUsers(page.items); setTotal(page.total); setUnassigned(page.unassigned); setSyncMode(page.departmentSync ?? 'OFF'); setOrganizations(o); setReviewers(leads.items); setSelected(ids => ids.filter(id => page.items.some(user => user.id === id))) })
  const reload = async () => { await load(); setVersion(value => value + 1) }
  const goToPage = (next: number) => { setOffset(next); setSelected([]); void load(query, filters, next) }
  useEffect(() => { load() }, [])
  useEffect(() => {
    // Typed searches are debounced: one request per keystroke over a directory
    // of thousands is a lot of work to throw away.
    // Back to the first page with the search: a narrowed directory has fewer
    // pages than the one being read, and page three of a two-result search is
    // a blank table under a filled-in search box.
    const timer = setTimeout(() => { setOffset(0); void load(query, filters, 0) }, 300)
    return () => clearTimeout(timer)
  }, [query, filters])
  const organizationName = (id?: number | null) => organizations.find(o => o.id === id)?.name
  const create = async () => { try { await post('/api/v1/admin/users', { ...form, organizationId: form.organizationId ? Number(form.organizationId) : null }); setForm({ username: '', displayName: '', email: '', password: '', role: 'USER', organizationId: '' }); setCreating(false); await reload(); notify('사용자를 등록했습니다.') } catch (error) { notify(errorText(error, '등록할 수 없습니다.'), 'error') } }
  const saveUser = async () => { if (!editing) return; try { await put(`/api/v1/admin/users/${editing.id}`, { displayName: editing.displayName, email: editing.email, password: editing.password, role: editing.role, organizationId: editing.organizationId ?? null, managerId: editing.managerId ?? null, active: editing.active, organizationLocked: !!editing.organizationLocked }); setEditing(undefined); await reload(); notify('사용자 정보를 저장했습니다.') } catch (error) { notify(errorText(error, '저장할 수 없습니다.'), 'error') } }
  const bulkChange = async (change: Record<string, unknown>, question: string, done: string) => {
    if (!confirm(`선택한 ${selected.length}명 — ${question}`)) return
    try { const result = await post<{ updated: number }>('/api/v1/admin/users/bulk', { ids: selected, ...change }); setSelected([]); setBulk({ organization: '', role: '' }); await reload(); notify(`${result.updated.toLocaleString()}명 ${done}`) } catch (error) { notify(errorText(error, '일괄 변경할 수 없습니다.'), 'error') }
  }
  // Moving somebody by hand while the directory sync is on would last until
  // their next sign-in, so a manual move pins them; 부서 연동으로 되돌리기 unpins.
  const pinOnMove = syncMode !== 'OFF'
  const exportCSV = async () => { try { await download(`/api/v1/admin/users/export?${filterQuery()}`, 'weekly-users.csv') } catch (error) { notify(errorText(error, '내려받을 수 없습니다.'), 'error') } }
  if (!users) return <Spinner/>
  const allSelected = users.length > 0 && users.every(user => selected.includes(user.id))
  const filtered = !!(query || filters.role || filters.organization || filters.status || filters.activity)
  return <>
    <DepartmentsCard organizations={organizations} notify={notify} reload={() => load()} version={version}/>
    <Card title={`사용자 ${total.toLocaleString()}명`} action={<div className="card-actions"><Button variant="secondary" onClick={exportCSV}>CSV 내려받기</Button><Button onClick={() => setCreating(!creating)}>{creating ? '등록 닫기' : '+ 사용자 등록'}</Button></div>}>
    {creating && <div className="inline-form wrap user-create"><label>아이디<input value={form.username} onChange={e => setForm({...form,username:e.target.value})}/></label><label>표시 이름<input value={form.displayName} onChange={e => setForm({...form,displayName:e.target.value})}/></label><label>이메일<input value={form.email} onChange={e => setForm({...form,email:e.target.value})}/></label><label>초기 비밀번호<input type="password" value={form.password} onChange={e => setForm({...form,password:e.target.value})} placeholder="12자 이상 또는 비움"/></label><label>역할<select value={form.role} onChange={e => setForm({...form,role:e.target.value})}>{roleOptions}</select></label><label>조직<select value={form.organizationId} onChange={e => setForm({...form,organizationId:e.target.value})}><option value="">미지정</option>{organizations.map(o => <option value={o.id} key={o.id}>{o.name}</option>)}</select></label><Button onClick={create}>등록</Button></div>}
    {unassigned > 0 && <p className="capture-warning">소속 조직이 지정되지 않은 계정이 <strong>{unassigned}명</strong> 있습니다. 이 계정의 주간보고는 어느 팀장에게도 보이지 않아, 제출해도 검토 대기 상태로 남습니다.{' '}
      <button className="text-button" onClick={() => setFilters({ ...filters, organization: filters.organization === 'none' ? '' : 'none' })}>{filters.organization === 'none' ? '전체 보기' : '해당 계정만 보기'}</button></p>}
    <div className="user-filters">
      <input className="search-inline" aria-label="사용자 검색" value={query} onChange={e => setQuery(e.target.value)} placeholder="아이디·이름·이메일·부서 검색"/>
      <select aria-label="역할 필터" value={filters.role} onChange={e => setFilters({ ...filters, role: e.target.value })}><option value="">모든 역할</option>{roleOptions}</select>
      <select aria-label="조직 필터" value={filters.organization} onChange={e => setFilters({ ...filters, organization: e.target.value })}><option value="">모든 조직</option><option value="none">조직 미지정</option>{organizations.map(o => <option key={o.id} value={o.id}>{o.name}</option>)}</select>
      <select aria-label="상태 필터" value={filters.status} onChange={e => setFilters({ ...filters, status: e.target.value })}><option value="">모든 상태</option><option value="active">활성</option><option value="inactive">비활성</option></select>
      <select aria-label="접속 필터" value={filters.activity} onChange={e => setFilters({ ...filters, activity: e.target.value })}><option value="">모든 접속 이력</option><option value="stale">90일 이상 미접속</option><option value="never">로그인한 적 없음</option></select>
      <select aria-label="정렬" value={filters.sort} onChange={e => setFilters({ ...filters, sort: e.target.value })}><option value="">이름순</option><option value="username">아이디순</option><option value="recent">최근 로그인순</option><option value="created">최근 등록순</option></select>
      {filtered && <button className="text-button" onClick={() => { setQuery(''); setFilters({ ...filters, role: '', organization: '', status: '', activity: '' }) }}>필터 지우기</button>}
    </div>
    {selected.length > 0 && <div className="bulk-bar" role="region" aria-label="선택한 사용자 일괄 변경">
      <strong>{selected.length}명 선택</strong>
      <select aria-label="옮길 조직" value={bulk.organization} onChange={e => setBulk({ ...bulk, organization: e.target.value })}><option value="">조직 이동…</option><option value="none">조직 미지정으로</option>{organizations.map(o => <option key={o.id} value={o.id}>{o.name}</option>)}</select>
      <Button variant="secondary" disabled={!bulk.organization} onClick={() => bulkChange({ organizationId: bulk.organization === 'none' ? null : Number(bulk.organization), ...(pinOnMove ? { organizationLocked: true } : {}) }, `'${bulk.organization === 'none' ? '조직 미지정' : organizationName(Number(bulk.organization))}'(으)로 옮기시겠습니까?${pinOnMove ? ' 부서 자동 지정이 켜져 있어, 옮긴 계정은 조직이 고정됩니다.' : ''}`, '조직을 옮겼습니다.')}>이동</Button>
      <select aria-label="바꿀 역할" value={bulk.role} onChange={e => setBulk({ ...bulk, role: e.target.value })}><option value="">역할 변경…</option>{roleOptions}</select>
      <Button variant="secondary" disabled={!bulk.role} onClick={() => bulkChange({ role: bulk.role }, `역할을 '${roleNames[bulk.role]}'(으)로 바꾸시겠습니까?`, '역할을 바꿨습니다.')}>변경</Button>
      <Button variant="secondary" onClick={() => bulkChange({ active: true }, '계정을 활성화하시겠습니까?', '활성화했습니다.')}>활성화</Button>
      <Button variant="secondary" onClick={() => bulkChange({ active: false }, '계정을 비활성화하시겠습니까? 비활성 계정은 로그인할 수 없습니다.', '비활성화했습니다.')}>비활성화</Button>
      {syncMode !== 'OFF' && <Button variant="secondary" onClick={() => bulkChange({ organizationLocked: false }, '조직 고정을 풀고 다음 로그인부터 Keycloak 부서를 따르게 하시겠습니까?', '조직 고정을 풀었습니다.')}>부서 연동으로 되돌리기</Button>}
      <button className="text-button" onClick={() => setSelected([])}>선택 해제</button>
    </div>}
    <div className="table-wrap"><table><thead><tr><th><input type="checkbox" aria-label="이 페이지 전체 선택" checked={allSelected} onChange={e => setSelected(e.target.checked ? users.map(user => user.id) : [])}/></th><th>사용자</th><th>역할</th><th>조직</th><th>검토 책임자</th><th>로그인</th><th>상태</th><th/></tr></thead><tbody>{users.map(user => {
      const departmentOrganization = user.ssoDepartment ? user.departmentOrganizationId : undefined
      const mismatch = !!user.ssoDepartment && !!departmentOrganization && departmentOrganization !== user.organizationId
      const manager = user.managerId ? reviewers.find(u => u.id === user.managerId)?.displayName ?? users.find(u => u.id === user.managerId)?.displayName ?? `#${user.managerId}` : '-'
      return <tr key={user.id} className={selected.includes(user.id) ? 'row-selected' : undefined}>
        <td><input type="checkbox" aria-label={`${user.displayName} 선택`} checked={selected.includes(user.id)} onChange={e => setSelected(e.target.checked ? [...selected, user.id] : selected.filter(id => id !== user.id))}/></td>
        <td><strong>{user.displayName}</strong><small className="cell-sub">{user.username}{user.email ? ` · ${user.email}` : ''}</small></td>
        <td><span className={`badge role-${user.role.toLowerCase()}`}>{roleNames[user.role] ?? user.role}</span></td>
        <td>{organizationName(user.organizationId) ?? <span className="danger-text">미지정</span>}{user.organizationLocked && <span className="badge pin-badge" title="관리자가 조직을 고정했습니다. Keycloak 부서 자동 지정이 이 계정의 조직을 바꾸지 않습니다.">고정</span>}
          {user.ssoDepartment && <small className={`cell-sub ${mismatch && !user.organizationLocked ? 'danger-text' : ''}`}>부서: {user.ssoDepartment}{mismatch ? ` → ${organizationName(departmentOrganization) ?? '?'}` : user.departmentMatch === 'unmatched' || user.departmentMatch === 'ambiguous' ? ' (연결된 조직 없음)' : ''}</small>}</td>
        <td>{manager}</td>
        <td>{user.sso && <span className="badge login-sso">SSO</span>}{user.localPassword && <span className="badge login-local">로컬</span>}{!user.sso && !user.localPassword && <span className="muted">없음</span>}<small className="cell-sub">{user.lastLoginAt ? formatDate(user.lastLoginAt) : '로그인 기록 없음'}</small></td>
        <td><span className={`dot ${user.active ? 'on' : ''}`}/>{user.active ? '활성' : '비활성'}</td>
        <td><button className="text-button" onClick={() => setEditing({...user,password:''})}>편집</button></td></tr> })}
      {users.length === 0 && <tr><td colSpan={8} className="muted">{filtered ? '조건에 맞는 사용자가 없습니다.' : '사용자가 없습니다.'}</td></tr>}</tbody></table></div>
    {total > users.length && <div className="audit-pager">
      <span className="muted">전체 {total.toLocaleString()}명 중 {users.length ? offset + 1 : 0}~{Math.min(offset + users.length, total)}</span>
      <Button variant="secondary" disabled={offset === 0} onClick={() => goToPage(Math.max(0, offset - userLimit))}>이전</Button>
      <Button variant="secondary" disabled={offset + users.length >= total} onClick={() => goToPage(offset + userLimit)}>다음</Button>
    </div>}</Card>
    {editing && <Modal onClose={() => setEditing(undefined)} beforeClose={confirmDiscardUser} label={`${editing.username} 사용자 편집`}><header><h2>{editing.username} 사용자 편집</h2><button onClick={() => { if (confirmDiscardUser()) setEditing(undefined) }}>×</button></header><div className="modal-form">
      <p className="setting-help modal-wide">로그인: {editing.sso ? 'Keycloak SSO 연결됨' : 'SSO 연결 없음'}{editing.localPassword ? ' · 로컬 비밀번호 있음' : ' · 로컬 비밀번호 없음'} · 최근 로그인 {editing.lastLoginAt ? formatDate(editing.lastLoginAt) : '없음'} · 등록 {formatDate(editing.createdAt)}
        {editing.ssoDepartment && <><br/>Keycloak 부서: <strong>{editing.ssoDepartment}</strong>{editing.departmentOrganizationId ? ` → ${organizationName(editing.departmentOrganizationId) ?? '?'}` : ' (연결된 조직 없음)'}{editing.ssoDepartmentAt ? ` · ${formatDate(editing.ssoDepartmentAt)} 수신` : ''}</>}</p>
      <label>표시 이름<input value={editing.displayName} onChange={e => setEditing({...editing,displayName:e.target.value})}/></label><label>이메일<input value={editing.email} onChange={e => setEditing({...editing,email:e.target.value})}/></label><label>역할<select value={editing.role} onChange={e => setEditing({...editing,role:e.target.value as AdminUser['role']})}>{roleOptions}</select></label><label>조직<select value={editing.organizationId ?? ''} onChange={e => setEditing({...editing,organizationId:e.target.value?Number(e.target.value):undefined,organizationLocked:pinOnMove ? true : editing.organizationLocked})}><option value="">미지정</option>{organizations.map(o=><option key={o.id} value={o.id}>{o.name}</option>)}</select></label><label>검토 책임자<select value={editing.managerId ?? ''} onChange={e => setEditing({...editing,managerId:e.target.value?Number(e.target.value):undefined})}><option value="">미지정</option>{reviewers.filter(u=>u.id!==editing.id).map(u=><option key={u.id} value={u.id}>{u.displayName}</option>)}</select></label><label>새 비밀번호<input type="password" value={editing.password} onChange={e => setEditing({...editing,password:e.target.value})} placeholder="변경할 때만 12자 이상 입력"/></label>
      <label className="checkbox-line"><input type="checkbox" checked={!!editing.organizationLocked} onChange={e=>setEditing({...editing,organizationLocked:e.target.checked})}/> 조직 고정 — Keycloak 부서 자동 지정이 이 계정의 조직을 바꾸지 않음</label>
      <label className="checkbox-line"><input type="checkbox" checked={editing.active} onChange={e=>setEditing({...editing,active:e.target.checked})}/> 활성 계정</label></div><footer><Button variant="secondary" onClick={()=>setEditing(undefined)}>취소</Button><Button onClick={saveUser}>저장</Button></footer></Modal>}
  </>
}
