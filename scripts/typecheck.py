#!/usr/bin/env python3
"""Run LuaLS with trusted tools, private state and fail-closed diagnostic reports."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parents[1]


def diagnostics(report):
    if not report.is_file() or report.is_symlink() or report.stat().st_size > 16 * 1024 * 1024:
        raise ValueError("LuaLS report missing or invalid")
    data = json.loads(report.read_text())
    if data == []:
        return []
    if not isinstance(data, dict):
        raise ValueError("LuaLS report must map source URIs to diagnostics")
    result = []
    for uri, entries in data.items():
        if not isinstance(uri, str) or not isinstance(entries, list):
            raise ValueError("malformed LuaLS diagnostic collection")
        for entry in entries:
            if not isinstance(entry, dict) or not isinstance(entry.get("message"), str):
                raise ValueError("malformed LuaLS diagnostic")
            region = entry.get("range", {})
            if not isinstance(region, dict) or not isinstance(region.get("start", {}), dict):
                raise ValueError("malformed LuaLS source range")
            for value in region.get("start", {}).values():
                if type(value) is not int or value < 0:
                    raise ValueError("malformed LuaLS source position")
            result.append(dict(entry, source_uri=uri))
    return result


def describe(entries, root):
    """Bound diagnostics to owned source locations; never print private tool logs."""
    lines = []
    for entry in entries[:100]:
        uri = urlsplit(entry.get("source_uri", ""))
        try:
            if uri.scheme != "file" or uri.netloc not in ("", "localhost"):
                raise ValueError("non-local source")
            relative = Path(unquote(uri.path)).resolve().relative_to(root.resolve()).as_posix()
        except ValueError:
            lines.append("external Lua library: diagnostic (details withheld)")
            continue
        start = entry.get("range", {}).get("start", {})
        line = start.get("line", 0) + 1
        column = start.get("character", 0) + 1
        message = entry["message"].replace(str(root), ".")
        text = f"{relative}:{line}:{column}: {entry.get('code', 'diagnostic')}: {message}"
        lines.append("".join(char if char.isprintable() else " " for char in text)[:600])
    if len(entries) > 100:
        lines.append(f"{len(entries) - 100} additional diagnostics omitted")
    return "\n".join(lines)


def executable(name):
    found = shutil.which(name)
    if found is None:
        raise ValueError("required executable unavailable: " + name)
    path = Path(found).resolve(strict=True)
    if not path.is_file() or not os.access(path, os.X_OK):
        raise ValueError("invalid tool executable")
    return str(path)


def run_check(server, workspace, config, private, env):
    private.mkdir(mode=0o700)
    report = private / "check.json"
    command = [server, "--check", str(workspace), "--checklevel=Warning", "--check_format=json",
               "--check_out_path", str(report), "--configpath", str(config),
               "--logpath", str(private / "log"), "--metapath", str(private / "meta")]
    result = subprocess.run(command, env=env, cwd=workspace, timeout=300, capture_output=True)
    entries = diagnostics(report)
    return result.returncode, entries


def main():
    server = executable(os.environ.get("LUA_LS") or "lua-language-server")
    nvim = executable(os.environ.get("NVIM") or "nvim")
    with tempfile.TemporaryDirectory(prefix="plugin-luals-") as temporary:
        private = Path(temporary)
        env = {"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8"}
        for name in ("HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "TMPDIR"):
            path = private / name.lower()
            path.mkdir(mode=0o700)
            env[name] = str(path)
        env["NVIM_LOG_FILE"] = str(private / "nvim.log")
        probe = subprocess.run([nvim, "--clean", "--headless", "-u", "NONE", "-i", "NONE", "--noplugin",
                                "-c", 'lua io.write(vim.env.VIMRUNTIME)', "-c", "qa!"],
                               env=env, cwd=private, capture_output=True, text=True, timeout=15, check=True)
        runtime = Path(probe.stdout)
        if not runtime.is_absolute() or not (runtime / "lua/vim").is_dir():
            raise ValueError("Neovim annotation library unavailable")
        settings = json.loads((ROOT / ".luarc.json").read_text())
        settings["workspace.library"] = [str(runtime / "lua")]
        config = private / "luarc.json"
        config.write_text(json.dumps(settings))
        # Confirm the tool actually diagnoses a known bad typed call before trusting
        # an empty report. No fixture is mixed into production source.
        canary = private / "canary"
        canary.mkdir(mode=0o700)
        (canary / "bad.lua").write_text(
            "---@param value integer\n---@return integer\n"
            "local function increment(value) return value + 1 end\nincrement('wrong')\n")
        _, entries = run_check(server, canary, config, private / "canary-report", env)
        if not any(entry.get("code") == "param-type-mismatch" for entry in entries):
            raise ValueError("LuaLS failed to diagnose the typed-call canary")
        status, entries = run_check(server, ROOT, config, private / "source-report", env)
        if status or entries:
            codes = sorted({str(entry.get("code", "unspecified")) for entry in entries})
            detail = describe(entries, ROOT)
            raise ValueError(f"LuaLS failed: exit {status}, {len(entries)} diagnostics ({', '.join(codes)})\n{detail}")
        print("LuaLS runtime typecheck: passed")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        raise SystemExit("typecheck failed: " + str(error))
