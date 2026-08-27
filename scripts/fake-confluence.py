#!/usr/bin/env python3
"""Confluence Server 6.9.1 자리에 서는 가짜 서버.

Weekly 의 Confluence 연동은 **2,516줄, 라우트 열 개, 화면 여러 곳**인데,
사내 Confluence 없이는 어느 배포에서도 켤 수 없습니다. 씨앗은 그것을 만들 수
없고, 그래서 씨를 뿌린 배포에서 동기화·후보·작성자 매핑 화면은 늘 비어
있으며 그 뒤의 경로는 단위 시험 말고는 아무도 걷지 않습니다.

이것은 Confluence 가 아닙니다. 연동이 실제로 부르는 REST 두 개만,
클라이언트가 읽는 모양 그대로 답합니다.

    GET /rest/api/content/search?cql=...&start=&limit=&expand=
    GET /rest/api/content/{id}?expand=body.storage,version

돌려주는 문서는 **일부러 가짜인 티가 납니다** — 이 배포에서 만들어진 주간보고
후보를 누가 진짜 사내 문서에서 온 것으로 오해하면 안 되니까요. 확인하는 것은
제품의 동기화 루프·본문 파싱·작성자 해석·후보 생성·상태 화면이 **실제 HTTP
왕복 위에서** 도는지입니다.

    python3 scripts/fake-confluence.py 18900 --pages 120

그리고 `관리자 설정 → Confluence` 에 `http://host.docker.internal:18900` 을
넣습니다. 인증 방식은 아무 것이나 됩니다 — 이 서버는 자격을 보지 않고,
받은 것을 로그에 적어 연동이 무엇을 보냈는지 보여 줍니다. 컨테이너는
`--add-host=host.docker.internal:host-gateway` 로 띄웁니다.
"""

import argparse
import datetime
import hashlib
import http.server
import json
import urllib.parse

SPACES = ["INFRA", "PLAT", "BIZ", "EDU", "SEC"]
SUBJECTS = [
    "회선 이설", "계약 표준화", "교육 통합", "로그 표준화", "방화벽 자동화",
    "예산 결산", "권한 정비", "백업 이중화", "단말 교체", "인증 연동",
]
# 사람은 씨앗의 계정 이름을 그대로 씁니다. 매핑되지 않은 작성자도 섞습니다 —
# 연동에는 '누구인지 모르겠다' 를 세는 자리가 있고, 그 자리가 늘 0 이면
# 화면이 맞는지 알 수 없습니다.
KNOWN = [f"u{index}" for index in range(1, 40)]
STRANGERS = ["contractor.kim", "vendor-lee", "svc_build", "퇴사자01"]


def page_people(index):
    """작성자와 최종 수정자. 다섯에 하나는 매핑되지 않은 사람입니다."""
    author = KNOWN[index % len(KNOWN)] if index % 5 else STRANGERS[index % len(STRANGERS)]
    editor = KNOWN[(index * 7 + 3) % len(KNOWN)]
    return author, editor


def page(index, base, total):
    subject = SUBJECTS[index % len(SUBJECTS)]
    author, editor = page_people(index)
    # 시각은 인자로 받은 기준에서 결정적으로 만듭니다. 같은 인덱스는 언제나
    # 같은 문서가 되어야 두 번 돌린 동기화를 견줄 수 있습니다.
    #
    # 마지막 문서는 **오늘** 고쳐진 것이어야 합니다. 처음에는 가장 최근
    # 문서가 27일 전이었고, 그래서 후보가 지난 주차들에만 생겨 사람이 실제로
    # 보는 `이번 주 후보` 화면은 여전히 비어 있었습니다. 씨앗이 만들 수 없는
    # 상태에는 결함이 숨습니다 — 화면을 걷게 하려고 만든 도구가 그 화면을
    # 못 켜면 아무 소용이 없습니다.
    days_ago = (total - 1 - index) * 120 // max(1, total)
    created = base - datetime.timedelta(days=days_ago + 60, hours=index % 24)
    updated = base - datetime.timedelta(days=days_ago, hours=(index * 5) % 24)
    return {
        "id": str(100000 + index),
        "type": "blogpost" if index % 11 == 0 else "page",
        "status": "current",
        "title": f"[가짜] {subject} 진행 기록 {index}",
        "space": {"key": SPACES[index % len(SPACES)]},
        "history": {
            "createdDate": created.strftime("%Y-%m-%dT%H:%M:%S.000+0900"),
            "createdBy": {"username": author, "displayName": f"{author} (가짜)"},
        },
        "version": {
            "number": 1 + index % 9,
            "when": updated.strftime("%Y-%m-%dT%H:%M:%S.000+0900"),
            "by": {"username": editor, "displayName": f"{editor} (가짜)"},
        },
        "_links": {"webui": f"/display/{SPACES[index % len(SPACES)]}/{100000 + index}"},
    }


def storage(index):
    """Confluence storage format. 연동이 실제로 만나는 마크업을 씁니다."""
    subject = SUBJECTS[index % len(SUBJECTS)]
    digest = hashlib.sha256(str(index).encode()).hexdigest()[:6]
    return (
        f"<h2>이번 주 한 일</h2><p>{subject} 작업을 {1 + index % 5}개 지사에 적용했습니다."
        f" 이 문서는 <strong>가짜 Confluence 서버</strong>가 만든 것입니다 ({digest}).</p>"
        f"<ul><li>{subject} 설계 검토 완료</li><li>협력사 일정 회신 대기</li></ul>"
        f"<h2>다음 주 계획</h2><p>잔여 {1 + (index * 3) % 4}개 지사를 마무리합니다.</p>"
        f"<h2>이슈</h2><p>{'예산 집행 승인이 나지 않아 계약을 못 합니다.' if index % 4 == 0 else '없음'}</p>"
        f"<ac:structured-macro ac:name=\"info\"><ac:rich-text-body>"
        f"<p>매크로 안의 글도 본문입니다.</p></ac:rich-text-body></ac:structured-macro>"
    )


class Handler(http.server.BaseHTTPRequestHandler):
    pages = 120
    base_time = datetime.datetime.now()

    def log_message(self, *args):
        pass  # 아래에서 요청마다 한 줄씩 찍습니다.

    def send_json(self, payload):
        body = json.dumps(payload, ensure_ascii=False).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json;charset=UTF-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        parsed = urllib.parse.urlparse(self.path)
        query = urllib.parse.parse_qs(parsed.query)
        auth = "있음" if self.headers.get("Authorization") else "없음"

        if parsed.path == "/rest/api/content/search":
            start = int((query.get("start") or ["0"])[0])
            limit = max(1, min(200, int((query.get("limit") or ["50"])[0])))
            window = [page(index, self.base_time, self.pages)
                      for index in range(start, min(start + limit, self.pages))]
            print(f"[cf] search start={start} limit={limit} → {len(window)}/{self.pages} · 인증 {auth}"
                  f" · cql={(query.get('cql') or [''])[0][:60]}", flush=True)
            # Confluence Server 는 size 에 **이 페이지의 건수**를, totalSize 에
            # 전체를 담습니다. 총계를 size 로 보내면 마지막 페이지 뒤로도 끝이
            # 났다는 신호가 서지 않습니다.
            self.send_json({
                "results": window, "start": start, "limit": limit,
                "size": len(window), "totalSize": self.pages,
                "_links": {"base": f"http://{self.headers.get('Host', 'localhost')}"},
            })
            return

        if parsed.path.startswith("/rest/api/content/"):
            page_id = parsed.path.rsplit("/", 1)[-1]
            try:
                index = int(page_id) - 100000
            except ValueError:
                index = -1
            if not 0 <= index < self.pages:
                print(f"[cf] body {page_id} → 404", flush=True)
                self.send_error(404, "no such page")
                return
            print(f"[cf] body {page_id} → storage {len(storage(index))}B", flush=True)
            self.send_json({
                "id": page_id,
                "version": {"number": 1 + index % 9},
                "body": {"storage": {"value": storage(index)}},
            })
            return

        print(f"[cf] {parsed.path} → 404", flush=True)
        self.send_error(404, "not a Confluence REST path this fake serves")


def main():
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("port", type=int, nargs="?", default=18900)
    parser.add_argument("--pages", type=int, default=120, help="돌려줄 문서 수 (기본 120)")
    options = parser.parse_args()
    Handler.pages = max(1, options.pages)
    print(f"[cf] 0.0.0.0:{options.port} 대기 — 문서 {Handler.pages}개, 일부러 가짜인 티가 납니다", flush=True)
    http.server.ThreadingHTTPServer(("0.0.0.0", options.port), Handler).serve_forever()


if __name__ == "__main__":
    main()
