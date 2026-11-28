"""The protocol and extension docs must list every message type, feature flag, extension point and manifest field.

The docs describe the wire; the lists of what is on the wire live in Go and in the manifest schema. A type, flag, point
or field added to the code without a row in the docs leaves clients and extension authors guessing, and the docs' own
numbers (an extension point's version) drift when the code moves on. This reads the sources as text (no Go toolchain)
and checks:

  (a) every key of managerMessageTypes and hubMessageTypes (server/internal/hub/bus_broadcast.go) has a row in the
      server -> client table under protocol.md "WebSocket /api/v1/ws", or in a "Server → client:" table of a doc
      split from protocol.md;
  (b) every case of the switch on msg.Type in onClientMessage (server/internal/hub/http.go) has a row in its
      client -> server table, or in a "Client → server:" table of a doc split from protocol.md;
  (c) every feature in proto.Features (the literal in server/internal/proto/proto.go, and each
      `Features = append(Features, …)` in a non-test file of proto: a domain's flags, from its features_<domain>.go),
      and every one the hub's features() appends (server/internal/hub/extensions_link.go; an h.method()... spread is
      followed into that method's returns), is named in protocol.md "Health check and feature flags", or in a section
      of a doc split from protocol.md whose heading ends with "feature flags";
  (d) every extension point in extension.Points has a row `| `x` (version N)` in extensions.md "Extension points",
      N its Version, and the manifest schema's $defs.Contributes lists the same points;
  (e) every property of the schema's $defs.Manifest has a row in the field table of extensions.md's manifest section.

Only the code-to-docs direction is checked: a doc row for something the code no longer has (or that an extension
declares, such as `lark`) is left to the reader.

A doc split from protocol.md is one that took sections out of it, says so in a moved-from line, and may hand some on
in turn (test_doc_paths.py, DocMoves). protocol.md keeps its two sections and their tables either way.
"""
from pathlib import Path
import json
import re
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import test_doc_paths  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]

INTERNAL = "server/internal"
PROTOCOL_DOC = "docs/protocol/protocol.md"
EXTENSIONS_DOC = "docs/extensions/extensions.md"
MANIFEST_SCHEMA = "schema/extension/manifest.schema.json"

# hubMessageTypes keys that never go to an app over the WebSocket.
NOT_ON_THE_WEBSOCKET = {
    # An APNs push type (relayproto.PushRequest{Type: "liveactivity"} in hub/push_send.go), not a WebSocket message:
    # it is reserved in hubMessageTypes only because TestBroadcastTypes' scan of the hub's code reads that Type.
    "liveactivity",
}

STRING = r'"(?:[^"\\]|\\.)*"'
# `Features = append(`, not a field of the same name (`r.Features = append(`).
FEATURE_APPEND = re.compile(r"(?<![\w.])Features\s*=\s*append\(")
BACKTICKED = re.compile(r"`([^`]+)`")
POINT_ROW = re.compile(r"^\|\s*`([a-z0-9_]+)`\s*\(version\s+(\d+)")


def read(path):
    return (ROOT / path).read_text()


# Go sources, read as text.

def strip_comments(code):
    """code with its // comments removed (a // inside a string literal stays)."""
    return re.sub(STRING + r"|//[^\n]*", lambda m: m.group(0) if m.group(0).startswith('"') else "", code)


def block(code, opening):
    """The text after the line that starts with opening up to the next line that is only `}`: a top-level
    declaration's body (gofmt puts its closing brace alone at the start of a line)."""
    start = code.find("\n" + opening)
    if start < 0:
        raise AssertionError(f"no line starts with {opening!r}")
    start = code.index("\n", start + 1) + 1
    end = code.find("\n}\n", start - 1)
    if end < 0:
        raise AssertionError(f"{opening!r} has no closing brace")
    return code[start:end]


def map_keys(code, name):
    """The keys of the map literal `var name = map[string]…{…}`."""
    body = strip_comments(block(code, f"var {name} = map[string]"))
    return [json.loads(key) for key in re.findall(r"(" + STRING + r")\s*:", body)]


def switch_cases(code, function, subject):
    """The string cases of the first `switch subject {` in function: only the switch's own cases, not those of a
    switch nested in one of them (gofmt indents its cases as deep as the switch, and closes it at that depth)."""
    body = block(code, function)
    match = re.search(r"^(\t*)switch " + re.escape(subject) + r" \{\n", body, re.M)
    if not match:
        raise AssertionError(f"{function!r} has no `switch {subject} {{`")
    indent, cases = match.group(1), []
    for line in body[match.end():].splitlines():
        if line == indent + "}":
            break
        if line.startswith(indent + "case "):
            head = strip_comments(line).split(":", 1)[0]
            cases += [json.loads(s) for s in re.findall(STRING, head)]
    return cases


def split_arguments(text):
    """text split at its top-level commas, each part stripped (empty parts dropped)."""
    parts, depth, current = [], 0, ""
    for token in re.findall(STRING + r"|[(){}\[\]]|,|[^\"(){}\[\],]+", text):
        if token in "({[":
            depth += 1
        elif token in ")}]":
            depth -= 1
        if token == "," and depth == 0:
            parts.append(current.strip())
            current = ""
        else:
            current += token
    parts.append(current.strip())
    return [part for part in parts if part]


def slice_elements(code, name):
    """The elements of `var name = []string{…}` as written: string literals and identifiers."""
    return split_arguments(strip_comments(block(code, f"var {name} = []string{{")))


def call_arguments(code, start):
    """The arguments, as written, of the call whose opening parenthesis ends at start."""
    depth, end = 1, start
    while depth:
        depth += {"(": 1, ")": -1}.get(code[end], 0)
        end += 1
    return split_arguments(code[start:end - 1])


def appends(code, function):
    """The arguments of each append(…) call in function, as written."""
    body = strip_comments(block(code, function))
    return [call_arguments(body, match.end()) for match in re.finditer(r"\bappend\(", body)]


def feature_appends(sources):
    """The flags a domain appends to proto.Features, as written: every argument after the first of each
    `Features = append(Features, …)` in sources ({file name: Go text}, the non-test files of proto), file by file in
    name order, as Go runs their init()s. A spread, or an append to anything but Features, cannot be read and fails
    (server/internal/proto/features_test.go asks for the same)."""
    found = []
    for name, code in sorted(sources.items()):
        code = strip_comments(code)
        for match in FEATURE_APPEND.finditer(code):
            arguments = call_arguments(code, match.end())
            if not arguments or arguments[0] != "Features" or any(a.endswith("...") for a in arguments):
                raise AssertionError(f"{name}: Features = append({', '.join(arguments)}) cannot be read: append each "
                                     "flag to Features as a constant, func init() { Features = append(Features, "
                                     "FeatureX) }")
            found += arguments[1:]
    return found


def resolve(element, package):
    """The value of a string or integer literal, or of a constant (Name in package, or pkg.Name) defined in a
    non-test file of that package under server/internal, followed through constants defined as other constants."""
    if element.startswith('"'):
        return json.loads(element)
    if element.isdigit():
        return int(element)
    if "." in element:
        package, element = element.split(".", 1)
    if not re.fullmatch(r"[A-Za-z_]\w*", element):
        raise AssertionError(f"cannot resolve {element!r}: not a literal or a constant name")
    # `Name = value` or `const Name type = value` at the start of a line: a constant, alone or in a const ( … ) group.
    value = "(" + STRING + r"|\w[\w.]*)"
    definition = re.compile(r"^[ \t]*(?:const[ \t]+)?" + element + r"(?:[ \t]+\w+)?[ \t]*=[ \t]*" + value, re.M)
    for source in sorted((ROOT / INTERNAL / package).glob("*.go")):
        if source.name.endswith("_test.go"):
            continue
        match = definition.search(source.read_text())
        if match:
            return resolve(match.group(1), package)
    raise AssertionError(f"no constant {element} in {INTERNAL}/{package}")


def returned(package, method):
    """The values of the `return []string{…}` statements of the Hub method `func (h *Hub) method() []string` defined in
    a non-test file of package (a `return nil` adds none; any other return fails)."""
    opening = f"func (h *Hub) {method}() []string {{"
    for source in sorted((ROOT / INTERNAL / package).glob("*.go")):
        code = source.read_text()
        if source.name.endswith("_test.go") or "\n" + opening not in code:
            continue
        found = set()
        for statement in re.findall(r"\breturn\b([^\n]*)", strip_comments(block(code, opening))):
            statement = statement.strip()
            if statement == "nil":
                continue
            literal = re.fullmatch(r"\[\]string\{(.*)\}", statement)
            if not literal:
                raise AssertionError(f"{opening!r} returns {statement!r}: only []string{{…}} or nil can be read")
            found |= {resolve(element, package) for element in split_arguments(literal.group(1))}
        return found
    raise AssertionError(f"no {opening!r} in {INTERNAL}/{package}")


def appended(code, function, package):
    """Every value function appends: each argument after the first (the slice appended to) of every append(…) in
    it, resolved in package, and each `h.method()...` followed into that method's returns. A variable of a
    `for … range` loop is left out (the values come in at run time); anything else that cannot be read fails."""
    ranged = set(re.findall(r"\bfor\s+(?:\w+\s*,\s*)?(\w+)\s*:?=\s*range\b", strip_comments(block(code, function))))
    found = set()
    for arguments in appends(code, function):
        for argument in arguments[1:]:
            spread = re.fullmatch(r"h\.(\w+)\(\)\.\.\.", argument)
            if spread:
                found |= returned(package, spread.group(1))
            elif argument not in ranged:
                found.add(resolve(argument, package))
    return found


def point_versions(sources):
    """{name: version as written} of the extension points registered in sources ({file name: Go text}): the entries
    of `var Points = map[string]Point{…}` and every `Points["x"] = Point{…}`."""
    found = {}
    for name, code in sorted(sources.items()):
        code = strip_comments(code)
        if "\nvar Points = map[string]Point{" in code:
            for match in re.finditer(r'^\t"([a-z0-9_]+)":\s*\{Name:[^\n]*?\bVersion:\s*(\w+)',
                                     block(code, "var Points = map[string]Point{"), re.M):
                found[match.group(1)] = match.group(2)
        for match in re.finditer(r'\bPoints\["([a-z0-9_]+)"\]\s*=\s*Point\{[^{}]*?\bVersion:\s*(\w+)', code, re.S):
            found[match.group(1)] = match.group(2)
    return found


# Markdown.

def section(markdown, heading):
    """The lines under the heading whose text is heading, up to the next heading of the same or a higher level."""
    lines = markdown.splitlines()
    for number, line in enumerate(lines):
        match = re.match(r"(#+) (.*)$", line)
        if match and match.group(2).strip() == heading:
            level, out = len(match.group(1)), []
            for rest in lines[number + 1:]:
                deeper = re.match(r"(#+) ", rest)
                if deeper and len(deeper.group(1)) <= level:
                    break
                out.append(rest)
            return out
    raise AssertionError(f"no heading {heading!r}")


def cells(row):
    """The cells of a Markdown table row (an escaped \\| stays in its cell)."""
    return [cell.strip() for cell in re.split(r"(?<!\\)\|", row.strip().strip("|"))]


def table_after(lines, marker):
    """The body rows of the first table from the line marker on (a line starting with it, which may be the table's
    header row), header and rule left out."""
    start = next((n for n, line in enumerate(lines) if line.startswith(marker)), None)
    if start is None:
        raise AssertionError(f"no line {marker!r}")
    rows = []
    for line in lines[start:]:
        if line.startswith("|"):
            rows.append(line)
        elif rows:
            break
    if len(rows) < 2:
        raise AssertionError(f"no table after {marker!r}")
    return rows[2:]


def tables_after(lines, marker):
    """The body rows of every table that follows a line starting with marker."""
    return [row for number, line in enumerate(lines) if line.startswith(marker)
            for row in table_after(lines[number:], marker)]


def sections_ending(markdown, suffix):
    """The lines under every heading whose text ends with suffix, case ignored, each up to the next heading of the same
    or a higher level."""
    lines, out = markdown.splitlines(), []
    for number, line in enumerate(lines):
        match = re.match(r"(#+) (.*)$", line)
        if match and match.group(2).strip().lower().endswith(suffix):
            out += section("\n".join(lines[number:]), match.group(2).strip())
    return out


def split_docs(doc):
    """The docs that took sections out of doc (a moved-from line), and those that took sections out of them in turn."""
    return test_doc_paths.DocMoves.read(ROOT).split_from(doc)


def first_cell_names(rows):
    """Every backticked name in the first cell of rows (`a` / `b`, `c`)."""
    return {name for row in rows for name in BACKTICKED.findall(cells(row)[0])}


def backticked(lines):
    return {name for line in lines for name in BACKTICKED.findall(line)}


# What the code has, and what the docs list.

def websocket_types():
    """(server -> client, client -> server) message types of the code, NOT_ON_THE_WEBSOCKET included."""
    broadcast = read(f"{INTERNAL}/hub/bus_broadcast.go")
    to_clients = set(map_keys(broadcast, "managerMessageTypes")) | set(map_keys(broadcast, "hubMessageTypes"))
    from_clients = set(switch_cases(read(f"{INTERNAL}/hub/http.go"), "func (h *Hub) onClientMessage(", "msg.Type"))
    return to_clients, from_clients


def documented_websocket_types():
    """The first cells of protocol.md's two tables under "WebSocket /api/v1/ws", and of every "Server → client:" and
    "Client → server:" table of a doc split from it."""
    lines = section(read(PROTOCOL_DOC), "WebSocket `/api/v1/ws`")
    to_clients = first_cell_names(table_after(lines, "Server → client:"))
    from_clients = first_cell_names(table_after(lines, "Client → server:"))
    for doc in split_docs(PROTOCOL_DOC):
        lines = read(doc).splitlines()
        to_clients |= first_cell_names(tables_after(lines, "Server → client:"))
        from_clients |= first_cell_names(tables_after(lines, "Client → server:"))
    return to_clients, from_clients


def documented_features():
    """The names in protocol.md "Health check and feature flags", and in each "... feature flags" section of a doc
    split from it."""
    documented = backticked(section(read(PROTOCOL_DOC), "Health check and feature flags"))
    for doc in split_docs(PROTOCOL_DOC):
        documented |= backticked(sections_ending(read(doc), "feature flags"))
    return documented


def features():
    """proto.Features (its literal, and the flags the domains' files append to it) and every feature the hub's
    features() appends to them (those the managers report at run time aside)."""
    proto = {p.name: p.read_text() for p in (ROOT / INTERNAL / "proto").glob("*.go") if not p.name.endswith("_test.go")}
    written = slice_elements(proto["proto.go"], "Features") + feature_appends(proto)
    found = {resolve(element, "proto") for element in written}
    return found | appended(read(f"{INTERNAL}/hub/extensions_link.go"), "func (h *Hub) features() []string {", "hub")


def points():
    """{extension point: version} of extension.Points."""
    sources = {p.name: p.read_text() for p in (ROOT / INTERNAL / "extension").glob("*.go")
               if not p.name.endswith("_test.go")}
    return {name: resolve(version, "extension") for name, version in point_versions(sources).items()}


def documented_points():
    """{extension point: version} of the rows of extensions.md "Extension points"."""
    rows = (POINT_ROW.match(line) for line in section(read(EXTENSIONS_DOC), "Extension points"))
    return {row.group(1): int(row.group(2)) for row in rows if row}


def schema_defs():
    return json.loads(read(MANIFEST_SCHEMA))["$defs"]


def documented_manifest_fields():
    """The top-level fields of the manifest field table (`engines.persona` documents `engines`)."""
    rows = table_after(section(read(EXTENSIONS_DOC), "Manifest `persona-extension.json`"), "| Field |")
    return {name.split(".", 1)[0] for name in first_cell_names(rows)}


def missing(have, documented):
    return sorted(set(have) - set(documented))


class ParserTests(unittest.TestCase):
    def test_map_keys(self):
        code = ('package hub\n\nvar other = map[string]bool{"x": true}\n\n// the types\nvar types = map[string]bool{\n'
                '\t// "commented": true,\n\t"a": true, "b.c": true,\n\t"d":   true, // "e": true\n}\n\nvar after = 1\n')
        self.assertEqual(["a", "b.c", "d"], map_keys(code, "types"))
        with self.assertRaises(AssertionError):
            map_keys(code, "gone")

    def test_switch_cases(self):
        code = ('package hub\n\nfunc other() {\n\tswitch msg.Type {\n\tcase "other":\n\t}\n}\n\n'
                'func (h *Hub) on(c *client) {\n\tif x {\n\t\treturn\n\t}\n\tswitch msg.Type {\n'
                '\tcase "a":\n\t\tswitch y {\n\t\tcase "nested":\n\t\t}\n'
                '\tcase "b", "c": // "d"\n\t\tf("not a case")\n\tdefault:\n\t}\n'
                '\tswitch z {\n\tcase "later":\n\t}\n}\n')
        self.assertEqual(["a", "b", "c"], switch_cases(code, "func (h *Hub) on(", "msg.Type"))
        with self.assertRaises(AssertionError):
            switch_cases(code, "func (h *Hub) on(", "msg.Kind")

    def test_slice_elements_and_appends(self):
        code = ('package proto\n\nvar Features = []string{\n\tother.Name, // a "quoted", comment\n'
                '\t"a",        // GET /x/{id}\n\t// "commented",\n\t"b", "c",\n}\n\n'
                'func (h *Hub) features() []string {\n\tout := append(slices.Clone(proto.Features), one, two)\n'
                '\tout = append(out, h.more(1, 2)...)\n\tif x {\n\t\tout = append(out, "views")\n\t}\n'
                '\treturn out\n}\n')
        self.assertEqual(['other.Name', '"a"', '"b"', '"c"'], slice_elements(code, "Features"))
        self.assertEqual([["slices.Clone(proto.Features)", "one", "two"], ["out", "h.more(1, 2)..."],
                          ["out", '"views"']], appends(code, "func (h *Hub) features() []string {"))

    def test_feature_appends(self):
        sources = {
            "proto.go": 'package proto\n\nvar Features = []string{\n\t"a",\n}\n',
            "features_zeta.go": 'package proto\n\nfunc init() { Features = append(Features, FeatureZ) }\n',
            "features_inbox.go": ('package proto\n\nconst FeatureInbox = "inbox_v2" // Features = append(Features, x)\n\n'
                                  'func init() {\n\tFeatures = append(Features,\n\t\tFeatureInbox, // the bar\n'
                                  '\t\t"inbox_rows",\n\t)\n}\n'),
            "other.go": ('package proto\n\nfunc f() []string {\n\tRoutes = append(Routes, "r")\n'
                         '\tr.Features = append(r.Features, "field")\n'
                         '\treturn append(slices.Clone(Features), "y")\n}\n'),
        }
        self.assertEqual(["FeatureInbox", '"inbox_rows"', "FeatureZ"], feature_appends(sources))
        for written in ("Features = append(Features, more...)", "Features = append(Other, FeatureX)"):
            with self.assertRaises(AssertionError, msg=written):
                feature_appends({"features_x.go": "package proto\n\nfunc init() { %s }\n" % written})

    def test_an_appended_flag_needs_its_row(self):
        protocol = "# Protocol\n\n## Health check and feature flags\n\n`tls`\n"
        with tempfile.TemporaryDirectory(prefix="doc-wire-appends-") as directory:
            root = Path(directory)
            test_doc_paths.write_tree(root, {
                f"{INTERNAL}/proto/proto.go": 'package proto\n\nvar Features = []string{\n\t"tls",\n}\n',
                f"{INTERNAL}/proto/features_inbox.go": ('package proto\n\nconst FeatureInbox = "inbox_v2"\n\n'
                                                        'func init() { Features = append(Features, FeatureInbox) }\n'),
                # Not a file the hub is built from.
                f"{INTERNAL}/proto/features_inbox_test.go": ('package proto\n\n'
                                                             'func init() { Features = append(Features, "test") }\n'),
                f"{INTERNAL}/hub/extensions_link.go": ('package hub\n\nfunc (h *Hub) features() []string {\n'
                                                       '\treturn append(slices.Clone(proto.Features))\n}\n'),
                PROTOCOL_DOC: protocol,
            })
            test_doc_paths.git_repository(root)
            with patch(__name__ + ".ROOT", root):
                self.assertEqual({"tls", "inbox_v2"}, features())
                self.assertEqual(["inbox_v2"], missing(features(), documented_features()))
                (root / PROTOCOL_DOC).write_text(protocol + "`inbox_v2`\n")
                self.assertEqual([], missing(features(), documented_features()))

    def test_resolve(self):
        with tempfile.TemporaryDirectory(prefix="doc-wire-") as directory:
            root = Path(directory)
            (root / INTERNAL / "hub").mkdir(parents=True)
            (root / INTERNAL / "other").mkdir(parents=True)
            (root / INTERNAL / "hub/a.go").write_text('package hub\n\nconst (\n\tfeatureA = "a" // a\n'
                                                       '\tfeatureB = other.Thing\n\tfeatureC = "c/d" // e\n)\n\n'
                                                       'const Version = 6\n\nfunc f() { featureZ := "z" }\n')
            (root / INTERNAL / "hub/a_test.go").write_text('package hub\n\nconst featureT = "t"\n')
            (root / INTERNAL / "other/b.go").write_text('package other\n\nconst Thing string = "thing"\n')
            with patch(__name__ + ".ROOT", root):
                self.assertEqual("a", resolve("featureA", "hub"))
                self.assertEqual("c/d", resolve("featureC", "hub"))
                self.assertEqual("thing", resolve("featureB", "hub"))
                self.assertEqual("thing", resolve("other.Thing", "hub"))
                self.assertEqual(6, resolve("Version", "hub"))
                self.assertEqual("x", resolve('"x"', "hub"))
                self.assertEqual(2, resolve("2", "hub"))
                for unknown in ("featureT", "featureZ", "h.more()"):
                    with self.assertRaises(AssertionError):
                        resolve(unknown, "hub")

    def test_appended(self):
        features = ('package hub\n\nconst (\n\tfeatureA = "a"\n\tfeatureB = "b"\n\tfeatureC = "c"\n)\n\n'
                    'func (h *Hub) features() []string {\n\tout := append(slices.Clone(proto.Features), featureA)\n'
                    '\tout = append(out, h.more()...)\n\tif x {\n\t\tout = append(out, featureB, "lit")\n\t}\n'
                    '\tfor _, f := range h.report().Features {\n\t\tif !slices.Contains(out, f) {\n'
                    '\t\t\tout = append(out, f)\n\t\t}\n\t}\n\treturn out\n}\n')
        more = ('package hub\n\nfunc (h *Hub) more() []string {\n\tif y {\n\t\treturn nil\n\t}\n'
                '\tif z {\n\t\treturn []string{featureC}\n\t}\n\treturn []string{featureC, "d"}\n}\n\n'
                'func (h *Hub) none() []string {\n\treturn h.more()\n}\n')
        opening = "func (h *Hub) features() []string {"
        with tempfile.TemporaryDirectory(prefix="doc-wire-") as directory:
            root = Path(directory)
            (root / INTERNAL / "hub").mkdir(parents=True)
            (root / INTERNAL / "hub/features.go").write_text(features)
            (root / INTERNAL / "hub/more.go").write_text(more)
            with patch(__name__ + ".ROOT", root):
                # Every append counts, conditional or not; a spread method is followed; the loop's f is left out.
                self.assertEqual({"a", "b", "c", "d", "lit"}, appended(features, opening, "hub"))
                # A new kind of argument fails rather than being skipped.
                for argument in ("local...", "h.none()...", "h.gone()...", "featureZ"):
                    with self.assertRaises(AssertionError, msg=argument):
                        appended(features.replace("featureB, ", argument + ", "), opening, "hub")

    def test_point_versions(self):
        sources = {
            "points.go": ('package extension\n\nvar Points = map[string]Point{\n'
                          '\t"conn": {Name: "conn", Namespace: "c", Version: 2, NeedsRPC: true, Item: func() {\n'
                          '\t\tx := map[string]Point{"inner": {Name: "inner", Version: 9}}\n\t}},\n'
                          '\t// "old": {Name: "old", Version: 1},\n'
                          '\t"ui": {Name: "ui", Version: UIVersion, Item: nil},\n}\n'),
            "looks.go": ('package extension\n\nfunc init() {\n'
                         '\tPoints["looks"] = Point{Name: "looks", Version: 1, Item: nil}\n}\n'),
            "routine_facts.go": ('package extension\n\nfunc init() {\n'
                                 '\tPoints["routine_facts"] = Point{\n'
                                 '\t\tName: "routine_facts", Namespace: "routine_facts", Version: 1, NeedsRPC: true,\n'
                                 '\t\tItem: func(raw json.RawMessage) (string, error) {\n'
                                 '\t\t\tvar item wire.RoutineFactsItem\n'
                                 '\t\t\tif err := strict(raw, &item); err != nil { return "", err }\n'
                                 '\t\t\treturn item.ID.String(), nil\n'
                                 '\t\t},\n'
                                 '\t}\n'
                                 '}\n'),
        }
        self.assertEqual({"conn": "2", "ui": "UIVersion", "looks": "1", "routine_facts": "1"}, point_versions(sources))

    def test_section_and_tables(self):
        markdown = ("# Doc\n\n## Wire\n\nServer → client:\n\n| type | Fields |\n| --- | --- |\n"
                    "| `a` | x |\n| `b` / `c` | `not_a_type` \\| `nor_this` |\n\nClient → server:\n\n"
                    "| type | Fields |\n| --- | --- |\n| `d` | |\n\n### Deeper\n\n`e`\n\n## Next\n\n`f`\n")
        lines = section(markdown, "Wire")
        self.assertIn("`e`", lines)
        self.assertNotIn("`f`", lines)
        self.assertEqual({"a", "b", "c"}, first_cell_names(table_after(lines, "Server → client:")))
        self.assertEqual({"d"}, first_cell_names(table_after(lines, "Client → server:")))
        self.assertEqual({"a", "b", "c"}, first_cell_names(table_after(lines, "| type |")))
        self.assertEqual({"e"}, backticked(section(markdown, "Deeper")))
        self.assertEqual(["`b` / `c`", "`not_a_type` \\| `nor_this`"],
                         cells("| `b` / `c` | `not_a_type` \\| `nor_this` |"))
        for heading, marker in (("Gone", "Server → client:"), ("Next", "Server → client:")):
            with self.assertRaises(AssertionError):
                table_after(section(markdown, heading), marker)

    def test_point_rows(self):
        rows = ["| `connectors` (version 2) | `connector.` |", "| `messaging` (version 1, #2138) | — |",
                "| `filter` → `segmented` (version 2) |", "| Extension point | Namespace |"]
        found = {m.group(1): m.group(2) for m in map(POINT_ROW.match, rows) if m}
        self.assertEqual({"connectors": "2", "messaging": "1"}, found)

    def test_missing(self):
        self.assertEqual(["b", "c"], missing({"a", "c", "b"}, {"a", "d"}))

    def test_docs_split_from_protocol_add_their_rows_and_feature_flags(self):
        protocol = ("# Protocol\n\n## Health check and feature flags\n\n`tls`\n\n## WebSocket `/api/v1/ws`\n\n"
                    "Server → client:\n\n| type | Fields |\n| --- | --- |\n| `hello` | |\n\n"
                    "Client → server:\n\n| type | Fields |\n| --- | --- |\n| `ping` | |\n\n## Engines\n")
        split = ("# Inbox API\n\n## Inbox feature flags\n\n`inbox_v2`\n\n### Rows\n\n`inbox_rows`\n\n"
                 "## Pushes\n\n`not_a_feature`\n\nServer → client:\n\n| type | Fields |\n| --- | --- |\n"
                 "| `inbox` / `inbox_item` | `not_a_type` |\n\nClient → server:\n\n| type | Fields |\n| --- | --- |\n"
                 "| `inbox_read` | |\n\n### More\n\nServer → client:\n\n| type |\n| --- |\n| `inbox_more` |\n")
        with tempfile.TemporaryDirectory(prefix="doc-wire-split-") as directory:
            root = Path(directory)
            test_doc_paths.write_tree(root, {
                PROTOCOL_DOC: protocol,
                "docs/protocol/inbox-api.md": "<!-- moved-from: docs/protocol/protocol.md -->\n" + split,
                # Took sections out of inbox-api.md in turn.
                "docs/protocol/inbox-rows.md": ("<!-- moved-from: docs/protocol/inbox-api.md -->\n# Rows\n\n"
                                                "Server → client:\n\n| type |\n| --- |\n| `inbox_row` |\n"),
                # No moved-from line: its tables are not protocol.md's.
                "docs/protocol/other.md": split.replace("inbox", "other"),
            })
            test_doc_paths.git_repository(root)
            with patch(__name__ + ".ROOT", root):
                self.assertEqual(({"hello", "inbox", "inbox_item", "inbox_more", "inbox_row"}, {"ping", "inbox_read"}),
                                 documented_websocket_types())
                self.assertEqual({"tls", "inbox_v2", "inbox_rows"}, documented_features())
                (root / "docs/protocol/inbox-api.md").write_text(split)
                self.assertEqual(({"hello"}, {"ping"}), documented_websocket_types())
                self.assertEqual({"tls"}, documented_features())
                # protocol.md keeps its own sections: a split doc does not stand in for them.
                (root / PROTOCOL_DOC).write_text("# Protocol\n\n## Engines\n")
                for read_docs in (documented_websocket_types, documented_features):
                    with self.assertRaises(AssertionError):
                        read_docs()


class DocWireTests(unittest.TestCase):
    maxDiff = None

    def test_websocket_types_are_documented(self):
        to_clients, from_clients = websocket_types()
        self.assertLessEqual(NOT_ON_THE_WEBSOCKET, to_clients, "an exemption the code no longer has: drop it")
        to_clients -= NOT_ON_THE_WEBSOCKET
        # The parsers still find the lists (a move of the code would otherwise pass with nothing to check).
        self.assertTrue({"hello", "sessions", "assistant_feed", "inbox", "health_intake"} <= to_clients, sorted(to_clients))
        self.assertTrue({"subscribe", "client_hello", "ping", "tool_result"} <= from_clients, sorted(from_clients))
        documented_to_clients, documented_from_clients = documented_websocket_types()
        self.assertEqual({"Server → client": [], "Client → server": []},
                         {"Server → client": missing(to_clients, documented_to_clients),
                          "Client → server": missing(from_clients, documented_from_clients)},
                         f"message types without a row in the tables of {PROTOCOL_DOC} \"WebSocket /api/v1/ws\" "
                         "or of a doc split from it "
                         "(Server → client: hub/bus_broadcast.go managerMessageTypes and hubMessageTypes; "
                         "Client → server: the msg.Type cases of hub/http.go onClientMessage)")

    def test_feature_flags_are_documented(self):
        have = features()
        self.assertTrue({"tls", "client_hello", "persona_contracts_v1", "sessions_list", "assistant_views",
                         "agent_session_archives", "agent_session_archive_close", "health_intake"} <= have, sorted(have))
        self.assertEqual([], missing(have, documented_features()),
                         f"features in the hub's hello and /health not named in {PROTOCOL_DOC} "
                         "\"Health check and feature flags\" or in a \"... feature flags\" section of a doc split "
                         "from it (proto.Features and its appends in proto/features_<domain>.go, "
                         "hub/extensions_link.go features())")

    def test_extension_points_are_documented(self):
        have = points()
        self.assertTrue({"connectors", "ui", "looks", "tunnels", "agent_filters", "skills"} <= set(have), sorted(have))
        contributes = set(schema_defs()["Contributes"]["properties"])
        self.assertEqual(sorted(have), sorted(contributes),
                         f"extension.Points and $defs.Contributes in {MANIFEST_SCHEMA} must list the same points")
        documented = documented_points()
        self.assertEqual([], missing(have, documented), f"extension points without a row `| \\`x\\` (version N)` in "
                                                        f"{EXTENSIONS_DOC} \"Extension points\"")
        wrong = {name: (documented[name], version) for name, version in have.items()
                 if name in documented and documented[name] != version}
        self.assertEqual({}, wrong, f"extension point versions in {EXTENSIONS_DOC} that are not the code's "
                                    "({point: (documented, code)})")

    def test_manifest_fields_are_documented(self):
        have = set(schema_defs()["Manifest"]["properties"])
        self.assertTrue({"manifest_version", "id", "contributes"} <= have, sorted(have))
        self.assertEqual([], missing(have, documented_manifest_fields()),
                         f"$defs.Manifest properties in {MANIFEST_SCHEMA} without a row in the field table of "
                         f"{EXTENSIONS_DOC} \"Manifest `persona-extension.json`\"")


if __name__ == "__main__":
    unittest.main()
