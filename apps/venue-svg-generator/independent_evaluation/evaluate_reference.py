"""Standalone evaluator. Never imported by extraction or prototype generation."""
from collections import Counter, defaultdict
from hashlib import sha256
import json
import math
from pathlib import Path
import re
from statistics import median
import xml.etree.ElementTree as ET
import numpy as np

ROOT = Path(__file__).resolve().parents[1]
MODEL = ROOT / 'seating_poc/output/seating-model.json'
REFERENCE = ROOT / 'ChatExport_2026-10-11/files/Palac_Akropolis.svg'
OUT = Path(__file__).resolve().parent / 'output'
SVG = '{http://www.w3.org/2000/svg}'
INK = '{http://www.inkscape.org/namespaces/inkscape}'


def matrix(text):
    result = np.eye(3)
    consumed = ''
    for match in re.finditer(r'([A-Za-z]+)\s*\(([^)]*)\)', text or ''):
        name, args = match.groups()
        v = [float(x) for x in re.findall(r'[-+]?(?:\d*\.\d+|\d+\.?\d*)(?:[eE][-+]?\d+)?', args)]
        t = np.eye(3)
        if name == 'matrix' and len(v) == 6:
            a,b,c,d,e,f = v; t = np.array([[a,c,e],[b,d,f],[0,0,1]])
        elif name == 'translate' and len(v) in (1,2):
            t[0,2] = v[0]; t[1,2] = v[1] if len(v) == 2 else 0
        elif name == 'scale' and len(v) in (1,2):
            t[0,0] = v[0]; t[1,1] = v[1] if len(v) == 2 else v[0]
        elif name == 'rotate' and len(v) in (1,3):
            a = math.radians(v[0]); t[:2,:2] = [[math.cos(a),-math.sin(a)],[math.sin(a),math.cos(a)]]
            if len(v) == 3:
                cx,cy = v[1:]; t[:2,2] = np.array([cx,cy]) - t[:2,:2] @ np.array([cx,cy])
        else:
            raise ValueError(f'Unsupported transform: {match.group()}')
        result = result @ t
        consumed += match.group()
    if re.sub(r'[\s,]', '', text or '') != re.sub(r'[\s,]', '', consumed):
        raise ValueError(f'Unparsed transform: {text}')
    return result


def reference_data(reference_path=REFERENCE):
    root = ET.parse(reference_path).getroot()
    groups, circles, excluded = defaultdict(list), [], Counter()
    def visit(node, parent_matrix, ancestors):
        world = parent_matrix @ matrix(node.get('transform'))
        if node.tag == SVG+'circle':
            reserved = next((a.get('id') for a in ancestors if a.get('id') in ('Legend','PriceCategory')), None)
            if reserved:
                excluded[reserved] += 1
            else:
                group = next((a for a in reversed(ancestors) if a.tag == SVG+'g' and a.get(INK+'label','').startswith('#') and a.findtext(SVG+'title') is not None), None)
                if group is None:
                    raise ValueError(f'Circle without contextual row: {node.get("id")}')
                xy = world @ [float(node.get('cx','0')),float(node.get('cy','0')),1]
                number = node.findtext(SVG+'title')
                if number is None:
                    raise ValueError('Seat without number title')
                record = {'xml_id': node.get('id'), 'number': number, 'position': xy[:2].tolist(),
                    'parent_group_id': group.get('id'), 'section_label': group.get(INK+'label'),
                    'row_title': group.findtext(SVG+'title'), 'circle_label': node.get(INK+'label'),
                    'world_matrix': world.tolist()}
                groups[group.get('id')].append(record); circles.append(record)
        for child in node:
            visit(child, world, ancestors + [node])
    visit(root, np.eye(3), [])
    ids = [n.get('id') for n in root.iter() if n.get('id')]
    return root, dict(groups), circles, dict(excluded), [x for x,c in Counter(ids).items() if c>1]


def fit_similarity(source, target):
    """One global uniform-scale orthogonal transform, reflection allowed and reported."""
    x,y = np.array(source),np.array(target)
    xc,yc = x.mean(axis=0),y.mean(axis=0)
    xx,yy = x-xc,y-yc
    u,s,vh = np.linalg.svd(xx.T @ yy)
    rotation = u @ vh
    scale = float(s.sum() / (xx*xx).sum())
    linear = scale * rotation.T
    translation = yc - linear @ xc
    return np.array([[*linear[0],translation[0]],[*linear[1],translation[1]],[0,0,1]])


def evaluate(model_path=MODEL, reference_path=REFERENCE, output_dir=OUT):
    # Paths supplied only by the independent evaluator, never by a converter.
    model_path, reference_path, output_dir = map(Path, (model_path, reference_path, output_dir))
    before = {str(p.relative_to(ROOT)): sha256(p.read_bytes()).hexdigest() for p in (model_path,reference_path)}
    model = json.loads(model_path.read_text(encoding='utf-8'))
    root, groups, seats, excluded, duplicate_ids = reference_data(reference_path)
    ref_sections = Counter(s['section_label'] for s in seats)
    parter = sorted([g for g in groups.values() if g[0]['section_label']=='#Parter'], key=lambda g: np.mean([s['position'][1] for s in g]))
    center = sorted([g for g in groups.values() if g[0]['section_label']=='#Balcony center'], key=lambda g: np.mean([s['position'][1] for s in g]))
    left = next(g for g in groups.values() if g[0]['section_label']=='#Balcony left')
    right = next(g for g in groups.values() if g[0]['section_label']=='#Balcony right')
    source_lookup = {s['seat_key']:s for s in model['seats']}
    source_rows = model['geometric_rows']
    mappings, mismatches, pairs, anchors = [], [], defaultdict(list), defaultdict(list)
    def compare(source_seats, ref, name, page):
        source_numbers = Counter(str(s['original_seat_number']) for s in source_seats)
        reference_numbers = Counter(s['number'] for s in ref)
        mismatch = {'source_only':list((source_numbers-reference_numbers).elements()),'reference_only':list((reference_numbers-source_numbers).elements())}
        mappings.append({'source_comparison_group':name, 'reference_group_id':ref[0]['parent_group_id'],
            'reference_section_label':ref[0]['section_label'], 'reference_row_title':ref[0]['row_title'],
            'source_count':len(source_seats), 'reference_count':len(ref), 'numbering_mismatch':mismatch,
            'status':'evaluation hypothesis only; not official source metadata'})
        if any(mismatch.values()): mismatches.append({'group':name,**mismatch})
        rby = {s['number']:s for s in ref}
        if len(rby) != len(ref) or len(source_numbers) != len(source_seats):
            mismatches.append({'group':name,'error':'nonunique number inside contextual comparison group'})
            return
        for s in source_seats:
            r = rby.get(str(s['original_seat_number']))
            if r: pairs[page].append((s,r))
        anchors[page].append((np.mean([[s['position_pt']['x'],s['position_pt']['y']] for s in source_seats],axis=0).tolist(),
                              np.mean([r['position'] for r in ref],axis=0).tolist()))
    p1 = [r for r in source_rows if r['source_page']==1]
    p2 = [r for r in source_rows if r['source_page']==2]
    if len(p1)!=len(parter) or len(center)!=5 or len(p2)!=15:
        raise ValueError('Architecture decision required: grouping fingerprint differs from expected diagnostic structure')
    for row, ref in zip(p1,parter): compare([source_lookup[k] for k in row['seat_keys_left_to_right']],ref,row['geometric_row_id'],1)
    # Evaluation-only projection of horizontal pairs into vertical chains.
    # This does not alter or feed the extraction/generation pipeline.
    for side,ref in [(0,left),(1,right)]:
        chain = [source_lookup[r['seat_keys_left_to_right'][side]] for r in p2[:10]]
        compare(chain,ref,f'P2-G01..G10:{"left" if side==0 else "right"}:evaluation-only',2)
    for row,ref in zip(p2[10:],center): compare([source_lookup[k] for k in row['seat_keys_left_to_right']],ref,row['geometric_row_id'],2)
    deviations = []
    for page in (1,2):
        source_anchors = [a[0] for a in anchors[page]]
        target_anchors = [a[1] for a in anchors[page]]
        rank = int(np.linalg.matrix_rank(np.array(source_anchors)-np.mean(source_anchors,axis=0)))
        # Parterre row centroids are nearly collinear: use shared-number endpoint
        # centroids on both sides as anchors to constrain width/orientation.
        if page == 1:
            source_anchors, target_anchors = [], []
            for side in (0,-1):
                candidates=[]
                for row,ref in zip(p1,parter):
                    ss=sorted([source_lookup[k] for k in row['seat_keys_left_to_right']],key=lambda s:s['position_pt']['x'])
                    s=ss[side]; rr=next(r for r in ref if r['number']==str(s['original_seat_number']))
                    candidates.append((s,rr))
                source_anchors.extend([[s['position_pt']['x'],s['position_pt']['y']] for s,r in candidates])
                target_anchors.extend([r['position'] for s,r in candidates])
            rank=int(np.linalg.matrix_rank(np.array(source_anchors)-np.mean(source_anchors,axis=0)))
        t=fit_similarity(source_anchors,target_anchors)
        distances=[]; residuals=[]
        for s,r in pairs[page]:
            xy=t @ [s['position_pt']['x'],s['position_pt']['y'],1]
            delta=xy[:2]-np.array(r['position']); d=float(np.linalg.norm(delta));distances.append(d)
            residuals.append({'source_seat_key':s['seat_key'],'reference_xml_id':r['xml_id'],'reference_parent':r['parent_group_id'],
                'original_number':s['original_number_text'],'aligned_position':xy[:2].tolist(),'reference_position':r['position'],
                'delta':delta.tolist(),'distance_reference_units':d})
        ref_groups=parter if page==1 else [left,right]+center
        steps=[]
        for g in ref_groups:
            ordered=sorted(g,key=lambda r:int(r['number']))
            steps.extend(float(np.linalg.norm(np.array(a['position'])-np.array(b['position']))) for a,b in zip(ordered,ordered[1:]) if int(b['number'])-int(a['number'])==1)
        spacing=median(steps)
        med,mx=median(distances),max(distances)
        accepted=rank==2 and med<=0.25*spacing and mx<=0.5*spacing
        deviations.append({'source_page':page,'candidate_transform_source_pt_to_reference_viewbox':t.tolist(),
            'inverse_transform':np.linalg.inv(t).tolist(),'transform_type':'uniform similarity, rotation/reflection allowed',
            'determinant':float(np.linalg.det(t[:2,:2])),'anchor_count':len(source_anchors),'anchor_rank':rank,
            'anchor_rule':'first/last numbered seat per parterre row' if page==1 else 'centroids of five center rows and two side chains',
            'pair_count':len(distances),'reference_neighbor_spacing':spacing,'median_deviation':med,'max_deviation':mx,
            'median_in_neighbor_spacings':med/spacing,'max_in_neighbor_spacings':mx/spacing,
            'thresholds':{'median_spacings':0.25,'max_spacings':0.5},
            'alignment_accepted_under_poc_thresholds':accepted,
            'limitation':None if accepted else 'No reliable single global alignment under predeclared PoC tolerances. Candidate residuals are diagnostic only; no per-row fitting.',
            'seat_residuals':residuals})
    contextual_duplicates=[]
    group_inventory=[]
    for gid,g in groups.items():
        dup=[n for n,c in Counter(s['number'] for s in g).items() if c>1]
        if dup:contextual_duplicates.append({'reference_group':gid,'numbers':dup})
        numbers=sorted({int(s['number']) for s in g})
        axis=1 if g[0]['section_label'] in ('#Balcony left','#Balcony right') else 0
        group_inventory.append({'xml_id':gid,'section_label':g[0]['section_label'],'row_title':g[0]['row_title'],
            'seat_count':len(g),'spatial_order_axis':'y' if axis else 'x',
            'numbers_in_spatial_order':[s['number'] for s in sorted(g,key=lambda s:s['position'][axis])],
            'gaps_inside_observed_range':sorted(set(range(min(numbers),max(numbers)+1))-set(numbers)),
            'duplicate_numbers':dup})
    report={'input_sha256':before,'expected_total':260,'model_total':len(model['seats']),'reference_seats':len(seats),
        'reference_circle_total':len(list(root.iter(SVG+'circle'))),'excluded_circles':excluded,
        'reference_sector_counts':dict(ref_sections),'model_page_counts':dict(Counter(s['source_page'] for s in model['seats'])),
        'reference_viewbox':root.get('viewBox'),'reference_document_size':{'width':root.get('width'),'height':root.get('height')},
        'reference_duplicate_xml_ids':duplicate_ids,'reference_duplicate_numbers_inside_parent_groups':contextual_duplicates,
        'reference_groups':group_inventory,'reference_seat_inventory':seats,
        'bil24_context':{'row_group_label_attribute':INK+'label','row_identity_element':SVG+'title',
            'seat_number_element':SVG+'title','missing_seat_title_count':sum(s['number'] is None for s in seats),
            'circle_label_present_count':sum(s['circle_label'] is not None for s in seats),
            'reserved_group_ids':list(excluded),'xml_ids_are_not_platform_seat_ids':True},
        'group_mappings':mappings,'numbering_mismatches':mismatches,'geometry':deviations,
        'grouping_difference':'P2 has 15 horizontal bands; reference has two 10-seat side groups and five 14-seat central rows. Only evaluation projection performed.',
        'semantic_limitations':['Reference titles/labels are reference facts, not approved official metadata of the extracted model.',
            'Source technical seat keys and reference XML IDs are distinct; equality is not expected.',
            'No reference category/price/status data transferred to model or generator.'],
        'architectural_contradiction_requiring_design_decision':False}
    output_dir.mkdir(parents=True, exist_ok=True)
    (output_dir/'reference-evaluation.json').write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
    assert before=={str(p.relative_to(ROOT)):sha256(p.read_bytes()).hexdigest() for p in (model_path,reference_path)}
    print(json.dumps({k:report[k] for k in ('model_total','reference_seats','reference_sector_counts','numbering_mismatches')},ensure_ascii=False))
    for g in deviations:print({k:g[k] for k in ('source_page','median_deviation','max_deviation','median_in_neighbor_spacings','max_in_neighbor_spacings','alignment_accepted_under_poc_thresholds')})
    return report


if __name__=='__main__':
    evaluate()
