"""TypeScript: interfaces, a union of its variants' interfaces, string enums open to values a newer hub sends."""

from __future__ import annotations

import json
import re

from .model import DEFAULT_HEADER, Call, Def, Field, SchemaError, TypeRef
from .names import comment, plural, sentence, upper_snake


def lower_camel_id(name: str) -> str:
    """An ID type's constructor: SessionID → sessionID."""
    return name[:1].lower() + name[1:]


def external_imports(defs: dict[str, Def], modules: dict[str, str] | None) -> list[str]:
    """import type lines for the IDs defs take from shared files, one per file, in name order."""
    by_file: dict[str, list[str]] = {}
    for d in defs.values():
        if d.external:
            by_file.setdefault(d.external, []).append(d.name)
    out = []
    for f, names in sorted(by_file.items()):
        if f not in (modules or {}):
            raise SchemaError(f"{', '.join(names)} of {f}: no module to import them from")
        out.append(f"import type {{ {', '.join(sorted(names))} }} from {json.dumps(modules[f])};")
    return out + [""] if out else []


def ts_calls(calls: list[Call]) -> list[str]:
    if not calls:
        return []
    out = ["/** One call or event of an extension point's protocol: who sends it, and the definitions its params and",
           " * its answer hold to (no result: any JSON; an event has no answer). */",
           "export interface Call {", '  from: "hub" | "program";', "  params: string;", "  result?: string;",
           "  event?: boolean;", "}", "", "/** The calls and events the schemas describe, by name as on the wire. */",
           "export const CALLS: Record<string, Call> = {"]
    for c in calls:
        parts = [f"from: {json.dumps(c.sender)}", f"params: {json.dumps(c.params)}"]
        if c.result:
            parts.append(f"result: {json.dumps(c.result)}")
        if c.event:
            parts.append("event: true")
        out.append(f"  {json.dumps(c.name)}: {{ {', '.join(parts)} }},")
    return out + ["};", ""]



def gen_ts(defs: dict[str, Def], calls: list[Call] | None = None, header: list[str] | None = None, *,
           defects: str = "ExtensionWire", flavor: str = "ts", modules: dict[str, str] | None = None) -> str:
    """TypeScript, or with flavor "arkts" the subset HarmonyOS's ArkTS accepts (an ID is a plain string alias there:
    ArkTS has no intersection types to brand it with). defects names the class whose unknownEnum hook closed enums
    with x-unknown "count" call. modules: the module each shared file's IDs (Def.external) are imported from."""
    out = [*(header or DEFAULT_HEADER), ""]
    out += external_imports(defs, modules)
    if any(d.count_unknown for d in defs.values()):
        out += [f"/** Called with a closed enum's name and the raw value each time a parse function with x-unknown",
                " * \"count\" meets a value this app does not know. null: nothing is called. */",
                f"export class {defects}Defects {{",
                "  static unknownEnum: ((enumName: string, raw: string) => void) | null = null;", "}", ""]

    any_type = "Object" if flavor == "arkts" else "unknown"

    def ttype(t: TypeRef) -> str:
        k = t.kind
        if k in ("string", "time"):
            return "string"
        if k in ("int", "int64", "number"):
            return "number"
        if k == "bool":
            return "boolean"
        if k == "any":
            return any_type
        if k == "null":
            return "null"
        if k == "array":
            inner = ttype(t.of)
            return f"{inner}[]" if re.fullmatch(r"\w+", inner) else f"({inner})[]"
        if k == "map":
            return f"Record<string, {ttype(t.of)}>"
        return t.ref

    def fields(fs: list[Field]) -> list[str]:
        lines = []
        for f in fs:
            if f.doc and len(f.doc) <= 108:
                lines.append(f"  /** {f.doc} */")
            elif f.doc:
                lines += ["  /**"] + comment("   * ", f.doc) + ["   */"]
            typ = json.dumps(f.literal) if f.literal is not None else ttype(f.type)
            lines.append(f"  {f.name}{'' if f.required else '?'}: {typ};")
        return lines

    def doc(text: str) -> list[str]:
        if not text:
            return []
        return ["/**"] + comment(" * ", text) + [" */"]

    for d in defs.values():
        if d.external:
            continue
        if d.kind == "enum" and d.closed:
            if f"Unknown{d.name}" in defs:
                raise SchemaError(f"{d.name}: Unknown{d.name} is the name of its unknown values")
            values = ", ".join(json.dumps(v) for v in d.values)
            out += doc(d.doc)
            out.append(f"export type {d.name} = {' | '.join(json.dumps(v) for v in d.values)};")
            out.append(f"export const {upper_snake(plural(d.name))}: readonly {d.name}[] = [{values}];")
            out += ["", f"/** A {d.name} this app does not know, kept as it came. */",
                    f"export interface Unknown{d.name} {{", "  unknown: string;", "}", "",
                    f"/** A raw {d.name}: one this app knows, or Unknown{d.name} with the raw value. */",
                    f"export function parse{d.name}(raw: string): {d.name} | Unknown{d.name} {{",
                    f"  const known = {upper_snake(plural(d.name))}.find((value) => value === raw);",
                    "  if (known !== undefined) {", "    return known;", "  }"]
            if d.count_unknown:
                out += [f"  const hook = {defects}Defects.unknownEnum;", "  if (hook !== null) {",
                        f"    hook({json.dumps(d.name)}, raw);", "  }"]
            out += [f"  const unknown: Unknown{d.name} = {{ unknown: raw }};", "  return unknown;", "}", ""]
        elif d.kind == "id":
            out += doc(d.doc or f"An ID: {d.name}.")
            brand = f' & {{ readonly __brand: "{d.name}" }}' if flavor == "ts" else ""
            out += [f"export type {d.name} = string{brand};", f"/** A {d.name} from its raw string. */",
                    f"export function {lower_camel_id(d.name)}(raw: string): {d.name} {{",
                    f"  return raw{'' if flavor != 'ts' else f' as {d.name}'};", "}", ""]
        elif d.kind == "text":
            # A plain alias, not a brand: callers keep passing string literals.
            out += doc(" ".join(x for x in (d.doc or f"A label: {d.name}.", "Free text, not a closed set.") if x))
            out += [f"export type {d.name} = string;", ""]
        elif d.kind == "opaque":
            out += doc(" ".join(x for x in (d.doc, f"Opaque JSON: {sentence(d.reason)}") if x))
            out += [f"export type {d.name} = {any_type};", ""]
        elif d.kind == "enum":
            out += doc(d.doc)
            if flavor == "arkts":
                out.append(f"export type {d.name} = string;")
                out.append(f"export const {upper_snake(plural(d.name))}: readonly string[] = "
                           f"[{', '.join(json.dumps(v) for v in d.values)}];")
            else:
                out.append(f"export type {d.name} = {' | '.join(json.dumps(v) for v in d.values)} | (string & {{}});")
                out.append(f"export const {upper_snake(plural(d.name))} = [{', '.join(json.dumps(v) for v in d.values)}] "
                           "as const;")
            out.append("")
        elif d.kind == "any":
            typ = " | ".join(ttype(t) for t in d.json_types) if d.json_types else any_type
            out += doc(d.doc) + [f"export type {d.name} = {typ};", ""]
        elif d.kind == "struct":
            out += doc(d.doc)
            out.append(f"export type {d.name} = {{" if d.exclusive else f"export interface {d.name} {{")
            if d.union:
                out.append(f"  {defs[d.union].discriminator}: {json.dumps(d.tag)};")
            out += fields([f for f in d.fields if f.name not in d.exclusive])
            if d.extra:
                out += ["  /** Extra keys are opaque JSON; declared properties keep their own types. */",
                        f"  [key: string]: {any_type};"]
            if d.exclusive:
                out += ["} & ("]
                for payload in d.exclusive:
                    parts = [f"{f.name}{'' if f.name == payload else '?'}: "
                             + (ttype(f.type) if f.name == payload else "never")
                             for f in d.fields if f.name in d.exclusive]
                    out.append("  | { " + "; ".join(parts) + " }")
                out += [");", ""]
            else:
                out += ["}", ""]
        elif d.kind == "union":
            out += doc(d.doc)
            out.append(f"export type {d.name} =")
            for i, v in enumerate(d.variants):
                out.append(f"  | {v}{';' if i == len(d.variants) - 1 else ''}")
            tags = ", ".join(json.dumps(defs[v].tag) for v in d.variants)
            if flavor == "arkts":
                out += [f"export const {upper_snake(d.name)}_{upper_snake(plural(d.discriminator))}: readonly string[] = [{tags}];"]
            else:
                out += [f"export const {upper_snake(d.name)}_{upper_snake(plural(d.discriminator))} = [{tags}] as const;"]
            if any(defs[v].since for v in d.variants):
                out.append(f"export const {upper_snake(d.name)}_SINCE: Record<string, number> = {{ "
                           + ", ".join(f"{defs[v].tag}: {defs[v].since}" for v in d.variants) + " };")
            out.append("")
    out += ts_calls(calls or [])
    return "\n".join(out)
