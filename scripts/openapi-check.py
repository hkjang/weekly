#!/usr/bin/env python3
"""Every route the service answers has to be in the contract, and the reverse.

README points integrators at docs/openapi.yaml for the specification. A route
that is missing from it is a feature nobody outside this repository can find:
revoking an API key, logging out and commenting on a report were all reachable
and all absent. A path in the document with no route behind it is worse — it
promises something that answers 404.

Two directions, both reported. Browser redirect endpoints are exempt because
they are not an API surface anyone codes against, and each exemption has to say
why: a list nobody has to justify becomes the place drift goes to hide.

Run: python3 scripts/openapi-check.py
"""
import pathlib
import re
import sys

import yaml

ROOT = pathlib.Path(__file__).resolve().parent.parent
ROUTE = re.compile(r'"(GET|POST|PUT|PATCH|DELETE) (/api/v1/[^"]*)"')
METHODS = ("get", "post", "put", "patch", "delete")

# Reachable, deliberately undocumented, with the reason attached.
EXEMPT = {
    "GET /api/v1/auth/oidc/start": "브라우저 리디렉션 진입점이며 API 로 호출하지 않는다",
    "GET /api/v1/auth/oidc/callback": "식별 공급자가 브라우저를 돌려보내는 자리다",
}


def routes_in_code():
    source = (ROOT / "internal" / "app" / "app.go").read_text(encoding="utf-8")
    return {f"{method} {path}" for method, path in ROUTE.findall(source)}


def routes_in_document():
    document = yaml.safe_load((ROOT / "docs" / "openapi.yaml").read_text(encoding="utf-8"))
    found = set()
    for path, operations in (document.get("paths") or {}).items():
        for method in operations or {}:
            if method.lower() in METHODS:
                found.add(f"{method.upper()} /api/v1{path}")
    return found


def main():
    code, document = routes_in_code(), routes_in_document()
    undocumented = sorted(code - document - set(EXEMPT))
    unreachable = sorted(document - code)

    stale = sorted(name for name in EXEMPT if name not in code)
    if stale:
        print("더 이상 없는 경로가 예외 목록에 남아 있습니다:")
        for name in stale:
            print(f"  {name}")

    if not undocumented and not unreachable and not stale:
        print(f"OpenAPI 검사: 통과 — 경로 {len(code)}개가 문서와 일치합니다"
              + (f" (예외 {len(EXEMPT)}개)" if EXEMPT else "") + ".")
        return 0

    if undocumented:
        print(f"문서에 없는 경로 {len(undocumented)}개 — 저장소 밖에서는 찾을 수 없는 기능입니다:")
        for name in undocumented:
            print(f"  {name}")
    if unreachable:
        print(f"\n코드에 없는 문서 경로 {len(unreachable)}개 — 404 를 약속하고 있습니다:")
        for name in unreachable:
            print(f"  {name}")
    return 1


if __name__ == "__main__":
    sys.exit(main())
