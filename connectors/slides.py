"""Render a versioned, editable PPTX from explicit slide headings."""

import io
import unicodedata

from pptx import Presentation
from pptx.dml.color import RGBColor
from pptx.enum.text import PP_ALIGN
from pptx.util import Inches, Pt


class OutlineError(ValueError):
    pass


def width(text):
    # Conservative measure for mixed CJK and Latin lines at the fixed font size.
    units = 0
    for char in text:
        if unicodedata.category(char).startswith("M"):
            continue
        narrow = char.isascii() and not char.isupper() and char not in "wm@%"
        units += 1 if narrow else 2
    return units


def outline_pages(text):
    if len(text) > 64000 or len(text.splitlines()) > 400:
        raise OutlineError("提纲最多 64000 字、400 行")
    text = text.removeprefix("\ufeff").replace("\r\n", "\n").replace("\r", "\n")
    if any(
        (unicodedata.category(c) in ("Cc", "Zl", "Zp") and c not in "\n\t")
        for c in text
    ):
        raise OutlineError("提纲含有无法显示的控制字符")
    pages = []
    for raw in text.split("\n"):
        line = raw.expandtabs(4).strip()
        if not line:
            continue
        if line == "##" or line.startswith("## "):
            title = line[2:].strip()
            if not title or width(title) > 50:
                raise OutlineError(
                    f"第 {len(pages) + 1} 页需要短标题（最多 25 个全角字符）"
                )
            if len(pages) >= 40:
                raise OutlineError("一份课件最多 40 页，请拆分提纲")
            pages.append((title, []))
        elif line.startswith("##"):
            raise OutlineError("请在页标题标记 ## 后加一个空格")
        elif not pages:
            raise OutlineError("请用“## 页标题”开始提纲，再填写本页正文")
        else:
            pages[-1][1].append(line)
    if not pages:
        raise OutlineError("请至少提供一页提纲")
    for i, (_, lines) in enumerate(pages, 1):
        if not lines:
            raise OutlineError(f"第 {i} 页缺少正文")
        if len(lines) > 7 or any(width(line) > 72 for line in lines):
            raise OutlineError(
                f"第 {i} 页正文过长。每页最多 7 行，每行最多 36 个全角字符，请分行或拆页"
            )
    return pages


def render_outline(pages):
    deck = Presentation()
    deck.slide_width, deck.slide_height = Inches(13.333), Inches(7.5)
    for title, lines in pages:
        slide = deck.slides.add_slide(deck.slide_layouts[5])
        heading = slide.shapes.title
        heading.left, heading.top = Inches(0.8), Inches(0.55)
        heading.width, heading.height = Inches(11.73), Inches(0.9)
        heading.name = "页标题"
        p = heading.text_frame.paragraphs[0]
        p.alignment = PP_ALIGN.LEFT
        p.text = title
        p.font.size, p.font.bold = Pt(30), True
        p.font.color.rgb = RGBColor.from_string("151719")
        body = slide.shapes.add_textbox(
            Inches(0.8), Inches(1.8), Inches(11.73), Inches(5.1)
        )
        body.name = "正文"
        frame = body.text_frame
        frame.word_wrap = True
        for i, line in enumerate(lines):
            p = frame.add_paragraph() if i else frame.paragraphs[0]
            p.text = line
            p.font.size = Pt(20)
            p.font.color.rgb = RGBColor.from_string("292D31")
            p.line_spacing, p.space_after = Pt(29), Pt(10)
    output = io.BytesIO()
    deck.save(output)
    return output.getvalue()
