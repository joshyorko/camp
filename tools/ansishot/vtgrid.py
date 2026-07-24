#!/usr/bin/env python3
"""vtgrid: a minimal VT100/xterm screen emulator.

Bubble Tea v2 renders by moving the cursor and rewriting changed cells rather
than clearing and reprinting whole frames, so replaying its raw PTY byte stream
linearly overlaps frames. vtgrid interprets the byte stream into a fixed cell
grid — the way a real terminal does — and emits the final screen as ANSI (one
SGR-coalesced line per row). It supports the subset Bubble Tea uses: CUP/CUU/
CUD/CUF/CUB cursor moves, ED/EL erases, SGR colors (truecolor/256/basic/reset),
alt-screen enter/leave, and plain text with wrap.

Usage:
  vtgrid.py --cols 120 --rows 40 IN.raw OUT.ansi
"""
import argparse
import re
import sys

CSI = "\x1b["
DEFAULT_FG = (220, 224, 228)


class Cell:
    __slots__ = ("ch", "fg", "bold")

    def __init__(self):
        self.ch = " "
        self.fg = None
        self.bold = False


class Screen:
    def __init__(self, cols, rows):
        self.cols = cols
        self.rows = rows
        self.grid = [[Cell() for _ in range(cols)] for _ in range(rows)]
        self.alt = [[Cell() for _ in range(cols)] for _ in range(rows)]
        self.using_alt = False
        self.cx = 0
        self.cy = 0
        self.fg = None
        self.bold = False

    def cur(self):
        return self.alt if self.using_alt else self.grid

    def clamp(self):
        self.cx = max(0, min(self.cols - 1, self.cx))
        self.cy = max(0, min(self.rows - 1, self.cy))

    def put(self, ch):
        if ch == "\r":
            self.cx = 0
            return
        if ch == "\n":
            self.cy += 1
            if self.cy >= self.rows:
                self.cy = self.rows - 1
            return
        if ch == "\t":
            self.cx = min(self.cols - 1, (self.cx // 8 + 1) * 8)
            return
        if ord(ch) < 32:
            return
        if self.cx >= self.cols:
            self.cx = self.cols - 1
        c = self.cur()[self.cy][self.cx]
        c.ch = ch
        c.fg = self.fg
        c.bold = self.bold
        self.cx += 1

    def erase_display(self, mode):
        g = self.cur()
        if mode == 2 or mode == 3:
            for row in g:
                for c in row:
                    c.ch, c.fg, c.bold = " ", None, False
        elif mode == 0:
            for x in range(self.cx, self.cols):
                g[self.cy][x] = Cell()
            for y in range(self.cy + 1, self.rows):
                for x in range(self.cols):
                    g[y][x] = Cell()
        elif mode == 1:
            for y in range(0, self.cy):
                for x in range(self.cols):
                    g[y][x] = Cell()
            for x in range(0, self.cx + 1):
                g[self.cy][x] = Cell()

    def erase_line(self, mode):
        g = self.cur()
        if mode == 0:
            for x in range(self.cx, self.cols):
                g[self.cy][x] = Cell()
        elif mode == 1:
            for x in range(0, self.cx + 1):
                g[self.cy][x] = Cell()
        elif mode == 2:
            for x in range(self.cols):
                g[self.cy][x] = Cell()

    def sgr(self, params):
        i = 0
        if not params:
            params = [0]
        while i < len(params):
            p = params[i]
            if p == 0:
                self.fg, self.bold = None, False
            elif p == 1:
                self.bold = True
            elif p == 22:
                self.bold = False
            elif p == 39:
                self.fg = None
            elif 30 <= p <= 37:
                self.fg = BASIC[p]
            elif 90 <= p <= 97:
                self.fg = BASIC[p]
            elif p == 38 and i + 1 < len(params):
                if params[i + 1] == 2 and i + 4 < len(params):
                    self.fg = (params[i + 2], params[i + 3], params[i + 4]); i += 4
                elif params[i + 1] == 5 and i + 2 < len(params):
                    self.fg = xterm256(params[i + 2]); i += 2
            i += 1


BASIC = {
    30: (30, 30, 30), 31: (205, 49, 49), 32: (13, 188, 121), 33: (229, 229, 16),
    34: (36, 114, 200), 35: (188, 63, 188), 36: (17, 168, 205), 37: (229, 229, 229),
    90: (102, 102, 102), 91: (241, 76, 76), 92: (35, 209, 139), 93: (245, 245, 67),
    94: (59, 142, 234), 95: (214, 112, 214), 96: (41, 184, 219), 97: (255, 255, 255),
}


def xterm256(n):
    if n < 16:
        return BASIC.get(n if n < 8 else n + 82, (200, 200, 200))
    if n < 232:
        n -= 16
        steps = [0, 95, 135, 175, 215, 255]
        return (steps[n // 36], steps[(n % 36) // 6], steps[n % 6])
    v = 8 + (n - 232) * 10
    return (v, v, v)


CSI_RE = re.compile(r"\x1b\[([0-9;?]*)([A-Za-z])")
OSC_RE = re.compile(r"\x1b\][^\x07\x1b]*(\x07|\x1b\\)")


def feed(screen, data):
    i = 0
    n = len(data)
    while i < n:
        ch = data[i]
        if ch == "\x1b":
            m = CSI_RE.match(data, i)
            if m:
                params_raw, cmd = m.group(1), m.group(2)
                priv = params_raw.startswith("?")
                nums = [int(x) for x in params_raw.lstrip("?").split(";") if x != ""]
                handle_csi(screen, nums, cmd, priv)
                i = m.end()
                continue
            om = OSC_RE.match(data, i)
            if om:
                i = om.end()
                continue
            # Unknown escape (e.g. ESC = / ESC >). Skip ESC and next byte.
            i += 2
            continue
        screen.put(ch)
        i += 1


def handle_csi(s, nums, cmd, priv):
    n0 = nums[0] if nums else 0
    if cmd == "H" or cmd == "f":
        row = (nums[0] if len(nums) >= 1 else 1) - 1
        col = (nums[1] if len(nums) >= 2 else 1) - 1
        s.cy, s.cx = row, col
        s.clamp()
    elif cmd == "A":
        s.cy -= max(1, n0); s.clamp()
    elif cmd == "B":
        s.cy += max(1, n0); s.clamp()
    elif cmd == "C":
        s.cx += max(1, n0); s.clamp()
    elif cmd == "D":
        s.cx -= max(1, n0); s.clamp()
    elif cmd == "G":
        s.cx = max(0, (n0 or 1) - 1); s.clamp()
    elif cmd == "d":
        s.cy = max(0, (n0 or 1) - 1); s.clamp()
    elif cmd == "J":
        s.erase_display(n0)
    elif cmd == "K":
        s.erase_line(n0)
    elif cmd == "m":
        s.sgr(nums)
    elif cmd == "h" and priv:
        if 1049 in nums or 1047 in nums or 47 in nums:
            s.using_alt = True
            s.cx = s.cy = 0
    elif cmd == "l" and priv:
        if 1049 in nums or 1047 in nums or 47 in nums:
            s.using_alt = False


def emit(screen):
    g = screen.cur()
    out_lines = []
    for row in g:
        # Trim trailing blank cells.
        last = -1
        for x, c in enumerate(row):
            if c.ch != " " or c.fg is not None:
                last = x
        if last < 0:
            out_lines.append("")
            continue
        parts = []
        cur_fg = "INIT"
        for x in range(last + 1):
            c = row[x]
            if c.fg != cur_fg:
                if c.fg is None:
                    parts.append("\x1b[0m")
                else:
                    parts.append(f"\x1b[38;2;{c.fg[0]};{c.fg[1]};{c.fg[2]}m")
                cur_fg = c.fg
            parts.append(c.ch)
        parts.append("\x1b[0m")
        out_lines.append("".join(parts))
    return "\n".join(out_lines)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--cols", type=int, default=120)
    ap.add_argument("--rows", type=int, default=40)
    ap.add_argument("inp")
    ap.add_argument("out")
    args = ap.parse_args()
    with open(args.inp, "r", encoding="utf-8", errors="replace") as f:
        data = f.read()
    screen = Screen(args.cols, args.rows)
    feed(screen, data)
    with open(args.out, "w", encoding="utf-8") as f:
        f.write(emit(screen))
    print(f"vtgrid: {args.cols}x{args.rows} -> {args.out}")


if __name__ == "__main__":
    main()
