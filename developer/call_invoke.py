import os
import subprocess
import sys


repository_root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

if len(sys.argv) < 2:
    raise SystemExit("no Invoke task supplied")

raise SystemExit(
    subprocess.run(("invoke", sys.argv[1]), cwd=repository_root, check=False).returncode
)
