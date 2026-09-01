import { APIError } from './api'

export const OIDC_AUTO_ATTEMPTED = 'weekly_oidc_auto_attempted'
export const OIDC_AUTO_SKIP = 'weekly_oidc_auto_skip'

type MarkerStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

function sessionMarkerStorage(storage?: MarkerStorage): MarkerStorage {
  return storage ?? window.sessionStorage
}

export function shouldAttemptOIDCAutoLogin(input: {
  oidc: boolean; anonymous: boolean; signedOut: boolean; attempted: boolean; skipped: boolean
}): boolean {
  return input.oidc && input.anonymous && !input.signedOut && !input.attempted && !input.skipped
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
