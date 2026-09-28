/**
 * 로그인 화면이 무엇을 먼저 보여 주는가.
 *
 * 조직 계정(SSO)이 켜진 배포에서 사람들이 쓰는 길은 거의 언제나 그것 하나입니다.
 * 그런데 화면은 아이디·비밀번호 칸을 맨 위에 파란 버튼과 함께 두고 SSO 를 "또는"
 * 아래 흰 버튼으로 밀어 두었습니다 — 가장 자주 쓰는 길이 가장 덜 눈에 띄었습니다.
 * appstore 의 로그인처럼 조직 계정을 주 버튼으로 올리고, 아이디·비밀번호는 접힌
 * 칸 뒤에 둡니다. 없어지지는 않습니다: SSO 가 없는 계정(관리자가 만든 로컬 계정)과
 * Keycloak 이 멈췄을 때의 복구 경로이기 때문입니다.
 */
export type LocalLogin = 'none' | 'form' | 'collapsed'

export function loginSections(providers: { local: boolean; oidc: boolean }): { sso: boolean; local: LocalLogin } {
  if (!providers.local) return { sso: providers.oidc, local: 'none' }
  // SSO 가 없으면 아이디·비밀번호가 유일한 길이므로 접지 않습니다.
  return { sso: providers.oidc, local: providers.oidc ? 'collapsed' : 'form' }
}

export interface LoginNotice {
  text: string
  /** alert 는 무언가 잘못된 것(만료·확인 실패), info 는 정상적인 결과를 설명하는 것. */
  tone: 'alert' | 'info'
}

/**
 * 로그인 화면 위에 한 줄로 말해 줄 것.
 *
 * 자동 로그인(prompt=none)이 Keycloak 에 세션이 없다는 답을 받아 돌아오면 화면은
 * 아무 말 없이 로그인 칸만 보여 줬습니다. 그 사람은 자동으로 들어가질 줄 알았고,
 * 왜 아니었는지 알 방법이 없었습니다. 세션이 없는 것은 오류가 아니므로 경고가
 * 아니라 설명으로 말합니다(appstore 의 `sso=none` 안내와 같은 문장).
 */
export function loginNotice(input: { signedOut: boolean; oidcAuto: string | null }): LoginNotice | undefined {
  if (input.signedOut) return { text: '세션이 만료되어 로그아웃됐습니다. 다시 로그인해 주세요.', tone: 'alert' }
  if (input.oidcAuto === 'unavailable') {
    return { text: 'Keycloak 자동 로그인을 확인하지 못했습니다. 아래 방식으로 로그인해 주세요.', tone: 'alert' }
  }
  if (input.oidcAuto === 'unreachable') {
    return { text: '이 브라우저에서 Keycloak 에 연결할 수 없어 자동 로그인을 건너뛰었습니다. 사내망(VPN) 연결을 확인하거나 아래 방식으로 로그인하세요.', tone: 'alert' }
  }
  if (input.oidcAuto === 'miss') {
    return { text: '조직 계정 세션이 없어 자동으로 로그인하지 않았습니다. 아래 버튼으로 로그인하세요.', tone: 'info' }
  }
  return undefined
}
