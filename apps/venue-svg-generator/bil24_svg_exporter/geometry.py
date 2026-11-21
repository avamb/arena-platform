"""Bounded presentation fitting of explicitly confirmed row segments.

No identities, source coordinates, aisle topology or seat counts are inferred.
Numpy is loaded only when the optional fitting profile is enabled.
"""
import math

def regularize(positions, seats, config):
    import numpy as np
    if not isinstance(config, dict) or not config.get("evidence"):
        raise ValueError("Geometry fitting requires source evidence")
    limit=config.get("max_shift_fraction", .35)
    if isinstance(limit,bool) or not isinstance(limit,(int,float)) or not math.isfinite(limit) or not 0<limit<=.5:
        raise ValueError("Maximum correction must be within half a source pitch")
    segments=config.get("segments")
    if not isinstance(segments,list):raise ValueError("Explicit segments required")
    result=dict(positions);report=[];seen=set()
    axis_groups={}
    for segment in segments:
        axis=segment.get("axis")
        if axis is not None and (axis not in {"horizontal","vertical"} or segment.get("kind")!="line"):
            raise ValueError("Axis constraints apply only to confirmed straight segments")
        group=segment.get("axis_group")
        if group is not None:
            if not isinstance(group,str) or not group or axis is None:raise ValueError("Invalid shared axis group")
            axis_groups.setdefault(group,[]).append(segment)
    axis_targets={}
    for group,parts in axis_groups.items():
        axes={part["axis"] for part in parts}
        keys=[k for part in parts for k in part["keys"]]
        row_names={str(s["row_label"]) for s in seats if s["seat_key"] in keys}
        if len(axes)!=1 or len(row_names)!=1 or any(k not in positions for k in keys):
            raise ValueError("Shared axes require one known row and consistent direction")
        dimension=1 if parts[0]["axis"]=="horizontal" else 0
        axis_targets[group]=float(np.median([positions[k][dimension] for k in keys]))
    rows={s["seat_key"]:str(s["row_label"]) for s in seats}
    for segment in segments:
        keys=segment.get("keys",[]);kind=segment.get("kind")
        if kind not in {"line","arc"} or not isinstance(keys,list) or len(keys)<3 or len(set(keys))!=len(keys):
            raise ValueError("Ordered distinct segment keys and line/arc required")
        if any(k not in positions or k in seen for k in keys) or len({rows[k] for k in keys})!=1:
            raise ValueError("Unknown, repeated or cross-row segment keys")
        seen.update(keys)
        points=np.array([positions[k] for k in keys],dtype=float)
        if not np.isfinite(points).all():raise ValueError("Nonfinite geometry")
        lengths=np.linalg.norm(np.diff(points,axis=0),axis=1)
        pitch=float(np.median(lengths))
        if pitch<=0:raise ValueError("Degenerate segment")
        entry={"keys":keys,"kind":kind,"source_pitch":pitch,"status":"UNCHANGED"}
        # A segment must not span an aisle, missing observation or sharp corner.
        if np.any(lengths>1.65*pitch) or np.any(lengths<.55*pitch):
            entry["reason"]="irregular_step_or_gap";report.append(entry);continue
        center=points.mean(axis=0);local=points-center
        if kind=="line":
            constraint=segment.get("axis")
            if constraint:
                axis=np.array([1.,0.] if constraint=="horizontal" else [0.,1.])
                dimension=1 if constraint=="horizontal" else 0
                center[dimension]=axis_targets.get(segment.get("axis_group"),float(np.median(points[:,dimension])))
                local=points-center
                entry["axis_constraint"]=constraint
                entry["axis_target"]=float(center[dimension])
            else:
                _,_,vectors=np.linalg.svd(local,full_matrices=False)
                axis=vectors[0]
            if np.dot(points[-1]-points[0],axis)<0:axis=-axis
            parameter=local@axis
            if not np.all(np.diff(parameter)>0):
                entry["reason"]="nonmonotonic_path";report.append(entry);continue
            targets=np.linspace(parameter[0],parameter[-1],len(keys))
            fitted=center+targets[:,None]*axis
        else:
            matrix=np.column_stack((2*local[:,0],2*local[:,1],np.ones(len(keys))))
            solution,_,rank,_=np.linalg.lstsq(matrix,(local**2).sum(axis=1),rcond=None)
            if rank<3:
                entry["reason"]="unconfirmed_curvature";report.append(entry);continue
            c=solution[:2];radius=math.sqrt(max(0,float(solution[2]+np.dot(c,c))))
            angles=np.unwrap(np.arctan2(local[:,1]-c[1],local[:,0]-c[0]))
            delta=np.diff(angles)
            if radius<=0 or abs(angles[-1]-angles[0])<.05 or abs(angles[-1]-angles[0])>math.pi or not (np.all(delta>0) or np.all(delta<0)):
                entry["reason"]="unconfirmed_curvature";report.append(entry);continue
            targets=np.linspace(angles[0],angles[-1],len(keys))
            fitted=center+c+radius*np.column_stack((np.cos(targets),np.sin(targets)))
            entry["radius"]=radius
        shifts=np.linalg.norm(fitted-points,axis=1)
        entry["max_shift"]=float(shifts.max())
        entry["step_spread_before"]=float(lengths.max()-lengths.min())
        if entry["max_shift"]>limit*pitch:
            entry["reason"]="correction_exceeds_bound";report.append(entry);continue
        entry["status"]="APPLIED"
        entry["y_spread_before"]=float(np.ptp(points[:,1]))
        entry["y_spread_after"]=float(np.ptp(fitted[:,1]))
        after=np.linalg.norm(np.diff(fitted,axis=0),axis=1)
        entry["step_spread_after"]=float(after.max()-after.min())
        for key,point in zip(keys,fitted):result[key]=(float(point[0]),float(point[1]))
        report.append(entry)
    return result,report


def split_path(positions, ordered_keys):
    """Suggest source-only straight pieces; caller must confirm topology/evidence.

    Preserve all keys, including short pieces. Do not use this to assign row IDs.
    Corners and gaps remain boundaries, never interpolated missing seats.
    """
    import numpy as np
    if len(set(ordered_keys))!=len(ordered_keys) or any(k not in positions for k in ordered_keys):
        raise ValueError("Distinct known path keys required")
    if len(ordered_keys)<3:return [list(ordered_keys)] if ordered_keys else []
    points=np.array([positions[k] for k in ordered_keys],dtype=float)
    if not np.isfinite(points).all():raise ValueError("Nonfinite path")
    vectors=np.diff(points,axis=0);lengths=np.linalg.norm(vectors,axis=1)
    if np.any(lengths<=0):raise ValueError("Degenerate path")
    pitch=float(np.median(lengths));pieces=[[]]
    for i,key in enumerate(ordered_keys):
        boundary=i>0 and lengths[i-1]>1.65*pitch
        if 1<i<len(ordered_keys):
            cosine=float(np.dot(vectors[i-2],vectors[i-1])/(lengths[i-2]*lengths[i-1]))
            boundary=boundary or cosine<math.cos(math.radians(30))
        if boundary:pieces.append([])
        pieces[-1].append(key)
    return pieces
