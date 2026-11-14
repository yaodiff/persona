"""The NAPI definition drift check preserves the exact callback and byte-buffer contract (#7414)."""

import copy
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

CORE = Path(__file__).resolve().parents[1]
SCRIPT = CORE / "scripts/ohos-napi-defs.py"
SPEC = importlib.util.spec_from_file_location("ohos_napi_defs_under_test", SCRIPT)
GENERATOR = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(GENERATOR)

RECORDS = [
    "function coreCreate(config: Buffer, changed: ((arg: number) => void), effects: ((arg: Buffer) => void)): number",
    "function coreCreateWithChat(config: Buffer, changed: ((arg: number) => void), changedChat: ((arg0: string, arg1: number) => void), effects: ((arg: Buffer) => void)): number",
    "function coreDispatch(handle: number, action: Buffer): boolean",
    "function coreDrain(handle: number, chat: string): Buffer",
    "function coreFlush(handle: number, timeoutMillis: number): boolean",
    "function coreClose(handle: number): void",
    "function coreConfig(): Buffer",
    "function coreProjectSort(input: string): string",
]


def records():
    result = []
    for definition in RECORDS:
        name = definition.split("(", 1)[0].removeprefix("function ")
        result.append({"kind": "fn", "name": name, "js_doc": "", "def": definition})
    return result


class OhosNapiDefinitionsTests(unittest.TestCase):
    def test_real_generator_shape_matches_committed_declaration(self):
        declaration = CORE.parent.parent / "harmony/entry/src/main/cpp/types/libpersona_core/index.d.ts"
        self.assertEqual(GENERATOR.generate(records()), declaration.read_text(encoding="utf-8"))

    def test_rejects_error_first_or_numeric_array_callbacks(self):
        for old, new in [
            ("changed: ((arg: number) => void)", "changed: ((err: Error | null, arg: number) => void)"),
            ("effects: ((arg: Buffer) => void)", "effects: ((arg: Array<number>) => void)"),
            ("effects: ((arg: Buffer) => void)", "effects: ((err: Error | null, arg: Buffer) => void)"),
        ]:
            with self.subTest(new=new):
                changed = records()
                changed[0]["def"] = changed[0]["def"].replace(old, new)
                with self.assertRaises(GENERATOR.DefinitionError):
                    GENERATOR.generate(changed)

    def test_rejects_tuple_instead_of_two_targeted_arguments(self):
        changed = records()
        changed[1]["def"] = changed[1]["def"].replace(
            "changedChat: ((arg0: string, arg1: number) => void)",
            "changedChat: ((arg: [string, number]) => void)",
        )
        with self.assertRaisesRegex(GENERATOR.DefinitionError, "unsupported callback parameter type"):
            GENERATOR.generate(changed)

    def test_rejects_removed_required_callback_parameters(self):
        for index, old in [(0, ", changed: ((arg: number) => void)"),
                           (1, ", changedChat: ((arg0: string, arg1: number) => void)")]:
            with self.subTest(index=index):
                changed = records()
                changed[index]["def"] = changed[index]["def"].replace(old, "")
                with self.assertRaisesRegex(GENERATOR.DefinitionError, "incomplete or changed positional"):
                    GENERATOR.generate(changed)

    def test_rejects_callback_return_and_missing_or_duplicate_exports(self):
        changed = records()
        changed[0]["def"] = changed[0]["def"].replace("=> void", "=> unknown")
        with self.assertRaisesRegex(GENERATOR.DefinitionError, "return void"):
            GENERATOR.generate(changed)
        with self.assertRaisesRegex(GENERATOR.DefinitionError, "unexpected NAPI function set"):
            GENERATOR.generate(records()[:-1])
        with self.assertRaisesRegex(GENERATOR.DefinitionError, "once"):
            GENERATOR.generate(records() + [copy.deepcopy(records()[0])])

    def test_check_mode_fails_on_drift_and_generation_is_repeatable(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            record_file = root / "records.jsonl"
            declaration = root / "index.d.ts"
            record_file.write_text("".join(json.dumps(record, separators=(",", ":")) + "\n" for record in records()))
            declaration.write_text(GENERATOR.generate(records()))
            command = ["python3", "-I", str(SCRIPT), "--records", str(record_file), "--declaration", str(declaration)]
            subprocess.run(command + ["--check"], check=True, capture_output=True, text=True)
            original = declaration.read_text()
            for old, new in [("Uint8Array", "Array<number>"),
                             ("changed(version: number): void", "changed(version: string): void"),
                             ("run(effect: Uint8Array): void", "run(effect: Array<number>): void")]:
                with self.subTest(declaration_drift=old):
                    declaration.write_text(original.replace(old, new))
                    mismatch = subprocess.run(command + ["--check"], capture_output=True, text=True)
                    self.assertNotEqual(mismatch.returncode, 0)
                    self.assertIn("declaration differs", mismatch.stderr)
            declaration.write_text(original.replace("Uint8Array", "Array<number>"))
            mismatch = subprocess.run(command + ["--check"], capture_output=True, text=True)
            self.assertNotEqual(mismatch.returncode, 0)
            self.assertIn("declaration differs", mismatch.stderr)
            subprocess.run(command, check=True, capture_output=True, text=True)
            first = declaration.read_bytes()
            subprocess.run(command, check=True, capture_output=True, text=True)
            self.assertEqual(first, declaration.read_bytes())
            self.assertEqual(first.decode(), GENERATOR.generate(records()))


if __name__ == "__main__":
    unittest.main()
