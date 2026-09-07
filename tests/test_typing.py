"""Checks for policy/report failure routing; actual semantic checking requires LuaLS."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, ROOT / "scripts" / filename)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


policy = load("source_policy", "check-source.py")
typecheck = load("typecheck", "typecheck.py")


class TypingTests(unittest.TestCase):
    def test_public_api_requires_complete_signature(self):
        self.assertTrue(policy.annotation_errors("function M.open(opts)\nend", True))
        typed = "---@param opts Options\n---@return boolean\nfunction M.open(opts)\nend"
        self.assertEqual(policy.annotation_errors(typed, True), [])
        self.assertTrue(policy.annotation_errors(typed.replace("opts Options", "other Options"), True))

    def test_escape_hatches_and_suppressions_fail(self):
        for annotation in ("---@param x any", "---@param x table", "---@return function",
                           "---@return unknown", "---@diagnostic disable: no-unknown"):
            with self.subTest(annotation=annotation):
                self.assertTrue(policy.annotation_errors(annotation))
        self.assertEqual(policy.annotation_errors("---@param x table<string, integer>"), [])

    def test_report_is_required_and_malformed_reports_fail(self):
        with tempfile.TemporaryDirectory() as temporary:
            report = Path(temporary) / "check.json"
            with self.assertRaises(ValueError):
                typecheck.diagnostics(report)
            for content in ("", "null", '"ok"', '{"file":{}}', '{"file":[{}]}'):
                report.write_text(content)
                with self.assertRaises(ValueError):
                    typecheck.diagnostics(report)
            for empty in ({}, []):
                report.write_text(json.dumps(empty))
                self.assertEqual(typecheck.diagnostics(report), [])
            report.write_text(json.dumps({"file:///fixture.lua": [{"message": "mismatch", "code": "x"}]}))
            self.assertEqual(len(typecheck.diagnostics(report)), 1)

    def test_diagnostic_output_is_bounded_and_project_relative(self):
        entry = {"source_uri": (ROOT / "lua/example.lua").as_uri(),
                 "message": "type mismatch\n" + "x" * 1000, "code": "param-type-mismatch",
                 "range": {"start": {"line": 2, "character": 3}}}
        text = typecheck.describe([entry] * 102, ROOT)
        self.assertTrue(text.startswith("lua/example.lua:3:4: param-type-mismatch:"))
        self.assertNotIn(str(ROOT), text)
        self.assertEqual(len(text.splitlines()), 101)
        self.assertTrue(all(len(line) <= 600 for line in text.splitlines()))
        external = dict(entry, source_uri="file:///outside/private.lua")
        self.assertEqual(typecheck.describe([external], ROOT),
                         "external Lua library: diagnostic (details withheld)")

    def test_missing_tool_fails(self):
        with self.assertRaises(ValueError):
            typecheck.executable("definitely-missing-plugin-checker")


if __name__ == "__main__":
    unittest.main()
