"""Offline tests of dependency lock admission and Git blob materialization."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('fetch_dependencies', ROOT / 'scripts/fetch-dependencies.py')
fetcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fetcher)


class DependencyTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='dependency-contract-')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.env = {'HOME': str(self.root), 'PATH': '/usr/bin:/bin',
                    'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_GLOBAL': '/dev/null'}

    def test_invalid_locks_are_rejected_before_acquisition(self):
        lock = self.root / 'lock.json'
        good = {'plugin': {'repository': 'https://github.com/owner/plugin', 'commit': 'a' * 40}}
        lock.write_text(json.dumps(good))
        self.assertEqual(fetcher.read_lock(lock), good)
        bad = [[], {}, {'../escape': good['plugin']}, {'plugin': {'commit': 'main'}},
               {'plugin': dict(good['plugin'], repository='file:///tmp/source')},
               {'plugin': dict(good['plugin'], repository='https://user:token@github.com/owner/plugin')}]
        for data in bad:
            lock.write_text(json.dumps(data))
            with self.subTest(data=data), self.assertRaises(ValueError):
                fetcher.read_lock(lock)

    def test_existing_destination_and_symlink_parent_are_preserved(self):
        existing = self.root / 'existing'
        existing.mkdir()
        (existing / 'foreign').write_text('keep')
        with patch.object(fetcher, 'git') as command:
            with self.assertRaises(FileExistsError):
                fetcher.fetch(existing)
            command.assert_not_called()
        self.assertEqual((existing / 'foreign').read_text(), 'keep')
        alias = self.root / 'alias'
        alias.symlink_to(self.root, target_is_directory=True)
        with self.assertRaises(ValueError):
            fetcher.fetch(alias / 'new')

    def tree(self, mode):
        repository = self.root / 'objects'
        fetcher.git(['init', '--bare', '--quiet', '--template=', str(repository)], self.env, self.root)
        blob = subprocess.check_output(['/usr/bin/git', '-C', str(repository), 'hash-object', '-w', '--stdin'],
                                       input=b'fixture\n', env=self.env).decode().strip()
        tree = subprocess.check_output(['/usr/bin/git', '-C', str(repository), 'mktree'],
                                       input=f'{mode} blob {blob}\tsource.lua\n'.encode(), env=self.env).decode().strip()
        return repository, tree

    def test_regular_blob_materializes_without_checkout_hooks(self):
        repository, tree = self.tree('100644')
        fetcher.materialize(repository, tree, self.env)
        self.assertEqual((repository / 'checkout/source.lua').read_text(), 'fixture\n')

    def test_symlink_blob_is_rejected(self):
        repository, tree = self.tree('120000')
        with self.assertRaises(ValueError):
            fetcher.materialize(repository, tree, self.env)
        self.assertFalse((repository / 'checkout/source.lua').exists())

    def test_git_subprocess_does_not_use_ambient_configuration(self):
        result = subprocess.CompletedProcess([], 0, stdout=b'ok')
        with patch.object(fetcher.subprocess, 'run', return_value=result) as run:
            fetcher.git(['fetch', 'https://github.com/owner/plugin', 'a' * 40], self.env, self.root)
        argv = run.call_args.args[0]
        self.assertIn('protocol.allow=never', argv)
        self.assertIn('core.hooksPath=/dev/null', argv)
        self.assertIn('credential.helper=', argv)
        self.assertIs(run.call_args.kwargs['env'], self.env)
        self.assertTrue(run.call_args.kwargs['check'])


if __name__ == '__main__':
    unittest.main()
