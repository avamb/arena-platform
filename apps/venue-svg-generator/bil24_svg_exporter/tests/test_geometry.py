import math
import copy
import pytest
from bil24_svg_exporter.geometry import regularize

def run(points, kind="line", keys=None):
    positions={str(i):p for i,p in enumerate(points)}
    seats=[{"seat_key":k,"row_label":"1"} for k in positions]
    config={"evidence":"confirmed source row segment","segments":[{"keys":keys or list(positions),"kind":kind}]}
    before=copy.deepcopy(positions)
    result,report=regularize(positions,seats,config)
    assert positions==before
    return result,report

def test_tilted_line_and_uniform_pitch():
    result,report=run([(0,.1),(10,2.1),(20,3.9),(30,6.1)])
    assert report[0]["status"]=="APPLIED"
    assert report[0]["step_spread_after"]<1e-10
    assert result["3"][1]>result["0"][1] # preserve angle

def test_circle_radius_and_equal_chords():
    points=[(100*math.cos(t)+.15*(-1)**i,100*math.sin(t)) for i,t in enumerate([.2,.3,.4,.5,.6,.7])]
    _,report=run(points,"arc")
    assert report[0]["status"]=="APPLIED"
    assert abs(report[0]["radius"]-100)<2
    assert report[0]["step_spread_after"]<1e-10

def test_aisle_never_closed():
    points=[(0,0),(10,0),(20,0),(50,0),(60,0),(70,0)]
    result,report=run(points)
    assert report[0]["reason"]=="irregular_step_or_gap"
    assert list(result.values())==points

def test_sharp_curve_cannot_be_flattened():
    points=[(0,0),(10,0),(10,10),(10,20)]
    result,report=run(points)
    assert report[0]["status"]=="UNCHANGED"
    assert list(result.values())==points

def test_collinear_arc_refused():
    _,report=run([(0,0),(10,0),(20,0)],"arc")
    assert report[0]["status"]=="UNCHANGED"

@pytest.mark.parametrize("config",[{}, {"evidence":"x","max_shift_fraction":float("nan"),"segments":[]},{"evidence":"x","segments":[{"keys":["a","b","unknown"],"kind":"line"}]}])
def test_invalid_profile_refused(config):
    with pytest.raises(ValueError):regularize({"a":(0,0),"b":(10,0)},[{"seat_key":"a","row_label":"1"},{"seat_key":"b","row_label":"1"}],config)

def test_cross_row_and_repeated_segments_refused():
    positions={str(i):(i*10,0) for i in range(3)}
    seats=[{"seat_key":k,"row_label":k} for k in positions]
    with pytest.raises(ValueError):regularize(positions,seats,{"evidence":"x","segments":[{"kind":"line","keys":list(positions)}]})


def test_path_splits_corners_and_gaps_without_dropping_keys():
    from bil24_svg_exporter.geometry import split_path
    points=[(0,0),(10,0),(20,0),(20,10),(20,20),(20,50),(20,60),(20,70)]
    positions={str(i):p for i,p in enumerate(points)}
    pieces=split_path(positions,list(positions))
    assert [k for part in pieces for k in part]==list(positions)
    assert len(pieces)==3


def test_horizontal_axis_shared_across_aisle():
    points={"a":(0,2),"b":(10,1),"c":(20,0),"d":(50,1),"e":(60,0),"f":(70,-1)}
    seats=[{"seat_key":k,"row_label":"7"} for k in points]
    segments=[{"kind":"line","axis":"horizontal","axis_group":"row7","keys":keys} for keys in (["a","b","c"],["d","e","f"])]
    result,report=regularize(points,seats,{"evidence":"source horizontal row across aisle","segments":segments})
    assert {p[1] for p in result.values()}=={.5}
    assert result["d"][0]-result["c"][0]==30 # aisle untouched
    assert all(r["y_spread_after"]==0 for r in report)
    assert points["a"]==(0,2)

def test_invalid_arc_axis_rejected():
    points={str(i):(i*10,0) for i in range(3)}
    seats=[{"seat_key":k,"row_label":"1"} for k in points]
    with pytest.raises(ValueError):regularize(points,seats,{"evidence":"x","segments":[{"kind":"arc","axis":"horizontal","keys":list(points)}]})
