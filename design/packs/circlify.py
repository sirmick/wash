import re,sys,math,statistics
INP=sys.argv[1]; OUTP=sys.argv[2]
MARGIN=int(sys.argv[3]) if len(sys.argv)>3 else 360
RF=float(sys.argv[4]) if len(sys.argv)>4 else 0.95
RMAX=float(sys.argv[6]) if len(sys.argv)>6 else 9.0
GAP=float(sys.argv[5]) if len(sys.argv)>5 else 1.0    # 1.0=may touch; >1 leaves a gap
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
# 1) dotify (circle-or-drop), collecting candidate circles (x,y,r,hex)
cands=[];first=True
for m in path_re.finditer(src):
    d,fill,tx,ty=m.group(1),m.group(2),float(m.group(3)),float(m.group(4))
    if first: first=False; continue   # background handled separately
    sps=subpaths(d)
    x0=y0=1e9;x1=y1=-1e9
    for pts in sps:
        xs=[p[0] for p in pts];ys=[p[1] for p in pts]
        x0=min(x0,min(xs));y0=min(y0,min(ys));x1=max(x1,max(xs));y1=max(y1,max(ys))
    if x1<x0: continue
    w=x1-x0;h=y1-y0;big=max(w,h);small=min(w,h);asp=small/big if big>0 else 0
    if 2<=big<=120 and asp>=0.32:
        cands.append([(x0+x1)/2+tx,(y0+y1)/2+ty,min((w+h)/4*RF,RMAX),fill])
# 2) greedy keep-or-absorb (largest first) → non-overlapping set; absorbed
#    colors fold into the keeper's list, final fill = per-channel median.
cands.sort(key=lambda c:-c[2])
maxr=cands[0][2] if cands else 1
CELL=max(8.0,maxr*2)
grid={}   # cell -> list of kept indices
kept=[]   # [x,y,r,[hexcolors]]
def cells_around(x,y,rad):
    c0=int((x-rad)//CELL);c1=int((x+rad)//CELL);r0=int((y-rad)//CELL);r1=int((y+rad)//CELL)
    for cx in range(c0,c1+1):
        for cy in range(r0,r1+1):
            yield (cx,cy)
for x,y,r,hexc in cands:
    hit=-1
    for cell in cells_around(x,y,r+maxr):
        for ki in grid.get(cell,()):
            kx,ky,kr,_=kept[ki]
            dx=kx-x;dy=ky-y
            if dx*dx+dy*dy < ((kr+r)*GAP)**2:   # overlap
                hit=ki;break
        if hit>=0:break
    if hit>=0:
        kept[hit][3].append(hexc)            # absorb color
    else:
        ki=len(kept);kept.append([x,y,r,[hexc]])
        for cell in cells_around(x,y,r):
            grid.setdefault(cell,[]).append(ki)
def med(colors):
    rs=[int(c[1:3],16) for c in colors];gs=[int(c[3:5],16) for c in colors];bs=[int(c[5:7],16) for c in colors]
    return '#%02x%02x%02x'%(int(statistics.median(rs)),int(statistics.median(gs)),int(statistics.median(bs)))
# 3) emit
BW=8;FRAME='#000000';M=MARGIN;W2=PW+2*M;H2=PH+2*M;hb=BW/2
body=[f'<circle cx="{round(x+0,1)}" cy="{round(y,1)}" r="{round(r,1)}" fill="{med(cs)}"/>' for x,y,r,cs in kept]
out=[f'<svg xmlns="http://www.w3.org/2000/svg" width="{W2}" height="{H2}" viewBox="0 0 {W2} {H2}">',
     f'<rect width="{W2}" height="{H2}" fill="{FRAME}"/>',
     f'<g transform="translate({M},{M})">', *body, '</g>',
     f'<rect x="{M-hb}" y="{M-hb}" width="{PW+BW}" height="{PH+BW}" fill="none" stroke="{FRAME}" stroke-width="{BW}"/>',
     '</svg>']
open(OUTP,'w').write('\n'.join(out))
print(f"candidates={len(cands)} kept(non-overlapping)={len(kept)} absorbed={len(cands)-len(kept)}")
