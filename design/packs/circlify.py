import re,sys,math
INP=sys.argv[1]; OUTP=sys.argv[2]
MARGIN=int(sys.argv[3]) if len(sys.argv)>3 else 360
RF=float(sys.argv[4]) if len(sys.argv)>4 else 0.85
PCT=float(sys.argv[5]) if len(sys.argv)>5 else 0.60   # fraction of shapes to dotify (roundest)
src=open(INP).read()
mW=re.search(r'<svg[^>]*width="(\d+)"[^>]*height="(\d+)"',src);PW,PH=int(mW.group(1)),int(mW.group(2))
path_re=re.compile(r'<path d="([^"]+)"\s+fill="(#[0-9a-fA-F]{6})"\s+transform="translate\(([\-0-9.]+),([\-0-9.]+)\)"\s*/>')
num_re=re.compile(r'(-?\d+(?:\.\d+)?)')
def subpaths(d):
    out=[]
    for c in re.split(r'[Mm]',d):
        c=c.strip()
        if not c:continue
        n=[float(x) for x in num_re.findall(c)];pts=list(zip(n[0::2],n[1::2]))
        if len(pts)>=3:out.append(pts)
    return out
def area(pts):
    a=0.0;n=len(pts)
    for i in range(n):
        x1,y1=pts[i];x2,y2=pts[(i+1)%n];a+=x1*y2-x2*y1
    return abs(a)/2
def perim(pts):
    p=0.0;n=len(pts)
    for i in range(n):
        x1,y1=pts[i];x2,y2=pts[(i+1)%n];p+=math.hypot(x2-x1,y2-y1)
    return p
shapes=[]   # dict per non-bg shape
bg=None
for m in path_re.finditer(src):
    d,fill,tx,ty=m.group(1),m.group(2),float(m.group(3)),float(m.group(4))
    raw=f'<path d="{d}" fill="{fill}" transform="translate({tx:g},{ty:g})"/>'
    if bg is None: bg=raw; continue
    sps=subpaths(d)
    if len(sps)!=1:
        shapes.append({'circ':-1,'raw':raw}); continue   # complex → never dotify
    pts=sps[0];xs=[p[0] for p in pts];ys=[p[1] for p in pts]
    w=max(xs)-min(xs);h=max(ys)-min(ys);big=max(w,h)
    A=area(pts);P=perim(pts)
    circ=(4*math.pi*A/(P*P)) if P>0 else 0     # 1.0=perfect circle, lower=less round
    cx=(max(xs)+min(xs))/2+tx;cy=(max(ys)+min(ys))/2+ty;r=(w+h)/4*RF
    shapes.append({'circ':circ,'big':big,'raw':raw,
                   'circle':f'<circle cx="{round(cx,1)}" cy="{round(cy,1)}" r="{round(r,1)}" fill="{fill}"/>'})
# rank by circularity; dotify the roundest PCT, keep the rest as polygons
ranked=sorted((s for s in shapes if s['circ']>=0), key=lambda s:-s['circ'])
ndot=int(round(len(ranked)*PCT))
dot_ids=set(id(s) for s in ranked[:ndot])
body=[];nc=npth=0
for s in shapes:
    if id(s) in dot_ids and 'circle' in s:
        body.append(s['circle']);nc+=1
    else:
        body.append(s['raw']);npth+=1
BW=8;FRAME='#000000';M=MARGIN;W2=PW+2*M;H2=PH+2*M;hb=BW/2
out=[f'<svg xmlns="http://www.w3.org/2000/svg" width="{W2}" height="{H2}" viewBox="0 0 {W2} {H2}">',
     f'<rect width="{W2}" height="{H2}" fill="{FRAME}"/>',
     f'<g transform="translate({M},{M})">', bg, *body, '</g>',
     f'<rect x="{M-hb}" y="{M-hb}" width="{PW+BW}" height="{PH+BW}" fill="none" stroke="{FRAME}" stroke-width="{BW}"/>',
     '</svg>']
open(OUTP,'w').write('\n'.join(out))
print(f"shapes={len(shapes)} dotified={nc} ({nc/max(1,nc+npth)*100:.0f}%) kept_polygons={npth}")
