"""User questions must refer to verified visible source regions, not raw AI counts."""
import math

def build_clarification(issue):
    if not issue.get("source_sha256") or not issue.get("evidence"):
        raise ValueError("Clarification must bind source and visible evidence")
    groups=issue.get("visible_groups")
    if not isinstance(groups,list) or not groups:raise ValueError("Visible groups required; hidden text alone is insufficient")
    ids=set();regions=[]
    for group in groups:
        key=group.get("id");box=group.get("bbox");count=group.get("cell_count")
        if not isinstance(key,str) or not key or key in ids:raise ValueError("Unique region IDs required")
        if not isinstance(box,list) or len(box)!=4 or any(isinstance(v,bool) or not isinstance(v,(int,float)) or not math.isfinite(v) for v in box) or not 0<=box[0]<box[2] or not 0<=box[1]<box[3]:raise ValueError("Visible source bbox required")
        if isinstance(count,bool) or not isinstance(count,int) or count<=0 or not group.get("description"):raise ValueError("Visible cell count and plain description required")
        ids.add(key);regions.append({"id":key,"bbox":box,"cell_count":count,"description":group["description"],"answer":None})
    descriptions="; ".join(f"{r['id']}: {r['description']} ({r['cell_count']} ячеек)" for r in regions)
    return {"schema":"source-clarification-v1","source_sha256":issue["source_sha256"],"status":"NEEDS_HUMAN_ANSWER",
            "question":"Что означают пустые ячейки, выделенные на изображении? "+descriptions+". Это свободное пространство или реальные места?",
            "regions":regions,"answer_options":[{"value":"not_seats","label":"Это не места — сохранить пространство"},{"value":"seats","label":"Это места — уточнить статус и нумерацию"},{"value":"unknown","label":"Не знаю — запросить информацию у площадки"}],
            "follow_up_if_seats":"Какой статус и обозначение должно быть у каждой группы? Без подтверждённого правила номера не назначаются.",
            "other_source_text":issue.get("other_source_text",[]),"policy":"Unmapped or render-unconfirmed text is a hypothesis, never a visually located seat group."}
