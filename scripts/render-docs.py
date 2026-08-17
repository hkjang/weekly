#!/usr/bin/env python3
"""Render docs/<NAME>.md into the published .html and .pdf.

The three formats are all linked from the GitHub Pages landing page, so they
have to stay in step. They used to be maintained by hand and drifted apart:
the Markdown said v0.10.0 while the HTML and PDF still described v0.5.0. This
makes Markdown the single source and derives the other two.

Usage:  python3 scripts/render-docs.py ROADMAP_PLAN [NAME ...]
"""
import html
import re
import shutil
import subprocess
import sys
from pathlib import Path

DOCS = Path(__file__).resolve().parent.parent / "docs"

STYLE = """
* { box-sizing: border-box; }
body { margin: 0; padding: 32px 18px 60px; background: #f1f5f9;
  font-family: 'Pretendard', 'Noto Sans KR', -apple-system, system-ui, sans-serif;
  color: #1e293b; line-height: 1.75; }
.doc-card { max-width: 980px; margin: 0 auto; background: #fff; border-radius: 16px;
  box-shadow: 0 18px 50px rgba(15, 23, 42, .10); padding: 44px 48px 56px; }
.header-meta { border-bottom: 2px solid #e2e8f0; padding-bottom: 22px; margin-bottom: 30px; }
.header-meta h1 { margin: 0 0 16px; font-size: 1.85rem; color: #0f172a; letter-spacing: -.02em; }
.meta-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(210px, 1fr)); gap: 10px; }
.meta-item { background: #f8fafc; border: 1px solid #e2e8f0; border-radius: 9px;
  padding: 9px 12px; font-size: .82rem; color: #475569; }
.meta-item strong { color: #1d4ed8; }
h2 { margin: 38px 0 14px; font-size: 1.3rem; color: #172554;
  border-left: 4px solid #2563eb; padding-left: 12px; }
h3 { margin: 26px 0 10px; font-size: 1.05rem; color: #1e293b; }
p { margin: 12px 0; }
ul, ol { margin: 12px 0; padding-left: 24px; }
li { margin: 6px 0; }
code { background: #eef2ff; color: #4338ca; padding: 2px 6px; border-radius: 5px;
  font-family: ui-monospace, 'SFMono-Regular', Menlo, monospace; font-size: .88em; }
.phase-box { background: #0f172a; color: #a7f3d0; border-radius: 11px; padding: 18px 20px;
  overflow-x: auto; font-family: ui-monospace, Menlo, monospace; font-size: .78rem;
  line-height: 1.6; white-space: pre; margin: 18px 0; }
table { width: 100%; border-collapse: collapse; margin: 18px 0; font-size: .88rem; }
th { background: #2563eb; color: #fff; text-align: left; padding: 10px 12px; font-weight: 700; }
td { padding: 9px 12px; border-bottom: 1px solid #e2e8f0; vertical-align: top; }
tr:nth-child(even) td { background: #f8fafc; }
hr { border: 0; border-top: 1px solid #e2e8f0; margin: 34px 0; }
blockquote { margin: 16px 0; padding: 12px 16px; background: #fffbeb;
  border-left: 4px solid #f59e0b; color: #78350f; border-radius: 0 8px 8px 0; }
.print-btn { display: inline-block; margin-bottom: 20px; background: #2563eb; color: #fff;
  border: 0; border-radius: 9px; padding: 9px 16px; font-size: .85rem; font-weight: 700;
  cursor: pointer; text-decoration: none; }
@media print {
  body { background: #fff; padding: 0; }
  .doc-card { box-shadow: none; padding: 0; max-width: none; }
  .print-btn { display: none; }
  h2 { break-after: avoid; }
  .phase-box { color: #0f172a; background: #f1f5f9; }
}
"""


def inline(text: str) -> str:
    """Escape, then apply the inline markup the documents actually use."""
    text = html.escape(text, quote=False)
    text = re.sub(r"`([^`]+)`", r"<code>\1</code>", text)
    text = re.sub(r"\*\*([^*]+)\*\*", r"<strong>\1</strong>", text)
    text = re.sub(r"\[([^\]]+)\]\((https?://[^)]+)\)", r'<a href="\2">\1</a>', text)
    return text


def render_table(rows: list[str]) -> str:
    cells = [[c.strip() for c in row.strip().strip("|").split("|")] for row in rows]
    if len(cells) < 2:
        return ""
    head, body = cells[0], cells[2:]
    out = ["<table><thead><tr>"]
    out += [f"<th>{inline(c)}</th>" for c in head]
    out.append("</tr></thead><tbody>")
    for row in body:
        out.append("<tr>" + "".join(f"<td>{inline(c)}</td>" for c in row) + "</tr>")
    out.append("</tbody></table>")
    return "".join(out)


def convert(markdown: str) -> tuple[str, str, list[str]]:
    """Return (title, body html, metadata bullet lines from the front matter)."""
    lines = markdown.splitlines()
    title = ""
    meta: list[str] = []
    out: list[str] = []
    index = 0

    # Title plus the leading metadata bullet list become the document header.
    while index < len(lines):
        line = lines[index]
        if line.startswith("# ") and not title:
            title = line[2:].strip()
            index += 1
            continue
        if title and line.startswith("- **"):
            meta.append(line[2:].strip())
            index += 1
            continue
        if title and (line.strip() == "" or line.strip() == "---"):
            index += 1
            if line.strip() == "---":
                break
            continue
        break

    fence: list[str] | None = None
    table: list[str] = []
    bullets = 0

    def close_bullets():
        nonlocal bullets
        while bullets:
            out.append("</ul>")
            bullets -= 1

    def close_table():
        nonlocal table
        if table:
            out.append(render_table(table))
            table = []

    while index < len(lines):
        line = lines[index]
        index += 1
        if line.startswith("```"):
            if fence is None:
                close_bullets()
                close_table()
                fence = []
            else:
                out.append('<div class="phase-box">' + html.escape("\n".join(fence)) + "</div>")
                fence = None
            continue
        if fence is not None:
            fence.append(line)
            continue
        if line.lstrip().startswith("|"):
            close_bullets()
            table.append(line)
            continue
        close_table()
        stripped = line.strip()
        if stripped == "":
            close_bullets()
            continue
        if stripped == "---":
            close_bullets()
            out.append("<hr>")
            continue
        if stripped.startswith("### "):
            close_bullets()
            out.append(f"<h3>{inline(stripped[4:])}</h3>")
            continue
        if stripped.startswith("## "):
            close_bullets()
            out.append(f"<h2>{inline(stripped[3:])}</h2>")
            continue
        if stripped.startswith("# "):
            close_bullets()
            out.append(f"<h2>{inline(stripped[2:])}</h2>")
            continue
        if stripped.startswith("> "):
            close_bullets()
            out.append(f"<blockquote>{inline(stripped[2:])}</blockquote>")
            continue
        marker = re.match(r"^(\s*)(?:[-*]|\d+\.)\s+(.*)$", line)
        if marker:
            depth = 1 + len(marker.group(1)) // 2
            while bullets < depth:
                out.append("<ul>")
                bullets += 1
            while bullets > depth:
                out.append("</ul>")
                bullets -= 1
            out.append(f"<li>{inline(marker.group(2))}</li>")
            continue
        close_bullets()
        out.append(f"<p>{inline(stripped)}</p>")

    close_bullets()
    close_table()
    return title, "\n".join(out), meta


def build(name: str) -> None:
    source = DOCS / f"{name}.md"
    if not source.exists():
        raise SystemExit(f"missing {source}")
    title, body, meta = convert(source.read_text(encoding="utf-8"))
    meta_html = "".join(f'<div class="meta-item">{inline(item)}</div>' for item in meta)
    page = f"""<!DOCTYPE html>
<html lang="ko">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>{html.escape(title)}</title>
<style>{STYLE}</style>
</head>
<body>
<div class="doc-card">
<a class="print-btn" href="{name}.pdf">PDF 내려받기</a>
<div class="header-meta"><h1>{html.escape(title)}</h1>
<div class="meta-grid">{meta_html}</div></div>
{body}
</div>
</body>
</html>
"""
    target = DOCS / f"{name}.html"
    target.write_text(page, encoding="utf-8")
    print(f"wrote {target.relative_to(DOCS.parent)}")

    chrome = next((path for path in (
        shutil.which("google-chrome"), shutil.which("chromium"),
        shutil.which("chromium-browser")) if path), None)
    if not chrome:
        print("  chrome not found: skipped PDF, run again where a browser is available")
        return
    pdf = DOCS / f"{name}.pdf"
    subprocess.run([chrome, "--headless", "--disable-gpu", "--no-sandbox",
                    "--no-pdf-header-footer", f"--print-to-pdf={pdf}",
                    target.as_uri()], check=True,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    print(f"wrote {pdf.relative_to(DOCS.parent)}")


if __name__ == "__main__":
    names = sys.argv[1:] or ["ROADMAP_PLAN"]
    for item in names:
        build(item)
