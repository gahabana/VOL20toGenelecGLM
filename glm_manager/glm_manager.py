#!/usr/bin/env python3
"""
GLM Manager - Standalone entry point.

This is a convenience wrapper that allows running:
    python glm_manager.py

Instead of:
    python -m glm_manager
"""

import runpy
import sys
import os

if __name__ == "__main__":
    # Get paths
    this_dir = os.path.dirname(os.path.abspath(__file__))
    repo_root = os.path.dirname(this_dir)  # Parent directory (repo root)

    # Add both this directory and repo root to path
    # Repo root is needed for glm_core, PowerOnOff, api modules
    sys.path.insert(0, this_dir)
    sys.path.insert(0, repo_root)

    # Run __main__.py
    runpy.run_path(os.path.join(this_dir, "__main__.py"), run_name="__main__")
