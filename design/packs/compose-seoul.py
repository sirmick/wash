import re

CARDS = [
    ('design/packs/src/hwatu/Hwatu_January_gwang.svg', 'c1'),  # crane
    ('design/packs/src/hwatu/Hwatu_March_gwang.svg',   'c2'),  # cherry
    ('design/packs/src/hwatu/Hwatu_August_gwang.svg',  'c3'),  # moon
]
VB_W, VB_H = 976, 1600

def inner_and_namespace(path, p):
    s = open(path, encoding='utf-8').read()
    s = re.sub(r'<\?xml.*?\?>', '', s, flags=re.S)
    s = re.sub(r'<metadata>.*?</metadata>', '', s, flags=re.S)
    m = re.search(r'<svg\b[^>]*>', s)
    inner = s[m.end(): s.rindex('</svg>')]
    for v in set(re.findall(r'id="([^"]+)"', inner)):
        ev = re.escape(v)
        inner = re.sub(rf'id="{ev}"', f'id="{p}_{v}"', inner)
        inner = re.sub(rf'url\(#{ev}\)', f'url(#{p}_{v})', inner)
        inner = re.sub(rf'((?:xlink:)?href)="#{ev}"', rf'\1="#{p}_{v}"', inner)
    inner = inner.replace('cls-', f'{p}_cls-')
    return inner

W, H = 1920, 1080
FIELD = '#efe4ca'
cardH = 760
cardW = cardH * VB_W / VB_H          # ~463.6
gap = 92
total = 3*cardW + 2*gap
x0 = (W - total) / 2
y0 = (H - cardH) / 2                  # vertically centered

parts = [f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" '
         f'viewBox="0 0 {W} {H}" preserveAspectRatio="xMidYMid slice">',
         f'<rect width="{W}" height="{H}" fill="{FIELD}"/>']
for i, ((path, p), ) in enumerate((c,) for c in CARDS):
    inner = inner_and_namespace(path, p)
    x = x0 + i*(cardW+gap)
    parts.append(f'<rect x="{x+9:.1f}" y="{y0+13:.1f}" width="{cardW:.1f}" height="{cardH:.1f}" rx="16" fill="rgba(60,40,12,0.20)"/>')
    parts.append(f'<svg x="{x:.1f}" y="{y0:.1f}" width="{cardW:.1f}" height="{cardH:.1f}" '
                 f'viewBox="0 0 {VB_W} {VB_H}" preserveAspectRatio="xMidYMid meet">{inner}</svg>')
parts.append('</svg>')
open('web/shell/public/wallpapers/seoul.svg','w',encoding='utf-8').write('\n'.join(parts))
print('wrote seoul.svg')
