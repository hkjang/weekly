/**
 * Hash routing with parameters, so a screen can be linked to directly:
 * `#/rollup?kind=QUARTER&period=2026-Q3`, `#/history?report=42`.
 *
 * Quick navigation depends on this: jumping to "2026년 3분기" is only possible
 * if the target state lives in the URL rather than in component state.
 */

export type PageName =
  | 'dashboard' | 'current' | 'history' | 'work' | 'rollup'
  | 'meeting' | 'digest' | 'insights' | 'handover'
  | 'import' | 'team' | 'analytics' | 'profile' | 'admin'

export const pageNames: PageName[] = [
  'dashboard', 'current', 'history', 'work', 'rollup',
  'meeting', 'digest', 'insights', 'handover',
  'import', 'team', 'analytics', 'profile', 'admin',
]

export interface Route {
  page: PageName
  params: Record<string, string>
}

export function parseRoute(hash: string = window.location.hash): Route | undefined {
  const raw = hash.replace(/^#\/?/, '')
  if (!raw) return undefined
  const [name, query = ''] = raw.split('?')
  if (!pageNames.includes(name as PageName)) return undefined
  const params: Record<string, string> = {}
  for (const [key, value] of new URLSearchParams(query)) params[key] = value
  return { page: name as PageName, params }
}

export function routeHash(page: PageName, params?: Record<string, string | undefined>): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params ?? {})) {
    if (value !== undefined && value !== '') search.set(key, value)
  }
  const query = search.toString()
  return `#/${page}${query ? `?${query}` : ''}`
}

/** navigateTo pushes a new entry so the browser back button works as expected. */
export function navigateTo(page: PageName, params?: Record<string, string | undefined>) {
  const next = routeHash(page, params)
  if (window.location.hash !== next) window.location.hash = next
}

/** replaceRoute rewrites the current entry, for state the user did not ask to record. */
export function replaceRoute(page: PageName, params?: Record<string, string | undefined>) {
  const next = routeHash(page, params)
  if (window.location.hash === next) return
  window.history.replaceState(null, '', `${window.location.pathname}${window.location.search}${next}`)
}

// ---------------------------------------------------------------------------
// Korean-aware matching
// ---------------------------------------------------------------------------

const choseong = ['ㄱ', 'ㄲ', 'ㄴ', 'ㄷ', 'ㄸ', 'ㄹ', 'ㅁ', 'ㅂ', 'ㅃ', 'ㅅ', 'ㅆ', 'ㅇ', 'ㅈ', 'ㅉ', 'ㅊ', 'ㅋ', 'ㅌ', 'ㅍ', 'ㅎ']

/**
 * initials turns "기간 업무보고" into "ㄱㄱㅇㅁㅂㄱ" so a user can find a screen by
 * typing only the leading consonants, which is how Korean users abbreviate.
 */
export function initials(value: string): string {
  let result = ''
  for (const character of value) {
    const code = character.codePointAt(0) ?? 0
    if (code >= 0xac00 && code <= 0xd7a3) {
      result += choseong[Math.floor((code - 0xac00) / 588)]
    } else if (/\S/.test(character)) {
      result += character.toLowerCase()
    }
  }
  return result
}

const isChoseongQuery = (query: string) => /^[ㄱ-ㅎ]+$/.test(query)

function normalize(value: string): string {
  return value.toLowerCase().replace(/\s+/g, '')
}

/**
 * wordInitials takes the leading consonant of each word, so "기간 업무보고"
 * becomes "ㄱㅇ" — how a two word title is usually abbreviated.
 */
function wordInitials(value: string): string {
  return value.split(/\s+/).filter(Boolean).map(word => initials(word).charAt(0)).join('')
}

/**
 * isSubsequence reports whether every character of needle appears in haystack in
 * order. Users type selected initials rather than every one, so "ㄱㅇㅂ" has to
 * match "ㄱㄱㅇㅁㅂㄱ" even though it is not a contiguous run.
 */
function isSubsequence(needle: string, haystack: string): boolean {
  let position = 0
  for (const character of haystack) {
    if (character === needle[position]) position++
    if (position === needle.length) return true
  }
  return position === needle.length
}

/**
 * matchScore ranks a candidate against the query. Higher is better, 0 means no
 * match. A prefix hit outranks a hit in the middle of the text.
 */
export function matchScore(query: string, label: string, keywords: string[] = []): number {
  const needle = normalize(query)
  if (!needle) return 1
  const haystacks = [label, ...keywords]
  let best = 0
  for (const candidate of haystacks) {
    const target = normalize(candidate)
    if (target === needle) best = Math.max(best, 100)
    else if (target.startsWith(needle)) best = Math.max(best, 80)
    else if (target.includes(needle)) best = Math.max(best, 55)
    if (isChoseongQuery(needle)) {
      const short = initials(candidate)
      const words = wordInitials(candidate)
      if (words === needle) best = Math.max(best, 90)
      else if (words.startsWith(needle)) best = Math.max(best, 78)
      else if (short.startsWith(needle)) best = Math.max(best, 72)
      else if (short.includes(needle)) best = Math.max(best, 60)
      else if (isSubsequence(needle, short)) best = Math.max(best, 45)
    }
  }
  return best
}
