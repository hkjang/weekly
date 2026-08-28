#!/usr/bin/env python3
"""쪽 넘김 검사 — 잘린 목록에서 나머지로 가는 길이 있는지 봅니다.

서버의 목록 응답은 `items` 와 함께 `total` 을 돌려줍니다. 화면이 100건을
받고 total 이 305 라면, 205건은 화면 어딘가에서 도달할 수 있어야 합니다.

관리자 사용자 목록은 그렇지 않았습니다. 카드 제목이 "사용자 305명 중
100명" 이라고 정직하게 말했지만, 그 뒤로 가는 단추가 없었습니다. 검색이
있으니 괜찮다는 판단이 코드에 적혀 있었고, 그것은 **한 계정을 찾는 일**만
덮습니다. 누가 검토 책임자가 없는지, 누가 어떤 역할인지 훑어 내려가는
일은 덮지 못합니다. 305명 중 205명 — 팀장 8명, 조직장 3명, 관리자 1명이
포함됩니다 — 이 어떤 클릭으로도 나오지 않았습니다.

그래서 이 검사는 total 을 받는 화면마다 둘 중 하나를 요구합니다.

  1. 요청에 offset 을 실어 보낸다 (이전·다음, 또는 더 보기)
  2. 아래 허용 목록에 왜 필요 없는지 적혀 있다

배운 것: "잘렸다고 말한다"는 세 번째 답이 아닙니다. 잘렸다는 사실을
말하면서 나머지로 가는 길이 없으면, 화면은 자기가 불완전하다는 것을 알면서
독자를 거기 세워 둘 뿐입니다.
"""
import re, sys, pathlib

ROOT = pathlib.Path(__file__).resolve().parent.parent
SRC = ROOT / "frontend" / "src"

# 왜 쪽 넘김이 없어도 되는지 — 화면마다 한 줄.
ALLOWED = {
    ("WorkItemsPage.tsx", "WorkItemPage"):
        "가장 먼저 봐야 할 200건을 이슈·정체·오래된 순으로 골라 싣고, 그 정렬을 화면이 "
        "말합니다. 뒤쪽은 순서상 덜 급한 것이라 쪽을 넘기는 것보다 검색이 맞습니다.",
    ("ImportPage.tsx", "ImportJobListView"):
        "Import 이력은 최근 50건이며 한 번짜리 이관 작업의 기록입니다. 오래된 작업은 "
        "다시 열 대상이 아니라 지난 일이고, 상세는 작업 번호로 직접 엽니다.",
    ("CommandPalette.tsx", "ReportListView"):
        "빠른 이동은 8건짜리 미리보기입니다. 목록이 아니라 지름길이고, 뒤는 히스토리 "
        "화면이 이어 받습니다.",
}


def paged_types(text):
    """items 와 total 을 함께 가진 형(型) 이름."""
    names = set()
    for match in re.finditer(r"(?:interface|type)\s+(\w+)\s*=?\s*\{(.*?)\}", text, re.S):
        name, body = match.group(1), match.group(2)
        if "items" in body and re.search(r"\btotal\s*:", body):
            names.add(name)
    return names


def components(text):
    """파일을 최상위 함수 단위로 자릅니다.

    처음에는 `api<T>(` 바로 뒤의 문자열만 봤고, 넷을 잘못 짚었습니다. 주소를
    그 자리에 적지 않고 조립해서 넘기는 곳들이었습니다 — ReportsPage 는
    `query(`offset=…`)`, 감사 탭은 URLSearchParams 로 만든 변수. 파일 전체를
    보면 이번에는 반대로 틀립니다: 한 파일에 사는 두 화면 중 하나만 쪽을
    넘겨도 둘 다 통과하고, AdminPage 가 정확히 그런 파일입니다.

    그래서 화면 하나가 한 덩어리입니다.
    """
    starts = [m.start() for m in re.finditer(
        r"^(?:export\s+)?(?:default\s+)?(?:function\s+\w+|const\s+\w+\s*=\s*\()", text, re.M)]
    # 첫 함수 앞의 머리도 한 덩어리입니다. 처음 쓴 판은 이것을 버렸고, 그래서
    # `export default function` 으로 시작하는 ImportPage 의 목록 호출이
    # 통째로 사라진 채 "통과" 가 나왔습니다. 검사에서 사라지는 것은 통과가
    # 아닙니다.
    bounds = [0] + starts + [len(text)]
    return [text[a:b] for a, b in zip(bounds, bounds[1:]) if text[a:b].strip()]


def main():
    types_text = (SRC / "types.ts").read_text(encoding="utf-8")
    known = paged_types(types_text)

    sites, missing, unused = [], [], []
    for path in sorted(SRC.rglob("*.tsx")):
        text = path.read_text(encoding="utf-8")
        local = known | paged_types(text)
        for block in components(text):
            for name in sorted(local):
                if not re.search(rf"api<{name}>\s*\(", block):
                    continue
                # offsetHeight 같은 이웃 낱말은 세지 않습니다.
                pages = re.search(r"\boffset(?![A-Za-z])", block) is not None
                key = (path.name, name)
                sites.append((path.name, name, pages))
                if pages and key in ALLOWED:
                    unused.append(key)
                elif not pages and key not in ALLOWED:
                    missing.append(key)

    for filename, name, pages in sites:
        mark = "쪽 넘김" if pages else ("사유 기록" if (filename, name) in ALLOWED else "없음")
        print(f"  {mark:9} {filename}  {name}")
        if not pages and (filename, name) in ALLOWED:
            print(f"      {ALLOWED[(filename, name)]}")
    print()

    for filename, name in unused:
        print(f"{filename} 의 {name} 은 이제 offset 을 보냅니다. 허용 목록에서 지우세요.")
    for filename, name in missing:
        print(f"{filename} 은 {name} 의 total 을 받으면서 offset 을 보내지 않습니다.")
        print("  잘린 뒤쪽으로 가는 길이 없습니다. 쪽 넘김을 붙이거나, ALLOWED 에 이유를 적으세요.")
    if missing or unused:
        return 1

    reasoned = sum(1 for _, _, pages in sites if not pages)
    print(f"쪽 넘김 검사: 통과 — 목록 {len(sites)}곳 중 {len(sites) - reasoned}곳은 쪽을 넘기고 "
          f"{reasoned}곳은 넘기지 않는 이유가 적혀 있습니다.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
