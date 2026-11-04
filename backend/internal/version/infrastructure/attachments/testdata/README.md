# PDF test fixtures

Both files are real, valid PDFs generated with the script in this directory
(`generate_fixtures.py`, PyMuPDF 1.28 + Pillow, see the script header for the
exact provenance). They are committed so the Go test suite has no Python
dependency.

| File | What it exercises | Structure |
|---|---|---|
| `cn-cid.pdf` | Chinese text through a CID font plus `/ToUnicode` | 2 pages; embedded subset of a Chinese TrueType font exposed as a Type0 font with `/Encoding /Identity-H`, `/DescendantFonts [CIDFontType2]` and a `/ToUnicode` CMap (`beginbfrange`); no images |
| `image-only.pdf` | The "no text layer" rejection path | 1 page with one embedded PNG and no text operators |

Expected strings are asserted in `pdf_test.go`:

- page 1: `验收标准：附件中的中文必须可读`, `第一页：需求描述与测试步骤`
- page 2: `第二页：分页文本仍需保留`, `结束语：PDF 文本抽取检查完成`

The Chinese font is a locally installed TrueType CJK font (default
`C:\Windows\Fonts\simhei.ttf`); it is not redistributed here. Only the glyphs
used by the two pages are embedded (`Document.subset_fonts()`), so the fixture
stays ~12 KiB.

To regenerate (optional; requires Python with `pymupdf` and `pillow`):

```powershell
python generate_fixtures.py .
```

`rsc.io/pdf` (the pre-Task-20 parser) cannot decode the CJK text of
`cn-cid.pdf`; the CID/ToUnicode capable parser must report the Chinese strings
and `ErrPDFNoTextLayer` for `image-only.pdf`.
