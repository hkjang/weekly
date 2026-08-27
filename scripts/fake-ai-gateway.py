#!/usr/bin/env python3
"""An OpenAI-compatible gateway that answers with something valid for whatever
schema it was asked for.

Three features need an AI gateway — 초안 작성, 결정 제안, PPTX 가져오기 — and
the scale seed cannot turn one on. So on a seeded deployment those screens only
ever show "AI Gateway가 비활성화되어 있습니다", and the paths behind them are
walked by unit tests and by nothing else. Every defect this repository has found
by walking a screen was found on a screen that had data behind it.

This is not a model. It reads the JSON Schema out of the request and returns a
document that satisfies it, so the product's parsing, validation, confidence
handling and preview all run against a real HTTP round trip. What comes back is
obviously fake on purpose: nobody should mistake this deployment's drafts for
something a model wrote.

    python3 scripts/fake-ai-gateway.py 18800

Then, in 관리자 설정 → AI Gateway:

    Endpoint  http://host.docker.internal:18800/v1/chat/completions
    모델      fake-model
    API Key   (비워 둡니다)

host.docker.internal works from a container started with
`--add-host=host.docker.internal:host-gateway`.
"""
import argparse
import http.server
import json
import re
import math
import hashlib
import socketserver


def sample_for(node, key="", ids=None, index=0):
    """A value that satisfies this schema node.

    Keys are read for their meaning where it helps — a date field gets a date,
    a progress field gets a number in range — so the product's own validation
    has something plausible to accept or reject rather than a string everywhere.

    `ids` are identifiers harvested from the request. Some contracts are not
    satisfiable by inventing values: the Confluence classifier must return a
    decision for **every page it was handed**, and the summariser must cite the
    pages its facts came from. Answering those with made-up ids is rejected —
    correctly — and the whole AI half of the sync stays unwalked. Echoing what
    the request carried is the least this can do and still be answerable.
    """
    kind = node.get("type")
    if isinstance(kind, list):
        kind = next((item for item in kind if item != "null"), "string")
    if kind == "object":
        return {name: sample_for(child, name, ids, index)
                for name, child in (node.get("properties") or {}).items()}
    if kind == "array":
        item = node.get("items") or {"type": "string"}
        # An array of identifiers is the request's own, not invented ones.
        if ids and key.lower().endswith("pageids"):
            return list(ids)
        # One entry per identifier where the contract is "decide on each of
        # these", so coverage checks have something to accept.
        if ids and key.lower() == "groups":
            count = len(ids)
        elif key.lower() == "facts":
            # At least one of each kind. A single page can still carry a plan
            # and an issue, and a screen that only ever receives 금주 실적
            # proves nothing about the two columns beside it.
            count = 3
        else:
            count = 2
        count = max(1, count)
        return [sample_for(item, key,
                           [ids[position]] if ids and key.lower() == "groups" else ids,
                           index * 7 + position)
                for position in range(count)]
    if kind in ("integer", "number"):
        lowered = key.lower()
        if "confidence" in lowered:
            return 0.9
        if "progress" in lowered:
            return 45
        return 1
    if kind == "boolean":
        return True
    if node.get("enum"):
        # Always the first value made every fact a CURRENT_RESULT, and a screen
        # that never receives the other two kinds proves nothing about them.
        choices = node["enum"]
        return choices[index % len(choices)]
    lowered = key.lower()
    # Varied by position. Every entry carrying the same words is the fixture
    # fault this project keeps meeting: 111 candidates with one title between
    # them cannot show whether the screen groups, sorts or de-duplicates.
    subject = SUBJECTS[index % len(SUBJECTS)]
    for marker, value in (
        ("date", "2026-08-24"), ("week", "2026-08-24"),
        ("categor", CATEGORIES[index % len(CATEGORIES)]),
        ("title", f"[가짜] {subject} {index}"),
        ("summary", f"[가짜] {subject} 진행 상황을 정리했습니다."),
        ("current", f"{subject}을(를) {1 + index % 5}개 지사에 적용했습니다."),
        ("next", f"잔여 {1 + index % 4}개 지사를 마무리합니다."),
        ("issue", "예산 집행 승인이 나지 않아 계약을 못 합니다." if index % 3 == 0 else ""),
        ("text", f"[가짜] {subject} 관련 사실 {index} 입니다."),
        ("rationale", "이 방향이 위험이 낮습니다."),
        ("reason", "근거 문장입니다."),
    ):
        if marker in lowered:
            return value
    return "가짜 응답"


# 임베딩 -------------------------------------------------------------------
#
# 의미 검색은 게이트웨이 없이는 어느 배포에서도 켤 수 없습니다. 씨를 뿌린
# 배포의 관리자 화면은 늘 "항목 110,534 · 임베딩 0" 을 보여 줍니다.
#
# 여기서 돌려주는 벡터는 모델의 것이 아니라 글자에서 결정적으로 만든
# 것입니다. 같은 글은 언제나 같은 벡터가 되고, 글자를 나눠 쓰는 두 글은
# 서로 가까워집니다. 검색 결과의 의미를 판단하는 데는 쓸 수 없지만,
# 임베딩 작업자·저장·차원 검사·유사도 질의·화면이 실제로 도는지는 이것으로
# 확인할 수 있습니다.

EMBED_DIMENSIONS = 256


def fake_vector(text: str) -> list:
    """글자에서 만든 결정적 단위 벡터. 겹치는 글자가 많을수록 가까워집니다."""
    values = [0.0] * EMBED_DIMENSIONS
    for token in re.findall(r"[0-9A-Za-z가-힣]+", text.lower()) or [text]:
        digest = hashlib.sha256(token.encode("utf-8")).digest()
        for index in range(0, EMBED_DIMENSIONS):
            values[index] += (digest[index % len(digest)] - 127.5) / 127.5
    length = math.sqrt(sum(value * value for value in values)) or 1.0
    return [round(value / length, 6) for value in values]


SUBJECTS = ["회선 이설", "계약 표준화", "교육 통합", "로그 표준화", "방화벽 자동화",
            "예산 결산", "권한 정비", "백업 이중화", "단말 교체", "인증 연동"]
CATEGORIES = ["개발", "운영", "기획", "인프라", "보안"]


def harvest_ids(body):
    """Every pageId the request carried, in the order it carried them."""
    text = json.dumps(body, ensure_ascii=False)
    seen, ids = set(), []
    for value in re.findall(r'\\?"pageId\\?"\s*:\s*\\?"([^"\\]+)', text):
        if value not in seen:
            seen.add(value)
            ids.append(value)
    return ids


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass  # The one line printed per request below is the useful log.

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        try:
            body = json.loads(self.rfile.read(length) or b"{}")
        except ValueError:
            self.send_error(400, "not json")
            return
        if self.path.rstrip("/").endswith("embeddings"):
            self.embeddings(body)
            return
        wrapper = (body.get("response_format") or {}).get("json_schema") or {}
        schema = wrapper.get("schema") or {"type": "object"}
        ids = harvest_ids(body)
        payload = json.dumps(sample_for(schema, "", ids), ensure_ascii=False)
        print(f"[ai] {self.path} schema={wrapper.get('name', '?')} ids={len(ids)} → {len(payload)}B", flush=True)
        reply = json.dumps({
            "id": "fake", "object": "chat.completion", "model": body.get("model", "fake"),
            "choices": [{"index": 0, "finish_reason": "stop",
                         "message": {"role": "assistant", "content": payload}}],
            "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
        }, ensure_ascii=False).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(reply)))
        self.end_headers()
        self.wfile.write(reply)

    def embeddings(self, body):
        inputs = body.get("input")
        if isinstance(inputs, str):
            inputs = [inputs]
        inputs = inputs or [""]
        reply = json.dumps({
            "object": "list", "model": body.get("model", "fake-embed"),
            "data": [{"object": "embedding", "index": index, "embedding": fake_vector(text)}
                     for index, text in enumerate(inputs)],
            "usage": {"prompt_tokens": len(inputs), "total_tokens": len(inputs)},
        }).encode()
        print(f"[ai] {self.path} inputs={len(inputs)} dim={EMBED_DIMENSIONS} → {len(reply)}B", flush=True)
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(reply)))
        self.end_headers()
        self.wfile.write(reply)


class Server(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True


def main():
    parser = argparse.ArgumentParser(
        description="AI Gateway 자리에 세우는 가짜 서버. 스키마에 맞는 답을 돌려줍니다.",
        epilog="예: python3 scripts/fake-ai-gateway.py 18800")
    parser.add_argument("port", nargs="?", type=int, default=18800, help="열 포트 (기본 18800)")
    parser.add_argument("--host", default="0.0.0.0", help="바인드 주소 (기본 0.0.0.0)")
    options = parser.parse_args()
    print(f"[ai] {options.host}:{options.port} 대기 — 스키마에 맞는 가짜 답을 돌려줍니다", flush=True)
    Server((options.host, options.port), Handler).serve_forever()


if __name__ == "__main__":
    main()
