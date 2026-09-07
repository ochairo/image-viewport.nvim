#!/usr/bin/env python3
"""Exercise the locked image.nvim adapter with synthetic processor/backend effects."""
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('dependency_sources', ROOT / 'scripts/fetch-dependencies.py')
sources = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sources)


def main():
    dependencies = os.environ.get('IMAGE_VIEWPORT_DEPENDENCIES')
    if not dependencies:
        raise ValueError('set IMAGE_VIEWPORT_DEPENDENCIES to acquired locked dependency objects')
    source = (Path(dependencies) / 'image.nvim').resolve(strict=True)
    nvim = shutil.which(os.environ.get('NVIM') or 'nvim')
    if not nvim:
        raise ValueError('Neovim is unavailable')
    nvim = str(Path(nvim).resolve(strict=True))
    commit = json.loads((ROOT / 'dependencies.json').read_text())['image.nvim']['commit']
    with tempfile.TemporaryDirectory(prefix='image-upstream-') as temporary:
        private = Path(temporary)
        env = {'PATH': '/usr/bin:/bin', 'LANG': 'C.UTF-8', 'LC_ALL': 'C.UTF-8',
               'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_GLOBAL': '/dev/null',
               'NVIM_LOG_FILE': str(private / 'nvim.log'), 'IMAGE_VIEWPORT_TEST_ROOT': str(ROOT)}
        for key in ('HOME', 'XDG_CONFIG_HOME', 'XDG_DATA_HOME', 'XDG_STATE_HOME', 'XDG_CACHE_HOME', 'TMPDIR'):
            folder = private / key.lower()
            folder.mkdir(mode=0o700)
            env[key] = str(folder)
        # Copy only the pinned objects into a new private bare repository; materialize
        # validated blobs there so an ambient checkout cannot influence the test.
        repository = private / 'objects'
        sources.git(['init', '--bare', '--quiet', '--template=', str(repository)], env, private)
        subprocess.run(['/usr/bin/git', '-c', 'core.hooksPath=/dev/null', '-c', 'protocol.allow=never',
                        '-c', 'protocol.file.allow=always', '-c', 'safe.directory=' + str(source),
                        '-c', 'fetch.fsckObjects=true', '-C', str(repository), 'fetch', '--quiet',
                        '--no-tags', '--no-recurse-submodules', str(source), commit],
                       env=env, check=True, capture_output=True, timeout=60)
        sources.materialize(repository, commit, env)
        env['IMAGE_VIEWPORT_UPSTREAM'] = str(repository / 'checkout')
        result = subprocess.run([nvim, '--clean', '--headless', '-u', 'NONE', '-i', 'NONE', '--noplugin',
                                 '-l', str(ROOT / 'tests/upstream.lua')],
                                env=env, cwd=private, timeout=60)
        return result.returncode


if __name__ == '__main__':
    try:
        raise SystemExit(main())
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        raise SystemExit('upstream compatibility check failed: ' + str(error))
