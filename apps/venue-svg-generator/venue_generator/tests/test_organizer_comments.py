import pytest
from venue_generator.organizer_comments import interpret_comment

def parse(text):return interpret_comment(text,source_sha256="a"*64,region_ids=["A","B"],evidence_id="owner-reply")

def test_owner_tentative_comment_only_allows_nonbookable_decor():
    result=parse("Предполагаю, что это служебные места, которые не отдаются в продажу. Они могут быть на схеме, но не могут быть активными.")
    assert result["status"]=="NONBOOKABLE_DISPLAY_ALLOWED"
    assert result["bookable"] is False and result["visible"] is True
    assert result["certainty"]=="tentative" and result["source_fact_verified"] is False

@pytest.mark.parametrize("text",["Это служебные места, не продаются.","Места для персонала, не для продажи."])
def test_supported_explicit_service_comments(text):
    assert parse(text)["status"]=="NONBOOKABLE_DISPLAY_ALLOWED"

@pytest.mark.parametrize("text",["Это не служебные места, их не продают.","Возможно, места.","Это служебные места.","A не продаются, B можно продавать, служебные.","Не знаю, что это."])
def test_ambiguity_negation_and_mixed_scopes_require_clarification(text):
    assert parse(text)["status"]=="NEEDS_CLARIFICATION"
    assert parse(text)["bookable"] is False

def test_scope_and_comment_are_preserved():
    comment="Служебные места, не продаются."
    result=parse(comment)
    assert result["original_comment"]==comment and result["region_ids"]==["A","B"]
    assert len(result["comment_sha256"])==64


def test_question_is_not_executed_as_organizer_instruction():
    assert parse("Это служебные места, они не продаются?")["status"]=="NEEDS_CLARIFICATION"


def test_separate_group_roles_are_not_collapsed_to_one_answer():
    result=parse("A: служебные места, не продаются; B: пустое пространство.")
    assert result["status"]=="NEEDS_CLARIFICATION"
