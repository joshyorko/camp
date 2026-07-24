#!/usr/bin/env python3
"""spritecheck: render a Camp sprite JSON to ANSI + PNG for visual review.

Sprite JSON schema (also consumed by the Go compositor):
{
  "name": "tent",
  "legend": { "A": "tent_light", "B": "tent_mid", ... },   # key -> palette.json color name
  "glyphs": ["  A  ", " AAA ", "AAAAA"],                    # what to draw; ' ' = transparent
  "colors": ["  p  ", " AAA ", "AAAAA"]                     # per-cell legend key; must align with glyphs
}
Rules:
  * glyphs[r][c] == ' '  -> transparent cell (background shows through)
  * otherwise the glyph is painted in the color legend[colors[r][c]]
  * a color key of ' ' (or missing) falls back to palette 'canvas'
  * every glyphs row and colors row must be the same length (sprite is rectangular)

Usage:
  spritecheck.py SPRITE.json OUT.png [--bg RRGGBB] [--scale N] [--pad N]
"""
import sys
import os
import json
import argparse

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from ansishot import render  # reuse the same font + cell renderer

HERE = os.path.dirname(os.path.abspath(__file__))


def hex_to_rgb(h):
    return tuple(int(h[k:k + 2], 16) for k in (0, 2, 4))


def load_palette():
    with open(os.path.join(HERE, "palette.json")) as f:
        return json.load(f)


def sprite_to_ansi(sprite, palette):
    legend = sprite.get("legend", {})
    glyphs = sprite["glyphs"]
    colors = sprite.get("colors", glyphs)
    lines = []
    for r, grow in enumerate(glyphs):
        crow = colors[r] if r < len(colors) else ""
        out = []
        cur = None
        for c, ch in enumerate(grow):
            if ch == " ":
                if cur is not None:
                    out.append("\x1b[0m"); cur = None
                out.append(" ")
                continue
            key = crow[c] if c < len(crow) else " "
            name = legend.get(key, "canvas")
            rgb = hex_to_rgb(palette.get(name, palette["canvas"]))
            if rgb != cur:
                r8, g8, b8 = rgb
                out.append(f"\x1b[38;2;{r8};{g8};{b8}m")
                cur = rgb
            out.append(ch)
        if cur is not None:
            out.append("\x1b[0m")
        lines.append("".join(out))
    return "\n".join(lines), max((len(g) for g in glyphs), default=0), len(glyphs)


def validate(sprite):
    glyphs = sprite["glyphs"]
    colors = sprite.get("colors", glyphs)
    if len(colors) != len(glyphs):
        raise SystemExit(f"row count mismatch: glyphs={len(glyphs)} colors={len(colors)}")
    for i, (g, c) in enumerate(zip(glyphs, colors)):
        if len(g) != len(c):
            raise SystemExit(f"row {i} width mismatch: glyph={len(g)} color={len(c)}\n  '{g}'\n  '{c}'")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("sprite")
    ap.add_argument("output")
    ap.add_argument("--bg", default=None)
    ap.add_argument("--scale", type=int, default=6)
    ap.add_argument("--pad", type=int, default=2)
    ap.add_argument("--font-size", type=int, default=16)
    args = ap.parse_args()

    palette = load_palette()
    with open(args.sprite) as f:
        sprite = json.load(f)
    validate(sprite)
    ansi, w, h = sprite_to_ansi(sprite, palette)
    bg = hex_to_rgb(args.bg) if args.bg else hex_to_rgb(palette["bg"])
    pad = args.pad
    padded = "\n".join([" " * (w + 2 * pad)] * pad
                       + [" " * pad + ln for ln in ansi.split("\n")]
                       + [" " * (w + 2 * pad)] * pad)
    ansi_path = os.path.splitext(args.output)[0] + ".ansi"
    with open(ansi_path, "w") as f:
        f.write(padded)
    render(padded, args.output, w + 2 * pad, h + 2 * pad, bg, args.scale, args.font_size)
    print(f"sprite '{sprite.get('name','?')}' {w}x{h} cells")


if __name__ == "__main__":
    main()
