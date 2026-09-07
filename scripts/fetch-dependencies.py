#!/usr/bin/env python3
"""Explicit networked acquisition of locked test sources; never run by make check."""
import argparse
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
LOCK = ROOT / ('tests/dependencies.json' if (ROOT / 'tests/dependencies.json').is_file() else 'dependencies.json')


def read_lock(path):
    entries = json.loads(path.read_text())
    if not isinstance(entries, dict) or not entries or len(entries) > 16:
        raise ValueError('invalid dependency lock')
    for name, entry in entries.items():
        if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,63}', name):
            raise ValueError('invalid dependency name')
        if not isinstance(entry, dict) or not re.fullmatch(r'[0-9a-f]{40}', entry.get('commit', '')):
            raise ValueError('dependency requires an exact commit')
        if not re.fullmatch(r'https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', entry.get('repository', '')):
            raise ValueError('dependency requires a public GitHub HTTPS origin')
    return entries


def git(arguments, env, cwd):
    result = subprocess.run(
        ['/usr/bin/git', '-c', 'core.hooksPath=/dev/null', '-c', 'credential.helper=',
         '-c', 'protocol.allow=never', '-c', 'protocol.https.allow=always',
         '-c', 'fetch.fsckObjects=true', *arguments],
        env=env, cwd=cwd, check=True, capture_output=True, timeout=180,
    )
    return result.stdout


def materialize(repository, commit, env):
    """Copy only bounded regular blobs; never checkout links, submodules or hooks."""
    listing = git(['ls-tree', '-rz', '--full-tree', commit], env, repository)
    records = listing.rstrip(b'\0').split(b'\0') if listing else []
    if len(listing) > 2 * 1024 * 1024 or len(records) > 10000:
        raise ValueError('dependency tree exceeds its bound')
    checkout = repository / 'checkout'
    checkout.mkdir(mode=0o700)
    total = 0
    for record in records:
        header, name = record.decode('utf-8').split('\t', 1)
        mode, kind, oid = header.split()
        parts = PurePosixPath(name).parts
        if (not parts or name.startswith('/') or '/'.join(parts) != name
                or any(part in ('.', '..', '.git') for part in parts)
                or any(ord(char) < 32 for char in name)
                or mode not in ('100644', '100755') or kind != 'blob'):
            raise ValueError('dependency tree contains an unsupported entry')
        size = int(git(['cat-file', '-s', oid], env, repository))
        total += size
        if size > 16 * 1024 * 1024 or total > 64 * 1024 * 1024:
            raise ValueError('dependency content exceeds its bound')
        data = git(['cat-file', 'blob', oid], env, repository)
        if len(data) != size:
            raise ValueError('dependency object changed')
        target = checkout.joinpath(*parts)
        target.parent.mkdir(parents=True, exist_ok=True)
        with target.open('xb') as stream:
            stream.write(data)
        target.chmod(0o755 if mode == '100755' else 0o644)


def fetch(destination):
    entries = read_lock(LOCK)
    destination = Path(os.path.abspath(destination))
    if destination.parent.resolve(strict=True) != destination.parent:
        raise ValueError('destination parent must be canonical')
    destination.mkdir(mode=0o700)  # Existing files and directories are never adopted.
    identity = destination.lstat()
    try:
        with tempfile.TemporaryDirectory(prefix='plugin-fetch-') as temporary:
            env = {'PATH': '/usr/bin:/bin', 'HOME': temporary, 'LANG': 'C.UTF-8',
                   'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_GLOBAL': '/dev/null',
                   'GIT_TERMINAL_PROMPT': '0', 'GIT_ALLOW_PROTOCOL': 'https',
                   'GIT_SSL_CAINFO': '/tools/etc/ssl/certs/ca-bundle.crt'}
            # Outside the image, let Git use its system trust store.
            if not Path(env['GIT_SSL_CAINFO']).is_file():
                del env['GIT_SSL_CAINFO']
            for name, entry in sorted(entries.items()):
                repository = destination / name
                git(['init', '--bare', '--quiet', '--template=', str(repository)], env, destination)
                git(['fetch', '--quiet', '--depth=1', '--no-tags', '--no-recurse-submodules',
                     entry['repository'], entry['commit']], env, repository)
                actual = git(['rev-parse', '--verify', 'FETCH_HEAD^{commit}'], env, repository).decode().strip()
                if actual != entry['commit']:
                    raise ValueError('fetched commit differs from lock')
                materialize(repository, actual, env)
    except BaseException:
        # Never remove a substituted or foreign destination during failure cleanup.
        if destination.exists() and not destination.is_symlink():
            current = destination.lstat()
            if (current.st_dev, current.st_ino) == (identity.st_dev, identity.st_ino):
                shutil.rmtree(destination)
        raise
    return destination


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('destination', type=Path)
    args = parser.parse_args()
    try:
        print(fetch(args.destination))
    except (OSError, ValueError, subprocess.SubprocessError):
        parser.exit(1, 'dependency acquisition failed; check locked revisions, network and destination\n')
