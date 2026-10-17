#!/usr/bin/env python3
"""Builds the Waymark Proxy logo files from scratch.

The mark is a "W" drawn as one route in two strokes (navy and teal: the two
nodes of an active/standby pair). The wordmark is Inter (SIL Open Font
License), converted to outlines so the SVGs need no fonts.

    pip install fonttools uharfbuzz cairosvg
    python3 branding/build_logos.py            # writes branding/ and src/web/img/

Colours: navy #123B5C, teal #14B8A6; on dark backgrounds the navy stroke
becomes #CDEFEA and the text #E3F1F4.
"""
import os
import sys

import cairosvg
import uharfbuzz as hb
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
from fontTools.ttLib import TTFont

NAVY, TEAL, PALE, LIGHT_TEXT = "#123B5C", "#14B8A6", "#CDEFEA", "#E3F1F4"
FONT_DIR = os.environ.get("INTER_DIR", "/usr/share/fonts/opentype/inter")
BOLD = os.path.join(FONT_DIR, "Inter-ExtraBold.otf")
REGULAR = os.path.join(FONT_DIR, "Inter-Regular.otf")

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
IMG = os.path.join(ROOT, "src", "web", "img")


def mark_paths(first, second, offset_x=0, offset_y=0, scale=1.0):
    t = f'transform="translate({offset_x} {offset_y}) scale({scale})"' if (offset_x or offset_y or scale != 1) else ""
    common = 'fill="none" stroke-width="8.5" stroke-linecap="round" stroke-linejoin="round"'
    return (f'<g {t}><path d="M9 17 L21 47 L32 23" stroke="{first}" {common}/>'
            f'<path d="M32 23 L43 47 L55 17" stroke="{second}" {common}/></g>')


def text_path(font_file, text, size, x, baseline):
    """Shapes text with HarfBuzz (kerning included) and returns (svg path d, advance)."""
    blob = hb.Blob.from_file_path(font_file)
    face = hb.Face(blob)
    font = hb.Font(face)
    buf = hb.Buffer()
    buf.add_str(text)
    buf.guess_segment_properties()
    hb.shape(font, buf, {"kern": True, "liga": True})
    tt = TTFont(font_file)
    gs = tt.getGlyphSet()
    order = tt.getGlyphOrder()
    upem = tt["head"].unitsPerEm
    k = size / upem
    pen = SVGPathPen(gs)
    cursor = 0
    for info, pos in zip(buf.glyph_infos, buf.glyph_positions):
        name = order[info.codepoint]
        tp = TransformPen(pen, (k, 0, 0, -k, x + (cursor + pos.x_offset) * k, baseline - pos.y_offset * k))
        gs[name].draw(tp)
        cursor += pos.x_advance
    return pen.getCommands(), cursor * k


def lockup(dark):
    first, text = (PALE, LIGHT_TEXT) if dark else (NAVY, NAVY)
    size, baseline, x0 = 40, 46.5, 76
    d1, w1 = text_path(BOLD, "Waymark", size, x0, baseline)
    gap = size * 0.26
    d2, w2 = text_path(REGULAR, "Proxy", size, x0 + w1 + gap, baseline)
    width = round(x0 + w1 + gap + w2 + 4)
    svg = (f'<svg xmlns="http://www.w3.org/2000/svg" width="{width * 3}" height="192" viewBox="0 0 {width} 64">'
           f'<title>Waymark Proxy</title>{mark_paths(first, TEAL)}'
           f'<path d="{d1}" fill="{text}"/><path d="{d2}" fill="{text}" fill-opacity="0.72"/></svg>')
    return svg, width


def mark(dark):
    return (f'<svg xmlns="http://www.w3.org/2000/svg" width="512" height="512" viewBox="0 0 64 64">'
            f'<title>Waymark Proxy</title>{mark_paths(PALE if dark else NAVY, TEAL)}</svg>')


def tile():
    # Favicon / app icon: the mark on a navy rounded square, readable on light and dark tab bars
    return (f'<svg xmlns="http://www.w3.org/2000/svg" width="512" height="512" viewBox="0 0 64 64">'
            f'<title>Waymark Proxy</title><rect x="2" y="2" width="60" height="60" rx="14" fill="{NAVY}"/>'
            f'{mark_paths(PALE, TEAL, offset_x=7.04, offset_y=7.04, scale=0.78)}</svg>')


def write(path, data):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", newline="\n") as f:
        f.write(data + "\n")


def png(svg, path, width=None, height=None):
    cairosvg.svg2png(bytestring=svg.encode(), write_to=path, output_width=width, output_height=height)


def main():
    light, w = lockup(False)
    dark, _ = lockup(True)
    m, md, t = mark(False), mark(True), tile()
    # Sources (branding/)
    for name, svg in {"logo.svg": light, "logo-dark.svg": dark, "mark.svg": m, "mark-dark.svg": md, "icon-tile.svg": t}.items():
        write(os.path.join(HERE, name), svg)
    png(light, os.path.join(HERE, "logo.png"), height=200)
    png(dark, os.path.join(HERE, "logo-dark.png"), height=200)
    png(t, os.path.join(HERE, "icon-tile.png"), 512, 512)
    # Web UI (src/web/img), same file names as before
    write(os.path.join(IMG, "logo.svg"), light)
    write(os.path.join(IMG, "logo_white.svg"), dark)
    png(light, os.path.join(IMG, "logo.png"), height=200)
    png(dark, os.path.join(IMG, "logo_white.png"), height=200)
    write(os.path.join(IMG, "public", "logo.svg"), light)
    png(light, os.path.join(IMG, "public", "logo.png"), height=200)
    write(os.path.join(IMG, "public", "icon.svg"), m)
    png(m, os.path.join(IMG, "public", "icon.png"), 512, 512)
    png(m, os.path.join(IMG, "icon.png"), 512, 512)
    png(m, os.path.join(IMG, "small_icon.png"), 128, 128)
    png(t, os.path.join(IMG, "desktop_icon.png"), 128, 128)
    png(t, os.path.join(ROOT, "src", "web", "favicon.png"), 512, 512)
    print("logo files written; lockup width", w)


if __name__ == "__main__":
    sys.exit(main())
