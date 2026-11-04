"""Regenerate the PDF fixtures used by pdf_test.go.

These are real, valid PDFs, not hand-rolled byte strings:

* ``cn-cid.pdf`` embeds a subset of a Chinese TrueType font as a Type0
  (``/Encoding /Identity-H``) CID font with a ``/ToUnicode`` CMap, i.e. the
  shape Word/LibreOffice produce for CJK documents. It has two pages.
* ``image-only.pdf`` embeds a PNG and has no text operators at all, so it has
  no text layer.

The fixtures are committed, so the Go test suite does not need Python.
To regenerate them (PyMuPDF >= 1.21, Pillow, and a CJK font file):

    python generate_fixtures.py <testdata-dir> [font-path]

Default font: C:\\Windows\\Fonts\\simhei.ttf. Any TrueType CJK font works; the
expected strings live in pdf_test.go.
"""

import io
import os
import sys

import pymupdf
from PIL import Image, ImageDraw

DEFAULT_FONT = r"C:\Windows\Fonts\simhei.ttf"
PAGE_W, PAGE_H = 595, 842

PAGE_ONE_LINES = (
    "验收标准：附件中的中文必须可读",
    "第一页：需求描述与测试步骤",
)
PAGE_TWO_LINES = (
    "第二页：分页文本仍需保留",
    "结束语：PDF 文本抽取检查完成",
)


def chinese_cid_pdf(path: str, font_path: str) -> None:
    doc = pymupdf.open()
    doc.set_metadata({"title": "EzTest CID/ToUnicode fixture", "producer": "PyMuPDF"})
    for lines in (PAGE_ONE_LINES, PAGE_TWO_LINES):
        page = doc.new_page(width=PAGE_W, height=PAGE_H)
        page.insert_font(fontname="fixture_cjk", fontfile=font_path)
        y = 760
        for line in lines:
            page.insert_text((72, y), line, fontname="fixture_cjk", fontsize=16)
            y -= 30
    # Embed only the glyphs the two pages use; the full CJK font is ~10 MiB.
    doc.subset_fonts()
    doc.save(path, garbage=4, deflate=True)
    doc.close()


def image_only_pdf(path: str) -> None:
    image = Image.new("RGB", (240, 120), (230, 240, 250))
    draw = ImageDraw.Draw(image)
    draw.rectangle([20, 20, 220, 100], outline=(30, 90, 160), width=3)
    draw.line([30, 60, 210, 60], fill=(30, 90, 160), width=2)
    buffer = io.BytesIO()
    image.save(buffer, format="PNG")

    doc = pymupdf.open()
    doc.set_metadata({"title": "EzTest image-only fixture", "producer": "PyMuPDF"})
    page = doc.new_page(width=PAGE_W, height=PAGE_H)
    page.insert_image(pymupdf.Rect(72, 600, 432, 780), stream=buffer.getvalue())
    doc.save(path, garbage=4, deflate=True)
    doc.close()


def main() -> None:
    if len(sys.argv) < 2:
        raise SystemExit("usage: python generate_fixtures.py <testdata-dir> [font-path]")
    out_dir = sys.argv[1]
    font_path = sys.argv[2] if len(sys.argv) > 2 else DEFAULT_FONT
    if not os.path.isfile(font_path):
        raise SystemExit(f"font not found: {font_path}")
    chinese_cid_pdf(os.path.join(out_dir, "cn-cid.pdf"), font_path)
    image_only_pdf(os.path.join(out_dir, "image-only.pdf"))


if __name__ == "__main__":
    main()
