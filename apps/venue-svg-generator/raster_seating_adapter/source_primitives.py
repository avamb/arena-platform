"""Configured source-pixel primitives; no venue numbers or expected counts."""
import math
import cv2
import numpy as np

def hue_dots(image,profile):
    hsv=cv2.cvtColor(image,cv2.COLOR_BGR2HSV)
    h,s,v=cv2.split(hsv);low,high=profile["hue"]
    x0,y0,x1,y1=profile["roi"]
    if not (0<=x0<x1<=image.shape[1] and 0<=y0<y1<=image.shape[0] and 0<=low<=high<=179):raise ValueError("Invalid hue/ROI profile")
    mask=((h>=low)&(h<=high)&(s>=profile["saturation_min"])&(v>=profile["value_min"])).astype("uint8")*255
    bounds=np.zeros_like(mask);bounds[y0:y1,x0:x1]=1;mask*=bounds
    count,labels,stats,centers=cv2.connectedComponentsWithStats(mask,8)
    result=[]
    amin,amax=profile["area_range"];dmin,dmax=profile["diameter_range"]
    for index,(x,y,w,h,area) in enumerate(stats[1:],1):
        if amin<=area<=amax and dmin<=w<=dmax and dmin<=h<=dmax and .4<w/h<2.5:
            result.append({"x":float(centers[index][0]),"y":float(centers[index][1]),"source_bbox":[int(x),int(y),int(x+w),int(y+h)],"pixel_area":int(area)})
    return sorted(result,key=lambda p:(p["y"],p["x"]))

def tilted_rows(points,*,neighbor_dx=(10,35),neighbor_dy=10,tolerance=7):
    if not points:raise ValueError("No source points")
    xy=np.array([[p["x"],p["y"]] for p in points],dtype=float)
    if not np.isfinite(xy).all():raise ValueError("Nonfinite source point")
    slopes=[(b[1]-a[1])/(b[0]-a[0]) for a in xy for b in xy if neighbor_dx[0]<b[0]-a[0]<neighbor_dx[1] and abs(b[1]-a[1])<neighbor_dy]
    if not slopes:raise ValueError("No consistent row direction evidence")
    slope=float(np.median(slopes));residual=xy[:,1]-slope*xy[:,0]
    groups=[]
    for index in np.argsort(residual):
        if not groups or residual[index]-np.mean(residual[groups[-1]])>tolerance:groups.append([])
        groups[-1].append(int(index))
    return [[points[i] for i in sorted(g,key=lambda j:xy[j,0])] for g in groups],{"slope":slope,"tolerance":tolerance}

def outline_faces(image,profile):
    gray=cv2.cvtColor(image,cv2.COLOR_BGR2GRAY)
    _,mask=cv2.threshold(gray,profile["ink_threshold"],255,cv2.THRESH_BINARY_INV)
    contours,hierarchy=cv2.findContours(mask,cv2.RETR_TREE,cv2.CHAIN_APPROX_SIMPLE)
    if hierarchy is None:return []
    x0,y0,x1,y1=profile["roi"];amin,amax=profile["area_range"];wmin,wmax=profile["width_range"];hmin,hmax=profile["height_range"]
    result=[]
    for index,contour in enumerate(contours):
        x,y,w,h=cv2.boundingRect(contour);area=cv2.contourArea(contour)
        if hierarchy[0,index,3]<0 or not (x0<x<x1 and y0<y<y1 and amin<=area<=amax and wmin<=w<=wmax and hmin<=h<=hmax):continue
        if any(a<x<b and t<y<c for a,t,b,c in profile.get("excluded_regions",[])):continue
        center=(x+w/2,y+h/2)
        if any(math.dist(center,(r["x"],r["y"]))<profile.get("deduplicate_distance",8) for r in result):continue
        result.append({"x":center[0],"y":center[1],"source_bbox":[x,y,x+w,y+h],"contour_area":area})
    return sorted(result,key=lambda r:(r["y"],r["x"]))
