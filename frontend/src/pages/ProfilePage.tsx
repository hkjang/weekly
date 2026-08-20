import { useEffect, useState } from 'react'
import { errorText, api, del, post } from '../api'
import { Button, Card, Empty, PageHeader, formatDate } from '../components'
import type { KeyView, SessionInfo } from '../types'

export default function ProfilePage({ session, notify, refreshSession }: { session: SessionInfo; notify: (message: string, kind?: 'success' | 'error') => void; refreshSession: () => Promise<void> }) {
  const [keys, setKeys] = useState<KeyView[]>([])
  const [keyVersion, setKeyVersion] = useState(session.user.keyVersion)
  const [name, setName] = useState('MCP / API')
  const [days, setDays] = useState(90)
  const [token, setToken] = useState<string>()
  const load = () => api<{ keyVersion: number; keys: KeyView[] }>('/api/v1/keys').then(value => { setKeys(value.keys); setKeyVersion(value.keyVersion) })
  useEffect(() => { load() }, [])
  const create = async () => { try { const value = await post<{ token: string }>('/api/v1/keys', { name, expiresInDays: days, scopes: ['reports:read', 'analytics:read', 'mcp:read'] }); setToken(value.token); await load(); notify('API 키를 생성했습니다.') } catch (error) { notify(errorText(error, '키를 만들 수 없습니다.'), 'error') } }
  const revoke = async (id: number) => { if (!confirm('이 API 키를 폐기하시겠습니까?')) return; await del(`/api/v1/keys/${id}`); await load(); notify('API 키를 폐기했습니다.') }
  const rotate = async () => { if (!confirm('모든 기존 API 키가 즉시 폐기됩니다. 키를 회전하시겠습니까?')) return; await post('/api/v1/keys/rotate'); setToken(undefined); await load(); await refreshSession(); notify('개인 키 버전을 회전하고 모든 기존 키를 폐기했습니다.') }
  return <><PageHeader title="개인 설정" description="프로필과 개인 API 키를 관리합니다."/>
    <div className="profile-grid"><Card title="프로필"><dl className="profile-details"><div><dt>이름</dt><dd>{session.user.displayName}</dd></div><div><dt>아이디</dt><dd>{session.user.username}</dd></div><div><dt>이메일</dt><dd>{session.user.email || '-'}</dd></div><div><dt>권한</dt><dd>{session.user.role}</dd></div><div><dt>서비스 버전</dt><dd>v{session.build.version} <small>{session.build.commit.slice(0, 8)}</small></dd></div></dl></Card>
      <Card title="개인 키 보안" action={<span className="key-version">Key version {keyVersion}</span>}><p>개인 키 회전은 발급된 모든 API·MCP 키를 한 번에 즉시 폐기합니다.</p><Button variant="danger" onClick={rotate}>모든 키 회전</Button></Card></div>
    <Card title="API · MCP 키 발급"><div className="inline-form"><label>키 이름<input value={name} onChange={e => setName(e.target.value)}/></label><label>유효기간<select value={days} onChange={e => setDays(Number(e.target.value))}><option value={30}>30일</option><option value={90}>90일</option><option value={180}>180일</option><option value={365}>365일</option></select></label><Button onClick={create}>새 키 발급</Button></div>{token && <div className="token-reveal"><strong>지금 복사하세요. 다시 표시되지 않습니다.</strong><code>{token}</code><Button variant="secondary" onClick={() => navigator.clipboard.writeText(token)}>복사</Button></div>}</Card>
    <Card title="활성 키">{keys.length ? <div className="table-wrap"><table><thead><tr><th>이름</th><th>접두사</th><th>범위</th><th>만료</th><th>최근 사용</th><th/></tr></thead><tbody>{keys.map(key => <tr key={key.id}><td>{key.name}</td><td><code>{key.prefix}…</code></td><td>{key.scopes.join(', ')}</td><td>{formatDate(key.expiresAt)}</td><td>{formatDate(key.lastUsedAt)}</td><td><button className="remove-button" onClick={() => revoke(key.id)}>폐기</button></td></tr>)}</tbody></table></div> : <Empty>발급된 API 키가 없습니다.</Empty>}</Card>
    <Card title="MCP 연결"><p className="muted">Streamable HTTP 방식으로 연결하고 위에서 발급한 키를 Bearer 토큰으로 사용합니다.</p><pre className="code-block">{`URL: ${location.origin}/mcp\nAuthorization: Bearer wky_...\nTools: weekly_submission_overview, weekly_reports_search`}</pre></Card>
  </>
}
