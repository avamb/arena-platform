"""Content sniffing for uploaded files. The client's `kinds` list and the file name are hints only.

    xlsx   a ZIP archive that holds `xl/workbook.xml`
    pdf    starts with `%PDF-`
    image  PNG or JPEG magic bytes

Anything else is not a plan the service can read (`None`).
"""

from __future__ import annotations

import zipfile
from io import BytesIO

# Guard against a zip bomb: openpyxl inflates the whole workbook into memory.
MAX_UNCOMPRESSED_BYTES = 200 * 1024 * 1024

PNG_MAGIC = b"\x89PNG\r\n\x1a\n"
JPEG_MAGIC = b"\xff\xd8\xff"


class UnpackedTooLarge(Exception):
    """The archive would inflate to more than MAX_UNCOMPRESSED_BYTES."""


def detect_kind(data: bytes) -> str | None:
    """Return `xlsx`, `pdf`, `image` or None. Raises UnpackedTooLarge for a zip bomb."""
    head = data[:8]
    if head.startswith(b"%PDF-"):
        return "pdf"
    if head.startswith(PNG_MAGIC) or head.startswith(JPEG_MAGIC):
        return "image"
    if head.startswith(b"PK\x03\x04"):
        try:
            with zipfile.ZipFile(BytesIO(data)) as archive:
                infos = archive.infolist()
                if sum(item.file_size for item in infos) > MAX_UNCOMPRESSED_BYTES:
                    raise UnpackedTooLarge()
                if any(item.filename == "xl/workbook.xml" for item in infos):
                    return "xlsx"
        except zipfile.BadZipFile:
            return None
    return None
