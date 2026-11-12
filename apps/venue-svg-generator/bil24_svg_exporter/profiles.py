"""Reusable presentation settings; no venue names, seat identities or source paths."""
from copy import deepcopy
from .core import _number

def uniform_contiguous_rows(profile, evidence, visible_gap=9, stroke_width=1):
    """Explicitly opt into even spacing after source rows were independently checked."""
    if not isinstance(evidence,str) or not evidence.strip():
        raise ValueError("Source evidence for contiguous rows is required")
    radius=_number(profile["seat_radius"],True)
    gap=_number(visible_gap,True)
    stroke=_number(stroke_width,True)
    result=deepcopy(profile)
    result["seat_stroke_width"]=stroke
    result["layout"]={"mode":"uniform_rows","seat_pitch":2*radius+stroke+gap,
        "contiguous_rows_confirmed":True,"evidence":evidence,
        "visible_gap":gap,"source_coordinate_recovery":"Use preserved data-source-x/y; uniform layout is presentation only."}
    result["transform"]["purpose"]="Base affine transform before row normalization; source coordinates retained separately."
    return result
