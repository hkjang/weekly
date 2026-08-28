"""Every control a person can type into has to say what it is, and everything
a mouse can open has to open from the keyboard.

The screens are labelled almost everywhere — someone did that work. What is
left over is what nobody looks at twice, and it is not the unimportant part:
the weekly summary, the single field this whole product exists to collect, was
a textarea with a placeholder and no accessible name. A screen reader announces
it as "edit text, blank".

Found the way it usually is found. Writing a browser test for something else,
`getByLabel(/요약/)` matched nothing, and the test typed into the AI draft box
instead — twice. A field an automated check cannot name is a field a person
using assistive technology cannot name either.

A placeholder is not a name. It disappears when the field has content, it is not
announced consistently, and it is not what this check accepts.

The second half is the same defect one step over. Four screens put the only way
into a detail on the table row itself, with no button inside it — 과거 보고,
팀 주간보고, 업무 추적, 기간 업무보고. Clicking worked and tabbing reached
nothing, so a keyboard-only reader could open none of them. A row that draws a
pointer cursor and is not focusable is a control that half the people cannot
reach.
"""
import argparse
import json
import subprocess
import sys

PAGES = ["dashboard", "current", "history", "work", "rollup", "meeting", "digest",
         "insights", "handover", "import", "team", "analytics", "profile", "admin"]

def parse_args():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", default="http://127.0.0.1:8080")
    parser.add_argument("--playwright", default="/home/hkjang/projects/Naviq/node_modules/playwright/index.mjs")
    parser.add_argument("--password", required=True)
    parser.add_argument("who", nargs="+", help="usernames to sweep; roles differ in what they can open")
    return parser.parse_args()


def build(base, playwright, password, user):
    return f"""
import {{ chromium }} from {json.dumps(playwright)}
const BASE = {json.dumps(base)}
const PAGES = {json.dumps(PAGES)}
const browser = await chromium.launch()
const page = await (await browser.newContext({{ viewport: {{ width: 1440, height: 900 }} }})).newPage()
await page.goto(BASE, {{ waitUntil: 'networkidle' }})
await page.getByLabel(/아이디|사용자/).fill({json.dumps(user)})
await page.locator('input[type=password]').fill({json.dumps(password)})
await page.getByRole('button', {{ name: '로그인' }}).click()
await page.waitForTimeout(2500)
const report = {{ total: 0, missing: [] }}
for (const name of PAGES) {{
  await page.goto(`${{BASE}}/#/${{name}}`, {{ waitUntil: 'load' }})
  await page.waitForTimeout(1800)
  const found = await page.evaluate(() => {{
    const named = el => Boolean(
      el.getAttribute('aria-label') ||
      el.getAttribute('aria-labelledby') ||
      el.closest('label') ||
      (el.id && document.querySelector(`label[for="${{CSS.escape(el.id)}}"]`)))
    const out = {{ total: 0, missing: [] }}
    for (const el of document.querySelectorAll('input:not([type=hidden]), textarea, select')) {{
      out.total++
      if (!named(el)) out.missing.push('이름 없음 · ' + el.tagName.toLowerCase() + ' · ' +
        (el.getAttribute('placeholder') || el.type || '').slice(0, 46))
    }}
    // A row a mouse can open has to be reachable by Tab. Only the row itself is
    // judged: its cells inherit the pointer cursor and are not the control.
    const reachable = el =>
      el.hasAttribute('tabindex') ? el.getAttribute('tabindex') !== '-1'
        : Boolean(el.querySelector('button, a, [role=button], input, select, textarea'))
    for (const row of document.querySelectorAll('tbody tr')) {{
      if (getComputedStyle(row).cursor !== 'pointer') continue
      out.total++
      if (!reachable(row)) out.missing.push('키보드로 닿지 않음 · 표 행 · ' +
        (row.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 40))
    }}
    return out
  }})
  report.total += found.total
  for (const item of found.missing) report.missing.push(name + ': ' + item)
}}
console.log(JSON.stringify(report))
await browser.close()
"""


def main():
    args = parse_args()
    total, missing = 0, []
    for user in args.who:
        done = subprocess.run(["node", "--input-type=module", "-e",
                               build(args.base, args.playwright, args.password, user)],
                              capture_output=True, text=True)
        line = done.stdout.strip().split("\n")[-1] if done.stdout.strip() else ""
        if not line.startswith("{"):
            raise SystemExit(f"{user}: 훑기가 실패했습니다\n{done.stderr.strip()[:400]}")
        found = json.loads(line)
        total += found["total"]
        missing += [f"{user} · {item}" for item in found["missing"]]

    print("접근성 검사")
    for item in missing:
        print(f"  {item}")
    if missing:
        print(f"접근성 검사: 조작할 수 있는 자리 {total}개 중 {len(missing)}개가 이름이 없거나 키보드로 닿지 않습니다")
        sys.exit(1)
    print(f"접근성 검사: 통과 — 조작할 수 있는 자리 {total}개가 모두 이름을 말하고 키보드로 닿습니다.")


if __name__ == "__main__":
    main()
