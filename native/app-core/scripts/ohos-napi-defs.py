#!/usr/bin/env python3
"""Generate or check HarmonyOS's native declaration from napi-derive's JSON-lines records.

The default command runs a host `cargo check` with the `ohos` feature. It expands the
proc macros and collects their definitions; it does not link or load a NAPI library.
`--records` checks that formatting and comparison independently of Cargo.
"""

import argparse
import difflib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile

CORE = Path(__file__).resolve().parents[1]
DECLARATION = CORE.parent.parent / "harmony/entry/src/main/cpp/types/libpersona_core/index.d.ts"
PREFIX = """export interface CoreHost {
  changed(version: number): void;
  changedChat?(chat: string, version: number): void;
  run(effect: Uint8Array): void;
}

export interface PersonaCoreNative {
"""
SUFFIX = """}

declare const native: PersonaCoreNative;
export default native;
"""
FUNCTION = re.compile(r"^function ([A-Za-z_$][\w$]*)\((.*)\): (void|number|string|boolean|Buffer)$")
CALLBACK = re.compile(r"^\(\((.*)\) => (void|unknown)\)$")
PARAM = re.compile(r"^([A-Za-z_$][\w$]*)(\??): (.+)$")
HOST_CALLBACKS = {"changed", "changedChat", "effects"}
TYPES = {"number", "string", "boolean", "Buffer", "void"}
SIGNATURES = {
    "coreCreate": [("config", "Uint8Array"), ("changed", "CoreHost['changed']"), ("effects", "CoreHost['run']")],
    "coreCreateWithChat": [("config", "Uint8Array"), ("changed", "CoreHost['changed']"),
                           ("changedChat", "NonNullable<CoreHost['changedChat']>"), ("effects", "CoreHost['run']")],
    "coreDispatch": [("handle", "number"), ("action", "Uint8Array")],
    "coreDrain": [("handle", "number"), ("chat", "string")],
    "coreFlush": [("handle", "number"), ("timeoutMillis", "number")],
    "coreClose": [("handle", "number")],
    "coreConfig": [],
    "coreProjectSort": [("input", "string")],
}


class DefinitionError(ValueError):
    """A macro definition cannot be represented by the committed declaration."""


def split_parameters(text):
    parts = []
    depth = 0
    start = 0
    for index, char in enumerate(text):
        if char in "([":
            depth += 1
        elif char in ")]":
            depth -= 1
            if depth < 0:
                raise DefinitionError(f"unbalanced type: {text}")
        elif char == "," and depth == 0:
            parts.append(text[start:index])
            start = index + 1
    if depth != 0:
        raise DefinitionError(f"unbalanced type: {text}")
    parts.append(text[start:])
    return [part.strip() for part in parts] if text else []


def ts_type(text):
    if text in TYPES:
        return "Uint8Array" if text == "Buffer" else text
    match = CALLBACK.fullmatch(text)
    if not match:
        raise DefinitionError(f"unsupported generated type: {text}")
    arguments, return_type = match.groups()
    if return_type != "void":
        raise DefinitionError("NAPI callbacks must return void")
    parameters = []
    for argument in split_parameters(arguments):
        parsed = PARAM.fullmatch(argument)
        if not parsed or parsed.group(2):
            raise DefinitionError(f"unsupported callback parameter: {argument}")
        name, _, kind = parsed.groups()
        if kind not in TYPES:
            raise DefinitionError(f"unsupported callback parameter type: {kind}")
        parameters.append((name, kind))
    return parameters


def parse_definition(record):
    if set(record) != {"kind", "name", "js_doc", "def"}:
        raise DefinitionError("unexpected napi-derive record fields")
    if record["kind"] != "fn" or record["js_doc"]:
        raise DefinitionError("only undocumented NAPI functions belong in this declaration")
    match = FUNCTION.fullmatch(record["def"])
    if not match:
        raise DefinitionError(f"unsupported generated function: {record['def']}")
    name, arguments, return_type = match.groups()
    if name != record["name"]:
        raise DefinitionError("generated function name does not match its record")
    parameters = []
    for argument in split_parameters(arguments):
        parsed = PARAM.fullmatch(argument)
        if not parsed:
            raise DefinitionError(f"unsupported generated parameter: {argument}")
        parameter, optional, kind = parsed.groups()
        if optional:
            raise DefinitionError("optional parameters need an explicit declaration policy")
        parameters.append((parameter, ts_type(kind)))
    return name, parameters, return_type


def host_parameter(parameter, kind):
    if not isinstance(kind, list):
        raise DefinitionError(f"{parameter} must be a NAPI callback")
    expected_types = {
        "changed": ["number"],
        "changedChat": ["string", "number"],
        "effects": ["Buffer"],
    }[parameter]
    if [kind for _, kind in kind] != expected_types:
        raise DefinitionError(f"{parameter} has the wrong positional callback arguments")
    reference = {
        "changed": "CoreHost['changed']",
        "changedChat": "NonNullable<CoreHost['changedChat']>",
        "effects": "CoreHost['run']",
    }[parameter]
    return reference


def generate(records):
    definitions = []
    for record in records:
        name, parameters, return_type = parse_definition(record)
        definitions.append((name, parameters, return_type))
    names = [name for name, _, _ in definitions]
    if len(names) != len(set(names)):
        raise DefinitionError("napi-derive must emit each function once")
    if sorted(names) != ["coreClose", "coreConfig", "coreCreate", "coreCreateWithChat", "coreDispatch", "coreDrain", "coreFlush", "coreProjectSort"]:
        raise DefinitionError(f"unexpected NAPI function set: {', '.join(names)}")
    order = ["coreCreate", "coreCreateWithChat", "coreDispatch", "coreDrain", "coreFlush", "coreClose", "coreConfig", "coreProjectSort"]
    lines = []
    for name, parameters, return_type in sorted(definitions, key=lambda definition: order.index(definition[0])):
        arguments = []
        for parameter, kind in parameters:
            if parameter in HOST_CALLBACKS:
                kind = host_parameter(parameter, kind)
            arguments.append((parameter, kind))
        if arguments != SIGNATURES[name]:
            raise DefinitionError(f"{name} has an incomplete or changed positional parameter contract")
        if return_type != ("void" if name == "coreClose" else "string" if name == "coreProjectSort"
                           else "Buffer" if name in ("coreDrain", "coreConfig")
                           else "number" if name.startswith("coreCreate") else "boolean"):
            raise DefinitionError(f"{name} has the wrong return type")
        lines.append(f"  {name}({', '.join(parameter + ': ' + kind for parameter, kind in arguments)}): {ts_type(return_type)};")
    return PREFIX + "\n".join(lines) + "\n" + SUFFIX


def read_records(path):
    records = []
    for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        if not line.strip():
            continue
        try:
            record = json.loads(line)
        except json.JSONDecodeError as error:
            raise DefinitionError(f"invalid JSON on record line {number}: {error.msg}") from error
        if not isinstance(record, dict):
            raise DefinitionError(f"record line {number} is not an object")
        records.append(record)
    return records


def compare(expected, output):
    if expected == output:
        print(f"OHOS NAPI declaration: up to date ({DECLARATION})")
        return
    diff = "".join(difflib.unified_diff(expected.splitlines(True), output.splitlines(True),
                                         fromfile=str(DECLARATION), tofile="generated index.d.ts"))
    raise DefinitionError("declaration differs; rerun the generator and inspect the diff\n" + diff)


def cargo_records(destination):
    environment = os.environ.copy()
    environment["NAPI_TYPE_DEF_TMP_FOLDER"] = destination
    environment["CARGO_NET_OFFLINE"] = "true"
    subprocess.run(["cargo", "check", "--locked", "--offline", "-p", "persona-core-ohos",
                    "--features", "ohos", "--target-dir", str(Path(destination) / "target")],
                   cwd=CORE, env=environment, check=True)
    record_file = Path(destination) / "persona-core-ohos"
    if not record_file.is_file():
        raise DefinitionError("napi-derive produced no definition records")
    return read_records(record_file)


def main():
    global DECLARATION
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--declaration", type=Path, default=DECLARATION)
    parser.add_argument("--records", type=Path, help="read macro records instead of running host cargo check")
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--check", action="store_true", help="fail rather than rewrite a different declaration")
    mode.add_argument("--print-records", action="store_true", help="print normalized macro records for inspection")
    args = parser.parse_args()
    DECLARATION = args.declaration.resolve()
    if args.print_records and not args.records:
        parser.error("--print-records requires --records")
    if args.print_records:
        for record in read_records(args.records):
            parse_definition(record)
            print(json.dumps(record, ensure_ascii=False, separators=(",", ":"), sort_keys=True))
        return
    if args.records:
        records = read_records(args.records)
    else:
        with tempfile.TemporaryDirectory(prefix="persona-ohos-napi-defs-") as temporary:
            records = cargo_records(temporary)
    output = generate(records)
    if args.check:
        compare(DECLARATION.read_text(encoding="utf-8"), output)
    else:
        DECLARATION.parent.mkdir(parents=True, exist_ok=True)
        DECLARATION.write_text(output, encoding="utf-8")
        print(f"OHOS NAPI declaration: generated {DECLARATION}")


if __name__ == "__main__":
    try:
        main()
    except (DefinitionError, OSError, subprocess.CalledProcessError) as error:
        sys.exit(f"OHOS NAPI declaration: FAILED ({error})")
