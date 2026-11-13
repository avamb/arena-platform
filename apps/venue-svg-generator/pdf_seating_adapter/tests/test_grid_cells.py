import pytest
from pdf_seating_adapter.grid_cells import reconstruct_numbered_cells

def borders():
    return [{"x0":x,"x1":x+1,"top":0,"bottom":12} for x in (0,15,30)]+[{"x0":0,"x1":31,"top":y,"bottom":y+1} for y in (0,11)]

def word(text,x): return {"text":text,"x0":x,"x1":x+4,"top":3,"bottom":8}

def test_bordered_cells_not_strokes():
    cells=reconstruct_numbered_cells(borders(),[word("9",5),word("2",20),word("1",45)])
    assert [s["number"] for s in cells]==["9","2"]
    assert [s["source_bbox"] for s in cells]==[[.5,.5,15.5,11.5],[15.5,.5,30.5,11.5]]
    assert all(s["identity_origin"]=="source_glyph" for s in cells)

def test_open_cell_and_ambiguous_text_are_rejected():
    assert reconstruct_numbered_cells(borders()[:-1],[word("9",5)])==[]
    assert reconstruct_numbered_cells(borders(),[word("9",5),word("note",9)])==[]
    assert reconstruct_numbered_cells(borders(),[word("?",5)])==[]

def test_invalid_limits_and_geometry():
    with pytest.raises(ValueError): reconstruct_numbered_cells([],[],cell_min=1)
    bad=borders();bad[0]["x0"]=float("nan")
    with pytest.raises(ValueError): reconstruct_numbered_cells(bad,[])


def test_nearest_upper_border_not_distant_header():
    r=borders()+[{"x0":0,"x1":31,"top":-20,"bottom":-19}]
    assert reconstruct_numbered_cells(r,[word("9",5)])[0]["source_bbox"]==[.5,.5,15.5,11.5]


def test_last_cell_from_closed_container_edge_only_when_enabled():
    rects=[{"x0":0,"x1":30,"top":0,"bottom":12},
           {"x0":15,"x1":16,"top":0,"bottom":12}]
    assert reconstruct_numbered_cells(rects,[word("12",20)])==[]
    cells=reconstruct_numbered_cells(rects,[word("12",20)],container_edges=True)
    assert len(cells)==1 and cells[0]["number"]=="12"
    assert cells[0]["source_bbox"]==[15.5,0,30,12]
    # No closed rectangle: an open fragment cannot be extrapolated to a seat.
    assert reconstruct_numbered_cells(rects[1:],[word("12",20)],container_edges=True)==[]
