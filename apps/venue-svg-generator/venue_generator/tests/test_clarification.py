import copy
import pytest
from venue_generator.clarification import build_clarification

def issue():return {"source_sha256":"a"*64,"evidence":"source image checked","visible_groups":[{"id":"A","bbox":[10,20,30,90],"cell_count":10,"description":"левая пустая ячейка в каждом ряду"},{"id":"B","bbox":[30,70,80,90],"cell_count":3,"description":"ещё три пустые ячейки в последнем ряду"}],"other_source_text":[{"text":"7 unnumbered","visibility":"NOT_CONFIRMED_ON_RENDER","mapped_to_seats":False}]}

def test_question_names_visible_regions_not_hidden_counts():
    source=issue();before=copy.deepcopy(source);result=build_clarification(source)
    assert "10 ячеек" in result["question"] and "3 ячеек" in result["question"]
    assert "7 unnumbered" not in result["question"]
    assert result["status"]=="NEEDS_HUMAN_ANSWER" and source==before
    assert all(r["answer"] is None for r in result["regions"])

def test_unlocated_count_cannot_become_user_question():
    source=issue();source["visible_groups"]=[]
    with pytest.raises(ValueError):build_clarification(source)

@pytest.mark.parametrize("box",[[1,2,1,4],[1,float("nan"),3,4],None])
def test_invalid_region_rejected(box):
    source=issue();source["visible_groups"][0]["bbox"]=box
    with pytest.raises(ValueError):build_clarification(source)
