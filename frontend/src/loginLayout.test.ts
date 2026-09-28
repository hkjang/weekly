import { describe, expect, it } from 'vitest'
import { loginNotice, loginSections } from './loginLayout'

describe('로그인 화면이 먼저 보여 주는 것', () => {
  it('조직 계정이 켜져 있으면 그것이 주 버튼이고 아이디·비밀번호는 접힌다', () => {
    expect(loginSections({ local: true, oidc: true })).toEqual({ sso: true, local: 'collapsed' })
  })

  it('조직 계정이 없으면 아이디·비밀번호를 접지 않는다 — 유일한 길이다', () => {
    expect(loginSections({ local: true, oidc: false })).toEqual({ sso: false, local: 'form' })
  })

  it('로컬 로그인이 꺼져 있으면 조직 계정만 남는다', () => {
    expect(loginSections({ local: false, oidc: true })).toEqual({ sso: true, local: 'none' })
  })

  it('둘 다 꺼져 있으면 아무 길도 없다고 화면이 따로 말한다', () => {
    expect(loginSections({ local: false, oidc: false })).toEqual({ sso: false, local: 'none' })
  })
})

describe('로그인 화면 위의 한 줄', () => {
  it('자동 로그인이 세션을 찾지 못하고 돌아오면 그 사실을 설명한다 — 오류가 아니라 안내로', () => {
    const notice = loginNotice({ signedOut: false, oidcAuto: 'miss' })
    expect(notice?.text).toContain('조직 계정 세션이 없어')
    expect(notice?.tone).toBe('info')
  })

  it('자동 로그인을 확인하지 못했으면 경고로 말한다', () => {
    expect(loginNotice({ signedOut: false, oidcAuto: 'unavailable' })?.tone).toBe('alert')
  })

  it('Keycloak 에 닿지 않아 건너뛰었으면 경고로 말하고 무엇을 확인할지 적는다', () => {
    const notice = loginNotice({ signedOut: false, oidcAuto: 'unreachable' })
    expect(notice?.tone).toBe('alert')
    expect(notice?.text).toContain('VPN')
  })

  it('세션 만료가 가장 먼저다 — 쓰던 사람에게 가장 중요한 사실이다', () => {
    expect(loginNotice({ signedOut: true, oidcAuto: 'miss' })?.text).toContain('세션이 만료')
  })

  it('아무 일도 없었으면 아무 말도 하지 않는다', () => {
    expect(loginNotice({ signedOut: false, oidcAuto: null })).toBeUndefined()
  })
})
