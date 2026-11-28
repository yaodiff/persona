"""schema/wire (#9117, #7891 stage 5): the generated hub wire types match their schemas, and every sample the shared
fixtures hold for them is exactly what the schema describes.

    python3 scripts/test/test_wire_schema.py

The generator's --check covers all generated files (schema/extension and schema/wire), so nobody can edit one by hand.
A sample must name no field the schema lacks, at any depth, and miss no required one: a generated client type would
otherwise drop it or fail on it, changing the bytes it writes back. Every definition is reached by some sample.
"""

import functools
import importlib.util
import json
import os
import re
import subprocess
import sys
import unittest
from pathlib import Path

IMPLEMENTATION_ROOT = Path(__file__).resolve().parents[2]


def _source_root():
    configured = os.environ.get("PERSONA_CI_SOURCE_ROOT")
    if configured:
        if not Path(configured).is_absolute():
            raise ValueError("PERSONA_CI_SOURCE_ROOT must be absolute")
        return Path(configured)
    return IMPLEMENTATION_ROOT


ROOT = _source_root()
WIRE = ROOT / "schema" / "wire"
FIXTURES = ROOT / "spec" / "fixtures"
GENERATOR = IMPLEMENTATION_ROOT / "scripts" / "gen" / "extension_schema.py"

spec = importlib.util.spec_from_file_location("test_extension_schema", IMPLEMENTATION_ROOT / "scripts/test/test_extension_schema.py")
ext = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ext)


def cases(name):
    return json.loads((FIXTURES / name).read_text())["cases"]


def recent_setups():
    """(definition, sample) of every setup in recents/recent_setups.json."""
    for c in cases("recents/recent_setups.json"):
        i, e = c.get("input", {}), c.get("expected", {})
        for key in ("before", "entries"):
            for s in i.get(key, []):
                yield "RecentSetup", f"{c['name']}.input.{key}", s
        if "setup" in i:
            yield "RecentSetup", f"{c['name']}.input.setup", i["setup"]
        for s in e.get("after", []):
            yield "RecentSetup", f"{c['name']}.expected.after", s


def domain_cases(domain):
    """(where, case) of every case in spec/fixtures/<domain>/*.json."""
    for path in sorted((FIXTURES / domain).glob("*.json")):
        for c in cases(f"{domain}/{path.name}"):
            yield f"{domain}/{path.name} {c['name']}", c


def todo_views():
    """(definition, sample) of every view todo-view expects, every counts and places todo-counts expects, every
    timeline, result and recap todo-rounds expects with what the item route carries of them, every answer and
    reply todo-asks holds, and every decision push's todo todo-push expects."""
    for where, c in domain_cases("todo-view"):
        view = c["expected"]["view"]
        yield "TodoSummary", f"{where}.expected.view", view
        if line := view.get("line"):
            # Verify the present producer's vocabulary. A consumer can still read a newer, unknown line or argument.
            yield "TodoLineKey", f"{where}.expected.view.line.key", line["key"]
            for key in line.get("args", {}):
                yield "TodoLineArg", f"{where}.expected.view.line.args", key
    for where, c in domain_cases("todo-counts"):
        yield "TodoCounts", f"{where}.expected.counts", c["expected"]["counts"]
        yield "TodoPlaces", f"{where}.expected.places", c["expected"]["places"]
    for where, c in domain_cases("todo-rounds"):
        e = c["expected"]
        yield "TodoTimeline", f"{where}.expected.timeline", e["timeline"]
        for i, r in enumerate(e.get("results", [])):
            yield "TodoResult", f"{where}.expected.results[{i}]", r
        if "recap" in e:
            yield "TodoRecap", f"{where}.expected.recap", e["recap"]
        yield "TodoItemDetail", f"{where} (the item route's)", item_detail(e)
    for where, c in domain_cases("todo-push"):
        yield "TodoDecidePush", f"{where}.expected.todo", c["expected"]["todo"]
    for where, c in domain_cases("todo-asks"):
        yield "TodoAskAnswer", f"{where}.input.answer", c["input"]["answer"]
        if "answered" in c["expected"]:
            yield "TodoAskAnswered", f"{where}.expected.answered", c["expected"]["answered"]


def item_detail(expected):
    """What GET /api/v1/todos/{id} carries beside the row for a todo-rounds case (#10465): the rounds the paper shows
    (the case's latest), how many there are, its recap and results."""
    rounds = expected["timeline"]["rounds"]
    detail = {"rounds": [r for r in rounds if r["id"] in expected.get("latest", [])], "rounds_total": len(rounds)}
    for key in ("recap", "results"):
        if key in expected:
            detail[key] = expected[key]
    return detail


def counts_views():
    """(definition, sample) of every view counts/view_samples.json expects (#15512), and of every view a device keeps
    or gets in counts/views.json (#15893)."""
    for c in cases("counts/view_samples.json"):
        yield "CountsView", f"counts/view_samples.json {c['name']}.expected.view", c["expected"]["view"]
    for c in cases("counts/views.json"):
        where = f"counts/views.json {c['name']}"
        for part in ("given", "then"):
            for i, k in enumerate(c[part]["kept"]):
                yield "CountsView", f"{where}.{part}.kept[{i}].view", k["view"]
        if "view" in c["when"]:
            yield "CountsView", f"{where}.when.view", c["when"]["view"]


def inbox_sources():
    """(definition, sample) of every stored-source fact whose visibility rule inbox-sources pins (#17399)."""
    for where, c in domain_cases("inbox-sources"):
        if stored := c["input"].get("stored_sources"):
            yield "InboxStoredSources", f"{where}.input.stored_sources", stored


def wire_ids():
    """(definition, sample) of every ID and AgentRef in ids/ids.json."""
    for where, c in domain_cases("ids"):
        yield c["expected"]["definition"], f"{where}.expected.value", c["expected"]["value"]


def agent_views():
    """(definition, sample) of every session's view agent-view expects (#15397)."""
    for where, c in domain_cases("agent-view"):
        for i, v in enumerate(c["expected"]["views"]):
            yield "AgentView", f"{where}.expected.views[{i}] {v['id']}", v["view"]


def todo_waits():
    """(definition, sample) of every waiting reason todo-wait expects (#16168)."""
    for where, c in domain_cases("todo-wait"):
        wait = c["expected"].get("wait")
        if wait is not None:
            yield "TodoWait", f"{where}.expected.wait", wait


def integrations_projections():
    """Catalog and detail samples for the owning assistant's Integrations API."""
    for where, c in domain_cases("integrations-projection"):
        for key, name in (("catalog", "IntegrationCatalog"), ("detail", "IntegrationDetail")):
            if key in c["expected"]:
                yield name, f"{where}.expected.{key}", c["expected"][key]


SAMPLES = {"agent-view.schema.json": agent_views, "counts.schema.json": counts_views, "ids.schema.json": wire_ids,
           "inbox-sources.schema.json": inbox_sources, "integrations-projection.schema.json": integrations_projections,
           "recents.schema.json": recent_setups,
           "todo-view.schema.json": todo_views, "todo-wait.schema.json": todo_waits}


def walk(doc, schema, value, at, reached):
    """The fields value names that schema lacks, at any depth; every definition value reaches goes into reached."""
    if "$ref" in schema:
        name = schema["$ref"].rsplit("/", 1)[1]
        reached.add(name)
        return walk(doc, doc["$defs"][name], value, at, reached)
    unknown = []
    if isinstance(value, dict) and "oneOf" in schema and schema.get("x-discriminator"):
        disc = schema["x-discriminator"]  # a tagged union: the variant its tag names
        for option in schema["oneOf"]:
            variant = doc["$defs"][option["$ref"].rsplit("/", 1)[1]]
            if variant.get("properties", {}).get(disc, {}).get("const") == value.get(disc):
                return walk(doc, option, value, at, reached)
        return [f"{at}.{disc}"]
    if isinstance(value, dict) and "properties" in schema:
        for k, v in value.items():
            if k in schema["properties"]:
                unknown += walk(doc, schema["properties"][k], v, f"{at}.{k}", reached)
            else:
                unknown.append(f"{at}.{k}")
    elif isinstance(value, dict):
        for k, v in value.items():
            if isinstance(schema.get("propertyNames"), dict):
                walk(doc, schema["propertyNames"], k, f"{at}.<key>", reached)
            if isinstance(schema.get("additionalProperties"), dict):
                unknown += walk(doc, schema["additionalProperties"], v, f"{at}.{k}", reached)
    elif isinstance(value, list) and isinstance(schema.get("items"), dict):
        for i, v in enumerate(value):
            unknown += walk(doc, schema["items"], v, f"{at}[{i}]", reached)
    return unknown


# ---- hand-written copies ----
# A schema's names are declared only by its generated files, in every language it generates (x-out) and in the ones it
# guards before or instead of generating them (x-guard): its definitions and the names it reserves for later
# (x-reserved). Per schema, so a schema that guards only TypeScript and Swift (recents) does not fail on copies in other
# languages that predate the rule.

ANNOTATED = r"^[ \t]*(?:@\w+(?:\([^)\n]*\))?\s+)*"  # Swift, Kotlin and ArkTS attributes or annotations first
TS_DECL = r"(?:export\s+)?(?:default\s+)?(?:declare\s+)?(?:abstract\s+)?"
# A type alias, not an inline `type` specifier on its own line in a wrapped import or export (`  type TodoSummary,`).
TS_ALIAS = r"type(?=\s+(?:NAMES)\s*[<=])"
LANGUAGES = {  # language: (suffixes, a declaration of one of NAMES)
    "swift": ((".swift",), ANNOTATED + r"(?:(?:public|private|fileprivate|internal|package|open|final|nonisolated|"
                                       r"indirect)\s+)*(?:struct|class|enum|protocol|actor|typealias)\s+(NAMES)\b"),
    "ts": ((".ts", ".tsx", ".mts", ".cts"),
           r"^[ \t]*" + TS_DECL + r"(?:class|interface|enum|const\s+enum|" + TS_ALIAS + r")\s+(NAMES)\b"),
    "arkts": ((".ets",), ANNOTATED + TS_DECL + r"(?:class|interface|enum|struct|const\s+enum|" + TS_ALIAS +
              r")\s+(NAMES)\b"),
    "kotlin": ((".kt", ".kts"), ANNOTATED + r"(?:(?:public|private|internal|protected|data|sealed|enum|value|inline|"
                                            r"annotation|open|abstract|inner|fun|companion|expect|actual)\s+)*"
                                            r"(?:class|interface|object|typealias)\s+(NAMES)\b"),
    "go": ((".go",), r"^[ \t]*type\s+(NAMES)\b"),  # and the specs of a type ( … ) group, below
    "rust": ((".rs",), r"^[ \t]*(?:#\[[^\]\n]*\]\s*)*(?:pub(?:\([^)\n]*\))?\s+)?(?:struct|enum|type|trait|union)"
                       r"\s+(NAMES)\b"),
}


def declared(language, text, names):
    """The names among names that text declares a type of, in language."""
    alt = "|".join(map(re.escape, sorted(names)))
    found = [m.group(1) for m in re.finditer(LANGUAGES[language][1].replace("NAMES", alt), text, re.M)]
    if language == "go":
        group = None  # the indentation of the type ( … ) group we are in: its specs are one tab deeper, fields deeper
        for line in text.splitlines():
            if group is None:
                m = re.match(r"([ \t]*)type\s*\(\s*(?://.*)?$", line)
                group = m.group(1) if m else None
            elif line.startswith(group + ")"):
                group = None
            elif m := re.match(re.escape(group) + r"\t(" + alt + r")\b", line):
                found.append(m.group(1))
    return found


# core's opaque agentref (server/internal/agentref) declares MachineID, SessionID and InteractionID on purpose: they are
# not copies of the contract IDs but their opaque counterparts, which an adapter converts once (typed core plan 2.3).
# Exempt only that package's files and only those names; any other Go file declaring them still fails.
OPAQUE_COUNTERPARTS = (("go", "/internal/agentref/", {"MachineID", "SessionID", "InteractionID"}),)


def hand_copies(schemas, read, tracked):
    """(file, name) of every declaration of a wire name outside the generated files. schemas: the wire schemas by file
    name; tracked: the repository's files; read(rel) their text."""
    generated = {rel for doc in schemas.values() for rel in doc["x-out"].values()}
    out = []
    for doc in schemas.values():
        names = list(doc["$defs"]) + doc.get("x-reserved", [])
        for language in sorted(set(doc["x-out"]) | set(doc.get("x-guard", []))):
            suffixes = LANGUAGES[language][0]
            for rel in tracked:
                # Fixtures under a testdata directory are inputs to other checks, not code a client builds: the domain
                # rules plant named ID types there on purpose (archguard's contract-bare-id cases).
                if rel in generated or not rel.endswith(suffixes) or "/testdata/" in "/" + rel:
                    continue
                exempt = set().union(*[n for lang, part, n in OPAQUE_COUNTERPARTS if lang == language and part in "/" + rel])
                out += [(rel, name) for name in declared(language, read(rel), names) if name not in exempt]
    return sorted(set(out))


# ---- hand-written decoders ----
# A decoder that reads the JSON key by key declares no type (HarmonyOS's Wire.text(obj, 'key')), so the name guard above
# never sees it. A schema's x-guard-keys are keys and values distinctive enough that a quoted one in a Swift, Kotlin or
# ArkTS source it guards, outside its generated files and the tests, is such a copy. TypeScript and Go are left out:
# plain code there compares the generated types' string values, and the Go hub produces them.
KEY_LANGUAGES = {"swift", "kotlin", "arkts"}
TEST_SOURCE = re.compile(r"(?:^|/)(?:src/[A-Za-z]*[Tt]est[A-Za-z]*|[^/]*Tests)/")  # src/test, src/androidTest, XTests


def hand_decoders(schemas, read, tracked):
    """(file, key) of every quoted x-guard-keys literal outside the generated files and the tests. Arguments as
    hand_copies'."""
    generated = {rel for doc in schemas.values() for rel in doc["x-out"].values()}
    out = []
    for doc in schemas.values():
        languages = (set(doc["x-out"]) | set(doc.get("x-guard", []))) & KEY_LANGUAGES
        if not doc.get("x-guard-keys") or not languages:
            continue
        suffixes = tuple(s for language in sorted(languages) for s in LANGUAGES[language][0])
        quoted = re.compile(r"""(["'])(""" + "|".join(map(re.escape, sorted(doc["x-guard-keys"]))) + r")\1")
        for rel in tracked:
            if rel in generated or not rel.endswith(suffixes) or TEST_SOURCE.search(rel):
                continue
            out += [(rel, m.group(2)) for m in quoted.finditer(read(rel))]
    return sorted(set(out))


def schema_words(doc):
    """Every property name and enum value doc's definitions use."""
    words = set()
    for d in doc["$defs"].values():
        words |= set(d.get("properties", {})) | {v for v in d.get("enum", []) if isinstance(v, str)}
    return words


def wire_schemas():
    return {p.name: json.loads(p.read_text()) for p in sorted(WIRE.glob("*.schema.json"))}


class WireSchema(unittest.TestCase):
    def test_generated_files_are_current(self):
        r = subprocess.run([sys.executable, str(GENERATOR), "--check"], capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stderr)

    def test_every_schema_has_samples(self):
        self.assertEqual({p.name for p in WIRE.glob("*.schema.json")}, set(SAMPLES))

    def test_schema_keywords_are_known(self):
        unknown = set()
        for name, doc in wire_schemas().items():
            unknown |= ext.unknown_keywords(doc, name)
        self.assertEqual(sorted(unknown), [])

    def test_guarded_languages_are_known(self):
        for name, doc in wire_schemas().items():
            self.assertLessEqual(set(doc["x-out"]) | set(doc.get("x-guard", [])), set(LANGUAGES), name)
            self.assertFalse(set(doc.get("x-reserved", [])) & set(doc["$defs"]), f"{name}: reserved and defined")
            # A key the schema does not use (a typo, a field renamed since) would guard nothing.
            self.assertLessEqual(set(doc.get("x-guard-keys", [])), schema_words(doc), f"{name}: x-guard-keys")

    def test_samples_are_exactly_the_schema(self):
        for fname, source in SAMPLES.items():
            doc = json.loads((WIRE / fname).read_text())
            v = ext.Validator({fname: doc})
            count, reached = 0, set()
            for definition, where, sample in source():
                count += 1
                reached.add(definition)
                schema = doc["$defs"][definition]
                errs = v.errors(schema, sample, fname)
                unknown = walk(doc, schema, sample, "$", reached)
                self.assertEqual(errs, [], f"{fname} {where}")
                self.assertEqual(unknown, [], f"{fname} {where}: fields the schema lacks")
            self.assertGreater(count, 0, fname)
            self.assertEqual(sorted(set(doc["$defs"]) - reached), [], f"{fname}: definitions no sample reaches")

    def test_places_carry_the_list_counts(self):
        for where, c in domain_cases("todo-counts"):
            self.assertEqual(c["expected"]["places"]["counts"], c["expected"]["counts"], where)

    def test_no_hand_written_copy_returns(self):
        tracked = subprocess.run(["git", "ls-files", "-z"], cwd=ROOT, capture_output=True, text=True,
                                 check=True).stdout.split("\0")
        tracked = [rel for rel in tracked if rel and (ROOT / rel).is_file()]

        @functools.cache
        def read(rel):
            return (ROOT / rel).read_text(errors="ignore")

        schemas = wire_schemas()
        for rel, name in hand_copies(schemas, read, tracked):
            self.fail(f"{rel} declares {name}, which schema/wire generates or reserves")
        for rel, key in hand_decoders(schemas, read, tracked):
            self.fail(f"{rel} reads {key!r} by hand: use the type schema/wire generates for it")

    def test_hand_decoder_guard_reads_quoted_keys(self):
        schemas = {
            "a.schema.json": {"x-out": {"ts": "web/a.ts", "swift": "apple/A.swift"}, "$defs": {},
                              "x-guard-keys": ["next_change_at"]},  # no Kotlin or ArkTS
            "b.schema.json": {"x-out": {"kotlin": "android/B.kt"}, "x-guard": ["arkts"], "$defs": {},
                              "x-guard-keys": ["default_at", "finished_seen"]},
        }
        files = {
            "apple/A.swift": 'case nextChangeAt = "next_change_at"\n',  # generated
            "apple/Row.swift": 'let at = view["next_change_at"] as? String\n',
            "apple/PersonaTests/RowTests.swift": 'let json = #"{"next_change_at": "x"}"#\n',
            "android/B.kt": '@SerialName("default_at") val defaultAt: String? = null\n',  # generated
            "android/app/src/main/Row.kt": 'if (reason == "finished_seen") hide()\n',
            "android/app/src/test/RowTest.kt": 'val sample = """{"default_at": "x"}"""\n',
            "android/app/src/main/Next.kt": 'val k = "next_change_at"\n',  # b.schema.json does not guard it
            "harmony/entry/src/main/ets/Todo.ets": "const at = Wire.text(view, 'default_at')\n",
            "harmony/entry/src/ohosTest/ets/Todo.test.ets": "expect(Wire.text(v, 'default_at'))\n",
            "harmony/entry/src/main/ets/Words.ets": "const s = 'the default_at field'\n",
            "web/src/row.ts": 'const order = ["next_change_at", "since"]\n',  # a.schema.json's TypeScript is not scanned
        }
        self.assertEqual(hand_decoders(schemas, files.__getitem__, sorted(files)), [
            ("android/app/src/main/Row.kt", "finished_seen"), ("apple/Row.swift", "next_change_at"),
            ("harmony/entry/src/main/ets/Todo.ets", "default_at"),
        ])

    def test_hand_copy_guard_reads_each_language(self):
        schemas = {
            "a.schema.json": {"x-out": {"ts": "web/a.ts", "swift": "apple/A.swift"}, "$defs": {"Thing": {}}},
            "b.schema.json": {"x-out": {"go": "server/b_gen.go", "kotlin": "android/B.kt"},
                              "x-guard": ["arkts", "rust"], "x-reserved": ["Later"], "$defs": {"Widget": {}}},
        }
        files = {
            "android/B.kt": "@Serializable\ndata class Widget(\n",  # generated
            "android/Thing.kt": "internal data class Thing(\n",  # a.schema.json guards no Kotlin
            "android/Widget.kt": "@Serializable\ninternal data class Widget(\n",
            "server/b_gen.go": "type Widget struct {}\n",  # generated
            "server/group.go": "type (\n\tLater struct {\n\t\tWidget Widget\n\t}\n)\n",
            "server/field.go": "type Holder struct {\n\tWidget Widget\n\tLater  string\n}\n",
            "server/local.go": "func f() {\n\ttype Widget int\n}\n",
            "core/src/thing.rs": "pub struct Thing {}\n",  # a.schema.json guards no Rust
            "core/src/widget.rs": "#[derive(Debug)]\npub(crate) struct Widget {}\n",
            "harmony/Widget.ets": "@Observed\nexport class Widget {\n",
            "harmony/use.ets": "let w: Widget = new Widget()\n",
            "apple/Thing.swift": "@MainActor private final class Thing {}\n",
            "apple/use.swift": "let thing: Thing = .init()\n",
            "web/thing.ts": "export type Thing = {\n",
            "web/generic.ts": "type Thing<T> =\n  T[]\n",
            "web/import.ts": "import {\n  type Thing,\n  other,\n} from \"./a\";\n",  # a wrapped import uses it
            "harmony/export.ets": "export {\n  type Widget,\n} from \"./widget\";\n",
            "web/use.ts": "const x: Thing = y;\n",
            "web/a.ts": "export interface Thing {}\n",  # generated
            "web/notes.md": "struct Widget\n",
        }
        self.assertEqual(hand_copies(schemas, files.__getitem__, sorted(files)), [
            ("android/Widget.kt", "Widget"), ("apple/Thing.swift", "Thing"), ("core/src/widget.rs", "Widget"),
            ("harmony/Widget.ets", "Widget"), ("server/group.go", "Later"), ("server/local.go", "Widget"),
            ("web/generic.ts", "Thing"), ("web/thing.ts", "Thing"),
        ])

    def test_testdata_fixtures_are_not_copies(self):
        schemas = {"ids.schema.json": {"x-out": {"go": "server/internal/wireids/ids_gen.go"}, "$defs": {"InteractionID": {}}}}
        files = {
            "server/internal/archguard/testdata/domainrules/planted/internal/assistantwire/wire.go": "type InteractionID string\n",
            "server/internal/assistantwire/wire.go": "type InteractionID string\n",
        }
        self.assertEqual(hand_copies(schemas, files.__getitem__, sorted(files)),
                         [("server/internal/assistantwire/wire.go", "InteractionID")])

    def test_agentref_exemption_is_narrow(self):
        schemas = {"ids.schema.json": {"x-out": {"go": "server/internal/wireids/ids_gen.go"}, "$defs": {"SessionID": {}, "TodoID": {}}}}
        files = {
            "server/internal/agentref/ref.go": "type SessionID struct{}\n",  # core's opaque counterpart
            "server/internal/other/ref.go": "type SessionID string\n",
            "server/internal/agentref/other.go": "type TodoID string\n",  # not a name the exemption covers
        }
        self.assertEqual(hand_copies(schemas, files.__getitem__, sorted(files)), [
            ("server/internal/agentref/other.go", "TodoID"), ("server/internal/other/ref.go", "SessionID")])

    def test_empty_strings_are_samples(self):
        # "" is a value, not an absent field: a generated type must write it back (Go omitempty would not).
        setups = [s for _, _, s in recent_setups()]
        self.assertTrue(any(s["launcher"] == "" for s in setups))
        self.assertTrue(any(s["mode"] == "" for s in setups))
        places = [p for d, _, s in todo_views() if d == "TodoPlaces" for p in s["places"]]
        self.assertTrue(any(p["title"] == "" for p in places))


if __name__ == "__main__":
    unittest.main()
