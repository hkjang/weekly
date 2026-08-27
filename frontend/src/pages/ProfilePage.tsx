import { useEffect, useState } from 'react'
import { errorText, api, del, post, put } from '../api'
import { Button, Card, Empty, PageHeader, formatDate } from '../components'
import type { KeyView, MailPreference, SessionInfo } from '../types'

export default function ProfilePage({ session, notify, refreshSession }: { session: SessionInfo; notify: (message: string, kind?: 'success' | 'error') => void; refreshSession: () => Promise<void> }) {
  const [keys, setKeys] = useState<KeyView[]>([])
  const [keyVersion, setKeyVersion] = useState(session.user.keyVersion)
  const [name, setName] = useState('MCP / API')
  const [days, setDays] = useState(90)
  const [token, setToken] = useState<string>()
  const [mail, setMail] = useState<MailPreference>()
  const [mailAddress, setMailAddress] = useState('')
  const [mailOn, setMailOn] = useState(false)
  const loadMail = () => api<MailPreference>('/api/v1/me/mail').then(value => {
    setMail(value); setMailAddress(value.address); setMailOn(value.onSubmit)
  })
  const saveMail = async () => {
    try {
      await put('/api/v1/me/mail', { address: mailAddress.trim(), onSubmit: mailOn })
      await loadMail()
      notify(mailOn ? '주간보고를 제출하면 이 주소로 발송합니다.' : '메일 발송을 껐습니다.')
    } catch (error) { notify(errorText(error, '메일 발송 설정을 저장할 수 없습니다.'), 'error') }
  }
  const load = () => api<{ keyVersion: number; keys: KeyView[] }>('/api/v1/keys').then(value => { setKeys(value.keys); setKeyVersion(value.keyVersion) })
  useEffect(() => { load(); loadMail() }, [])
  const create = async () => { try { const value = await post<{ token: string }>('/api/v1/keys', { name, expiresInDays: days, scopes: ['reports:read', 'analytics:read', 'mcp:read'] }); setToken(value.token); await load(); notify('API 키를 생성했습니다.') } catch (error) { notify(errorText(error, '키를 만들 수 없습니다.'), 'error') } }
  const revoke = async (id: number) => { if (!confirm('이 API 키를 폐기하시겠습니까?')) return; await del(`/api/v1/keys/${id}`); await load(); notify('API 키를 폐기했습니다.') }
  const rotate = async () => { if (!confirm('모든 기존 API 키가 즉시 폐기됩니다. 키를 회전하시겠습니까?')) return; await post('/api/v1/keys/rotate'); setToken(undefined); await load(); await refreshSession(); notify('개인 키 버전을 회전하고 모든 기존 키를 폐기했습니다.') }
  return <><PageHeader title="개인 설정" description="프로필과 개인 API 키를 관리합니다."/>
    <div className="profile-grid"><Card title="프로필"><dl className="profile-details"><div><dt>이름</dt><dd>{session.user.displayName}</dd></div><div><dt>아이디</dt><dd>{session.user.username}</dd></div><div><dt>이메일</dt><dd>{session.user.email || '-'}</dd></div><div><dt>권한</dt><dd>{session.user.role}</dd></div><div><dt>서비스 버전</dt><dd>v{session.build.version} <small>{session.build.commit.slice(0, 8)}</small></dd></div></dl></Card>
      <Card title="개인 키 보안" action={<span className="key-version">Key version {keyVersion}</span>}><p>개인 키 회전은 발급된 모든 API·MCP 키를 한 번에 즉시 폐기합니다.</p><Button variant="danger" onClick={rotate}>모든 키 회전</Button></Card></div>
    <Card title="주간보고 메일 발송" action={mail && <span className="muted-chip">{mail.onSubmit ? '켜짐' : '꺼짐'}</span>}>
      <p className="muted">주간보고를 <strong>제출할 때</strong> 아래 주소로 내용을 보냅니다. 계정 이메일과 달라도 됩니다.</p>
      {/* A writer who turns this on and receives nothing would otherwise have
          no way to tell their own mistake from an unconfigured server. */}
      {mail && !mail.relayReady && <div className="edit-notice">
        관리자가 아직 메일 서버를 설정하지 않았습니다. 지금 켜 두면 설정이 끝나는 대로 발송됩니다.
      </div>}
      <div className="inline-form">
        <label>받을 주소<input type="email" value={mailAddress} placeholder="name@example.com"
          onChange={e => setMailAddress(e.target.value)}/></label>
        <label className="toggle-row"><span>제출할 때 보내기</span>
          <input type="checkbox" checked={mailOn} onChange={e => setMailOn(e.target.checked)}/></label>
        <Button onClick={saveMail}>저장</Button>
      </div>
      {mail && (mail.deliveries.length
        ? <div className="table-wrap"><table><thead><tr><th>주차</th><th>주소</th><th>상태</th><th>보낸 시각</th></tr></thead>
          <tbody>{mail.deliveries.map(delivery => <tr key={delivery.id}>
            <td>{delivery.weekStart}</td>
            <td className="truncate">{delivery.address}</td>
            <td>{delivery.status === 'SENT' ? '발송됨'
              : delivery.status === 'FAILED' ? <span className="danger-text">실패</span>
              : `대기 중${delivery.attempts > 0 ? ` · 시도 ${delivery.attempts}회` : ''}`}
              {/* The reason travels with the row: a delivery that has not
                  arrived is a question, and the relay already answered it. */}
              {delivery.error && <small className="cell-sub">{delivery.error}</small>}</td>
            <td>{formatDate(delivery.sentAt ?? undefined)}</td>
          </tr>)}</tbody></table></div>
        : <Empty>{mail.onSubmit ? '아직 발송한 주간보고가 없습니다. 다음 제출부터 여기에 기록됩니다.' : '발송이 꺼져 있습니다.'}</Empty>)}
    </Card>
    <Card title="API · MCP 키 발급"><div className="inline-form"><label>키 이름<input value={name} onChange={e => setName(e.target.value)}/></label><label>유효기간<select value={days} onChange={e => setDays(Number(e.target.value))}><option value={30}>30일</option><option value={90}>90일</option><option value={180}>180일</option><option value={365}>365일</option></select></label><Button onClick={create}>새 키 발급</Button></div>{token && <div className="token-reveal"><strong>지금 복사하세요. 다시 표시되지 않습니다.</strong><code>{token}</code><Button variant="secondary" onClick={() => navigator.clipboard.writeText(token)}>복사</Button></div>}</Card>
    <Card title="활성 키">{keys.length ? <div className="table-wrap"><table><thead><tr><th>이름</th><th>접두사</th><th>범위</th><th>만료</th><th>최근 사용</th><th/></tr></thead><tbody>{keys.map(key => <tr key={key.id}><td>{key.name}</td><td><code>{key.prefix}…</code></td><td>{key.scopes.join(', ')}</td><td>{formatDate(key.expiresAt)}</td><td>{formatDate(key.lastUsedAt)}</td><td><button className="remove-button" onClick={() => revoke(key.id)}>폐기</button></td></tr>)}</tbody></table></div> : <Empty>발급된 API 키가 없습니다.</Empty>}</Card>
    <Card title="MCP 연결"><p className="muted">Streamable HTTP 방식으로 연결하고 위에서 발급한 키를 Bearer 토큰으로 사용합니다.</p><pre className="code-block">{`URL: ${location.origin}/mcp\nAuthorization: Bearer wky_...\nTools: weekly_submission_overview, weekly_reports_search`}</pre></Card>
  </>
}
