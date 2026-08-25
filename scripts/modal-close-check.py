#!/usr/bin/env python3
"""Every way out of a guarded dialog must ask the same question.

`Modal` runs `beforeClose` for the Escape key and the backdrop, but a dialog's
own × button is written by the call site and calls whatever it likes. Both
dialogs that guard unsaved edits got this wrong the same way: Escape asked, the
× — the button people actually reach for — threw the work away without a word.

So this checks two things in any file that passes `beforeClose`:

  1. the guard is a named function, not an inline closure. An inline closure
     cannot be reused, which is precisely why the × grew its own behaviour.
  2. every × close button in that file mentions the guard by name.

Run: python3 scripts/modal-close-check.py
"""
import pathlib
import re
import sys

SRC = pathlib.Path(__file__).resolve().parent.parent / 'frontend' / 'src'
BEFORE_CLOSE = re.compile(r'beforeClose=\{([^}]*)\}')
BARE_NAME = re.compile(r'^[A-Za-z_$][\w$]*$')
# The close affordance: a button whose only content is the × glyph. Its
# attributes cannot be matched forward — an arrow function's `=>` looks like the
# end of the tag — so find the glyph and walk back to the tag that opens it.
CLOSE_GLYPH = re.compile(r'>\s*×\s*</button>')


def close_button_attrs(text):
    for match in CLOSE_GLYPH.finditer(text):
        start = text.rfind('<button', 0, match.start())
        if start == -1:
            continue
        yield match.start(), text[start + len('<button'):match.start()]


def main() -> int:
    problems = []
    checked = 0
    for path in sorted(SRC.rglob('*.tsx')):
        text = path.read_text(encoding='utf-8')
        guards = BEFORE_CLOSE.findall(text)
        if not guards:
            continue
        rel = path.relative_to(SRC.parent.parent)
        named = []
        for guard in guards:
            guard = guard.strip()
            if BARE_NAME.match(guard):
                named.append(guard)
            else:
                problems.append(
                    f'{rel}: beforeClose 가 이름 없는 함수입니다 — 닫기 버튼이 같은 검사를 쓸 수 없습니다.\n'
                    f'    beforeClose={{{guard[:70]}}}'
                )
        for position, body in close_button_attrs(text):
            checked += 1
            if not any(name in body for name in named):
                line = text.count('\n', 0, position) + 1
                problems.append(
                    f'{rel}:{line}: × 버튼이 {" / ".join(named) or "beforeClose"} 를 거치지 않습니다.\n'
                    f'    <button {" ".join(body.split())[:80]}>×</button>'
                )

    if problems:
        print(f'닫기 검사: {len(problems)}건')
        for problem in problems:
            print(f'  - {problem}')
        return 1
    print(f'닫기 검사: 통과 — 가드가 있는 대화상자의 × 버튼 {checked}개가 모두 같은 검사를 거칩니다.')
    return 0


if __name__ == '__main__':
    sys.exit(main())
