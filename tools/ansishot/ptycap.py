#!/usr/bin/env python3
"""ptycap: run a command in a PTY of a fixed size, send scripted keystrokes,
and capture the final alternate-screen frame as ANSI.

We spawn the child attached to a real pseudo-terminal so it takes the rich
interactive path, set the window size via TIOCSWINSZ, drive it with timed key
sends, then extract the last full frame (everything after the final
clear/home or alt-screen switch) and print it to stdout.

Usage:
  ptycap.py --cols 120 --rows 40 --send-file script.txt -- CMD [ARGS...]

script.txt lines: either "sleep 0.4" or "key <text>" (text sent verbatim;
use \\r for Enter, \\t for Tab, \\x03 for Ctrl-C, \\x1b for Esc).
"""
import argparse
import os
import pty
import select
import struct
import sys
import termios
import fcntl
import time


def set_winsize(fd, rows, cols):
    winsize = struct.pack("HHHH", rows, cols, 0, 0)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, winsize)


def decode_escapes(s):
    return (s.replace("\\r", "\r").replace("\\n", "\n").replace("\\t", "\t")
             .replace("\\x03", "\x03").replace("\\x1b", "\x1b"))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--cols", type=int, default=120)
    ap.add_argument("--rows", type=int, default=40)
    ap.add_argument("--send-file", default=None)
    ap.add_argument("--settle", type=float, default=0.6, help="seconds to wait after last key before capturing")
    ap.add_argument("--env", action="append", default=[], help="KEY=VALUE env overrides")
    ap.add_argument("cmd", nargs=argparse.REMAINDER)
    args = ap.parse_args()

    cmd = args.cmd
    if cmd and cmd[0] == "--":
        cmd = cmd[1:]
    if not cmd:
        print("no command", file=sys.stderr)
        return 2

    script = []
    if args.send_file:
        with open(args.send_file) as f:
            script = [l.rstrip("\n") for l in f if l.strip()]

    env = dict(os.environ)
    env["TERM"] = "xterm-256color"
    env["COLORTERM"] = "truecolor"
    env.pop("NO_COLOR", None)
    env.pop("CI", None)
    for kv in args.env:
        k, _, v = kv.partition("=")
        env[k] = v

    pid, fd = pty.fork()
    if pid == 0:
        # Child: exec the command with the inherited controlling PTY.
        os.execvpe(cmd[0], cmd, env)
        os._exit(127)

    set_winsize(fd, args.rows, args.cols)

    captured = bytearray()

    def drain(timeout):
        end = time.time() + timeout
        while time.time() < end:
            r, _, _ = select.select([fd], [], [], max(0, end - time.time()))
            if fd in r:
                try:
                    data = os.read(fd, 65536)
                except OSError:
                    return False
                if not data:
                    return False
                captured.extend(data)
        return True

    # Let the program draw its first frame.
    drain(0.7)
    for line in script:
        if line.startswith("sleep "):
            drain(float(line.split()[1]))
        elif line.startswith("key "):
            payload = decode_escapes(line[4:])
            os.write(fd, payload.encode())
            drain(0.15)
    drain(args.settle)

    try:
        os.close(fd)
    except OSError:
        pass
    try:
        os.waitpid(pid, os.WNOHANG)
    except OSError:
        pass

    text = captured.decode("utf-8", errors="replace")
    # Keep everything after the last full clear so we capture the final frame.
    for marker in ("\x1b[2J\x1b[H", "\x1b[2J", "\x1b[H"):
        idx = text.rfind(marker)
        if idx != -1:
            text = text[idx + len(marker):]
            break
    sys.stdout.write(text)


if __name__ == "__main__":
    raise SystemExit(main())
