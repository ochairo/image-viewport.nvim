#!/usr/bin/env python3
"""Run setup contracts in an isolated Neovim, without image processing or user config."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]


def main():
    selected = shutil.which(os.environ.get("NVIM") or "nvim")
    if not selected:
        raise SystemExit("Neovim is unavailable; set NVIM or install nvim on PATH")
    nvim = str(Path(selected).resolve(strict=True))
    with tempfile.TemporaryDirectory(prefix="image-viewport-tests-") as temporary:
        private = Path(temporary)
        env = {"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8", "IMAGE_VIEWPORT_TEST_ROOT": str(ROOT),
               "NVIM_LOG_FILE": str(private / "nvim.log")}
        for name in ("HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "TMPDIR"):
            path = private / name.lower()
            path.mkdir(mode=0o700)
            env[name] = str(path)
        for script in ("setup.lua", "setup_failure.lua"):
            result = subprocess.run([nvim, "--clean", "--headless", "-u", "NONE", "-i", "NONE", "--noplugin",
                                     "-l", str(ROOT / "tests" / script)], env=env, cwd=private, timeout=60)
            if result.returncode:
                return result.returncode
        return 0


if __name__ == "__main__":
    raise SystemExit(main())
