import cv2,numpy as np,pytest
from raster_seating_adapter.source_primitives import hue_dots,tilted_rows,outline_faces
def test_hue_dots_exclude_header_and_different_color():
    im=np.full((90,140,3),255,np.uint8)
    for x,y,color in [(30,40,(255,0,0)),(65,45,(255,0,0)),(100,40,(0,0,255)),(30,10,(255,0,0))]:cv2.circle(im,(x,y),5,color,-1)
    p={"hue":[90,130],"roi":[0,25,140,90],"saturation_min":90,"value_min":25,"area_range":[30,120],"diameter_range":[6,15]}
    assert len(hue_dots(im,p))==2
    with pytest.raises(ValueError):hue_dots(im,{**p,"roi":[0,0,150,90]})
def test_tilted_rows_do_not_assign_labels():
    points=[{"x":x,"y":base+.2*x} for base in (40,65) for x in (10,30,50,70)]
    rows,evidence=tilted_rows(points)
    assert [len(r) for r in rows]==[4,4] and abs(evidence["slope"]-.2)<1e-9
    assert all("row_label" not in p for r in rows for p in r)
def test_outline_exclusion_removes_context_faces():
    im=np.full((120,120,3),255,np.uint8)
    cv2.rectangle(im,(20,30),(50,65),(0,0,0),2);cv2.rectangle(im,(70,30),(100,65),(0,0,0),2)
    profile={"ink_threshold":180,"roi":[0,0,120,120],"area_range":[300,1500],"width_range":[15,40],"height_range":[15,45],"excluded_regions":[[60,0,120,120]]}
    assert len(outline_faces(im,profile))==1
