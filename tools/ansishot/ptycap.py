#!/usr/bin/env python3
"""ptycap: run a command in a PTY and emit the complete raw PTY byte stream.

We spawn the child attached to a real pseudo-terminal so it takes the rich
interactive path, set the window size via TIOCSWINSZ, execute a scripted send
file, then print the captured raw byte stream to stdout.

Usage:
  ptycap.py --cols 120 --rows 40 --send-file script.txt -- CMD [ARGS...]

script.txt lines:
- "sleep <seconds>" to wait (float accepted).
- "key <text>" to write bytes literally (use \r for Enter, \t for Tab,
  \x03 for Ctrl-C, \x1b for Esc).
- "resize <cols> <rows>" to resize the attached PTY mid-capture.
"""
import argparse
import os
import pty
import select
import signal
import struct
import sys
import termios
import fcntl
import time


def set_winsize(fd, rows, cols):
    winsize = struct.pack("HHHH", rows, cols, 0, 0)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, winsize)


def decode_escapes(s):
    return (s.replace("\\r", "\r")
            .replace("\\n", "\n")
            .replace("\\t", "\t")
            .replace("\\x03", "\x03")
            .replace("\\x1b", "\x1b"))


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
        # Child: size the PTY before exec so the program never observes the
        # transient 0×0 window pty.fork starts with.
        try:
            set_winsize(0, args.rows, args.cols)
        except OSError:
            pass
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
        elif line.startswith("resize "):
            _, cols_s, rows_s = line.split()
            set_winsize(fd, int(rows_s), int(cols_s))
            try:
                os.kill(pid, signal.SIGWINCH)
            except OSError:
                pass
            drain(0.4)
    drain(args.settle)

    # End the PTY session and force child exit.
    try:
        os.close(fd)
    except OSError:
        pass

    deadline = time.time() + 2.0
    while time.time() < deadline:
        pid_done, _ = os.waitpid(pid, os.WNOHANG)
        if pid_done == pid:
            break
        time.sleep(0.05)
    else:
        try:
            os.kill(pid, signal.SIGTERM)
        except OSError:
            pass
        deadline = time.time() + 1.0
        while time.time() < deadline:
            pid_done, _ = os.waitpid(pid, os.WNOHANG)
            if pid_done == pid:
                break
            time.sleep(0.05)
        else:
            try:
                os.kill(pid, signal.SIGKILL)
            except OSError:
                pass
            try:
                os.waitpid(pid, 0)
            except OSError:
                pass

    # Emit the raw byte stream verbatim. Bubble Tea renders via cursor moves and
    # cell rewrites, not full-frame clears, so the stream must be replayed
    # through a terminal emulator (vtgrid.py) to reconstruct the final screen.
    sys.stdout.buffer.write(captured)
    sys.stdout.flush()


if __name__ == "__main__":
    raise SystemExit(main())
