import re,sys
INP=sys.argv[1]; OUTP=sys.argv[2]
src=open(INP).read()
mW=re.search(r'<svg[^>]*width="(\d+)"[^>]*height="(\d+)"',src);PW,PH=int(mW.group(1)),int(mW.group(2))
path_re=re.compile(r'<path d="([^"]+)"\s+fill="(#[0-9a-fA-F]{6})"\s+transform="translate\(([\-0-9.]+),([\-0-9.]+)\)"\s*/>')
num_re=re.compile(r'(-?\d+(?:\.\d+)?)')
def subpaths(d):
    out=[]
    for c in re.split(r'[Mm]',d):
        c=c.strip()
        if not c: continue
        n=[float(x) for x in num_re.findall(c)];pts=list(zip(n[0::2],n[1::2]))
        if len(pts)>=3: out.append(pts)
    return out
def area(pts):
    a=0.0;n=len(pts)
    for i in range(n):
        x1,y1=pts[i];x2,y2=pts[(i+1)%n];a+=x1*y2-x2*y1
    return abs(a)/2
elems=[];nc=npth=0
for m in path_re.finditer(src):
    d,fill,tx,ty=m.group(1),m.group(2),float(m.group(3)),float(m.group(4))
    sps=subpaths(d);done=False
    if len(sps)==1:
        pts=sps[0];xs=[p[0] for p in pts];ys=[p[1] for p in pts]
        w=max(xs)-min(xs);h=max(ys)-min(ys);big=max(w,h);small=min(w,h)
        asp=small/big if big>0 else 0;fr=area(pts)/(w*h) if w*h>0 else 0
        if 3<=big<=34 and asp>=0.6 and fr>=0.58:
            cx=(max(xs)+min(xs))/2+tx;cy=(max(ys)+min(ys))/2+ty;r=(w+h)/4
            elems.append(f'<circle cx="{round(cx,1)}" cy="{round(cy,1)}" r="{round(r,1)}" fill="{fill}"/>');nc+=1;done=True
    if not done:
        elems.append(f'<path d="{d}" fill="{fill}" transform="translate({tx:g},{ty:g})"/>');npth+=1
M=30;BW=14;FRAME='#000000'
W2=PW+2*M;H2=PH+2*M;hb=BW/2
out=[f'<svg xmlns="http://www.w3.org/2000/svg" width="{W2}" height="{H2}" viewBox="0 0 {W2} {H2}">',
     f'<rect width="{W2}" height="{H2}" fill="{FRAME}"/>',
     f'<g transform="translate({M},{M})">', *elems, '</g>',
     f'<rect x="{M-hb}" y="{M-hb}" width="{PW+BW}" height="{PH+BW}" fill="none" stroke="{FRAME}" stroke-width="{BW}"/>',
     '</svg>']
open(OUTP,'w').write('\n'.join(out))
print(f"circles={nc} paths={npth} canvas={W2}x{H2}")
