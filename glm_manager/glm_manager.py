#!/usr/bin/env python3
"""
GLM Manager - Standalone entry point.

This is a convenience wrapper that allows running:
    python glm_manager.py

Instead of:
    python -m glm_manager

Or:
    python __main__.py
"""

import runpy
import sys
import os

if __name__ == "__main__":
    # Run __main__.py in this directory
    sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
    runpy.run_path(os.path.join(os.path.dirname(__file__), "__main__.py"), run_name="__main__")
