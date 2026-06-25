import sys, numpy as np
from PIL import Image
from scipy import ndimage as ndi
from scipy.spatial import cKDTree

SRC=sys.argv[1]; OUT=sys.argv[2]
MIN_DIST=float(sys.argv[3]) if len(sys.argv)>3 else 6.0   # min spacing between dot centers (px)
BGthr=int(sys.argv[4]) if len(sys.argv)>4 else 45         # brightness below this = background gap
RSCALE=float(sys.argv[5]) if len(sys.argv)>5 else 1.15    # dot radius scale (overlap if >1)
MARG=int(sys.argv[6]) if len(sys.argv)>6 else 360         # black-frame margin

im=Image.open(SRC).convert('RGB'); a=np.asarray(im); H,W,_=a.shape
bright=a.max(2)
mask=bright>BGthr                                   # foreground = the painted dots
# distance transform: peak = dot center, value ≈ dot radius
dt=ndi.distance_transform_edt(mask)
# local maxima of dt, spaced >= MIN_DIST, above a small radius floor
size=int(max(3,MIN_DIST))
mx=ndi.maximum_filter(dt,size=size,mode='constant')
peaks=(dt==mx)&(dt>=1.5)&mask
ys,xs=np.nonzero(peaks)
# de-duplicate plateau peaks that are closer than MIN_DIST (keep strongest)
order=np.argsort(-dt[ys,xs])
ys,xs=ys[order],xs[order]
tree_pts=[]; keep=[]
for y,x in zip(ys,xs):
    if tree_pts:
        t=cKDTree(tree_pts)
        if t.query([x,y])[0]<MIN_DIST: continue
    tree_pts.append([x,y]); keep.append((x,y,dt[y,x]))
cx=np.array([p[0] for p in keep]); cy=np.array([p[1] for p in keep]); cr=np.array([p[2] for p in keep])
n=len(keep)
# Voronoi partition of every foreground pixel → nearest center; median colour per cell
fy,fx=np.nonzero(mask)
tree=cKDTree(np.column_stack([cx,cy]))
_,lab=tree.query(np.column_stack([fx,fy]))
cols=a[fy,fx]                                       # (M,3)
order=np.argsort(lab); lab=lab[order]; cols=cols[order]
bounds=np.searchsorted(lab,np.arange(n+1))
fills=[]
for i in range(n):
    seg=cols[bounds[i]:bounds[i+1]]
    if len(seg)==0: fills.append('#000000'); continue
    r,g,b=int(np.median(seg[:,0])),int(np.median(seg[:,1])),int(np.median(seg[:,2]))
    fills.append('#%02x%02x%02x'%(r,g,b))
# radius: image dot radius (dt) blended toward cell size, scaled
nn=tree.query(np.column_stack([cx,cy]),k=2)[0][:,1]
rad=np.minimum(np.maximum(cr,1.5), nn*0.62)*RSCALE
# frame + emit. MARG/BW default to 0 → a full-bleed painting (black field +
# dots, edge to edge) so the wallpaper covers the whole viewport; any black
# frame is now a theme concern (--wash-viewport-border). Pass MARG>0 (arg 6)
# and/or BW>0 (arg 7) to bake a margin + border into the SVG instead.
M=MARG;BW=int(sys.argv[7]) if len(sys.argv)>7 else 0;FRAME='#000000';W2=W+2*M;Hh=H+2*M;hb=BW/2
body=[f'<circle cx="{x+M:.1f}" cy="{y+M:.1f}" r="{r:.1f}" fill="{f}"/>' for x,y,r,f in zip(cx,cy,rad,fills)]
svg=[f'<svg xmlns="http://www.w3.org/2000/svg" width="{W2}" height="{Hh}" viewBox="0 0 {W2} {Hh}">',
     f'<rect width="{W2}" height="{Hh}" fill="{FRAME}"/>', *body]
if BW>0:
    svg.append(f'<rect x="{M-hb}" y="{M-hb}" width="{W+BW}" height="{H+BW}" fill="none" stroke="{FRAME}" stroke-width="{BW}"/>')
svg.append('</svg>')
open(OUT,'w').write('\n'.join(svg))
print(f"detected dots={n}  median radius={np.median(rad):.1f}px")
