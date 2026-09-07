#!/usr/bin/python3
"""Fixed sandbox-internal operations for the Neovim image viewport plugin."""

import os
import re
import socket
import subprocess
import sys
import time

MAGICK = "/usr/bin/magick-im7.q16"
GHOSTSCRIPT = "/usr/bin/gs"
INPUT = "/input/source"
OUTPUT = "/output/frame"
NORMALIZED = "/tmp/source.png"
FORMATS = {
    "png": "PNG",
    "jpeg": "JPEG",
    "webp": "WEBP",
    "gif": "GIF",
    "bmp": "BMP",
    "heic": "HEIC",
    "xpm": "XPM",
    "ico": "ICO",
    "avif": "AVIF",
    "svg": "MSVG",
    "xml": "MSVG",
    "pdf": "PDF",
}
INTEGER = re.compile(r"^(0|[1-9][0-9]{0,5})$")


def fail(message: str) -> "NoReturn":
    raise SystemExit(f"image worker: {message}")


def bounded_integer(value: str, minimum: int = 0, maximum: int = 16384) -> int:
    if not INTEGER.fullmatch(value):
        fail("invalid numeric argument")
    parsed = int(value)
    if parsed < minimum or parsed > maximum:
        fail("numeric argument is outside the reviewed range")
    return parsed


def run(arguments: list[str], *, capture: bool = False) -> str:
    result = subprocess.run(
        arguments,
        check=False,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE if capture else subprocess.DEVNULL,
        stderr=subprocess.PIPE,
        text=True,
        timeout=9,
        env={
            "HOME": "/tmp/home",
            "LANG": "C.UTF-8",
            "LC_ALL": "C.UTF-8",
            "MAGICK_CONFIGURE_PATH": "/policy",
            "MAGICK_TEMPORARY_PATH": "/tmp",
            "PATH": "/usr/bin:/bin",
            "TMPDIR": "/tmp",
        },
    )
    if result.returncode != 0:
        fail("sandboxed decoder rejected the image")
    return result.stdout if capture else ""


def input_spec(source_format: str) -> str:
    if source_format not in FORMATS:
        fail("unsupported image format")
    if source_format == "pdf":
        run(
            [
                GHOSTSCRIPT,
                "-q",
                "-dSAFER",
                "-dBATCH",
                "-dNOPAUSE",
                "-dNOPROMPT",
                "-dFirstPage=1",
                "-dLastPage=1",
                "-sDEVICE=pngalpha",
                "-r144",
                f"-sOutputFile={NORMALIZED}",
                INPUT,
            ]
        )
        return f"PNG:{NORMALIZED}[0]"
    return f"{FORMATS[source_format]}:{INPUT}[0]"


def validate_svg_contents() -> None:
    tail = b""
    with open(INPUT, "rb", buffering=0) as source:
        while True:
            chunk = source.read(1024 * 1024)
            if not chunk:
                return
            inspected = (tail + chunk).upper()
            if b"<!DOCTYPE" in inspected or b"<!ENTITY" in inspected:
                fail("SVG declarations are not permitted")
            tail = inspected[-16:]


def detected_format() -> str:
    with open(INPUT, "rb", buffering=0) as source:
        header = source.read(8192)
        source.seek(0, os.SEEK_END)
        size = source.tell()
        tail = b""
        if size >= 2:
            source.seek(-2, os.SEEK_END)
            tail = source.read(2)
    if header.startswith(b"\x89PNG\r\n\x1a\n"):
        return "png"
    if header.startswith(b"\xff\xd8\xff") and tail == b"\xff\xd9":
        return "jpeg"
    if header.startswith(b"RIFF") and header[8:12] == b"WEBP":
        return "webp"
    if header.startswith((b"GIF87a", b"GIF89a")):
        return "gif"
    if header.startswith(b"BM"):
        return "bmp"
    if header[4:12] in {b"ftypheic", b"ftypheix", b"ftyphevc", b"ftyphevx", b"ftypmif1"}:
        return "heic"
    if header[4:12] in {b"ftypavif", b"ftypavis"}:
        return "avif"
    if header.startswith(b"/* XPM */"):
        return "xpm"
    if header.startswith(b"\x00\x00\x01\x00"):
        return "ico"
    if header.startswith(b"%PDF"):
        return "pdf"
    text = header.decode("utf-8", "ignore")
    if "<!DOCTYPE" in text.upper() or "<!ENTITY" in text.upper():
        fail("SVG declarations are not permitted")
    compact = text.lstrip("\ufeff\x00\t\r\n ")
    if compact.startswith("<svg"):
        return "svg"
    if compact.startswith("<?xml") and re.search(r"\?>\s*(?:<!--.*?-->\s*)*<svg(?:\s|>)", compact, re.S):
        return "xml"
    fail("input magic is unsupported")


def verified_input(source_format: str) -> str:
    actual = detected_format()
    if actual != source_format:
        fail("input magic changed")
    if actual in {"svg", "xml"}:
        validate_svg_contents()
    return input_spec(source_format)


def probe() -> None:
    policy = run([MAGICK, "identify", "-list", "policy"], capture=True)
    if "/policy/policy.xml" not in policy or "/etc/ImageMagick" in policy:
        fail("unexpected ImageMagick policy source")
    managed_policy = policy.split("Path: /policy/policy.xml", 1)[1].split("\nPath:", 1)[0]
    security_rules = []
    current_domain = None
    current_attributes = {}
    for line in managed_policy.splitlines() + ["  Policy: End"]:
        if line.startswith("  Policy: "):
            if current_domain in {"Delegate", "Filter", "Module", "Coder"}:
                security_rules.append((current_domain, current_attributes))
            current_domain = line.removeprefix("  Policy: ").strip()
            current_attributes = {}
        elif line.startswith("    ") and ":" in line:
            name, value = line.strip().split(":", 1)
            current_attributes[name] = " ".join(value.split())
    expected_security_rules = [
        ("Delegate", {"rights": "None", "pattern": "*"}),
        ("Filter", {"rights": "None", "pattern": "*"}),
        ("Module", {"rights": "None", "pattern": "*"}),
        (
            "Module",
            {"rights": "Read Write", "pattern": "{PNG,JPEG,WEBP,GIF,BMP,HEIC,XPM,ICON,SVG,MVG}"},
        ),
        (
            "Coder",
            {"rights": "None", "pattern": "{HTTP,HTTPS,URL,MSL,TEXT,LABEL,CAPTION,EPHEMERAL,INLINE}"},
        ),
    ]
    # Fail closed when a compatible ImageMagick update changes the rendered
    # rule surface; later broad rules can override earlier deny entries.
    if security_rules != expected_security_rules:
        fail("ImageMagick security policy rules are not exact")
    required = (
        r"name: list-length\s+value: 8",
        r"name: width\s+value: 16KP",
        r"name: height\s+value: 16KP",
    )
    if any(not re.search(pattern, policy) for pattern in required):
        fail("ImageMagick policy is incomplete")
    denied = subprocess.run(
        [MAGICK, "identify", "@/input/source"],
        stdin=subprocess.DEVNULL,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        timeout=2,
        env={"MAGICK_CONFIGURE_PATH": "/policy", "PATH": "/usr/bin:/bin"},
    )
    if denied.returncode == 0:
        fail("ImageMagick indirect-path policy is ineffective")
    nested = subprocess.run(
        ["/usr/bin/bwrap", "--unshare-user", "--", "/usr/bin/true"],
        stdin=subprocess.DEVNULL,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        timeout=2,
    )
    if nested.returncode == 0:
        fail("nested user namespaces are available")
    if os.path.exists("/etc/passwd"):
        fail("sandbox exposes an unreviewed host path")
    connection = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    connection.settimeout(0.25)
    try:
        if connection.connect_ex(("1.1.1.1", 53)) == 0:
            fail("sandbox network namespace is ineffective")
    finally:
        connection.close()
    print("dotfiles-image-processor-probe-v1")


def probe_process_limit() -> None:
    """Create a hostile comm plus enough descendants to require supervisor termination."""
    with open("/proc/self/comm", "w", encoding="ascii") as handle:
        handle.write("worker ) spaced")
    children = []
    try:
        for _ in range(40):
            child = os.fork()
            if child == 0:
                time.sleep(3)
                os._exit(0)
            children.append(child)
        time.sleep(3)
    finally:
        for child in children:
            try:
                os.kill(child, 9)
            except ProcessLookupError:
                pass
        for child in children:
            try:
                os.waitpid(child, 0)
            except ChildProcessError:
                pass
    fail("supervisor accepted too many sandbox descendants")


def identify(source_format: str) -> None:
    source = verified_input(source_format)
    dimensions = run([MAGICK, "identify", "-format", "%w %h", source], capture=True)
    if not re.fullmatch(r"[1-9][0-9]{0,5} [1-9][0-9]{0,5}", dimensions):
        fail("decoder returned invalid dimensions")
    width, height = (int(part) for part in dimensions.split())
    if width > 16384 or height > 16384 or width * height > 16 * 1024 * 1024:
        fail("source dimensions exceed the reviewed limit")
    print(dimensions)


def detect() -> None:
    actual = detected_format()
    if actual in {"svg", "xml"}:
        validate_svg_contents()
    print(actual)


def transform(arguments: list[str]) -> None:
    if len(arguments) != 11:
        fail("invalid transform request")
    source_format, crop_stage = arguments[0:2]
    if crop_stage not in {"none", "before", "after"}:
        fail("invalid crop stage")
    width = bounded_integer(arguments[2], 1)
    height = bounded_integer(arguments[3], 1)
    crop = [bounded_integer(value) for value in arguments[4:8]]
    effect = arguments[8]
    effect_value = arguments[9]
    output_format = arguments[10]
    if output_format != "png" or effect not in {"none", "brightness", "saturation", "hue"}:
        fail("invalid transform mode")
    if width * height > 16 * 1024 * 1024:
        fail("output dimensions exceed the reviewed limit")
    if effect == "none":
        if effect_value != "0":
            fail("invalid effect value")
    else:
        try:
            numeric_effect = float(effect_value)
        except ValueError:
            fail("invalid effect value")
        if not -200.0 <= numeric_effect <= 200.0:
            fail("effect value is outside the reviewed range")

    command = [MAGICK, verified_input(source_format)]
    crop_geometry = f"{crop[2]}x{crop[3]}+{crop[0]}+{crop[1]}"
    if crop_stage == "before":
        command.extend(["-crop", crop_geometry, "+repage"])
    command.extend(["-resize", f"{width}x{height}!"])
    if crop_stage == "after":
        command.extend(["-crop", crop_geometry, "+repage"])
    if effect == "brightness":
        command.extend(["-modulate", f"{float(effect_value)},100,100"])
    elif effect == "saturation":
        command.extend(["-modulate", f"100,{float(effect_value)},100"])
    elif effect == "hue":
        command.extend(["-modulate", f"100,100,{float(effect_value)}"])
    command.append(f"PNG:{OUTPUT}")
    run(command)


def main() -> None:
    if len(sys.argv) == 2 and sys.argv[1] == "probe":
        probe()
        return
    if len(sys.argv) == 2 and sys.argv[1] == "probe-process-limit":
        probe_process_limit()
        return
    if len(sys.argv) == 2 and sys.argv[1] == "detect":
        detect()
        return
    if len(sys.argv) == 3 and sys.argv[1] == "identify":
        identify(sys.argv[2])
        return
    if len(sys.argv) >= 3 and sys.argv[1] == "transform":
        transform(sys.argv[2:])
        return
    fail("invalid request")


if __name__ == "__main__":
    main()
