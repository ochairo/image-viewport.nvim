"""Focused file-boundary checks that do not claim sandbox execution coverage."""
import contextlib
import fcntl
import importlib.util
import io
import os
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("image_launch", ROOT / "runtime/launch.py")
launch = importlib.util.module_from_spec(spec)
spec.loader.exec_module(launch)


class RuntimeTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="image-boundary-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)

    def test_snapshot_is_immutable_and_independent_of_later_source_writes(self):
        source = self.root / "image with spaces.png"
        source.write_bytes(b"original image bytes")
        descriptor = launch.sealed_snapshot(str(source))
        try:
            source.write_bytes(b"changed")
            self.assertEqual(os.read(descriptor, 100), b"original image bytes")
            seals = fcntl.fcntl(descriptor, fcntl.F_GET_SEALS)
            self.assertTrue(seals & fcntl.F_SEAL_WRITE)
            with self.assertRaises(OSError):
                os.write(descriptor, b"mutate")
        finally:
            os.close(descriptor)

    def test_fifo_is_rejected_without_waiting_for_a_writer(self):
        fifo = self.root / "fifo"
        os.mkfifo(fifo)
        with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            launch.sealed_snapshot(str(fifo))

    def test_foreign_output_directory_is_preserved(self):
        directory = self.root / "foreign"
        directory.mkdir(mode=0o755)
        marker = directory / "keep"
        marker.write_text("foreign")
        with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            with launch.managed_staging(str(directory / ("a" * 64 + ".png"))):
                self.fail("non-private output was accepted")
        self.assertEqual(marker.read_text(), "foreign")


if __name__ == "__main__":
    unittest.main()
