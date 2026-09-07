#!/usr/bin/python3
"""Supervise one isolated plugin image operation and publish a verified PNG."""

import contextlib
import fcntl
import os
import re
import resource
import secrets
import signal
import stat
import subprocess
import sys
import time

MAXIMUM_INPUT_BYTES = 128 * 1024 * 1024
MAXIMUM_OUTPUT_BYTES = 32 * 1024 * 1024
MAXIMUM_PROCESSES = 32
MAXIMUM_RSS_KIB = 1024 * 1024
RUNTIME = os.path.dirname(os.path.realpath(__file__))
WORKER = os.path.join(RUNTIME, "worker.py")
POLICY = os.path.join(RUNTIME, "policy.xml")
FORMATS = {"png", "jpeg", "webp", "gif", "bmp", "heic", "xpm", "ico", "avif", "svg", "xml", "pdf"}
MANAGED_NAME = re.compile(r"^[0-9a-f]{64}\.png$")
PNG = b"\x89PNG\r\n\x1a\n"
ACTIVE_PROCESS: subprocess.Popen[bytes] | None = None
CANCELLED = False


class OperationCancelled(Exception):
    """Stop publication while allowing owned descriptors to unwind."""


def fail(message: str, code: int = 70) -> "NoReturn":
    print(f"image processor: {message}", file=sys.stderr)
    raise SystemExit(code)


def regular_metadata(file_descriptor: int) -> os.stat_result:
    metadata = os.fstat(file_descriptor)
    if not stat.S_ISREG(metadata.st_mode) or metadata.st_nlink < 1:
        fail("input is not a regular file")
    return metadata


def stable_identity(metadata: os.stat_result) -> tuple[int, ...]:
    return (
        metadata.st_dev,
        metadata.st_ino,
        metadata.st_size,
        metadata.st_mtime_ns,
        metadata.st_ctime_ns,
    )


def sealed_snapshot(path: str) -> int:
    canonical = os.path.realpath(path)
    source = os.open(canonical, os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        before = regular_metadata(source)
        if before.st_size < 1 or before.st_size > MAXIMUM_INPUT_BYTES:
            fail("input size exceeds the reviewed limit")
        snapshot = os.memfd_create("dotfiles-image-source", os.MFD_CLOEXEC | os.MFD_ALLOW_SEALING)
        copied = 0
        while True:
            chunk = os.read(source, min(1024 * 1024, MAXIMUM_INPUT_BYTES + 1 - copied))
            if not chunk:
                break
            copied += len(chunk)
            if copied > MAXIMUM_INPUT_BYTES:
                fail("input changed beyond the reviewed limit")
            view = memoryview(chunk)
            while view:
                view = view[os.write(snapshot, view):]
        after = regular_metadata(source)
        if copied != before.st_size or stable_identity(before) != stable_identity(after):
            fail("input changed while it was being captured")
        fcntl.fcntl(
            snapshot,
            fcntl.F_ADD_SEALS,
            fcntl.F_SEAL_WRITE | fcntl.F_SEAL_GROW | fcntl.F_SEAL_SHRINK | fcntl.F_SEAL_SEAL,
        )
        os.lseek(snapshot, 0, os.SEEK_SET)
        return snapshot
    finally:
        os.close(source)


def safe_runtime() -> None:
    uid = os.getuid()
    for path in (RUNTIME, WORKER, POLICY):
        metadata = os.lstat(path)
        if stat.S_ISLNK(metadata.st_mode) or metadata.st_uid != uid:
            fail("managed runtime metadata is unsafe")
    if not stat.S_ISDIR(os.lstat(RUNTIME).st_mode):
        fail("managed runtime is not a directory")
    if not stat.S_ISREG(os.lstat(WORKER).st_mode) or not stat.S_ISREG(os.lstat(POLICY).st_mode):
        fail("managed runtime files are invalid")


@contextlib.contextmanager
def managed_staging(target: str):
    directory, name = os.path.split(target)
    if not os.path.isabs(target) or not MANAGED_NAME.fullmatch(name):
        fail("output path is not managed")
    directory_fd: int | None = None
    output: int | None = None
    staging_name: str | None = None
    try:
        directory_fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW)
        metadata = os.fstat(directory_fd)
        if metadata.st_uid != os.getuid() or stat.S_IMODE(metadata.st_mode) != 0o700:
            fail("output directory is not private")
        staging_name = f".{name}.{os.getpid()}.{secrets.token_hex(8)}.new"
        output = os.open(
            staging_name,
            os.O_RDWR | os.O_CREAT | os.O_EXCL | os.O_CLOEXEC | os.O_NOFOLLOW,
            0o600,
            dir_fd=directory_fd,
        )
        # Yield one object only after the context manager's cleanup callback is
        # registered by WITH. This closes the signal handoff gap that tuple
        # unpacking would leave between acquisition and the caller's finally.
        yield {
            "directory_fd": directory_fd,
            "output_fd": output,
            "staging_name": staging_name,
            "target_name": name,
        }
    finally:
        if output is not None:
            os.close(output)
        if directory_fd is not None and staging_name is not None:
            try:
                os.unlink(staging_name, dir_fd=directory_fd)
            except FileNotFoundError:
                pass
        if directory_fd is not None:
            os.close(directory_fd)


def parse_process_parent(record: bytes) -> int:
    """Parse PPID after the final parenthesized comm field in procfs stat."""
    closing = record.rfind(b")")
    if closing < 2:
        raise ValueError("process stat has no command boundary")
    fields = record[closing + 1 :].split()
    if len(fields) < 2 or len(fields[0]) != 1:
        raise ValueError("process stat is incomplete")
    return int(fields[1])


def process_tree(root: int) -> tuple[set[int], int]:
    processes: dict[int, tuple[int, int]] = {}
    for name in os.listdir("/proc"):
        if not name.isdigit():
            continue
        try:
            with open(f"/proc/{name}/stat", "rb") as handle:
                parent = parse_process_parent(handle.read())
            with open(f"/proc/{name}/status", encoding="ascii") as handle:
                status_fields = handle.readlines()
        except (FileNotFoundError, PermissionError, ProcessLookupError):
            continue
        rss = 0
        for line in status_fields:
            if line.startswith("VmRSS:"):
                rss = int(line.split()[1])
                break
        processes[int(name)] = (parent, rss)
    selected = {root}
    changed = True
    while changed:
        changed = False
        for pid, (parent, _) in processes.items():
            if pid not in selected and parent in selected:
                selected.add(pid)
                changed = True
    return selected, sum(processes.get(pid, (0, 0))[1] for pid in selected)


def resource_limits() -> None:
    resource.setrlimit(resource.RLIMIT_AS, (1536 * 1024 * 1024, 1536 * 1024 * 1024))
    resource.setrlimit(resource.RLIMIT_CPU, (10, 10))
    resource.setrlimit(resource.RLIMIT_FSIZE, (64 * 1024 * 1024, 64 * 1024 * 1024))
    resource.setrlimit(resource.RLIMIT_NOFILE, (128, 128))


def terminate(process: subprocess.Popen[bytes]) -> None:
    if process.poll() is not None:
        return
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        return
    try:
        process.wait(timeout=0.25)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait(timeout=1)


def cancel_operation(_signal: int, _frame: object) -> None:
    global CANCELLED
    CANCELLED = True
    for handled in (signal.SIGHUP, signal.SIGINT, signal.SIGTERM):
        signal.signal(handled, signal.SIG_IGN)
    if ACTIVE_PROCESS is not None:
        terminate(ACTIVE_PROCESS)
    raise OperationCancelled


def sandbox_arguments(input_fd: int, output_fd: int | None, worker_arguments: list[str]) -> list[str]:
    arguments = [
        "/usr/bin/bwrap",
        "--die-with-parent",
        "--new-session",
        "--unshare-all",
        "--unshare-user",
        "--disable-userns",
        "--clearenv",
        "--setenv", "HOME", "/tmp/home",
        "--setenv", "LANG", "C.UTF-8",
        "--setenv", "LC_ALL", "C.UTF-8",
        "--setenv", "MAGICK_CONFIGURE_PATH", "/policy",
        "--setenv", "MAGICK_TEMPORARY_PATH", "/tmp",
        "--setenv", "PATH", "/usr/bin:/bin",
        "--setenv", "TMPDIR", "/tmp",
        "--size", "134217728",
        "--tmpfs", "/tmp",
        "--dir", "/tmp/home",
        "--dir", "/work",
        "--dir", "/input",
        "--dir", "/output",
        "--dir", "/policy",
        "--ro-bind", "/usr", "/usr",
        "--symlink", "usr/bin", "/bin",
        "--symlink", "usr/lib", "/lib",
        "--ro-bind", "/etc/ld.so.cache", "/etc/ld.so.cache",
        "--ro-bind", "/etc/fonts", "/etc/fonts",
        "--ro-bind", "/var/cache/fontconfig", "/var/cache/fontconfig",
        "--ro-bind", RUNTIME, "/runtime",
        "--ro-bind", POLICY, "/policy/policy.xml",
        # A sealed memfd has no stable filesystem name, so bwrap must copy its
        # immutable bytes instead of resolving the deleted memfd link.
        "--ro-bind-data", str(input_fd), "/input/source",
    ]
    if output_fd is not None:
        arguments.extend(["--bind-fd", str(output_fd), "/output/frame"])
    # Keep inherited descriptor mounts ahead of the sandbox /proc mount. bwrap
    # resolves --*-bind-fd through its current /proc/self/fd namespace.
    arguments.extend(["--proc", "/proc", "--dev", "/dev"])
    arguments.extend(["--chdir", "/work", "/usr/bin/python3", "-I", "-S", "/runtime/worker.py"])
    arguments.extend(worker_arguments)
    return arguments


def run_sandbox(arguments: list[str], pass_fds: tuple[int, ...]) -> tuple[int, bytes]:
    global ACTIVE_PROCESS
    process = subprocess.Popen(
        arguments,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        env={},
        pass_fds=pass_fds,
        preexec_fn=resource_limits,
        start_new_session=True,
    )
    ACTIVE_PROCESS = process
    deadline = time.monotonic() + 10
    try:
        while process.poll() is None:
            descendants, rss = process_tree(process.pid)
            if len(descendants) > MAXIMUM_PROCESSES or rss > MAXIMUM_RSS_KIB or time.monotonic() >= deadline:
                terminate(process)
                return 75, b""
            time.sleep(0.05)
        stdout, _ = process.communicate(timeout=1)
        return process.returncode, stdout
    finally:
        terminate(process)
        ACTIVE_PROCESS = None


def publish(directory_fd: int, output_fd: int, staging_name: str, target_name: str) -> None:
    metadata = os.fstat(output_fd)
    if (
        not stat.S_ISREG(metadata.st_mode)
        or metadata.st_uid != os.getuid()
        or stat.S_IMODE(metadata.st_mode) != 0o600
        or metadata.st_nlink != 1
        or metadata.st_size < len(PNG)
        or metadata.st_size > MAXIMUM_OUTPUT_BYTES
    ):
        fail("sandbox output metadata is invalid")
    if os.pread(output_fd, len(PNG), 0) != PNG:
        fail("sandbox output is not PNG")
    os.fsync(output_fd)
    try:
        existing = os.stat(target_name, dir_fd=directory_fd, follow_symlinks=False)
    except FileNotFoundError:
        existing = None
    if existing is not None and (
        not stat.S_ISREG(existing.st_mode)
        or existing.st_uid != os.getuid()
        or stat.S_IMODE(existing.st_mode) != 0o600
        or existing.st_nlink != 1
    ):
        fail("existing managed output is unsafe")
    os.replace(staging_name, target_name, src_dir_fd=directory_fd, dst_dir_fd=directory_fd)
    os.fsync(directory_fd)


def perform() -> None:
    safe_runtime()
    if len(sys.argv) == 2 and sys.argv[1] == "probe":
        input_fd = os.memfd_create("dotfiles-image-probe", os.MFD_CLOEXEC | os.MFD_ALLOW_SEALING)
        os.write(input_fd, PNG)
        fcntl.fcntl(input_fd, fcntl.F_ADD_SEALS, fcntl.F_SEAL_WRITE | fcntl.F_SEAL_GROW | fcntl.F_SEAL_SHRINK | fcntl.F_SEAL_SEAL)
        os.lseek(input_fd, 0, os.SEEK_SET)
        try:
            code, output = run_sandbox(sandbox_arguments(input_fd, None, ["probe"]), (input_fd,))
            limit_code, _ = run_sandbox(sandbox_arguments(input_fd, None, ["probe-process-limit"]), (input_fd,))
        finally:
            os.close(input_fd)
        if code != 0 or output != b"dotfiles-image-processor-probe-v1\n":
            fail("sandbox probe failed")
        if limit_code != 75:
            fail("sandbox process-tree limit is ineffective")
        sys.stdout.buffer.write(output)
        return
    if len(sys.argv) == 3 and sys.argv[1] == "detect":
        input_fd = sealed_snapshot(sys.argv[2])
        try:
            code, output = run_sandbox(sandbox_arguments(input_fd, None, ["detect"]), (input_fd,))
        finally:
            os.close(input_fd)
        detected = output.decode("ascii", "strict").strip() if code == 0 else ""
        if detected not in FORMATS:
            fail("sandboxed format detection failed", code or 70)
        print(detected)
        return
    if len(sys.argv) < 4 or sys.argv[1] not in {"identify", "transform"} or sys.argv[2] not in FORMATS:
        fail("invalid request", 64)
    operation = sys.argv[1]
    source_format = sys.argv[2]
    input_fd = sealed_snapshot(sys.argv[3])
    try:
        if operation == "identify":
            if len(sys.argv) != 4:
                fail("invalid identify request", 64)
            code, output = run_sandbox(sandbox_arguments(input_fd, None, ["identify", source_format]), (input_fd,))
            if code != 0:
                fail("sandboxed identify failed", code)
            sys.stdout.buffer.write(output)
            return

        if len(sys.argv) != 15:
            fail("invalid transform request", 64)
        target = sys.argv[4]
        with managed_staging(target) as staging:
            directory_fd = staging["directory_fd"]
            output_fd = staging["output_fd"]
            worker_arguments = ["transform", source_format] + sys.argv[5:]
            code, _ = run_sandbox(
                sandbox_arguments(input_fd, output_fd, worker_arguments),
                (input_fd, output_fd),
            )
            if code != 0:
                fail("sandboxed transform failed", code)
            publish(directory_fd, output_fd, staging["staging_name"], staging["target_name"])
    finally:
        os.close(input_fd)
    print(target)


def main() -> None:
    for handled in (signal.SIGHUP, signal.SIGINT, signal.SIGTERM):
        signal.signal(handled, cancel_operation)
    try:
        perform()
    except OperationCancelled:
        fail("operation cancelled", 75)


if __name__ == "__main__":
    main()
