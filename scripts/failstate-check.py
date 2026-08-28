#!/usr/bin/env python3
"""빈 목록은 주장입니다 — 실패한 화면이 "없습니다" 라고 말하는지 봅니다.

첨부 패널의 주석이 이미 규칙을 적어 두었습니다: *"A failed load used to become
an empty list, and an empty list here is a claim: readers were told the report
has no captures, which is a different thing from not knowing."* 그 규칙이 그
파일에만 있었습니다.

각 화면의 주 요청을 500으로 만들고, 알림이 사라진 뒤 남는 화면을 읽었습니다.
다섯 곳이 실패를 "없음"으로 바꿔 말하고 있었습니다.

  업무 추적    "아직 추적할 업무가 없습니다"
  인수인계     "선택한 담당자에게는 아직 인수인계할 업무 기록이 없습니다"
  경영 요약    "핵심 0건 · 업무 0건" · "선정 기준을 넘는 항목이 없습니다"
  회의 모드    "0명 · 0건의 안건" · "이번 주에는 회의에서 다룰 변화가 없습니다"
  개인 설정    "발급된 API 키가 없습니다"

뒤의 둘이 특히 나쁩니다. 숫자를 지어냅니다 — 실패에서 만들어진 0이 경영 요약과
회의 안건의 규모로 읽힙니다.

이 검사가 묻는 것은 하나입니다: **요청이 실패했는데 화면이 비어 있다고 말하는가.**
"화면이 오류를 말하는가"는 묻지 않습니다. 어떤 화면은 알림으로만 말하고 그것도
설계일 수 있지만, 없는 것을 없다고 말하는 것은 어떤 설계로도 옳지 않습니다.

만들면서 검사 자신이 두 번 틀렸습니다. 처음에는 "불러오지 못했습니다"를 찾는
정규식에 그 표현이 빠져 있어 대시보드를 거짓 양성으로 잡았고, 다음에는 화면이
서버가 준 문구를 그대로 보여 준다는 것을 몰라 고쳐진 화면을 못 봤습니다. 그래서
지금은 **직접 주입한 표시 문구**를 찾습니다.

Run: python3 scripts/failstate-check.py --password ... [--user hq1]
"""
import argparse
import json
import re
import subprocess
import sys

# 실패한 뒤에도 참일 수 있는 빈 상태 — 그 요청이 실패한 것이 아닌 경우.
# 비어 있습니다. 처음에는 개인 설정의 키 목록을 여기 적어 두었는데, 검사가
# "그 예외는 더는 나오지 않습니다" 라고 알려 줘서 지웠습니다. 예외는 남는 것이
# 아니라 갚는 것입니다.
ALLOWED: set[tuple[str, str]] = set()

SCREENS = [
    ("dashboard", r"/api/v1/reports/current"),
    ("history", r"/api/v1/reports\?"),
    ("work", r"/api/v1/work-items\?"),
    ("team", r"/api/v1/team/reports"),
    ("rollup", r"/api/v1/rollups\?"),
    ("handover", r"/api/v1/handover"),
    ("digest", r"/api/v1/digest"),
    ("insights", r"/api/v1/insights/work-graph"),
    ("meeting", r"/api/v1/meeting"),
    ("changes", r"/api/v1/changes"),
    ("analytics", r"/api/v1/analytics/overview"),
    ("profile", r"/api/v1/keys"),
]

MARKER = "주입한실패표시"


def script(base, playwright, user, password):
    return f"""
import {{ chromium }} from {json.dumps(playwright)}
const BASE = {json.dumps(base)}
const SCREENS = {json.dumps(SCREENS)}
const browser = await chromium.launch()
const page = await (await browser.newContext({{viewport:{{width:1400,height:1000}}}})).newPage()
await page.goto(BASE, {{waitUntil:'networkidle'}})
await page.getByLabel(/아이디|사용자/).fill({json.dumps(user)})
await page.locator('input[type=password]').fill({json.dumps(password)})
await page.getByRole('button', {{name:'로그인'}}).click()
await page.waitForTimeout(2500)
const out = []
for (const [route, pattern] of SCREENS) {{
  await page.unrouteAll()
  await page.route(new RegExp(pattern), r => r.fulfill({{status:500, contentType:'application/json',
    body: JSON.stringify({{success:false,data:null,error:{{code:'QUERY_FAILED',message:{json.dumps(MARKER)}}},traceId:'x'}})}}))
  await page.evaluate(() => {{ location.hash = '#/dashboard' }})
  await page.waitForTimeout(600)
  await page.evaluate(x => {{ location.hash = '#/' + x }}, route)
  await page.waitForTimeout(2400)
  // 알림은 사라집니다. 남는 화면이 무엇이라고 말하는지가 이 검사의 질문입니다.
  await page.evaluate(() => document.querySelectorAll('.toast, [role=alert]').forEach(n => n.remove()))
  const text = (await page.locator('body').innerText()).replace(/[ \\t\\n]+/g, ' ')
  out.push({{ route, said: text.includes({json.dumps(MARKER)}),
              claims: (text.match(/[^.]{{0,44}}(?:아직[^.]{{0,34}})?없습니다/g) || []) }})
}}
await browser.close()
console.log(JSON.stringify(out))
"""


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", default="http://localhost:19311")
    parser.add_argument("--password", required=True)
    parser.add_argument("--user", default="hq1")
    parser.add_argument("--playwright", default="/home/hkjang/projects/Naviq/node_modules/playwright/index.mjs")
    args = parser.parse_args()

    done = subprocess.run(["node", "--input-type=module", "-e",
                           script(args.base, args.playwright, args.user, args.password)],
                          capture_output=True, text=True)
    if done.returncode != 0:
        print(done.stdout[-2000:], file=sys.stderr)
        print(done.stderr[-2000:], file=sys.stderr)
        return 2
    results = json.loads(done.stdout.strip().split("\n")[-1])

    lying, unused = [], set(ALLOWED)
    for entry in results:
        route = entry["route"]
        claims = [re.sub(r"\s+", " ", c).strip() for c in entry["claims"]]
        excused = []
        for claim in list(claims):
            for allowed_route, allowed_text in ALLOWED:
                if allowed_route == route and allowed_text in claim:
                    excused.append(claim)
                    unused.discard((allowed_route, allowed_text))
        remaining = [c for c in claims if c not in excused]
        mark = "화면이 말함" if entry["said"] else "알림만"
        print(f"  {route:11} {mark:9} · 실패 뒤 '없습니다' {len(remaining)}건"
              + (f" (사유 기록 {len(excused)}건)" if excused else ""))
        for claim in remaining:
            print(f"      {claim}")
            lying.append((route, claim))

    print()
    for route, text in sorted(unused):
        print(f"{route} 의 허용 항목 '{text}' 이 더는 나오지 않습니다. ALLOWED 에서 지우세요.")
    for route, claim in lying:
        print(f"{route}: 요청이 실패했는데 화면은 '{claim}' 이라고 말합니다.")
        print("  없다는 것과 모른다는 것은 다릅니다. 실패를 실패로 그리거나, ALLOWED 에 이유를 적으세요.")
    if lying or unused:
        return 1
    print(f"실패 상태 검사: 통과 — 화면 {len(results)}곳 중 실패를 '없음'으로 바꿔 말하는 곳이 없습니다.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
