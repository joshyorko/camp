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


def read_capture(path):
    with open(path, "rb") as f:
        return f.read().decode("utf-8", errors="replace")


class Cell:
    __slots__ = ("ch", "fg", "bg", "bold")

    def __init__(self, bg=None):
        self.ch = " "
        self.fg = None
        self.bg = bg
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
        self.bg = None
        self.bold = False
        self.saved = {
            "main": (0, 0, None, None, False, False),
            "alt": (0, 0, None, None, False, False),
        }
        self.wrap_pending = False

    def cur(self):
        return self.alt if self.using_alt else self.grid

    def clamp(self):
        self.cx = max(0, min(self.cols - 1, self.cx))
        self.cy = max(0, min(self.rows - 1, self.cy))

    def lf(self):
        self.cy += 1
        if self.cy >= self.rows:
            g = self.cur()
            del g[0]
            g.append([Cell(bg=self.bg) for _ in range(self.cols)])
            self.cy = self.rows - 1

    def wrap_if_pending(self):
        if self.wrap_pending:
            self.wrap_pending = False
            self.cx = 0
            self.lf()

    def put(self, ch):
        if ch == "\r":
            self.cx = 0
            self.wrap_pending = False
            return
        if ch == "\n":
            # LF preserves the column in raw mode unless a wrap is pending.
            if self.wrap_pending:
                self.wrap_pending = False
                self.cx = 0
                self.lf()
                return
            self.lf()
            return
        if ch == "\t":
            self.cx = min(self.cols - 1, (self.cx // 8 + 1) * 8)
            self.wrap_pending = False
            return
        if ord(ch) < 32:
            if ch == "\x08":
                self.cx = max(0, self.cx - 1)
                self.wrap_pending = False
            if ch == "\x7f":
                self.wrap_pending = False
            return
        if self.wrap_pending:
            self.wrap_if_pending()
        elif self.cx >= self.cols:
            self.cx = self.cols - 1
        c = self.cur()[self.cy][self.cx]
        c.ch = ch
        c.fg = self.fg
        c.bg = self.bg
        c.bold = self.bold
        if self.cx == self.cols - 1:
            self.cx = self.cols
            self.wrap_pending = True
        else:
            self.cx += 1

    def erase_display(self, mode):
        self.wrap_pending = False
        # BCE: erased cells take the current background color, which is how
        # Bubble Tea fills the scene background.
        g = self.cur()
        if mode == 2 or mode == 3:
            for row in g:
                for x in range(self.cols):
                    row[x] = Cell(bg=self.bg)
        elif mode == 0:
            for x in range(self.cx, self.cols):
                g[self.cy][x] = Cell(bg=self.bg)
            for y in range(self.cy + 1, self.rows):
                for x in range(self.cols):
                    g[y][x] = Cell(bg=self.bg)
        elif mode == 1:
            for y in range(0, self.cy):
                for x in range(self.cols):
                    g[y][x] = Cell(bg=self.bg)
            for x in range(0, self.cx + 1):
                g[self.cy][x] = Cell(bg=self.bg)

    def erase_line(self, mode):
        self.wrap_pending = False
        g = self.cur()
        if mode == 0:
            for x in range(self.cx, self.cols):
                g[self.cy][x] = Cell(bg=self.bg)
        elif mode == 1:
            for x in range(0, self.cx + 1):
                g[self.cy][x] = Cell(bg=self.bg)
        elif mode == 2:
            for x in range(self.cols):
                g[self.cy][x] = Cell(bg=self.bg)

    def sgr(self, params):
        i = 0
        if not params:
            params = [0]
        while i < len(params):
            p = params[i]
            if p == 0:
                self.fg, self.bg, self.bold = None, None, False
            elif p == 1:
                self.bold = True
            elif p == 22:
                self.bold = False
            elif p == 39:
                self.fg = None
            elif p == 49:
                self.bg = None
            elif 30 <= p <= 37:
                self.fg = BASIC[p]
            elif 90 <= p <= 97:
                self.fg = BASIC[p]
            elif 40 <= p <= 47:
                self.bg = BASIC[p - 10]
            elif 100 <= p <= 107:
                self.bg = BASIC[p - 10]
            elif p in (38, 48) and i + 1 < len(params):
                target = "fg" if p == 38 else "bg"
                if params[i + 1] == 2 and i + 4 < len(params):
                    setattr(self, target, (params[i + 2], params[i + 3], params[i + 4])); i += 4
                elif params[i + 1] == 5 and i + 2 < len(params):
                    setattr(self, target, xterm256(params[i + 2])); i += 2
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


CSI_RE = re.compile(r"\x1b\[([0-9;?=><]*)([ !\"#$%&'()*+,\-./]*)([@-~])")
OSC_RE = re.compile(r"\x1b\][^\x07\x1b]*(\x07|\x1b\\\\)")


def feed(screen, data):
    i = 0
    n = len(data)
    while i < n:
        ch = data[i]
        if ch == "\x1b":
            m = CSI_RE.match(data, i)
            if m:
                params_raw, intermediates, cmd = m.group(1), m.group(2), m.group(3)
                if intermediates or any(c in params_raw for c in "=><"):
                    # Private/intermediate sequences (kitty keyboard, mode
                    # queries, XTWINOPS …) don't affect the cell grid.
                    i = m.end()
                    continue
                priv = params_raw.startswith("?")
                nums = [int(x) for x in params_raw.lstrip("?").split(";") if x != ""]
                handle_csi(screen, nums, cmd, priv)
                i = m.end()
                continue
            om = OSC_RE.match(data, i)
            if om:
                i = om.end()
                continue
            nxt = data[i + 1] if i + 1 < n else ""
            if nxt == "7":  # DECSC save cursor
                key = "alt" if screen.using_alt else "main"
                screen.saved[key] = (screen.cx, screen.cy, screen.fg, screen.bg, screen.bold, screen.wrap_pending)
                i += 2
                continue
            if nxt == "8":  # DECRC restore cursor
                key = "alt" if screen.using_alt else "main"
                screen.cx, screen.cy, screen.fg, screen.bg, screen.bold, screen.wrap_pending = screen.saved.get(key, (screen.cx, screen.cy, screen.fg, screen.bg, screen.bold, screen.wrap_pending))
                screen.clamp()
                i += 2
                continue
            # Unknown escape (e.g. ESC = / ESC >). Skip ESC and next byte.
            i += 2
            continue
        screen.put(ch)
        i += 1


def handle_csi(s, nums, cmd, priv):
    cursor_move = {"A", "B", "C", "D", "G", "H", "f", "d", "J", "K", "X", "P", "@", "L", "M", "S", "T", "h", "l"}
    if cmd == "m":
        pass
    elif cmd in cursor_move:
        s.wrap_pending = False

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
    elif cmd == "X":  # ECH: erase n chars at cursor (BCE, no cursor move)
        n = n0 if nums else 1
        if n == 0:
            return
        g = s.cur()
        for x in range(s.cx, min(s.cols, s.cx + n)):
            g[s.cy][x] = Cell(bg=s.bg)
    elif cmd == "P":  # DCH: delete n chars, shifting the remainder left
        n = n0 if nums else 1
        if n == 0:
            return
        g = s.cur()
        row = g[s.cy]
        del row[s.cx:s.cx + n]
        row.extend(Cell(bg=s.bg) for _ in range(s.cols - len(row)))
    elif cmd == "@":  # ICH: insert n blank chars at cursor
        n = n0 if nums else 1
        if n == 0:
            return
        g = s.cur()
        row = g[s.cy]
        for _ in range(n):
            row.insert(s.cx, Cell(bg=s.bg))
        del row[s.cols:]
    elif cmd == "L":  # IL: insert n blank lines at cursor row
        n = n0 if nums else 1
        if n == 0:
            return
        g = s.cur()
        for _ in range(n):
            g.insert(s.cy, [Cell(bg=s.bg) for _ in range(s.cols)])
        del g[s.rows:]
    elif cmd == "M":  # DL: delete n lines at cursor row
        n = n0 if nums else 1
        if n == 0:
            return
        g = s.cur()
        del g[s.cy:s.cy + n]
        g.extend([Cell(bg=s.bg) for _ in range(s.cols)] for _ in range(s.rows - len(g)))
    elif cmd == "S":  # SU: scroll up n lines
        n = n0 if nums else 1
        if n == 0:
            return
        g = s.cur()
        del g[0:n]
        g.extend([Cell(bg=s.bg) for _ in range(s.cols)] for _ in range(s.rows - len(g)))
    elif cmd == "T":  # SD: scroll down n lines
        n = n0 if nums else 1
        if n == 0:
            return
        g = s.cur()
        for _ in range(n):
            g.insert(0, [Cell(bg=s.bg) for _ in range(s.cols)])
        del g[s.rows:]
    elif cmd == "m":
        s.sgr(nums)
    elif cmd == "h" and priv:
        if 1049 in nums or 1047 in nums or 47 in nums:
            if not s.using_alt:
                s.saved["main"] = (s.cx, s.cy, s.fg, s.bg, s.bold, s.wrap_pending)
            s.using_alt = True
            s.cx = s.cy = 0
            s.wrap_pending = False
    elif cmd == "l" and priv:
        if 1049 in nums or 1047 in nums or 47 in nums:
            if s.using_alt:
                s.using_alt = False
                s.cx, s.cy, s.fg, s.bg, s.bold, s.wrap_pending = s.saved["main"]
                s.clamp()
            return


def emit(screen):
    g = screen.cur()
    out_lines = []
    for row in g:
        # Trim trailing blank cells (no glyph, no explicit color).
        last = -1
        for x, c in enumerate(row):
            if c.ch != " " or c.fg is not None or c.bg is not None:
                last = x
        if last < 0:
            out_lines.append("")
            continue
        parts = []
        cur_fg = "INIT"
        cur_bg = "INIT"
        for x in range(last + 1):
            c = row[x]
            if c.fg != cur_fg or c.bg != cur_bg:
                seq = ["0"]
                if c.fg is not None:
                    seq.append(f"38;2;{c.fg[0]};{c.fg[1]};{c.fg[2]}")
                if c.bg is not None:
                    seq.append(f"48;2;{c.bg[0]};{c.bg[1]};{c.bg[2]}")
                parts.append("\x1b[" + ";".join(seq) + "m")
                cur_fg, cur_bg = c.fg, c.bg
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
    data = read_capture(args.inp)
    screen = Screen(args.cols, args.rows)
    feed(screen, data)
    with open(args.out, "w", encoding="utf-8") as f:
        f.write(emit(screen))
    print(f"vtgrid: {args.cols}x{args.rows} -> {args.out}")


if __name__ == "__main__":
    main()
