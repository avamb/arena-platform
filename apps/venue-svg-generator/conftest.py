"""Skip tests that need private, git-ignored data (client XLSX, frozen results, seating_poc).

The upstream repository kept those files locally (see .gitignore). In the monorepo and
in CI they do not exist, so such tests are skipped instead of failing. On a machine that
has the data (ChatExport_2026-10-11/ and seating_poc/ next to this file) nothing is skipped.
"""
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parent
HAS_PRIVATE_DATA = (ROOT / "ChatExport_2026-10-11").is_dir() and (ROOT / "seating_poc").is_dir()

# Tests found to depend on private data (verified 14.10.2026 on a clean checkout).
NEEDS_PRIVATE_DATA = {
    "independent_evaluation/test_reference_evaluator.py::EvaluationTests::test_generator_does_not_import_evaluator",
    "seating_svg_renderer/tests/test_renderer.py::RendererTests::test_aribau_render_and_gaps",
    "seating_svg_renderer/tests/test_renderer.py::RendererTests::test_hunter_render",
    "seating_svg_renderer/tests/test_renderer.py::RendererTests::test_pass_with_missing_coordinate_fails_closed",
    "seating_svg_renderer/tests/test_renderer.py::RendererTests::test_quarantine_requires_explicit_mode",
    "seating_svg_renderer/tests/test_renderer.py::RendererTests::test_schema_and_source_guards",
    "xlsx_seating_adapter/tests/test_adapter.py::AdapterRegressionTests::test_aribau_semantics_match_phase8c",
    "xlsx_seating_adapter/tests/test_adapter.py::AdapterRegressionTests::test_hunter_semantics_match_phase7b",
    "xlsx_seating_adapter/tests/test_adapter.py::AdapterRegressionTests::test_merged_headers_service_cells_repeated_labels_and_gaps",
}


def pytest_collection_modifyitems(config, items):
    if HAS_PRIVATE_DATA:
        return
    skip = pytest.mark.skip(reason="needs private client data that is not in the repository")
    for item in items:
        if item.nodeid.replace("\\", "/") in NEEDS_PRIVATE_DATA:
            item.add_marker(skip)
