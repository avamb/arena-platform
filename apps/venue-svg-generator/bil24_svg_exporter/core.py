"""Reviewed source geometry to one authoring document, with explicit presentation profile."""
import hashlib
import json
import math
import re
import xml.etree.ElementTree as ET

SVG = "http://www.w3.org/2000/svg"
INK = "http://www.inkscape.org/namespaces/inkscape"
ET.register_namespace("", SVG)
ET.register_namespace("inkscape", INK)

def digest(data):
    return hashlib.sha256(json.dumps(data, sort_keys=True, ensure_ascii=False,
                                    separators=(",", ":"), allow_nan=False).encode()).hexdigest()

def _number(value, positive=False):
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value):
        raise ValueError("Finite numeric presentation/source coordinates required")
    if positive and value <= 0:
        raise ValueError("Positive dimensions required")
    return value

def _label(value):
    if isinstance(value, bool) or not isinstance(value, (str, int)) or not str(value).strip() or any(ord(c)<32 for c in str(value)):
        raise ValueError("Missing or invalid identity")
    return str(value)

def _color(value):
    if not isinstance(value, str) or not re.fullmatch(r"#[0-9a-fA-F]{6}", value):
        raise ValueError("Six-digit RGB color required")
    return value

def _categories(profile):
    """Unpriced template slots, never a claim about commercial allocation."""
    palette=profile.get("category_palette")
    if palette is None:
        category=profile["category"]
        if category.get("label")!="TEST_ONLY" or category.get("price_text")!="%price%":
            raise ValueError("No commercial prices or unapproved categories")
        return [category],category
    if profile.get("category_template")!="bil24-unpriced-slots-v1" or not profile.get("category_template_evidence"):
        raise ValueError("Unpriced category template requires explicit evidence")
    if not isinstance(palette,list) or not 1<=len(palette)<=15 or not all(isinstance(c,dict) for c in palette):
        raise ValueError("Category palette must contain 1 to 15 slots")
    labels=[];colors=[]
    for category in palette:
        label=_label(category.get("label"))
        if "#" in label or category.get("price_text")!="%price%" or set(category)-{"label","color","price_text"}:
            raise ValueError("Category slots must be unpriced placeholders")
        labels.append(label);colors.append(_color(category.get("color")).lower())
    if len(set(labels))!=len(labels) or len(set(colors))!=len(colors):
        raise ValueError("Duplicate category labels/colors")
    selected=profile.get("default_category")
    if selected not in labels: raise ValueError("Default category missing from palette")
    return palette,palette[labels.index(selected)]

def _node(parent, tag, text=None, **attrs):
    attributes={k.replace("_", "-"): str(v) for k,v in attrs.items()}
    # Forzum authoring examples store paint/font declarations in inline style.
    # Keep ordinary SVG attributes too; both representations must agree.
    declarations={key:attributes[key] for key in ("fill","stroke","stroke-width","font-size","font-family","text-anchor","display") if key in attributes}
    if declarations:
        attributes["style"]=";".join(key+":"+value for key,value in declarations.items())
    node = ET.SubElement(parent, "{"+SVG+"}"+tag, attributes)
    if text is not None:
        node.text = str(text)
    return node

def _text(parent, text, x, y, size=18, anchor="start", **attrs):
    return _node(parent, "text", text, x=x, y=y, font_size=size,
                 text_anchor=anchor, fill="#17202a", **attrs)

def _positions(model, profile):
    """Presentation layout only; never assigns identities or edits source geometry."""
    transform=profile["transform"]
    sx,sy,tx,ty=(transform[k] for k in ("scale_x","scale_y","translate_x","translate_y"))
    positions={}
    for seat in model["seats"]:
        point=seat["source_coordinate"]
        positions[seat["seat_key"]]=(_number(point["x"])*sx+tx,_number(point["y"])*sy+ty)
    overrides=profile.get("presentation_overrides",{})
    if overrides:
        if not isinstance(overrides,dict) or not profile.get("presentation_override_evidence"):
            raise ValueError("Explicit presentation overrides require evidence")
        if set(overrides)-set(positions):raise ValueError("Unknown source key in presentation overrides")
        for key,value in overrides.items():
            if not isinstance(value,dict) or set(value)!={"x","y"}:raise ValueError("Invalid presentation override")
            positions[key]=(_number(value["x"]),_number(value["y"]))
    layout=profile.get("layout",{"mode":"source_affine"})
    if layout.get("mode")=="source_affine":
        return positions
    if layout.get("mode")!="uniform_rows":
        raise ValueError("Unknown presentation layout mode")
    if model.get("gaps") or layout.get("contiguous_rows_confirmed") is not True or not layout.get("evidence"):
        raise ValueError("Uniform rows require confirmed contiguous structure with no gaps")
    pitch=_number(layout["seat_pitch"],True)
    groups={}
    for seat in model["seats"]:
        groups.setdefault(_label(seat["row_label"]),[]).append(seat)
    for row,seats in groups.items():
        ordered=sorted(seats,key=lambda seat:positions[seat["seat_key"]][0])
        xs=[positions[seat["seat_key"]][0] for seat in ordered]
        if len(xs)!=len(set(xs)):
            raise ValueError("Ambiguous source x order; uniform layout refused")
        center=(min(xs)+max(xs))/2
        y=sum(positions[seat["seat_key"]][1] for seat in ordered)/len(ordered)
        for index,seat in enumerate(ordered):
            positions[seat["seat_key"]]=(center+(index-(len(ordered)-1)/2)*pitch,y)
    return positions

def render(model, profile, review):
    if not all(isinstance(value, dict) for value in (model, profile, review)):
        raise ValueError("Model, profile and review must be JSON objects")
    if model.get("schema_version") != "seating-json-v1" or model.get("source_only") is not True or model.get("reference_svg_used") is not False:
        raise ValueError("Source-only seating-json-v1 required")
    if profile.get("schema") != "bil24-presentation-v1" or profile.get("test_only") is not True:
        raise ValueError("Only explicitly TEST_ONLY profiles supported")
    if review.get("schema") != "bil24-export-review-v1" or review.get("model_sha256") != digest(model) or review.get("verdict") != "PASS":
        raise ValueError("Missing/mismatched independent export review")
    if not review.get("evidence") or review.get("source_sha256") != model.get("source", {}).get("sha256"):
        raise ValueError("Review must bind source and evidence")
    section = _label(profile.get("section_label"))
    if "#" in section or section in {"PriceCategory", "Legend"} or not profile.get("section_evidence"):
        raise ValueError("Explicit section configuration/evidence required")
    decision=model.get("organizer_decision",{})
    if decision.get("event_intent")=="carry_source_sales" and decision.get("output_purpose")!="static_preview_only":
        raise ValueError("Carried sales require editable target event-state application; SVG-only export is not sufficient")
    seats = model.get("seats", [])
    if not isinstance(seats, list) or not seats or not all(isinstance(seat, dict) for seat in seats):
        raise ValueError("No seats")
    identities = []
    source_keys = set()
    for seat in seats:
        row, number = _label(seat.get("row_label")), _label(seat.get("seat_number"))
        if seat.get("section_id") not in (None, section):
            raise ValueError("This bounded exporter supports one explicitly mapped section")
        if seat.get("status") not in {"VALID", "QUARANTINE"}:
            raise ValueError("Unknown seat status")
        if seat.get("seat_key") in source_keys or not seat.get("seat_key"):
            raise ValueError("Duplicate/missing source key")
        source_keys.add(seat["seat_key"])
        identities.append((row, number))
    if len(set(identities)) != len(identities):
        raise ValueError("Duplicate row/seat identities")
    reviewed = review.get("approved_identities", [])
    if sorted(reviewed) != sorted([list(key) for key in identities]):
        raise ValueError("Every identity including quarantined observations must be independently reviewed")
    width, height = (_number(profile[k], True) for k in ("width", "height"))
    if width > 2000 or height > 2000:
        raise ValueError("Bil24 canvas exceeds 2000px")
    radius = _number(profile["seat_radius"], True)
    stroke_width = _number(profile.get("seat_stroke_width",3),True)
    transform = profile["transform"]
    sx, sy = (_number(transform[k], True) for k in ("scale_x", "scale_y"))
    tx, ty = (_number(transform[k]) for k in ("translate_x", "translate_y"))
    positions = _positions(model,profile)
    geometry_report=[]
    if profile.get("geometry_regularization") is not None:
        from .geometry import regularize
        positions,geometry_report=regularize(positions,seats,profile["geometry_regularization"])
    palette,category = _categories(profile)
    category_label = _label(category["label"])
    fill = _color(category["color"])
    root = ET.Element("{"+SVG+"}svg", {"width":str(width),"height":str(height),
        "viewBox":f"0 0 {width} {height}","preserveAspectRatio":"xMidYMid meet", "version":"1.1"})
    root.set("font-family", "Arial, sans-serif")
    _node(root, "title", profile.get("name", "Seating plan")+" — TEST_ONLY")
    provenance = {"schema":"bil24-export-provenance-v1","model_sha256":digest(model),
        "source_sha256":model["source"]["sha256"],"profile_sha256":digest(profile),
        "review_sha256":digest(review),"source_only":True,"reference_svg_used_for_extraction":False,
        "input_validation":model.get("validation"),"test_only":True,"commercial_data":False,
        "bil24_import":"NOT_TESTED","presentation_transform":transform,
        "geometry_regularization":geometry_report,"geometry_evidence":profile.get("geometry_regularization",{}).get("evidence"),
        "presentation_layout":profile.get("layout",{"mode":"source_affine"}),"presentation_overrides":profile.get("presentation_overrides",{}),"presentation_override_evidence":profile.get("presentation_override_evidence"),"seat_stroke_width":stroke_width,
        "section_evidence":profile["section_evidence"],"stage_evidence":profile["stage"]["evidence"]}
    _node(root, "metadata", json.dumps(provenance, ensure_ascii=False, sort_keys=True))
    _node(root, "rect", x=0,y=0,width=width,height=height,fill="#ffffff",id="Background")
    stage = profile["stage"]
    stage_group = _node(root, "g", id="Stage")
    _node(stage_group,"rect",x=_number(stage["x"]),y=_number(stage["y"]),
          width=_number(stage["width"],True),height=_number(stage["height"],True),
          fill="#eef1f4",stroke="#17202a",stroke_width=2,rx=4)
    _text(stage_group,stage["label"],stage["x"]+stage["width"]/2,
          stage["y"]+stage["height"]/2+8,28,"middle")
    _text(root,section,width/2,profile["section_y"],22,"middle",id="SectionLabel")
    _text(root,"TEST_ONLY · prices not assigned",width/2,height-45,16,"middle",id="TestNotice")
    legend = _node(root,"g",id="Legend")
    legend.set("{"+INK+"}label","#Legend")
    _text(root,"Status legend",30,34,17)
    states = [("Sold","#999999","sold"),("None","#ffffff","not available"),
              ("Reserved","#4d4d4d","reserved"),("MyTickets","#ff00cc","my seats")]
    legend_text = _node(legend,"text",id="LegendText",font_size=15,fill="#17202a")
    for index,(key,color,label) in enumerate(states):
        y=58+index*26
        swatch=_node(legend,"circle",id=key,cx=40,cy=y,r=7,fill=color,stroke="#17202a",stroke_width=1)
        swatch.set("{"+INK+"}label","#"+key)
        text=_node(legend_text,"tspan",label,x=55,y=y+5)
        text.set("{"+INK+"}label","#"+key)
    # Match accepted authoring files: four status circles before LegendText.
    legend.remove(legend_text)
    legend.append(legend_text)
    legend_text.set("{"+INK+"}label","#LegendText")
    categories=_node(root,"g",id="PriceCategory")
    categories.set("{"+INK+"}label","#PriceCategory")
    prices=_node(categories,"text",id="PriceCategoryText",font_size=15,fill="#17202a")
    prices.set("{"+INK+"}label","#PriceCategoryText")
    for index,slot in enumerate(palette):
        y=199+index*18
        swatch=_node(categories,"circle",cx=40,cy=y,r=7,fill=_color(slot["color"]),stroke="#17202a",stroke_width=1,id="CategorySlot"+str(index+1))
        swatch.set("{"+INK+"}label","#"+slot["label"])
        price=_node(prices,"tspan","%price%",x=55,y=y+5)
        price.set("{"+INK+"}label","#"+slot["label"])
    categories.remove(prices);categories.append(prices)
    if len(palette)==1: _text(root,"TEST_ONLY category",30,232,14)
    else: _text(root,"Unpriced categories",30,178,14)
    provenance["category_template"]={"slots":palette,"default_category":category_label,"prices_assigned":False}
    number_labels=_node(root,"g",id="SeatNumberLabels")
    groups={}
    for seat,(row,number) in zip(seats,identities):
        point=seat["source_coordinate"]
        x,y=positions[seat["seat_key"]]
        if not (radius+stroke_width/2 <= x <= width-radius-stroke_width/2 and radius+stroke_width/2 <= y <= height-radius-stroke_width/2):
            raise ValueError("Seat outside viewBox")
        if row not in groups:
            group=_node(root,"g",id="row-"+hashlib.sha256(row.encode()).hexdigest()[:16])
            group.set("{"+INK+"}label","#"+section)
            _node(group,"title",row)
            groups[row]=(group,[])
        group,points=groups[row]
        points.append((x,y))
        circle=_node(group,"circle",cx=x,cy=y,r=radius,fill=fill,stroke="#001505",stroke_width=stroke_width,
                     id="seat-"+hashlib.sha256((section+"\0"+row+"\0"+number).encode()).hexdigest(),
                     data_section=section,data_row=row,data_seat=number,data_source_key=seat["seat_key"],
                     data_source_x=point["x"],data_source_y=point["y"],data_test_only="true")
        _node(circle,"title",number)
        if profile.get("visible_seat_numbers", True):
            _text(number_labels,number,x,y+_number(profile.get("seat_number_font_size",12),True)/3,_number(profile.get("seat_number_font_size",12),True),"middle",pointer_events="none")
    root.remove(number_labels)
    root.append(number_labels)
    sides=profile.get("visible_row_label_sides",["left","right"])
    if not isinstance(sides,list) or any(side not in ("left","right") for side in sides) or len(sides)!=len(set(sides)):
        raise ValueError("Invalid visible row label sides")
    if sides!=["left","right"] and not profile.get("row_label_visibility_evidence"):
        raise ValueError("Row label visibility requires evidence")
    row_text=_node(root,"g",id="RowLabels")
    for row,(_,points) in groups.items():
        y=sum(p[1] for p in points)/len(points)
        custom=profile.get("row_label_positions",{}).get(row)
        if custom is None:
            custom=[[profile["row_label_left"],y+7],[profile["row_label_right"],y+7]]
        for side,(x,label_y) in zip(["left","right"],custom):
            if side in sides:_text(row_text,row,x,label_y,22,"middle")
    data=ET.tostring(root,encoding="utf-8",xml_declaration=True)
    result=validate(data,model,profile)
    return data,result,provenance

def validate(data,model,profile):
    root=ET.fromstring(data)
    ids=[node.attrib["id"] for node in root.iter() if "id" in node.attrib]
    if len(ids)!=len(set(ids)):
        raise ValueError("Duplicate SVG IDs")
    legend=root.find("{"+SVG+"}g[@id='Legend']")
    required=("Sold","None","Reserved","MyTickets")
    if legend is None or [ch.attrib.get("id") for ch in list(legend)[:4]]!=list(required):
        raise ValueError("Legend must start with Sold/None/Reserved/MyTickets circles")
    for key in required:
        node=legend.find("{"+SVG+"}circle[@id='"+key+"']")
        if node is None or node.attrib.get("{"+INK+"}label")!="#"+key:
            raise ValueError("Missing Legend status circle/label: "+key)
        paint=dict(item.split(":",1) for item in node.attrib.get("style","").split(";") if ":" in item)
        if paint.get("fill")!=node.attrib.get("fill"):
            raise ValueError("Legend inline fill missing/mismatched: "+key)
    palette,default=_categories(profile)
    category_group=root.find("{"+SVG+"}g[@id='PriceCategory']")
    if category_group is None: raise ValueError("Missing PriceCategory")
    swatches=category_group.findall("{"+SVG+"}circle")
    spans=category_group.findall("{"+SVG+"}text/{"+SVG+"}tspan")
    if len(swatches)!=len(palette) or len(spans)!=len(palette): raise ValueError("Category slot loss")
    for swatch,span,slot in zip(swatches,spans,palette):
        paint=dict(item.split(":",1) for item in swatch.get("style","").split(";") if ":" in item)
        if swatch.get("{"+INK+"}label")!="#"+slot["label"] or paint.get("fill")!=slot["color"] or span.get("{"+INK+"}label")!="#"+slot["label"] or span.text!="%price%":
            raise ValueError("Category metadata/price mismatch")
    actual=[]
    circles=[]
    section=profile["section_label"]
    positions=_positions(model,profile)
    if profile.get("geometry_regularization") is not None:
        from .geometry import regularize
        positions,_=regularize(positions,model["seats"],profile["geometry_regularization"])
    stroke_width=profile.get("seat_stroke_width",3)
    for group in root.findall("{"+SVG+"}g"):
        if group.attrib.get("{"+INK+"}label")!="#"+section:
            continue
        row=group.findtext("{"+SVG+"}title")
        if not row or any(child.tag not in {"{"+SVG+"}circle","{"+SVG+"}title"} for child in group):
            raise ValueError("Functional rows must contain title and circles only; put text in decor")
        for circle in group.findall("{"+SVG+"}circle"):
            number=circle.findtext("{"+SVG+"}title")
            paint=dict(item.split(":",1) for item in circle.get("style","").split(";") if ":" in item)
            if paint.get("fill")!=default["color"]: raise ValueError("Seat default category mismatch")
            actual.append((row,number))
            x,y=float(circle.attrib["cx"]),float(circle.attrib["cy"])
            circles.append((x,y,float(circle.attrib["r"])))
    expected=[(_label(s["row_label"]),_label(s["seat_number"])) for s in model["seats"]]
    if sorted(actual)!=sorted(expected):
        raise ValueError("Seat metadata loss")
    by_identity={(_label(s["row_label"]),_label(s["seat_number"])):s for s in model["seats"]}
    for identity,(x,y,r) in zip(actual,circles):
        expected_x,expected_y=positions[by_identity[identity]["seat_key"]]
        if abs(x-expected_x)>1e-8 or abs(y-expected_y)>1e-8:
            raise ValueError("Coordinate mismatch")
    overlaps=sum(math.hypot(x-a,y-b)<r+s+stroke_width-1e-8 for i,(x,y,r) in enumerate(circles) for a,b,s in circles[i+1:])
    if overlaps:
        raise ValueError("Seat outlines overlap")
    stage=profile["stage"]
    for x,y,r in circles:
        nearest_x=max(stage["x"],min(x,stage["x"]+stage["width"]))
        nearest_y=max(stage["y"],min(y,stage["y"]+stage["height"]))
        if math.hypot(x-nearest_x,y-nearest_y)<r+stroke_width/2+1:
            raise ValueError("Seat overlaps stage decoration")
    custom_labels=profile.get("row_label_positions")
    if custom_labels is not None:
        if not profile.get("row_label_position_evidence") or set(custom_labels)!={r for r,n in actual}:
            raise ValueError("Explicit row label positions require complete row coverage and evidence")
        for points in custom_labels.values():
            if not isinstance(points,list) or len(points)!=2:raise ValueError("Two row endpoint labels required")
            for point in points:
                if not isinstance(point,list) or len(point)!=2:raise ValueError("Invalid row label point")
                x,y=map(_number,point)
                if not 15<=x<=profile["width"]-15 or not 22<=y<=profile["height"]-15:raise ValueError("Row label outside canvas")
                if any(abs(x-a)<r+15 and abs(y-7-b)<r+15 for a,b,r in circles):raise ValueError("Row label overlaps seating area")
    elif not (0 <= profile["row_label_left"] < min(x-r-15 for x,y,r in circles) and
            max(x+r+15 for x,y,r in circles) < profile["row_label_right"] <= profile["width"]):
        raise ValueError("Row labels must be outside seating area")
    row_gaps={}
    for row in dict.fromkeys(r for r,n in actual):
        row_points=sorted((x,y,radius) for identity,(x,y,radius) in zip(actual,circles) if identity[0]==row)
        gaps=[b[0]-a[0]-a[2]-b[2]-stroke_width for a,b in zip(row_points,row_points[1:])]
        row_gaps[row]={"minimum":round(min(gaps),6),"maximum":round(max(gaps),6)} if gaps else None
    return {"status":"LOCAL_STRUCTURAL_PASS","seats":len(actual),"rows":len({r for r,n in actual}),
            "categories":len(palette),"default_category":default["label"],"category_seat_counts":{c["label"]:len(actual) if c["label"]==default["label"] else 0 for c in palette},"prices_assigned":False,"sections":1,"unique_ids":len(ids),"identity_mismatches":0,"coordinate_mismatches":0,
            "seat_outline_overlaps":overlaps,"row_visible_gaps":row_gaps,"test_only":True,"bil24_import":"NOT_TESTED"}
