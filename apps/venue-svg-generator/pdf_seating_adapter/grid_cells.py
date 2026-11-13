"""Source-only numbered cells bounded by explicit PDF border rectangles.

This opt-in primitive detector does not assign row/section names or fill sequences.
"""
import math
import re

def reconstruct_numbered_cells(rectangles, words, *, border_max=2, cell_min=5, cell_max=30, container_edges=False):
    if not (0<border_max<cell_min<cell_max): raise ValueError("Invalid cell detection limits")
    rects=[]
    for r in rectangles:
        coords=[r[k] for k in ("x0","x1","top","bottom")]
        if any(isinstance(v,bool) or not isinstance(v,(int,float)) or not math.isfinite(v) for v in coords):
            raise ValueError("Invalid PDF border coordinates")
        if coords[0]>=coords[1] or coords[2]>=coords[3]: continue
        rects.append(r)
    vertical=[r for r in rects if r["x1"]-r["x0"]<=border_max and r["bottom"]-r["top"]>=cell_min]
    horizontal=[r for r in rects if r["bottom"]-r["top"]<=border_max and r["x1"]-r["x0"]>=cell_min]
    cells=[];seen=set()
    for w in words:
        if not re.fullmatch(r"[0-9]+",w.get("text","")) or int(w["text"])<1: continue
        x=(w["x0"]+w["x1"])/2;y=(w["top"]+w["bottom"])/2
        vx=[(r["x0"]+r["x1"])/2 for r in vertical if r["top"]<=y<=r["bottom"]]
        hy=[(r["top"]+r["bottom"])/2 for r in horizontal if r["x0"]<=x<=r["x1"]]
        if container_edges:
            # Some PDF generators paint the last cell boundary as the edge of
            # a wider row container. Only real closed source rectangles qualify.
            containers=[r for r in rects if cell_min<=r["bottom"]-r["top"]<=cell_max
                        and r["x1"]-r["x0"]>=cell_min and r["x0"]<x<r["x1"] and r["top"]<y<r["bottom"]]
            vx.extend(edge for r in containers for edge in (r["x0"],r["x1"]))
            hy.extend(edge for r in containers for edge in (r["top"],r["bottom"]))
        left=[v for v in vx if v<x];right=[v for v in vx if v>x]
        top=[v for v in hy if v<y];bottom=[v for v in hy if v>y]
        if not all((left,right,top,bottom)): continue
        box=[max(left),max(top),min(right),min(bottom)]
        if not (cell_min<=box[2]-box[0]<=cell_max and cell_min<=box[3]-box[1]<=cell_max): continue
        if not (box[0]<=w["x0"]<=w["x1"]<=box[2] and box[1]<=w["top"]<=w["bottom"]<=box[3]): continue
        inside=[z for z in words if box[0]<(z["x0"]+z["x1"])/2<box[2] and box[1]<(z["top"]+z["bottom"])/2<box[3]]
        if len(inside)!=1: continue
        key=tuple(round(v,5) for v in box)
        if key in seen: continue
        seen.add(key)
        cells.append({"source_bbox":box,"x":(box[0]+box[2])/2,"y":(box[1]+box[3])/2,
            "number":w["text"],"evidence_word":dict(w),"identity_origin":"source_glyph"})
    return sorted(cells,key=lambda s:(s["y"],s["x"]))
