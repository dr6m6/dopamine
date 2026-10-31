#!/usr/bin/env python3
"""Source launcher: every invocation builds the current checkout through Nix."""
from pathlib import Path
import runpy

runpy.run_path(str(Path(__file__).resolve().with_name("project-hub-source.py")), run_name="__main__")
