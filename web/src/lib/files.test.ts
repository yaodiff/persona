import { describe, expect, it } from "vitest";
import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  below,
  columns,
  diffTotals,
  dirName,
  fileName,
  gitLetter,
  gitMark,
  gitTone,
  glyphOf,
  hasUnstaged,
  inlineHTML,
  isMachineRef,
  isStaged,
  joinPath,
  markdownChunks,
  parseDiff,
  prettyJSON,
  refNames,
  resolveRef,
  simplifyHTML,
  splitDiff,
  splitLines,
  toolFile,
  viewKind,
  type FileContent,
  type GitFile,
  type GitStatus,
} from "./files";
import { extendStates, languageOf, lexLine, type LexState, type Token } from "./files-syntax";

const f = (path: string, index: string, worktree: string, added = 0, deleted = 0): GitFile => ({ path, index, worktree, added, deleted });

describe("loading boundary", () => {
  it("keeps the viewer's lexer out of the first screen's file helpers", () => {
    const source = readFileSync(new URL("./files.ts", import.meta.url), "utf8");
    expect(source).not.toContain("files-syntax");
    expect(source).not.toMatch(/export (?:function|interface|type) (?:lexLine|languageOf|extendStates|LexState|TokenKind)\b/);
  });
});

describe("paths", () => {
  it("joins, splits and resolves", () => {
    expect(joinPath("/a/b/", "c")).toBe("/a/b/c");
    expect(joinPath("/", "c")).toBe("/c");
    expect(dirName("/a/b/c")).toBe("/a/b");
    expect(dirName("/a")).toBe("/");
    expect(below("/u/code/weather", "/u/code/weather/Sources/App")).toEqual(["Sources", "App"]);
    expect(below("/u/code/weather", "/u/code/weather/")).toEqual([]);
    expect(below("/u/code/weather", "/u/code/weatherman")).toBeNull();
  });

  it("resolves what a Markdown file refers to against its folder", () => {
    expect(resolveRef("/u/w", "docs/icon.png")).toBe("/u/w/docs/icon.png");
    expect(resolveRef("/u/w/docs", "../README.md#top")).toBe("/u/w/README.md");
    expect(resolveRef("/u/w", "./a%20b.png")).toBe("/u/w/a b.png");
    expect(resolveRef("/u/w", "/etc/hosts")).toBe("/etc/hosts");
    expect(resolveRef("/u/w", "~/notes.txt")).toBe("~/notes.txt");
    expect(resolveRef("/u/w", "https://example.com/x.png")).toBe("");
    expect(resolveRef("/u/w", "#install")).toBe("");
    expect(isMachineRef("mailto:me@example.com")).toBe(false);
    expect(isMachineRef("//cdn.example.com/x.png")).toBe(false);
    expect(isMachineRef("docs/x.md")).toBe(true);
  });

  // A Windows computer answers its paths with backslashes (filepath.Clean on the node): its folder, its name and what its
  // Markdown refers to are read by either separator (#9968), the same test as baseName in lib/format.ts.
  it("reads a Windows computer's path by backslashes and slashes", () => {
    expect(dirName("C:\\Users\\me\\docs\\README.md")).toBe("C:\\Users\\me\\docs");
    expect(dirName("C:\\Users\\me\\docs\\")).toBe("C:\\Users\\me");
    expect(dirName("C:\\Users\\me\\proj/src/a.ts")).toBe("C:\\Users\\me\\proj/src");
    expect(dirName("C:\\README.md")).toBe("C:\\");
    expect(dirName("C:/README.md")).toBe("C:/");
    expect(dirName("\\\\server\\share\\docs\\a.md")).toBe("\\\\server\\share\\docs");
    expect(fileName("C:\\Users\\me\\docs\\README.md")).toBe("README.md");
    expect(fileName("C:\\Users\\me\\docs\\")).toBe("docs");
    expect(fileName("\\\\server\\share\\a.md")).toBe("a.md");
  });

  it("resolves a Windows Markdown file's references in its folder, never above its drive or share", () => {
    expect(resolveRef("C:\\Users\\me\\docs", "img/logo.png")).toBe("C:\\Users\\me\\docs\\img\\logo.png");
    expect(resolveRef("C:\\Users\\me\\docs", "../notes.md#top")).toBe("C:\\Users\\me\\notes.md");
    expect(resolveRef("C:\\Users\\me\\docs", "./a%20b.png")).toBe("C:\\Users\\me\\docs\\a b.png");
    expect(resolveRef("C:\\Users\\me\\docs", "sub\\c.md")).toBe("C:\\Users\\me\\docs\\sub\\c.md");
    expect(resolveRef("C:\\", "../../x.md")).toBe("C:\\x.md");
    expect(resolveRef("\\\\server\\share\\docs", "../../x.md")).toBe("\\\\server\\share\\x.md");
    expect(resolveRef("C:\\Users\\me\\docs", "~/notes.txt")).toBe("~/notes.txt");
    expect(resolveRef("C:\\Users\\me\\docs", "https://example.com/x.png")).toBe("");
  });

  it("keeps a backslash that is part of a name anywhere else", () => {
    expect(dirName("/home/me/odd\\name")).toBe("/home/me");
    expect(fileName("/home/me/odd\\name")).toBe("odd\\name");
    expect(fileName("/home/me/docs/")).toBe("docs");
    expect(dirName("a.md")).toBe("");
    expect(dirName("/")).toBe("");
    expect(resolveRef("/u/w", "odd\\name.png")).toBe("/u/w/odd\\name.png");
  });
});

describe("git", () => {
  it("letters and stages a change the way iOS GitFile does", () => {
    expect(gitLetter(f("a", "?", "?"))).toBe("U");
    expect(gitLetter(f("a", "U", "U"))).toBe("!");
    expect(gitLetter(f("a", "M", "M"))).toBe("M");
    expect(gitLetter(f("a", ".", "D"))).toBe("D");
    expect(gitLetter(f("a", "A", "."))).toBe("A");
    const both = f("a", "M", "M");
    expect(isStaged(both) && hasUnstaged(both)).toBe(true);
    const untracked = f("a", "?", "?");
    expect(isStaged(untracked) || hasUnstaged(untracked)).toBe(false);
    expect([gitTone("A"), gitTone("U"), gitTone("D"), gitTone("!"), gitTone("R"), gitTone("M")]).toEqual(["add", "add", "del", "del", "moved", "changed"]);
  });

  it("marks a file and the folders that hold a change", () => {
    const st: GitStatus = { is_repo: true, root: "/u/w", files: [f("Sources/App/main.swift", "M", "M"), f("docs/icon.png", "?", "?")], commits: [] };
    expect(gitMark(st, "/u/w/Sources/App/main.swift", false)).toBe("M");
    expect(gitMark(st, "/u/w/Sources", true)).toBe("M");
    expect(gitMark(st, "/u/w/docs", true)).toBe("U");
    expect(gitMark(st, "/u/w/Sources", false)).toBeNull();
    expect(gitMark(st, "/u/w/Tests", true)).toBeNull();
    expect(gitMark(st, "/u/other/Sources", true)).toBeNull();
    expect(gitMark({ ...st, is_repo: false }, "/u/w/Sources", true)).toBeNull();
  });

  it("reads ref names and totals", () => {
    expect(refNames("HEAD -> main, origin/main, tag: v1")).toEqual(["main", "origin/main", "tag: v1"]);
    expect(refNames("HEAD")).toEqual([]);
    expect(refNames()).toEqual([]);
    expect(diffTotals([f("a", "M", ".", 2, 1), f("b", ".", "M", 3, 0)])).toEqual({ added: 5, deleted: 1 });
  });
});

describe("diff", () => {
  const one =
    "diff --git a/Sources/App/main.swift b/Sources/App/main.swift\nindex 1..2 100644\n--- a/Sources/App/main.swift\n+++ b/Sources/App/main.swift\n" +
    '@@ -1,3 +1,4 @@ import\n import Foundation\n \n-print("Weather: sunny")\n+let city = "Paris"\n+print(city)\n\\ No newline at end of file';

  it("numbers old and new lines and drops the file headers", () => {
    const lines = parseDiff(one);
    expect(lines.map((l) => l.kind)).toEqual(["hunk", "context", "context", "del", "add", "add", "info"]);
    expect(lines[1]).toEqual({ kind: "context", text: "import Foundation", old: 1, new: 1 });
    expect(lines[3]).toEqual({ kind: "del", text: 'print("Weather: sunny")', old: 3 });
    expect(lines[4]).toEqual({ kind: "add", text: 'let city = "Paris"', new: 3 });
    expect(lines[5].new).toBe(4);
  });

  it("keeps a line that only looks like a header inside a hunk", () => {
    const lines = parseDiff("@@ -5 +5 @@\n--- a removed dashed line\n+++ an added plus line");
    expect(lines.map((l) => [l.kind, l.text, l.old ?? l.new])).toEqual([
      ["hunk", "@@ -5 +5 @@", undefined],
      ["del", "-- a removed dashed line", 5],
      ["add", "++ an added plus line", 5],
    ]);
  });

  it("says what a new binary file is", () => {
    const lines = parseDiff("diff --git a/docs/icon.png b/docs/icon.png\nnew file mode 100644\nBinary files /dev/null and b/docs/icon.png differ");
    expect(lines.map((l) => l.kind)).toEqual(["info", "info"]);
  });

  it("splits a commit's diff per file by the new path", () => {
    const two = one + "\ndiff --git a/my docs/a b.md b/my docs/a b.md\n--- a/my docs/a b.md\n+++ b/my docs/a b.md\n@@ -1 +1,2 @@\n x\n+y";
    const parts = splitDiff(two);
    expect(Object.keys(parts)).toEqual(["Sources/App/main.swift", "my docs/a b.md"]);
    expect(parseDiff(parts["my docs/a b.md"]).filter((l) => l.kind === "add")).toEqual([{ kind: "add", text: "y", new: 2 }]);
  });

  it.each([
    ["say\"hello.ts", String.raw`"b/say\"hello.ts"`],
    ["a\tb.ts", String.raw`"b/a\tb.ts"`],
    ["a\\b.ts", String.raw`"b/a\\b.ts"`],
    ["a\nb.ts", String.raw`"b/a\nb.ts"`],
    ["a\x07\b\v\f\r\x1b\x7fb.ts", String.raw`"b/a\a\b\v\f\r\033\177b.ts"`],
    ["café.ts", String.raw`"b/caf\303\251.ts"`],
    ["你好\t.ts", String.raw`"b/你好\t.ts"`],
    ['\uFEFFsay"hello.ts', String.raw`"b/\357\273\277say\"hello.ts"`],
    ["literal\\141.ts", String.raw`"b/literal\\141.ts"`],
  ])("keeps the exact chunk for Git-quoted path %j", (path, target) => {
    const chunk = `diff --git a/before.ts ${target}\n--- a/before.ts\n+++ ${target}\n@@ -1 +1 @@\n-before\n+after\n`;
    const following = "diff --git a/next.ts b/next.ts\nnew file mode 100644\n--- /dev/null\n+++ b/next.ts\n@@ -0,0 +1 @@\n+next\n";
    const parts = splitDiff(chunk + following);
    expect(Object.keys(parts)).toEqual([path, "next.ts"]);
    expect(parts[path]).toBe(chunk.slice(0, -1));
    expect(parts["next.ts"]).toBe(following);
    expect(parseDiff(parts[path]).filter((line) => line.kind === "add")).toEqual([{ kind: "add", text: "after", new: 1 }]);
  });

  it("maps an actual Git-produced quoted new-file header", () => {
    const dir = mkdtempSync(join(tmpdir(), "persona-quoted-diff-"));
    try {
      const path = 'say"hello.ts';
      writeFileSync(join(dir, path), "new content\n");
      const result = spawnSync("git", ["-c", "core.quotepath=off", "diff", "--no-index", "--", "/dev/null", path], { cwd: dir, encoding: "utf8", timeout: 10_000 });
      expect(result.status).toBe(1); // git diff reports a difference
      expect(result.stderr).toBe("");
      expect(splitDiff(result.stdout)[path]).toBe(result.stdout);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });

  it.each([
    ['renamed"file.ts', 'diff --git a/old.ts "b/renamed\\"file.ts"\nsimilarity index 100%\nrename from old.ts\nrename to "renamed\\"file.ts"\n'],
    ["gone\tfile.ts", 'diff --git "a/gone\\tfile.ts" "b/gone\\tfile.ts"\ndeleted file mode 100644\n--- "a/gone\\tfile.ts"\n+++ /dev/null\n@@ -1 +0,0 @@\n-gone\n'],
  ])("preserves a rename or deletion chunk for %j", (path, diff) => {
    expect(splitDiff(diff)).toEqual({ [path]: diff });
  });

  it("preserves unquoted paths containing ' b/' across edits, deletions, binaries and renames", () => {
    const edited = "diff --git a/docs/part b/guide.md b/docs/part b/guide.md\n--- a/docs/part b/guide.md\t\n+++ b/docs/part b/guide.md\t\n@@ -1 +1,2 @@\n x\n+++ b/not-a-header.md";
    const deleted = "diff --git a/a b/gone.ts b/a b/gone.ts\ndeleted file mode 100644\n--- a/a b/gone.ts\n+++ /dev/null\n@@ -1 +0,0 @@\n-old";
    const binary = "diff --git a/assets/set b/logo.png b/assets/set b/logo.png\nnew file mode 100644\nBinary files /dev/null and b/assets/set b/logo.png differ";
    const renamed = "diff --git a/old.md b/docs/part b/renamed.md\nsimilarity index 100%\nrename from old.md\nrename to docs/part b/renamed.md";
    const parts = splitDiff([edited, deleted, binary, renamed].join("\n"));
    expect(Object.keys(parts)).toEqual(["docs/part b/guide.md", "a b/gone.ts", "assets/set b/logo.png", "docs/part b/renamed.md"]);
    expect(parts["docs/part b/guide.md"]).toBe(edited);
    expect(parts["a b/gone.ts"]).toBe(deleted);
    expect(parts["assets/set b/logo.png"]).toBe(binary);
    expect(parts["docs/part b/renamed.md"]).toBe(renamed);
  });

  it("keeps only filenames with a diff chunk in the lookup", () => {
    const README = "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-old\n+new\n";
    const file = "diff --git a/constructor b/constructor\n--- a/constructor\n+++ b/constructor\n@@ -1 +1 @@\n-old\n+new\n";
    expect(splitDiff(README)["constructor"]).toBeUndefined();
    expect(splitDiff(README + file)["constructor"]).toBe(file);
  });

  it("maps actual Git-produced headers for names with spaces, which end their ---/+++ lines with a tab", () => {
    const dir = mkdtempSync(join(tmpdir(), "persona-space-diff-"));
    const git = (...args: string[]) => {
      const result = spawnSync("git", ["-c", "core.quotepath=on", ...args], { cwd: dir, encoding: "utf8", timeout: 10_000 });
      expect(result.status, result.stderr).toBe(0);
      return result.stdout;
    };
    const names = ["my file.md", "docs/part b/guide.md", "a b\u00e9.md", "new name.md"];
    try {
      git("init", "-q");
      mkdirSync(join(dir, "docs/part b"), { recursive: true });
      for (const name of [...names.slice(0, 3), "old name.md"]) writeFileSync(join(dir, name), "one\ntwo\nthree\nfour\n");
      git("add", "-A");
      const base = git("write-tree").trim();
      for (const name of names.slice(0, 3)) writeFileSync(join(dir, name), "one\ntwo\nthree\nfour\nfive\n");
      git("mv", "old name.md", "new name.md");
      writeFileSync(join(dir, "new name.md"), "one\ntwo\nthree\nfour\nsix\n");
      git("add", "-A");
      const edited = git("write-tree").trim();
      const diff = git("diff", "-M", base, edited);
      expect(diff).toContain("+++ b/my file.md\t\n");
      const parts = splitDiff(diff);
      expect(Object.keys(parts).sort()).toEqual([...names].sort());
      for (const name of names) expect(parseDiff(parts[name]).filter((line) => line.kind === "add").length).toBe(1);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });
});

describe("viewer", () => {
  const file = (name: string, extra: Partial<FileContent>): FileContent => ({ path: "/u/w/" + name, name, size: 1, truncated: false, is_dir: false, ...extra });

  it("picks how a file shows", () => {
    expect(viewKind(file("README.md", { text: "# x", mime: "text/markdown" }))).toBe("markdown");
    expect(viewKind(file("a.json", { text: "{}" }))).toBe("json");
    expect(viewKind(file("a.jsonl", { text: "{}\n{}", mime: "application/x-ndjson" }))).toBe("code");
    expect(viewKind(file("main.swift", { text: "x" }))).toBe("code");
    expect(viewKind(file("icon.png", { data: "AA==", mime: "image/png" }))).toBe("image");
    expect(viewKind(file("a.pdf", { data: "AA==", mime: "application/pdf" }))).toBe("pdf");
    expect(viewKind(file("a.bin", { data: "AA==", mime: "application/octet-stream" }))).toBe("binary");
    expect(viewKind(file("a.csv", { text: "a,b", mime: "text/csv" }))).toBe("sheet");
    expect(viewKind(file("a.tsv", { text: "a\tb" }))).toBe("sheet");
    expect(viewKind(file("export", { text: "a,b", mime: "text/csv" }))).toBe("sheet");
    expect(viewKind(file("Book.xlsx", { stream: true, mime: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" }))).toBe("workbook");
    expect(viewKind(file("old.xls", { data: "AA==" }))).toBe("workbook");
    expect(viewKind(file("clip.mp4", { stream: true, mime: "video/mp4" }))).toBe("video");
    expect(viewKind(file("IMG_0001.MOV", { data: "AA==", mime: "application/octet-stream" }))).toBe("video");
    expect(viewKind(file("memo.m4a", { stream: true, mime: "audio/mp4" }))).toBe("audio");
    expect(viewKind(file("logo.svg", { text: "<svg/>", mime: "text/plain; charset=utf-8" }))).toBe("image");
    expect(viewKind(file("Report.docx", { stream: true }))).toBe("office");
    expect(viewKind(file("analysis.ipynb", { text: "{}", mime: "application/x-ipynb+json" }))).toBe("notebook");
    expect(viewKind(file("report.html", { text: "<p>x</p>", mime: "text/html" }))).toBe("html");
    expect(viewKind(file("deck.pptx", { data: "AA==" }))).toBe("office");
    expect(viewKind(file("old.doc", { stream: true }))).toBe("office");
    expect(viewKind(file("notes.rtf", { text: "{\\rtf1 hi}" }))).toBe("office");
    expect(viewKind(file("id.key", { text: "-----BEGIN PRIVATE KEY-----" }))).toBe("code"); // a PEM key, not Keynote
    // A .ts is what the hub read in it (#7974): TypeScript is code, an MPEG transport stream a video.
    expect(viewKind(file("app.ts", { text: "export const n: number = 1;\n", mime: "text/typescript" }))).toBe("code");
    expect(viewKind(file("clip.ts", { stream: true, mime: "video/mp2t" }))).toBe("video");
    expect(viewKind(file("Talk.key", { stream: true }))).toBe("office");
    expect(viewKind(file("docs", { is_dir: true, entries: ["icon.png"] }))).toBe("directory");
    expect(glyphOf("icon.png")).toBe("image");
    expect(glyphOf("Package.swift")).toBe("code");
    expect(glyphOf("config.yaml")).toBe("data");
    expect(glyphOf("export.csv")).toBe("sheet");
    expect(glyphOf("Budget.xlsx")).toBe("sheet");
    expect(glyphOf("Report.docx")).toBe("doc");
    expect(glyphOf("build-log.zip")).toBe("file");
    expect(glyphOf("App.dmg")).toBe("file");
  });

  it("counts columns with wide characters taking two", () => {
    expect(columns("abc")).toBe(3);
    expect(columns("温度 21°C")).toBe(9);
    expect(columns("a😀")).toBe(3);
  });

  it("splits lines without a phantom last one", () => {
    expect(splitLines("a\nb\n")).toEqual(["a", "b"]);
    expect(splitLines("a\r\nb")).toEqual(["a", "b"]);
    expect(splitLines("")).toEqual([]);
    expect(splitLines("\n")).toEqual([""]);
  });

  it("indents JSON keeping key order and big numbers", () => {
    expect(prettyJSON('{"b":1,"2":[],"id":12345678901234567890,"s":"a,b:{c}","o":{"x":[1,{"y":null}]}}')).toBe(
      '{\n  "b": 1,\n  "2": [],\n  "id": 12345678901234567890,\n  "s": "a,b:{c}",\n  "o": {\n    "x": [\n      1,\n      {\n        "y": null\n      }\n    ]\n  }\n}',
    );
    expect(prettyJSON('"esc \\" quote"')).toBe('"esc \\" quote"');
    expect(prettyJSON("{ }")).toBe("{}");
    expect(prettyJSON("{nope")).toBeNull();
  });
});

describe("lexer", () => {
  const kinds = (line: string, toks: Token[]) => toks.map((t) => [t.kind, line.slice(t.from, t.to)]);
  const lex = (name: string, line: string, state: LexState = null) => {
    const lang = languageOf(name)!;
    const r = lexLine(line, lang, state);
    return { k: kinds(line, r.tokens), state: r.state };
  };

  it("detects languages by extension, name and first line", () => {
    expect(languageOf("main.swift")?.id).toBe("Swift");
    expect(languageOf("App.tsx")?.id).toBe("TypeScript");
    expect(languageOf("Dockerfile")?.id).toBe("Dockerfile");
    expect(languageOf(".zshrc")?.id).toBe("Shell");
    expect(languageOf("run", "#!/usr/bin/env python3")?.id).toBe("Python");
    expect(languageOf("deploy", "#!/bin/bash -e")?.id).toBe("Shell");
    expect(languageOf("Info", "<?xml version=\"1.0\"?>")?.id).toBe("XML");
    expect(languageOf("notes.txt")).toBeNull();
  });

  it("colors comments, strings and numbers", () => {
    expect(lex("main.swift", 'let n = 21 // "not a string"').k).toEqual([
      ["number", "21"],
      ["comment", '// "not a string"'],
    ]);
    expect(lex("main.go", 'fmt.Printf("a \\" b", 0x1F, 3.5e2)').k).toEqual([
      ["string", '"a \\" b"'],
      ["number", "0x1F"],
      ["number", "3.5e2"],
    ]);
    expect(lex("a.py", "x = v2 + i18n  # 1 two").k).toEqual([["comment", "# 1 two"]]);
    expect(lex("a.sh", 'echo $# "${#a}" # done').k).toEqual([
      ["string", '"${#a}"'],
      ["comment", "# done"],
    ]);
    expect(lex("a.sh", "echo 'a\\' x").k).toEqual([["string", "'a\\'"]]);
  });

  it("carries block comments and multi-line strings to the next line", () => {
    const a = lex("main.swift", 'let s = """ start');
    expect(a.state).toEqual({ kind: "string", end: '"""' });
    const b = lex("main.swift", 'still """ + 1', a.state);
    expect(b.k).toEqual([
      ["string", 'still """'],
      ["number", "1"],
    ]);
    expect(b.state).toBeNull();
    const c = lex("App.ts", "x /* open");
    expect(c.state).toEqual({ kind: "comment", end: "*/" });
    expect(lex("App.ts", "  middle", c.state).k).toEqual([["comment", "  middle"]]);
    // a plain string does not run onto the next line
    expect(lex("App.ts", 'let s = "unterminated').state).toBeNull();
  });

  it("tells JSON keys from values, and constants", () => {
    expect(lex("a.json", '  "name": "x", "n": -1.5, "ok": true, "v": null').k).toEqual([
      ["key", '"name"'],
      ["string", '"x"'],
      ["key", '"n"'],
      ["number", "1.5"],
      ["key", '"ok"'],
      ["const", "true"],
      ["key", '"v"'],
      ["const", "null"],
    ]);
  });

  it("only opens YAML / HTML strings where a value starts", () => {
    expect(lex("a.yaml", "title: it's here # c").k).toEqual([["comment", "# c"]]);
    expect(lex("a.yaml", "title: 'quoted'").k).toEqual([["string", "'quoted'"]]);
    expect(lex("a.html", '<p class="x">don\'t</p>').k).toEqual([["string", '"x"']]);
  });

  it("colors whole diff lines", () => {
    expect(lex("a.diff", "+added").k).toEqual([["add", "+added"]]);
    expect(lex("a.diff", "--- a/x").k).toEqual([["meta", "--- a/x"]]);
    expect(lex("a.diff", " same").k).toEqual([]);
  });

  it("keeps the state at each line start, extending on demand", () => {
    const lines = ["a /*", "b", "c */ d", "e"];
    const states: LexState[] = [];
    const lang = languageOf("x.ts")!;
    extendStates(lines, lang, states, 2);
    expect(states).toEqual([null, { kind: "comment", end: "*/" }, { kind: "comment", end: "*/" }]);
    extendStates(lines, lang, states, 99);
    expect(states).toEqual([null, { kind: "comment", end: "*/" }, { kind: "comment", end: "*/" }, null, null]);
  });
});

describe("markdown", () => {
  it("turns a README's HTML into Markdown and leaves fences alone", () => {
    const md = '# W\n\n<p align="center"><img src="docs/icon.png" alt="icon" width="80"></p>\n\n```html\n<b>keep</b>\n```\n<!-- hidden -->\ntext';
    expect(simplifyHTML(md)).toBe("# W\n\n![icon](docs/icon.png)\n\n```html\n<b>keep</b>\n```\ntext");
    expect(inlineHTML('<a href="https://example.com"><img src="a.png" alt="A"></a>&nbsp;&amp; more<br/>x')).toBe("[![A](a.png)](https://example.com) & more x");
  });

  it("turns an HTML table into a Markdown one", () => {
    const md = "<table>\n<tr><th>A</th><th>B|C</th></tr>\n<tr><td><img src=\"x.png\"></td></tr>\n</table>";
    expect(simplifyHTML(md).split("\n")).toEqual(["", "| A | B\\|C |", "| --- | --- |", "| ![](x.png) |  |", ""]);
  });

  it("cuts long files between blocks, never inside a fence, and repeats link definitions", () => {
    const para = (n: number) => Array.from({ length: n }, (_, i) => `line ${i}`).join("\n");
    const md = `${para(5)}\n\n\`\`\`\n${para(3)}\n\n${para(3)}\n\`\`\`\n\n${para(5)}\n\n- item\n\n  continued\n\n[ref]: https://example.com`;
    const chunks = markdownChunks(md, 4);
    expect(chunks.length).toBeGreaterThan(2);
    for (const c of chunks) {
      expect((c.match(/```/g) ?? []).length % 2).toBe(0);
      expect(c.endsWith("[ref]: https://example.com")).toBe(true);
      expect(c.startsWith("  continued")).toBe(false);
    }
    expect(markdownChunks("short", 4)).toEqual(["short"]);
  });

  it("keeps a longer fence open past a shorter one inside it", () => {
    // a ```` block that shows a ``` one (a README about Markdown): only ```` ends it (CommonMark)
    const md = '````md\n```html\n<p align="center">x</p>\n```\n````\n<b>after</b>';
    expect(simplifyHTML(md).split("\n").slice(0, 5)).toEqual(["````md", "```html", '<p align="center">x</p>', "```", "````"]);
    const para = (n: number) => Array.from({ length: n }, (_, i) => `line ${i}`).join("\n");
    const long = `${para(5)}\n\n\`\`\`\`md\n\`\`\`js\n${para(3)}\n\n${para(3)}\n\`\`\`\n\`\`\`\`\n\n${para(5)}`;
    const chunks = markdownChunks(long, 4);
    expect(chunks.length).toBeGreaterThan(1);
    for (const c of chunks) expect((c.match(/^````/gm) ?? []).length % 2).toBe(0);
  });

  it("never cuts inside display math", () => {
    const para = (n: number) => Array.from({ length: n }, (_, i) => `line ${i}`).join("\n");
    const md = `${para(5)}\n\n$$\na\n\nb\n\nc\n$$\n\n${para(5)}\n\n\\[\nx\n\ny\n\nz\n\\]\n\n${para(5)}`;
    const chunks = markdownChunks(md, 4);
    expect(chunks.length).toBeGreaterThan(2);
    for (const c of chunks) {
      expect((c.match(/\$\$/g) ?? []).length % 2).toBe(0);
      expect((c.match(/\\\[/g) ?? []).length).toBe((c.match(/\\\]/g) ?? []).length);
    }
    expect(chunks.some((c) => c.includes("$$\na\n\nb\n\nc\n$$"))).toBe(true);
  });
});

describe("tool steps", () => {
  it("finds the file a step works on", () => {
    expect(toolFile("Read", { file_path: "/u/w/a.swift", offset: 40, limit: 20 })).toEqual({ path: "/u/w/a.swift", line: 40 });
    expect(toolFile("Edit", { file_path: "WeatherWidget/Provider.swift", diff: "@@" })).toEqual({ path: "WeatherWidget/Provider.swift" });
    expect(toolFile("NotebookEdit", { notebook_path: "/u/w/a.ipynb" })).toEqual({ path: "/u/w/a.ipynb" });
    expect(toolFile("Bash", { command: "ls" })).toBeNull();
    expect(toolFile("Write", {})).toBeNull();
  });
});
