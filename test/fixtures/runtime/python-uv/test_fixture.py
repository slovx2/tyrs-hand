import os
import sys

import idna

assert sys.version_info[:3] == (3, 13, 14)
assert os.environ["VIRTUAL_ENV"].endswith("/.venv")
assert idna.__version__ == "3.10"
