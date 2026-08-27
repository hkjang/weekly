#!/usr/bin/env python3
"""화면 캡처 첨부를 실제로 올려, 그 기능이 있는 배포를 만듭니다.

`seed-scale.sql` 은 첨부를 만들 수 없습니다. 첨부는 절반이 PostgreSQL 의
행이고 절반이 상태 볼륨의 **파일**이라, 행만 넣으면 기동 로그에
`attachment files are missing` 이 뜨고 캡처 패널이 전부 `파일 없음` 이 됩니다.
그래서 씨를 뿌린 배포의 첨부는 **언제나 0건**이고, 캡처 패널·PPTX 캡처
슬라이드·`본문 앞/뒤` 배치·유실 파일 안내는 아무도 걷지 않습니다.

이것은 제품의 업로드 경로를 그대로 씁니다 — 로그인하고,
`POST /api/v1/reports/{id}/attachments` 로 멀티파트를 보냅니다. 그래서
크기 상한, 개수 상한, 형식 검사, 저장 경로, 중복 해시 처리가 모두 실제로
돕니다. 파일을 볼륨에 직접 놓지 않는 이유가 그것입니다.

    python3 scripts/seed-captures.py --base http://127.0.0.1:19271 \
        --users u1,u2,u3 --password WeeklyVerify1234

그림은 일부러 가짜인 티가 납니다 — 이 배포의 PPTX 를 누가 진짜 캡처가 담긴
것으로 오해하면 안 되니까요. 표에서 만든 색 띠 위에 사용자와 번호를 적은
PNG 이며, 순수 파이썬으로 그립니다(zlib 만 씁니다).
"""

import argparse
import http.cookiejar
import json
import struct
import urllib.request
import zlib

# 색은 사용자마다 다릅니다. 스무 장이 전부 같은 그림이면 화면이 순서를
# 지키는지 배치를 지키는지 알 수 없습니다.
BANDS = [(214, 69, 65), (52, 120, 198), (46, 148, 96), (198, 140, 40), (120, 84, 176)]


def png(width, height, band):
    """단색 띠 위에 굵은 눈금을 넣은 PNG. 순수 파이썬으로 만듭니다."""
    red, green, blue = band
    rows = bytearray()
    for y in range(height):
        rows.append(0)  # filter type
        for x in range(width):
            # 눈금이 있어야 슬라이드에서 그림이 늘어났는지 잘렸는지 보입니다.
            grid = (x % 40 < 2) or (y % 40 < 2)
            shade = 255 if grid else 0
            rows += bytes((max(red, shade) if grid else red,
                           max(green, shade) if grid else green,
                           max(blue, shade) if grid else blue))

    def chunk(tag, payload):
        body = tag + payload
        return struct.pack(">I", len(payload)) + body + struct.pack(">I", zlib.crc32(body))

    header = struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0)
    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", header)
            + chunk(b"IDAT", zlib.compress(bytes(rows), 6)) + chunk(b"IEND", b""))


def multipart(fields, files):
    boundary = "----weeklycaptureseed"
    body = bytearray()
    for name, value in fields.items():
        body += f"--{boundary}\r\nContent-Disposition: form-data; name=\"{name}\"\r\n\r\n{value}\r\n".encode()
    for name, filename, payload in files:
        body += (f"--{boundary}\r\nContent-Disposition: form-data; name=\"{name}\"; "
                 f"filename=\"{filename}\"\r\nContent-Type: image/png\r\n\r\n").encode()
        body += payload + b"\r\n"
    body += f"--{boundary}--\r\n".encode()
    return f"multipart/form-data; boundary={boundary}", bytes(body)


class Session:
    def __init__(self, base):
        self.base = base.rstrip("/")
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def call(self, method, path, body=None, content_type="application/json"):
        request = urllib.request.Request(self.base + path, data=body, method=method)
        request.add_header("Origin", self.base)
        if body is not None:
            request.add_header("Content-Type", content_type)
        with self.opener.open(request, timeout=60) as answer:
            return json.loads(answer.read() or b"{}")

    def login(self, username, password):
        self.call("POST", "/api/v1/auth/login",
                  json.dumps({"username": username, "password": password}).encode())


def main():
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base", default="http://127.0.0.1:8080")
    parser.add_argument("--users", default="u1,u2,u3", help="쉼표로 구분한 로그인 아이디")
    parser.add_argument("--password", default="WeeklyVerify1234")
    parser.add_argument("--per-report", type=int, default=3, help="보고서당 캡처 수")
    parser.add_argument("--size", type=int, default=320, help="한 변의 픽셀 수")
    options = parser.parse_args()

    total = 0
    for index, username in enumerate(u.strip() for u in options.users.split(",") if u.strip()):
        session = Session(options.base)
        try:
            session.login(username, options.password)
            current = session.call("GET", "/api/v1/reports/current").get("data") or {}
        except Exception as error:            # 한 사람이 막혀도 나머지는 계속합니다.
            print(f"  {username}: 들어가지 못했습니다 — {error}")
            continue
        report = current.get("id")
        if not report:
            print(f"  {username}: 이번 주 보고서가 없습니다")
            continue
        # 같은 그림 셋을 올리면 제품이 해시로 합쳐 **파일 하나**로 저장합니다.
        # 옳은 동작이지만, 그러면 순서·배치·PPTX 슬라이드가 도는지 알 수
        # 없습니다. 장마다 색을 달리해 서로 다른 파일이 되게 합니다.
        files = [("files", f"{username}-캡처{n + 1}.png",
                  png(options.size, options.size, BANDS[(index + n) % len(BANDS)]))
                 for n in range(options.per_report)]
        # 앞뒤 배치를 둘 다 만듭니다. 한쪽만 있으면 그 구분이 도는지 알 수 없습니다.
        placement = "BEFORE" if index % 2 == 0 else "AFTER"
        content_type, body = multipart({"placement": placement}, files)
        try:
            answer = session.call("POST", f"/api/v1/reports/{report}/attachments", body, content_type)
        except Exception as error:
            print(f"  {username}: 올리지 못했습니다 — {error}")
            continue
        data = answer.get("data")
        added = len(data if isinstance(data, list) else (data or {}).get("attachments") or [])
        total += added
        print(f"  {username}: 보고서 {report} 에 {added}장 · 배치 {placement}")
    print(f"캡처 {total}장을 올렸습니다.")


if __name__ == "__main__":
    main()
