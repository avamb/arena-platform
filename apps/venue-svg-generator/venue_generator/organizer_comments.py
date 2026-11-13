"""Conservative interpretation of supported organizer comments.

Unrecognized/mixed meanings request clarification. Free text never directly
changes sellable inventory. A tentative role can only produce nonbookable decor.
"""
import hashlib,re

VERSION="organizer-comment-ru-v1"

def interpret_comment(text,*,source_sha256,region_ids,evidence_id):
    if not isinstance(text,str) or not text.strip() or not evidence_id:
        raise ValueError("Original organizer comment and evidence ID required")
    if not re.fullmatch(r"[0-9a-f]{64}",source_sha256):raise ValueError("Source hash required")
    if not region_ids or any(not isinstance(k,str) or not k for k in region_ids) or len(set(region_ids))!=len(region_ids):raise ValueError("Explicit unique region scope required")
    normalized=" ".join(text.lower().replace("ё","е").split())
    base={"schema":"organizer-decision-v1","parser_version":VERSION,"source_sha256":source_sha256,
          "region_ids":list(region_ids),"evidence_id":evidence_id,"original_comment":text,
          "comment_sha256":hashlib.sha256(text.encode()).hexdigest(),"status":"NEEDS_CLARIFICATION",
          "role":None,"visible":None,"bookable":False,"certainty":"tentative" if re.search(r"предполага|возможно|может быть|наверно|думаю|похоже",normalized) else "stated",
          "source_fact_verified":False,"reasons":[]}
    if "?" in normalized:
        base["reasons"].append("question_is_not_an_instruction");return base
    positive=re.search(r"можно\s+(?:их\s+)?(?:продава|активир)|разреш(?:аю|ено)\s+(?:их\s+)?(?:прода|актив)|отдать\s+в\s+продажу",normalized)
    negative=re.search(r"не\s+отда\w*\s+в\s+продажу|не\s+прода\w*|не\s+для\s+продажи|не\s+(?:могут|должны|должен)\s+быть\s+актив\w*|нельзя\s+(?:их\s+)?(?:продава|активир)",normalized)
    service=re.search(r"служебн\w*|для\s+персонала|техническ\w*\s+мест",normalized)
    negated_service=re.search(r"не\s+служебн|неслужебн",normalized)
    individually_scoped=re.search(r"(?:^|[.;,]\s*)[абab]\s*(?:[-—–:]|это|служеб)|групп[а-я]*\s+[абab]\b",normalized)
    if positive or negated_service or (len(region_ids)>1 and individually_scoped) or re.search(r"не\s+места\b",normalized):
        base["reasons"].append("mixed_or_unsupported_sale_or_role_instruction");return base
    if not service or not negative:
        base["reasons"].append("explicit_service_role_and_non_sale_instruction_required");return base
    base.update(status="NONBOOKABLE_DISPLAY_ALLOWED",role="service",visible=True,bookable=False,
                review_state="PROVISIONAL_ROLE" if base["certainty"]=="tentative" else "ORGANIZER_STATED_ROLE",
                rendering="decor_only; no seat identity or functional import record",
                matched_evidence=[normalized[service.start():service.end()],normalized[negative.start():negative.end()]])
    return base
