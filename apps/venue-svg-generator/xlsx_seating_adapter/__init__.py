"""Limited, source-only XLSX to Seating JSON adapter."""

from .core import ADAPTER_VERSION, SCHEMA_VERSION, extract_workbook, inspect_workbook, validate_model, write_json

__all__ = ["ADAPTER_VERSION", "SCHEMA_VERSION", "extract_workbook", "inspect_workbook", "validate_model", "write_json"]
