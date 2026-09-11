#!/usr/bin/env python3
"""가이드에 싣는 화면 캡처를 실제로 띄운 배포에서 찍습니다.

사용자 가이드와 관리자 가이드의 그림은 전부 이 스크립트가 만든 것입니다 — 목업이
아니라, `seed-scale.sql` 을 넣은 배포에 로그인해 headless Chrome 이 1440x900 으로
찍은 화면입니다. 화면이 바뀌면 다시 돌려 그림을 갈아 끼웁니다.

    WEEKLY_GUIDE_BASE=http://127.0.0.1:8080 WEEKLY_GUIDE_PASSWORD=... \
    WEEKLY_GUIDE_ADMIN=admin WEEKLY_GUIDE_ADMIN_PASSWORD=... \
        python3 scripts/guide-captures.py

대상은 **버려도 되는 배포**여야 합니다. 씨 뿌린 계정(u1·u225)으로 들어가 상황판에
줄을 몇 개 만들고 끝나면 지우지만, 실제 배포를 가리키면 그 사이 누군가의 벽 화면에
가짜 일정이 걸립니다. 그래서 대상 주소는 다른 검사와 공유하지 않는 전용 변수로만
받고, 루프백이 아니면 `WEEKLY_GUIDE_DISPOSABLE=yes` 를 함께 요구합니다. 전역 설정은
읽기만 하고 바꾸지 않습니다. 비밀번호는 환경 변수로만 받습니다 — 여기에 적지 않습니다.
"""
import argparse
import json
import os
import subprocess
import sys
from pathlib import Path
from urllib.parse import urlparse

OUT = Path(__file__).resolve().parent.parent / "docs" / "assets" / "guide"

# 씨 뿌린 배포의 자리: u1 은 팀 2 의 팀원, u225 는 같은 팀의 팀장입니다
# (seed-scale.sql: g % 25 = 0 이 팀장, 조직은 TM(1 + g % 32)).
MEMBER = "u1"
LEADER = "u225"


def env(name, required=True):
    value = os.environ.get(name, "").strip()
    if required and not value:
        sys.exit(f"{name} 환경 변수가 필요합니다. 이 스크립트는 값을 짐작하지 않습니다.")
    return value


def parse_args():
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--playwright",
                        default="/home/hkjang/projects/Naviq/node_modules/playwright/index.mjs")
    parser.add_argument("--only", help="쉼표로 구분한 그림 이름. 비우면 전부")
    return parser.parse_args()


def script(base, playwright, out, users, only):
    return rf"""
import {{ chromium }} from {json.dumps(playwright)}
import {{ mkdirSync }} from 'node:fs'
const BASE = {json.dumps(base)}
const OUT = {json.dumps(str(out))}
const USERS = {json.dumps(users)}
const ONLY = {json.dumps(only)}
mkdirSync(OUT, {{ recursive: true }})
const browser = await chromium.launch()
const wanted = name => !ONLY.length || ONLY.includes(name)
const taken = []
const created = []   // 상황판에 만든 줄. 끝나면 지웁니다.

const settle = async page => {{
  // 스피너가 박제된 캡처는 다시 찍어야 하므로, 사라질 때까지 기다립니다.
  await page.waitForLoadState('networkidle').catch(() => {{}})
  await page.locator('.spinner, [aria-busy=true]').first().waitFor({{ state: 'hidden', timeout: 15000 }}).catch(() => {{}})
  await page.waitForTimeout(900)
}}

const shot = async (page, name) => {{
  if (!wanted(name)) return
  await settle(page)
  await page.screenshot({{ path: `${{OUT}}/${{name}}.png` }})
  taken.push(name)
}}

const open = async (page, hash) => {{
  await page.goto(`${{BASE}}/#/${{hash}}`, {{ waitUntil: 'load' }})
  await page.waitForTimeout(1500)
}}

const signIn = async who => {{
  const context = await browser.newContext({{ viewport: {{ width: 1440, height: 900 }}, locale: 'ko-KR', timezoneId: 'Asia/Seoul' }})
  const page = await context.newPage()
  await page.goto(BASE, {{ waitUntil: 'networkidle' }})
  await page.getByLabel(/아이디|사용자/).fill(USERS[who].user)
  await page.locator('input[type=password]').fill(USERS[who].password)
  await page.getByRole('button', {{ name: '로그인' }}).click()
  await page.waitForTimeout(2500)
  if (await page.locator('input[type=password]').count()) throw new Error(`${{who}} 로 로그인하지 못했습니다`)
  return page
}}

// 같은 세션의 fetch 를 쓰므로 쿠키와 Origin 이 브라우저가 보내는 그대로 갑니다.
const call = (page, method, path, body) => page.evaluate(async ([method, path, body]) => {{
  const answer = await fetch(path, {{ method, headers: body ? {{ 'Content-Type': 'application/json' }} : {{}},
    body: body ? JSON.stringify(body) : undefined }})
  const text = await answer.text()
  if (!answer.ok) throw new Error(`${{method}} ${{path}} → ${{answer.status}} ${{text.slice(0, 200)}}`)
  return text ? JSON.parse(text) : {{}}
}}, [method, path, body])

const iso = d => d.toISOString().slice(0, 10)
const today = new Date()
const day = n => {{ const d = new Date(today); d.setDate(d.getDate() + n); return iso(d) }}

// 1. 로그인 화면 — 아무도 들어가지 않은 상태.
if (wanted('login')) {{
  const context = await browser.newContext({{ viewport: {{ width: 1440, height: 900 }}, locale: 'ko-KR' }})
  const page = await context.newPage()
  await page.goto(BASE, {{ waitUntil: 'networkidle' }})
  await shot(page, 'login')
  await context.close()
}}

// 2. 팀장이 상황판에 부서의 달을 짭니다. 빈 달력을 찍지 않기 위한 줄이며 끝나면 지웁니다.
const leader = await signIn('leader')
{{
  const me = (await call(leader, 'GET', '/api/v1/me')).data.user
  const members = (await call(leader, 'GET', '/api/v1/me/report-inclusions')).data?.members ?? []
  const byName = Object.fromEntries(members.map(m => [m.username, m.id]))
  const rows = [
    ['9월 정기 배포 준비', '운영', day(-3), day(2), 'URGENT', byName[{json.dumps(MEMBER)}]],
    ['고객사 요구사항 정리', '기획', day(1), day(4), 'IMPORTANT', byName[{json.dumps(MEMBER)}]],
    ['분기 보안 점검 보고', '보안', day(6), day(8), 'NEEDED', byName[{json.dumps(MEMBER)}]],
    ['신규 입사자 온보딩', '인사', day(-1), day(-1), 'NORMAL', byName[{json.dumps(MEMBER)}]],
    ['월간 실적 취합', '경영', day(10), day(12), 'IMPORTANT', me.id],
    ['협력사 미팅', '영업', day(3), day(3), 'NORMAL', me.id],
    ['서버 패치 적용', '운영', day(-7), day(-5), 'URGENT', me.id],
  ]
  for (const [title, category, startDate, endDate, priority, assigneeId] of rows) {{
    if (!assigneeId) continue
    const made = await call(leader, 'POST', '/api/v1/schedule', {{ title, category, startDate, endDate, priority, assigneeId, note: '' }})
    if (made.data?.id) created.push(made.data.id)
  }}
  // 하나는 끝낸 것으로. 취소선과 완료율이 0 이 아닌 판을 찍기 위해서입니다.
  if (created.length >= 4) await call(leader, 'POST', `/api/v1/schedule/${{created[3]}}/done`, {{ done: true }})
}}

try {{
  // 3. 팀원 화면.
  const member = await signIn('member')
  await open(member, 'dashboard'); await shot(member, 'dashboard')
  await open(member, 'current'); await shot(member, 'current')
  await open(member, 'history'); await shot(member, 'history')
  await open(member, 'work'); await shot(member, 'work')
  await open(member, 'rollup'); await shot(member, 'rollup')
  await open(member, 'import'); await shot(member, 'import')
  await open(member, 'profile'); await shot(member, 'profile')
  await open(member, 'schedule'); await shot(member, 'schedule-month')
  if (wanted('schedule-add')) {{
    await member.getByRole('button', {{ name: '일정 추가', exact: true }}).click()
    await member.waitForTimeout(600)
    await shot(member, 'schedule-add')
    await member.keyboard.press('Escape')
    await member.waitForTimeout(400)
  }}
  for (const [label, name] of [['주', 'schedule-week'], ['목록', 'schedule-list'], ['담당자', 'schedule-assignee']]) {{
    if (!wanted(name)) continue
    const tab = member.getByRole('tab', {{ name: label, exact: true }}).first()
    if (await tab.count()) {{ await tab.click(); await shot(member, name) }}
  }}
  await member.context().close()

  // 4. 팀장 화면.
  await open(leader, 'team'); await shot(leader, 'team')
  await open(leader, 'analytics'); await shot(leader, 'analytics')
  await open(leader, 'meeting'); await shot(leader, 'meeting')
  await open(leader, 'digest'); await shot(leader, 'digest')
  await open(leader, 'insights'); await shot(leader, 'insights')
  await open(leader, 'handover'); await shot(leader, 'handover')
  await open(leader, 'profile')
  if (wanted('profile-inclusions')) {{
    const card = leader.getByText('팀원 주간보고 자료', {{ exact: false }}).first()
    if (await card.count()) await card.scrollIntoViewIfNeeded()
    await shot(leader, 'profile-inclusions')
  }}

  // 5. 관리자 화면. 탭만 옮겨 다니고 아무것도 저장하지 않습니다.
  const admin = await signIn('admin')
  await open(admin, 'admin')
  for (const [label, name] of [['분석', 'admin-analytics'], ['서비스 설정', 'admin-settings'],
                               ['Confluence 자동화', 'admin-confluence'], ['사용자', 'admin-users'],
                               ['조직', 'admin-organizations'], ['PPTX 템플릿', 'admin-pptx'], ['감사 로그', 'admin-audit']]) {{
    if (!wanted(name)) continue
    await admin.locator('.tabs').getByRole('button', {{ name: label, exact: true }}).click()
    await admin.waitForTimeout(1200)
    await shot(admin, name)
  }}
  await admin.context().close()
}} finally {{
  // 만든 줄은 지웁니다 — 누군가 이 배포를 계속 쓴다면 가짜 일정이 남아서는 안 됩니다.
  for (const id of created) await call(leader, 'DELETE', `/api/v1/schedule/${{id}}`).catch(e => console.error(String(e)))
  await browser.close()
}}
console.log(JSON.stringify({{ taken, cleaned: created.length }}))
"""


def main():
    options = parse_args()
    base = env("WEEKLY_GUIDE_BASE").rstrip("/")
    host = urlparse(base).hostname or ""
    if host not in ("127.0.0.1", "localhost", "::1") and env("WEEKLY_GUIDE_DISPOSABLE", required=False) != "yes":
        sys.exit(f"{base} 는 루프백이 아닙니다. 버려도 되는 배포라면 WEEKLY_GUIDE_DISPOSABLE=yes 를 함께 주십시오.")
    users = {
        "member": {"user": MEMBER, "password": env("WEEKLY_GUIDE_PASSWORD")},
        "leader": {"user": LEADER, "password": env("WEEKLY_GUIDE_PASSWORD")},
        "admin": {"user": env("WEEKLY_GUIDE_ADMIN"), "password": env("WEEKLY_GUIDE_ADMIN_PASSWORD")},
    }
    only = [name.strip() for name in (options.only or "").split(",") if name.strip()]
    done = subprocess.run(["node", "--input-type=module", "-e",
                           script(base, options.playwright, OUT, users, only)],
                          capture_output=True, text=True)
    if done.returncode != 0:
        print(done.stderr.strip() or done.stdout.strip())
        sys.exit(f"캡처에 실패했습니다 (exit {done.returncode})")
    lines = [line for line in done.stdout.splitlines() if line.startswith("{")]
    result = json.loads(lines[-1]) if lines else {"taken": [], "cleaned": 0}
    for name in result["taken"]:
        print(f"  {name}.png")
    print(f"{len(result['taken'])}장을 {OUT} 에 찍었고, 상황판에 만든 줄 {result['cleaned']}개를 지웠습니다.")


if __name__ == "__main__":
    main()
