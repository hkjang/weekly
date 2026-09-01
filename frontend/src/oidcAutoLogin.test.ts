import { describe, expect, it } from 'vitest'
import { APIError } from './api'
import {
  beginOIDCAutoLogin, clearOIDCAutoLoginMarkers, isAnonymousSessionProbe,
  oidcAutoLoginMarkers, oidcStartURL, shouldAttemptOIDCAutoLogin,
  skipOIDCAutoLogin, withoutOIDCAutoResult,
} from './oidcAutoLogin'

class MemoryStorage {
  values = new Map<string, string>()
  getItem(key: string) { return this.values.get(key) ?? null }
  setItem(key: string, value: string) { this.values.set(key, value) }
  removeItem(key: string) { this.values.delete(key) }
}

describe('Keycloak 기존 세션 자동 로그인', () => {
  it('OIDC가 켜져 있고 최초 세션 확인이 401일 때만 시도한다', () => {
    expect(shouldAttemptOIDCAutoLogin({ oidc: true, anonymous: true, signedOut: false, attempted: false, skipped: false })).toBe(true)
    expect(shouldAttemptOIDCAutoLogin({ oidc: true, anonymous: false, signedOut: false, attempted: false, skipped: false })).toBe(false)
    expect(shouldAttemptOIDCAutoLogin({ oidc: false, anonymous: true, signedOut: false, attempted: false, skipped: false })).toBe(false)
  })

  it('실패한 시도와 명시적 로그아웃은 리다이렉트 반복을 막는다', () => {
    expect(shouldAttemptOIDCAutoLogin({ oidc: true, anonymous: true, signedOut: false, attempted: true, skipped: false })).toBe(false)
    expect(shouldAttemptOIDCAutoLogin({ oidc: true, anonymous: true, signedOut: false, attempted: false, skipped: true })).toBe(false)
    expect(shouldAttemptOIDCAutoLogin({ oidc: true, anonymous: true, signedOut: true, attempted: false, skipped: false })).toBe(false)
  })

  it('현재 SPA hash를 query로 인코딩해 IdP 왕복 뒤에도 보존한다', () => {
    const url = new URL(oidcStartURL('#/history?report=17', true), 'https://weekly.test')
    expect(url.pathname).toBe('/api/v1/auth/oidc/start')
    expect(url.searchParams.get('silent')).toBe('1')
    expect(url.searchParams.get('returnTo')).toBe('#/history?report=17')
    expect(url.hash).toBe('')
  })

  it('401만 무세션이며 서버 장애와 네트워크 오류는 자동 로그인 조건이 아니다', () => {
    expect(isAnonymousSessionProbe(new APIError(401, 'UNAUTHORIZED', '로그인이 필요합니다.'))).toBe(true)
    expect(isAnonymousSessionProbe(new APIError(503, 'DATABASE_UNAVAILABLE', '서버 오류'))).toBe(false)
    expect(isAnonymousSessionProbe(new TypeError('Failed to fetch'))).toBe(false)
  })

  it('첫 시도를 기록한 뒤 같은 탭의 두 번째 시도를 막는다', () => {
    const storage = new MemoryStorage()
    expect(beginOIDCAutoLogin(storage)).toBe(true)
    expect(beginOIDCAutoLogin(storage)).toBe(false)
    expect(oidcAutoLoginMarkers(storage)).toEqual({ attempted: true, skipped: false })
  })

  it('명시적 로그아웃은 자동 로그인을 막고 수동 로그인은 marker를 지운다', () => {
    const storage = new MemoryStorage()
    expect(skipOIDCAutoLogin(storage)).toBe(true)
    expect(beginOIDCAutoLogin(storage)).toBe(false)
    expect(clearOIDCAutoLoginMarkers(storage)).toBe(true)
    expect(beginOIDCAutoLogin(storage)).toBe(true)
  })

  it('storage를 읽거나 쓸 수 없으면 redirect를 허용하지 않는다', () => {
    const denied = {
      getItem() { throw new DOMException('denied', 'SecurityError') },
      setItem() { throw new DOMException('denied', 'SecurityError') },
      removeItem() { throw new DOMException('denied', 'SecurityError') },
    }
    expect(oidcAutoLoginMarkers(denied)).toBeUndefined()
    expect(beginOIDCAutoLogin(denied)).toBe(false)

    const ignoresWrites = { getItem: () => null, setItem: () => {}, removeItem: () => {} }
    expect(beginOIDCAutoLogin(ignoresWrites)).toBe(false)
  })

  it('callback marker만 주소에서 지우고 다른 query와 SPA route는 보존한다', () => {
    expect(withoutOIDCAutoResult('/weekly', '?tenant=a&oidc_auto=miss&lang=ko', '#/history?report=17'))
      .toBe('/weekly?tenant=a&lang=ko#/history?report=17')
  })

  it('서버가 거부할 returnTo는 dashboard로 제한한다', () => {
    for (const hash of ['', '#evil', '#/bad\\path', `#/${'a'.repeat(2049)}`]) {
      const url = new URL(oidcStartURL(hash, true), 'https://weekly.test')
      expect(url.searchParams.get('returnTo')).toBe('#/dashboard')
    }
  })
})
