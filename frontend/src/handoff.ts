// 서비스 간 문서 넘기기 — 받는 쪽의 순수한 부분.
//
// The import screen is opened at #/import?source=…&claim=… by another
// service's "다른 서비스로 보내기". What the screen does with that is decided
// here, where a test can ask without a browser.

export interface HandoffRequest { source: string; claim: string }

/** handoffRequest reads a handoff out of the route, or nothing. */
export function handoffRequest(params: Record<string, string> | undefined): HandoffRequest | undefined {
  const source = (params?.source ?? '').trim()
  const claim = (params?.claim ?? '').trim()
  if (!source || !claim) return undefined
  return { source, claim }
}

/** handoffOriginLabel is the host a person recognises, not the whole address. */
export function handoffOriginLabel(source: string): string {
  try {
    const parsed = new URL(source)
    return parsed.host || source
  } catch {
    return source
  }
}

export type HandoffOutcome =
  | { state: 'receiving'; text: string }
  | { state: 'received'; text: string }
  | { state: 'failed'; text: string }

/** handoffOutcome is the sentence the card shows for each stage. It stays on
 *  the screen rather than passing as a toast, because a claim is spent on the
 *  first attempt and the person needs to read what happened to it. */
export function handoffOutcome(stage: 'receiving' | 'received' | 'failed', source: string, detail?: string): HandoffOutcome {
  const origin = handoffOriginLabel(source)
  switch (stage) {
    case 'receiving':
      return { state: 'receiving', text: `${origin} 에서 문서를 받는 중입니다…` }
    case 'received':
      return { state: 'received', text: `${origin} 에서 ${detail || '문서'} 를 넘겨받았습니다. 아래에서 분석 결과를 검토한 뒤 확정하세요.` }
    default:
      return { state: 'failed', text: `${origin} 에서 넘겨받지 못했습니다: ${detail || '알 수 없는 오류'} 표는 한 번만 쓸 수 있으므로 보내는 쪽에서 다시 보내 주십시오.` }
  }
}
