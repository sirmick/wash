import re,sys
INP=sys.argv[1]; OUTP=sys.argv[2]
MARGIN=int(sys.argv[3]) if len(sys.argv)>3 else 360
RF=float(sys.argv[4]) if len(sys.argv)>4 else 0.85
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
def bbox(pts):
    xs=[p[0] for p in pts];ys=[p[1] for p in pts]
    return min(xs),min(ys),max(xs),max(ys)
elems=[];nc=npth=ndrop=0;first=True
for m in path_re.finditer(src):
    d,fill,tx,ty=m.group(1),m.group(2),float(m.group(3)),float(m.group(4))
    if first:  # full-canvas background — keep
        elems.append(f'<path d="{d}" fill="{fill}" transform="translate({tx:g},{ty:g})"/>');npth+=1;first=False;continue
    sps=subpaths(d)
    # union bbox across subpaths (handles dot-with-hole as one blob)
    xs0=ys0=1e9;xs1=ys1=-1e9
    for pts in sps:
        a,b,c,e=bbox(pts)
        xs0=min(xs0,a);ys0=min(ys0,b);xs1=max(xs1,c);ys1=max(ys1,e)
    if xs1<xs0: ndrop+=1; continue
    w=xs1-xs0;h=ys1-ys0;big=max(w,h);small=min(w,h);asp=small/big if big>0 else 0
    # EXTREME dotify: almost anything roundish-or-square becomes a dot;
    # only very elongated strokes (river/outlines) or oversized fills are
    # dropped. Nothing but the background stays a polygon.
    if 2<=big<=120 and asp>=0.32:
        cx=(xs0+xs1)/2+tx;cy=(ys0+ys1)/2+ty;r=(w+h)/4*RF
        elems.append(f'<circle cx="{round(cx,1)}" cy="{round(cy,1)}" r="{round(r,1)}" fill="{fill}"/>');nc+=1;continue
    ndrop+=1
BW=8;FRAME='#000000';M=MARGIN
W2=PW+2*M;H2=PH+2*M;hb=BW/2
out=[f'<svg xmlns="http://www.w3.org/2000/svg" width="{W2}" height="{H2}" viewBox="0 0 {W2} {H2}">',
     f'<rect width="{W2}" height="{H2}" fill="{FRAME}"/>',
     f'<g transform="translate({M},{M})">', *elems, '</g>',
     f'<rect x="{M-hb}" y="{M-hb}" width="{PW+BW}" height="{PH+BW}" fill="none" stroke="{FRAME}" stroke-width="{BW}"/>',
     '</svg>']
open(OUTP,'w').write('\n'.join(out))
print(f"circles={nc} paths={npth} dropped={ndrop}  circle%={nc/(nc+npth)*100:.1f} of drawn")
