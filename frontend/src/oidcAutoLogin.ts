import { APIError } from './api'

export const OIDC_AUTO_ATTEMPTED = 'weekly_oidc_auto_attempted'
export const OIDC_AUTO_SKIP = 'weekly_oidc_auto_skip'

type MarkerStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

function sessionMarkerStorage(storage?: MarkerStorage): MarkerStorage {
  return storage ?? window.sessionStorage
}

/**
 * `autoLogin` is the administrator's oidc.auto_login as the server publishes
 * it (off by default). A server that does not publish it leaves the field
 * undefined, which is read as off: the silent redirect exists only where it
 * was switched on.
 */
export function shouldAttemptOIDCAutoLogin(input: {
  oidc: boolean; autoLogin: boolean | undefined; anonymous: boolean; signedOut: boolean; attempted: boolean; skipped: boolean
}): boolean {
  return input.oidc && input.autoLogin === true && input.anonymous && !input.signedOut && !input.attempted && !input.skipped
}

/** Only a verified 401 means that asking Keycloak for an existing session is valid. */
export function isAnonymousSessionProbe(error: unknown): boolean {
  return error instanceof APIError && error.status === 401
}

/**
 * Read both markers together. `undefined` means storage cannot be trusted, so
 * the caller must not start a redirect it cannot remember across the callback.
 */
export function oidcAutoLoginMarkers(storage?: MarkerStorage): { attempted: boolean; skipped: boolean } | undefined {
  try {
    const target = sessionMarkerStorage(storage)
    return {
      attempted: target.getItem(OIDC_AUTO_ATTEMPTED) === '1',
      skipped: target.getItem(OIDC_AUTO_SKIP) === '1',
    }
  } catch {
    return undefined
  }
}

/** Reserve the one automatic redirect. Storage failure is deliberately closed. */
export function beginOIDCAutoLogin(storage?: MarkerStorage): boolean {
  try {
    const target = sessionMarkerStorage(storage)
    if (target.getItem(OIDC_AUTO_ATTEMPTED) === '1' || target.getItem(OIDC_AUTO_SKIP) === '1') return false
    target.setItem(OIDC_AUTO_ATTEMPTED, '1')
    return target.getItem(OIDC_AUTO_ATTEMPTED) === '1'
  } catch {
    return false
  }
}

export function rememberOIDCAutoReturn(storage?: MarkerStorage): boolean {
  try {
    const target = sessionMarkerStorage(storage)
    target.setItem(OIDC_AUTO_ATTEMPTED, '1')
    return target.getItem(OIDC_AUTO_ATTEMPTED) === '1'
  } catch {
    return false
  }
}

export function skipOIDCAutoLogin(storage?: MarkerStorage): boolean {
  try {
    const target = sessionMarkerStorage(storage)
    target.setItem(OIDC_AUTO_SKIP, '1')
    return target.getItem(OIDC_AUTO_SKIP) === '1'
  } catch {
    return false
  }
}

export function clearOIDCAutoLoginMarkers(storage?: MarkerStorage): boolean {
  try {
    const target = sessionMarkerStorage(storage)
    target.removeItem(OIDC_AUTO_ATTEMPTED)
    target.removeItem(OIDC_AUTO_SKIP)
    return true
  } catch {
    return false
  }
}

export function oidcStartURL(hash: string, silent: boolean): string {
  const returnTo = hash.length <= 2048 && hash.startsWith('#/') && !/[\\\r\n\t]/.test(hash) ? hash : '#/dashboard'
  const query = new URLSearchParams({ returnTo })
  if (silent) query.set('silent', '1')
  return `/api/v1/auth/oidc/start?${query.toString()}`
}

/** Remove only the callback marker; application query parameters and the hash survive. */
export function withoutOIDCAutoResult(pathname: string, search: string, hash: string): string {
  const query = new URLSearchParams(search)
  query.delete('oidc_auto')
  const remaining = query.toString()
  return `${pathname}${remaining ? `?${remaining}` : ''}${hash}`
}

/**
 * Keycloak 이 이 브라우저에서 닿는지, 탭을 보내기 전에 한 번 묻습니다.
 *
 * 자동 로그인은 탭 전체를 Keycloak 으로 보냅니다. 브라우저가 Keycloak 에 닿지
 * 못하면 — VPN 이 끊겼거나, IdP 를 재시작하는 중이거나, 사무실 망이 그쪽으로
 * 가지 않거나 — 그 탭은 브라우저의 "사이트에 연결할 수 없음" 화면에 떨어지고
 * Weekly 는 사라집니다. 어떻게 돌아가야 하는지 말해 줄 화면도 함께 사라집니다.
 *
 * `no-cors` 요청은 응답을 읽을 수 없는 대신, 서버가 무엇이든 답하면 성공하고
 * 연결 자체가 안 되면 실패합니다. 알고 싶은 것이 정확히 그 차이입니다. 제한
 * 시간 안에 답이 없으면 닿지 않는 것으로 봅니다 — 느린 IdP 로 탭을 보내는 것도
 * 사람을 빈 화면 앞에 세워 두는 것이기 때문입니다.
 */
export async function issuerReachable(
  issuer: string,
  timeoutMs = 2500,
  fetcher: (input: string, init: RequestInit) => Promise<unknown> = (input, init) => fetch(input, init),
): Promise<boolean> {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), timeoutMs)
  try {
    await fetcher(`${issuer.replace(/\/+$/, '')}/.well-known/openid-configuration`, {
      mode: 'no-cors', cache: 'no-store', credentials: 'omit', signal: controller.signal,
    })
    return true
  } catch {
    return false
  } finally {
    clearTimeout(timer)
  }
}
