#!/usr/bin/env python3
"""check-interactive — the interaction-layer drift guard.

Everything clickable in wash wears the interaction layer (web/lib/src/hit.ts):
one stylesheet, keyed on `data-wash-hit`, that draws hover / press /
keyboard-focus feedback on an ::after overlay. This guard fails if a
clickable element appears without it, so the sweep that added the attribute
to 220 sites cannot silently rot back to the state it replaced -- where the
desktop had :hover in three files and :active in none.

An element is clickable if it is a <button>, or declares cursor:'pointer'
(inline, or through a style constant/factory defined in the same file --
`const iconBtnStyle`, `function rowStyle(sel)`, which is how much of wash's
chrome is written), or carries an onClick.

Two escapes, both explicit:
  data-wash-hit      it is a hit target (what you almost always want)
  data-wash-no-hit   it takes a click but is not something you point AT --
                     a dismiss backdrop, a scroll viewport clearing a
                     selection. Marked rather than left bare so a decision
                     is distinguishable from an oversight.

Skipped: component tags (<Button>, <MenuItem>) own the attribute
internally, and replaced elements (input/select/textarea/img) cannot render
the ::after overlay the layer draws.
"""
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
ROOTS = ['web/shell/src', 'web/lib/src', 'apps']

# Elements that can host the overlay AND plausibly be clicked.
HIT_ELEMENTS = (
    'button|div|span|a|li|label|tr|td|th|section|header|footer|nav|ul|ol|p'
    '|h1|h2|h3|h4|h5|h6|summary|figure|article|aside|main|form|fieldset|legend'
    '|pre|code|em|strong|small|b|i'
)
TAG_RE = re.compile(r'<(' + HIT_ELEMENTS + r')(?=[\s/>])')
POINTER = re.compile(r"cursor:\s*'pointer'")


def tag_end(src: str, i: int) -> int:
    """Index of the '>' closing the opening tag whose name ends at i.

    Brace/quote/comment aware. Comments matter: JSX opening tags here carry
    `//` explanations, and an apostrophe inside one ("the titlebar's drag
    handler") reads as a string delimiter to a naive scanner, which then
    swallows the rest of the tag.
    """
    j, depth, n = i, 0, len(src)
    while j < n:
        ch = src[j]
        if ch == '/' and j + 1 < n and src[j + 1] == '/':
            j = src.find('\n', j)
            if j < 0:
                return -1
        elif ch == '/' and j + 1 < n and src[j + 1] == '*':
            k = src.find('*/', j + 2)
            if k < 0:
                return -1
            j = k + 1
        elif ch in '"\'':
            q, j = ch, j + 1
            while j < n and src[j] != q:
                j += 2 if src[j] == '\\' else 1
        elif ch == '`':
            j += 1
            while j < n and src[j] != '`':
                j += 2 if src[j] == '\\' else 1
        elif ch == '{':
            depth += 1
        elif ch == '}':
            depth -= 1
        elif ch == '>' and depth == 0:
            return j
        j += 1
    return -1


def strip_comments(body: str) -> str:
    """Blank out // and /* */ comments in a tag body.

    Load-bearing, not tidiness: an opening tag whose comment merely
    MENTIONS data-wash-hit (Button's does, explaining the attribute order)
    would otherwise satisfy the guard forever -- a guard that cannot fail.
    """
    out, i, n = [], 0, len(body)
    while i < n:
        if body[i] == '/' and i + 1 < n and body[i + 1] == '/':
            j = body.find('\n', i)
            i = n if j < 0 else j
        elif body[i] == '/' and i + 1 < n and body[i + 1] == '*':
            j = body.find('*/', i + 2)
            i = n if j < 0 else j + 2
        else:
            out.append(body[i])
            i += 1
    return ''.join(out)


def pointer_styles(src: str) -> set:
    """Names of style constants/factories whose body sets cursor:'pointer'."""
    names = set()
    for m in re.finditer(r'(?:const|function)\s+([A-Za-z_$][\w$]*)', src):
        name, start = m.group(1), m.end()
        brace = src.find('{', start)
        if brace < 0 or brace - start > 160:
            continue
        depth, j = 0, brace
        while j < len(src):
            if src[j] == '{':
                depth += 1
            elif src[j] == '}':
                depth -= 1
                if depth == 0:
                    break
            j += 1
        if POINTER.search(src[brace:j]):
            names.add(name)
    return names


def sources():
    out = subprocess.run(
        ['find', *ROOTS, '-name', '*.tsx'],
        cwd=ROOT, capture_output=True, text=True, check=True).stdout.split()
    return [f for f in out if '.ctest.' not in f and '.test.' not in f]


def main() -> int:
    misses = []
    checked = 0
    for rel in sorted(sources()):
        src = (ROOT / rel).read_text()
        styles = pointer_styles(src)
        style_re = re.compile(r'\b(?:' + '|'.join(map(re.escape, styles)) + r')\b') if styles else None
        for m in TAG_RE.finditer(src):
            end = tag_end(src, m.end())
            if end < 0:
                continue
            body = strip_comments(src[m.end():end])
            clickable = (
                m.group(1) == 'button'
                or 'onClick' in body
                or POINTER.search(body)
                or (style_re and style_re.search(body))
            )
            if not clickable:
                continue
            checked += 1
            if 'data-wash-hit' in body or 'data-wash-no-hit' in body:
                continue
            testid = re.search(r'data-testid=\{?[`"\']([^`"\'}]*)', body)
            misses.append((rel, src[:m.start()].count('\n') + 1, m.group(1),
                           testid.group(1) if testid else ''))

    if misses:
        print('check-interactive: clickable elements with no interaction layer '
              '(hover/press/focus feedback):')
        for rel, line, tag, testid in misses:
            print(f'  {rel}:{line}  <{tag}>{(" " + testid) if testid else ""}')
        print('check-interactive: add data-wash-hit (see web/lib/src/hit.ts), or')
        print('check-interactive: data-wash-no-hit if it takes a click but is not '
              'a thing you point at (a dismiss backdrop).')
        return 1
    print(f'check-interactive: all {checked} clickable elements carry the '
          f'interaction layer')
    return 0


if __name__ == '__main__':
    sys.exit(main())
