#!/usr/bin/env python3
"""Run Trunk's pinned Pinact adapter with the v5 long-option spelling."""

import importlib.util
import sys
from pathlib import Path


adapter_path = Path(sys.argv[1])
spec = importlib.util.spec_from_file_location("trunk_pinact_adapter", adapter_path)
if spec is None or spec.loader is None:
    raise RuntimeError(f"could not load Pinact adapter: {adapter_path}")
adapter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(adapter)

upstream_args = adapter.build_pinact_args
long_options = {"-format", "-update", "-no-api", "-verify-comment"}


def v5_args(mode: str) -> list[str]:
    return ["-" + arg if arg in long_options else arg for arg in upstream_args(mode)]


adapter.build_pinact_args = v5_args
sys.argv = [sys.argv[0], *sys.argv[2:]]
raise SystemExit(adapter.main())
