import { describe, expect, it } from "vitest";
import { capLines, languageFile, parseNotebook, stripAnsi, withAttachmentRefs } from "./notebook";

const nb = (cells: unknown[], metadata: unknown = { kernelspec: { language: "python", display_name: "Python 3" } }) => JSON.stringify({ nbformat: 4, nbformat_minor: 5, metadata, cells });

describe("parseNotebook", () => {
  it("reads cells, sources as arrays, counts and the kernel", () => {
    const n = parseNotebook(
      nb([
        { cell_type: "markdown", source: ["# 标题\n", "正文"], metadata: {} },
        { cell_type: "code", execution_count: 3, source: "print(1)", outputs: [{ output_type: "stream", name: "stdout", text: ["1\n"] }] },
        { cell_type: "raw", source: "raw" },
      ]),
    );
    expect(n).toMatchObject({ language: "python", kernel: "Python 3", format: "4.5" });
    expect(n.cells.map((c) => [c.type, c.source, c.count])).toEqual([
      ["markdown", "# 标题\n正文", null],
      ["code", "print(1)", 3],
      ["raw", "raw", null],
    ]);
    expect(n.cells[1].outputs).toEqual([{ kind: "stream", name: "stdout", text: "1\n" }]);
  });

  it("picks one representation per output, best first", () => {
    const [c] = parseNotebook(
      nb([
        {
          cell_type: "code",
          source: "",
          outputs: [
            { output_type: "execute_result", data: { "text/plain": "<Figure>", "image/png": "iVBOR\nw0K" } },
            { output_type: "display_data", data: { "text/plain": "df", "text/html": ["<table>", "</table>"] } },
            { output_type: "display_data", data: { "text/latex": "x^2" } },
            { output_type: "display_data", data: { "image/svg+xml": "<svg/>" } },
            { output_type: "execute_result", data: { "application/json": { a: 1 } } },
            { output_type: "stream", name: "stderr", text: "warn " },
            { output_type: "stream", name: "stderr", text: "again" },
            { output_type: "error", ename: "ValueError", evalue: "bad", traceback: ["\u001b[0;31mValueError\u001b[0m: bad"] },
          ],
        },
      ]),
    ).cells;
    expect(c.outputs).toEqual([
      { kind: "image", mime: "image/png", src: "data:image/png;base64,iVBORw0K" },
      { kind: "html", html: "<table></table>" },
      { kind: "markdown", text: "$$\nx^2\n$$" },
      { kind: "image", mime: "image/svg+xml", src: "data:image/svg+xml;charset=utf-8,%3Csvg%2F%3E" },
      { kind: "json", text: '{\n  "a": 1\n}' },
      { kind: "stream", name: "stderr", text: "warn again" }, // consecutive streams merged
      { kind: "error", name: "ValueError", value: "bad", traceback: "ValueError: bad" },
    ]);
  });

  it("merges consecutive stream chunks before stripping ANSI and capping lines", () => {
    const chunk = (n: number, start: number) => Array.from({ length: n }, (_, i) => `line ${start + i}\n`).join("");
    const [capped, splitAnsi] = parseNotebook(
      nb([
        {
          cell_type: "code",
          source: "",
          outputs: [
            { output_type: "stream", name: "stdout", text: chunk(1500, 1) },
            { output_type: "stream", name: "stdout", text: chunk(1000, 1501) },
          ],
        },
        {
          cell_type: "code",
          source: "",
          outputs: [
            { output_type: "stream", name: "stdout", text: "\u001b[32m" },
            { output_type: "stream", name: "stdout", text: "ok\u001b[0" },
            { output_type: "stream", name: "stdout", text: "m" },
          ],
        },
      ]),
    ).cells;
    expect(capped.outputs).toEqual([{ kind: "stream", name: "stdout", text: capLines(chunk(2500, 1)) }]);
    expect(splitAnsi.outputs).toEqual([{ kind: "stream", name: "stdout", text: "ok" }]);
  });

  it("reads attachments and nbformat 3 worksheets", () => {
    const n = parseNotebook(nb([{ cell_type: "markdown", source: "![a](attachment:p.png)", attachments: { "p.png": { "image/png": "AAAA" } } }]));
    expect(n.cells[0].attachments).toEqual({ "p.png": "data:image/png;base64,AAAA" });
    const v3 = parseNotebook(
      JSON.stringify({ nbformat: 3, metadata: { language: "julia" }, worksheets: [{ cells: [{ cell_type: "heading", level: 2, source: "Hi" }, { cell_type: "code", input: "1+1", prompt_number: 1, outputs: [{ output_type: "pyout", "text/plain": "2" }] }] }] }),
    );
    expect(v3.language).toBe("julia");
    expect(v3.cells.map((c) => c.source)).toEqual(["## Hi", "1+1"]);
    expect(v3.cells[1].outputs).toEqual([{ kind: "text", text: "2" }]);
  });

  it("explains what is not a notebook", () => {
    expect(() => parseNotebook("{nope")).toThrow("JSON");
    expect(() => parseNotebook("{}")).toThrow("cells");
  });
});

describe("helpers", () => {
  it("strips color codes and caps long output", () => {
    expect(stripAnsi("\u001b[1;32mok\u001b[0m")).toBe("ok");
    expect(capLines("a\nb\nc\nd\n", 2)).toBe("a\nb\n… 另有 2 行未显示");
    expect(capLines("a\nb", 5)).toBe("a\nb");
  });
  it("points attachments at a fragment and names the colorer's file", () => {
    expect(withAttachmentRefs("![x](attachment:my%20pic.png)")).toBe("![x](#nb-attachment=my%2520pic.png)");
    expect(languageFile("R")).toBe("cell.py"); // callers lower-case first
    expect(languageFile("r")).toBe("cell.r");
  });
});
