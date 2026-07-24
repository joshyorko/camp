#!/usr/bin/env python3
"""ansishot: render ANSI truecolor text into a PNG that looks like a terminal.

Usage:
    ansishot.py INPUT.ansi OUTPUT.png [--cols N] [--rows N] [--bg RRGGBB] [--scale S]

It parses SGR sequences (\x1b[...m) for truecolor / 256 / basic foreground and
background plus bold, and paints each terminal cell with a fixed-width mono
font. Cells with no explicit background inherit the scene default so we can
verify the composed art the way a real terminal shows it.
"""
import sys
import re
import argparse
from PIL import Image, ImageDraw, ImageFont

SGR = re.compile(r"\x1b\[([0-9;]*)m")
# other CSI (cursor moves, clears) — stripped for the static capture
OTHER_CSI = re.compile(r"\x1b\[[0-9;?]*[A-Za-z]")

FONT_CANDIDATES = [
    "/usr/share/fonts/jetbrains-mono-fonts/JetBrainsMono-Regular.otf",
    "/usr/share/fonts/jetbrains-mono-fonts/JetBrainsMono-Medium.otf",
    "/usr/share/fonts/google-noto-vf/NotoSansMono[wght].ttf",
    "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf",
]
BOLD_CANDIDATES = [
    "/usr/share/fonts/jetbrains-mono-fonts/JetBrainsMono-Bold.otf",
    "/usr/share/fonts/jetbrains-mono-fonts/JetBrainsMono-ExtraBold.otf",
]
# Symbola / Noto Symbols 2 cover box / block / geometric / dingbat glyphs a
# programming font renders as .notdef tofu. Ordered most-complete first.
FALLBACK_CANDIDATES = [
    "/usr/share/fonts/gdouros-symbola/Symbola.ttf",
    "/usr/share/fonts/google-noto/NotoSansSymbols2-Regular.ttf",
    "/usr/share/fonts/google-noto-vf/NotoSansMono[wght].ttf",
    "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf",
]

BASIC = {
    30: (30, 30, 30), 31: (205, 49, 49), 32: (13, 188, 121), 33: (229, 229, 16),
    34: (36, 114, 200), 35: (188, 63, 188), 36: (17, 168, 205), 37: (229, 229, 229),
    90: (102, 102, 102), 91: (241, 76, 76), 92: (35, 209, 139), 93: (245, 245, 67),
    94: (59, 142, 234), 95: (214, 112, 214), 96: (41, 184, 219), 97: (255, 255, 255),
}


def load_font(candidates, size):
    for path in candidates:
        try:
            return ImageFont.truetype(path, size)
        except OSError:
            continue
    return ImageFont.load_default()


def is_wide(cp):
    return (
        (0x1100 <= cp <= 0x115F) or (0x2329 <= cp <= 0x232A)
        or (0x2E80 <= cp <= 0xA4CF) or (0xAC00 <= cp <= 0xD7A3)
        or (0xF900 <= cp <= 0xFAFF) or (0xFE10 <= cp <= 0xFE19)
        or (0xFE30 <= cp <= 0xFE6F) or (0xFF00 <= cp <= 0xFF60)
        or (0xFFE0 <= cp <= 0xFFE6) or (0x1F300 <= cp <= 0x1FAFF)
        or (0x20000 <= cp <= 0x3FFFD)
    )


def parse_256(n):
    if n < 16:
        return BASIC.get(n if n < 8 else n + 82, (200, 200, 200))
    if n < 232:
        n -= 16
        r, g, b = n // 36, (n % 36) // 6, n % 6
        steps = [0, 95, 135, 175, 215, 255]
        return (steps[r], steps[g], steps[b])
    v = 8 + (n - 232) * 10
    return (v, v, v)


def render(text, out_path, cols, rows, default_bg, scale, font_size):
    font = load_font(FONT_CANDIDATES, font_size)
    bold = load_font(BOLD_CANDIDATES, font_size)
    fallback = load_font(FALLBACK_CANDIDATES, font_size)
    fallback_bold = fallback

    # Fixed cell metrics from an em box.
    probe = Image.new("RGB", (10, 10))
    d = ImageDraw.Draw(probe)
    bbox = d.textbbox((0, 0), "M", font=font)
    cell_w = max(1, bbox[2] - bbox[0])
    ascent, descent = font.getmetrics()
    cell_h = ascent + descent + max(2, font_size // 6)

    default_fg = (220, 224, 228)
    lines = text.split("\n")
    if rows <= 0:
        rows = len(lines)
    if cols <= 0:
        cols = max((visible_len(l) for l in lines), default=80)

    W = cols * cell_w
    H = rows * cell_h
    img = Image.new("RGB", (W, H), default_bg)
    draw = ImageDraw.Draw(img)

    # A missing glyph renders as the font's .notdef box, which still has a
    # bounding box — so bbox alone can't detect tofu. Compare each glyph's
    # raster bytes against a codepoint we know is absent (U+E000, private use).
    # If they match, the font is drawing .notdef and we must fall back.
    def mask_bytes(mask):
        return Image.frombytes("L", mask.size, bytes(mask)).tobytes()

    def notdef_signature(fnt):
        try:
            return mask_bytes(fnt.getmask(""))
        except Exception:
            return None

    notdef_cache = {}

    def has_glyph(fnt, ch):
        if ch == " ":
            return True
        try:
            mask = fnt.getmask(ch)
        except Exception:
            return False
        if mask.getbbox() is None:
            return False
        sig = notdef_cache.get(id(fnt))
        if sig is None:
            sig = notdef_signature(fnt)
            notdef_cache[id(fnt)] = sig or b""
        return mask_bytes(mask) != sig

    for row, line in enumerate(lines[:rows]):
        fg = default_fg
        bg = None
        is_bold = False
        col = 0
        i = 0
        while i < len(line):
            m = SGR.match(line, i)
            if m:
                codes = [int(c) for c in m.group(1).split(";") if c != ""] or [0]
                j = 0
                while j < len(codes):
                    c = codes[j]
                    if c == 0:
                        fg, bg, is_bold = default_fg, None, False
                    elif c == 1:
                        is_bold = True
                    elif c == 22:
                        is_bold = False
                    elif c == 39:
                        fg = default_fg
                    elif c == 49:
                        bg = None
                    elif c in BASIC:
                        fg = BASIC[c]
                    elif c + 10 in BASIC and 40 <= c <= 47 or 100 <= c <= 107:
                        bg = BASIC.get(c - 10, bg)
                    elif c == 38 and j + 1 < len(codes):
                        if codes[j + 1] == 2 and j + 4 < len(codes):
                            fg = (codes[j + 2], codes[j + 3], codes[j + 4]); j += 4
                        elif codes[j + 1] == 5 and j + 2 < len(codes):
                            fg = parse_256(codes[j + 2]); j += 2
                    elif c == 48 and j + 1 < len(codes):
                        if codes[j + 1] == 2 and j + 4 < len(codes):
                            bg = (codes[j + 2], codes[j + 3], codes[j + 4]); j += 4
                        elif codes[j + 1] == 5 and j + 2 < len(codes):
                            bg = parse_256(codes[j + 2]); j += 2
                    j += 1
                i = m.end()
                continue
            om = OTHER_CSI.match(line, i)
            if om:
                i = om.end()
                continue
            ch = line[i]
            i += 1
            cp = ord(ch)
            w = 2 if is_wide(cp) else 1
            x0 = col * cell_w
            y0 = row * cell_h
            if bg is not None:
                draw.rectangle([x0, y0, x0 + w * cell_w - 1, y0 + cell_h - 1], fill=bg)
            if ch not in (" ", "\t"):
                use = bold if is_bold else font
                use_fb = fallback_bold if is_bold else fallback
                chosen = use if has_glyph(use, ch) else use_fb
                draw.text((x0, y0), ch, font=chosen, fill=fg)
            col += w

    if scale != 1:
        img = img.resize((W * scale, H * scale), Image.LANCZOS)
    img.save(out_path)
    print(f"wrote {out_path} ({img.width}x{img.height}, {cols}x{rows} cells)")


def visible_len(line):
    line = SGR.sub("", line)
    line = OTHER_CSI.sub("", line)
    n = 0
    for ch in line:
        n += 2 if is_wide(ord(ch)) else 1
    return n


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("input")
    ap.add_argument("output")
    ap.add_argument("--cols", type=int, default=0)
    ap.add_argument("--rows", type=int, default=0)
    ap.add_argument("--bg", default="0b1020")
    ap.add_argument("--scale", type=int, default=2)
    ap.add_argument("--font-size", type=int, default=16)
    args = ap.parse_args()

    with open(args.input, "r", encoding="utf-8") as f:
        text = f.read()
    # Drop screen-clear / home so a capture shows the final composed frame only.
    if "\x1b[2J\x1b[H" in text:
        text = text.split("\x1b[2J\x1b[H")[-1]
    text = text.replace("<ESC>", "\x1b")
    bg = tuple(int(args.bg[k:k + 2], 16) for k in (0, 2, 4))
    render(text, args.output, args.cols, args.rows, bg, args.scale, args.font_size)


if __name__ == "__main__":
    main()
