"""Dispatch existing CLIs without bypassing their acceptance gates."""
import sys

def main(argv=None):
    args = list(sys.argv[1:] if argv is None else argv)
    if not args or args[0] not in {"xlsx", "pdf", "jpg", "bil24"}:
        print("Usage: python -m venue_generator {xlsx|pdf|jpg|bil24} <adapter arguments>")
        return 1
    format_name = args.pop(0)
    if format_name == "xlsx":
        from m1_product.cli import main as run
    elif format_name == "pdf":
        from pdf_seating_adapter.cli import main as run
    elif format_name == "jpg":
        from raster_seating_adapter.cli import main as run
    else:
        from bil24_svg_exporter.cli import main as run
    return run(args)
