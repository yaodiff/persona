import { afterEach, describe, expect, it, vi } from "vitest";
import katex from "katex";
import { hasMath, linkify, normalizeMath, splitCode } from "./Markdown";

// The machine's answers about paths (lib/path-presence.ts), for the cache's path-link key; every other request is real.
const api = vi.hoisted(() => vi.fn(async (_url: string) => ({ stats: [] as { path: string; kind: string }[] })));
vi.mock("../lib/api", async (orig) => ({ ...(await orig<typeof import("../lib/api")>()), api: (url: string) => api(url) }));
// How many blocks react-markdown parsed: its Markdown() runs once per parse, called by markdown-cache.ts or drawn as
// <ReactMarkdown>.
const parses = vi.hoisted(() => ({ n: 0 }));
vi.mock("react-markdown", async (orig) => {
  const real = await orig<typeof import("react-markdown")>();
  return { ...real, default: (options: Parameters<typeof real.default>[0]) => (parses.n++, real.default(options)) };
});

describe("normalizeMath", () => {
  it("rewrites \\( \\) to inline dollars", () => {
    expect(normalizeMath("面积是 \\(\\pi r^2\\)。")).toBe("面积是 $\\pi r^2$。");
  });

  it("rewrites \\[ \\] to a display block", () => {
    expect(normalizeMath("公式：\\[ E = mc^2 \\]")).toBe("公式：\n$$\nE = mc^2\n$$");
    expect(normalizeMath("公式：\\[ E = mc^2 \\] 完")).toBe("公式：\n$$\nE = mc^2\n$$\n 完");
  });

  it("keeps display blocks inside list items and quotes", () => {
    expect(normalizeMath("- 公式：\\[ E=mc^2 \\]")).toBe("- 公式：\n  $$\n  E=mc^2\n  $$");
    expect(normalizeMath("> 引用里：\\[ a \\]")).toBe("> 引用里：\n> $$\n> a\n> $$");
    expect(normalizeMath("- $$x^2$$")).toBe("- $$\n  x^2\n  $$");
    expect(normalizeMath("> $$x^2$$")).toBe("> $$\n> x^2\n> $$");
  });

  it("leaves dollars alone", () => {
    expect(normalizeMath("$a_1 * b_2$ 和 $$x^2$$")).toBe("$a_1 * b_2$ 和 $$x^2$$");
  });

  it("turns a line holding only $$…$$ into a display block", () => {
    expect(normalizeMath("积分：\n$$\\int_0^1 x\\,dx$$\n完")).toBe("积分：\n$$\n\\int_0^1 x\\,dx\n$$\n完");
    expect(normalizeMath("$$a$$ 和 $$b$$")).toBe("$$a$$ 和 $$b$$");
  });

  it("keeps prices as text", () => {
    expect(normalizeMath("价格 $5 和 $10")).toBe("价格 \\$5 和 \\$10");
    expect(normalizeMath("共 $5，公式 $x^2$")).toBe("共 \\$5，公式 $x^2$");
    expect(normalizeMath("$x^2$ 和 $2x$")).toBe("$x^2$ 和 $2x$");
  });

  it("does not pair an unclosed $ with the next formula", () => {
    expect(normalizeMath("未闭合 $\\frac{a}{b 后面 $\\sqrt{2}$")).toBe("未闭合 \\$\\frac{a}{b 后面 $\\sqrt{2}$");
  });

  it("rewrites | inside math on table rows", () => {
    expect(normalizeMath("| 绝对值 | $|x|$ |\n| 范数 | $\\|v\\|$ |")).toBe(
      "| 绝对值 | $\\vert{}x\\vert{}$ |\n| 范数 | $\\Vert{}v\\Vert{}$ |",
    );
    expect(normalizeMath("不是表格 $|x|$")).toBe("不是表格 $|x|$");
  });

  it("keeps $ next to letters or digits as text", () => {
    expect(normalizeMath("$HOME/$USER/bin")).toBe("\\$HOME/\\$USER/bin");
    expect(normalizeMath("US$5 和 A$10")).toBe("US\\$5 和 A\\$10");
  });

  it("handles backslashes before and inside math", () => {
    expect(normalizeMath("换行 \\\\$x$")).toBe("换行 \\\\$x$");
    expect(normalizeMath("$\\text{售价 \\$5}$")).toBe('$\\text{售价 {\\char"24}5}$');
  });

  it("leaves code alone", () => {
    const md = "`\\(x\\)` 和\n```\n\\[y\\]\n```";
    expect(normalizeMath(md)).toBe(md);
    for (const code of [
      "~~~\n$5 和 \\(x\\)\n~~~",
      "前文\n\n    cost = $5 + $6\n    \\(x\\)\n\n后文",
      "``a `$5` b $6``",
      "```\n未闭合 $5 和 $6",
      "- 列表\n  ```\n  $5 和 $6\n  ```",
    ]) {
      expect(normalizeMath(code)).toBe(code);
    }
  });
});

// In this order: KaTeX is loaded once per module, the first tests see the page before it is (#1996).
describe("KaTeX", () => {
  it("knows which text has a formula, so only that loads KaTeX", () => {
    for (const md of ["$x^2$", "\\(a+b\\)", "前文\n\n\\[\nE=mc^2\n\\]", "$$\\frac12$$"]) expect(hasMath(normalizeMath(md))).toBe(true);
    for (const md of ["$5 和 $10", "US$5", "`$x$`", "```\n$x$\n```", "没有公式"]) expect(hasMath(normalizeMath(md))).toBe(false);
  });

  it("shows a formula as its TeX until KaTeX is in", async () => {
    const { renderToStaticMarkup } = await import("react-dom/server");
    const { createElement } = await import("react");
    const { Markdown } = await import("./Markdown");
    const html = renderToStaticMarkup(createElement(Markdown, { text: "面积 $\\pi r^2$\n\n$$\nE=mc^2\n$$" }));
    expect(html).not.toContain('class="katex');
    expect(html).toContain('<span data-math-pending="inline">$\\pi r^2$</span>');
    expect(html).toMatch(/data-math-pending="display">\$\$\nE=mc\^2\n\$\$/);
    // an ordinary code span is still code
    expect(renderToStaticMarkup(createElement(Markdown, { text: "`a_b`" }))).toMatch(/<code [^>]*data-copy-code[^>]*>a_b<\/code>/);
  });

  it("renders what normalizeMath writes and mhchem once loaded", async () => {
    const { loadMath } = await import("./stream/math");
    await loadMath();
    for (const tex of ['\\text{售价 {\\char"24}5}', "\\ce{H2O}", "\\pu{123 kJ/mol}", "\\vert{}x\\vert{}"]) {
      expect(() => katex.renderToString(tex, { throwOnError: true })).not.toThrow();
    }
    const { renderToStaticMarkup } = await import("react-dom/server");
    const { createElement } = await import("react");
    const { Markdown } = await import("./Markdown");
    const html = renderToStaticMarkup(createElement(Markdown, { text: "面积 $\\pi r^2$" }));
    expect(html).toContain('class="katex"');
    expect(html).not.toContain("data-math-pending");
    // the block the test before drew while KaTeX was out: drawn again with it, not as the kept tree (markdown-cache.ts)
    const again = renderToStaticMarkup(createElement(Markdown, { text: "面积 $\\pi r^2$\n\n$$\nE=mc^2\n$$" }));
    expect(again).toContain('class="katex-display"');
    expect(again).not.toContain("data-math-pending");
  });
});

describe("splitCode", () => {
  it("returns text and code alternately", () => {
    expect(splitCode("a `b` c")).toEqual(["a ", "`b`", " c"]);
    expect(splitCode("x\n~~~\ny\n~~~\nz")).toEqual(["x\n", "~~~\ny\n~~~\n", "z"]);
    expect(splitCode("x\n\n    code\ny")).toEqual(["x\n\n", "    code\n", "y"]);
  });

  it("does not treat indented list continuations as code", () => {
    expect(splitCode("- a\n\n    b $5")).toEqual(["- a\n\n    b $5"]);
  });
});

describe("splitCode containers", () => {
  it("advances past whitespace-only indented lines", () => {
    expect(splitCode("text\n\n    \nnext")).toEqual(["text\n\n    \nnext"]);
    expect(splitCode("    ")).toEqual(["    "]);
  });

  it("recognizes a fence on the list-marker line", () => {
    const md = "- ```sh\n  echo $HOME\n  ```\n\nFormula: \\(x\\)";
    expect(splitCode(md)).toEqual(["", "- ```sh\n  echo $HOME\n  ```\n", "\nFormula: \\(x\\)"]);
    expect(normalizeMath(md)).toBe("- ```sh\n  echo $HOME\n  ```\n\nFormula: $x$");
  });

  it("ends an unclosed fence where its quote ends", () => {
    expect(normalizeMath("> ```\n> code\n\nFormula: \\(x\\)")).toBe("> ```\n> code\n\nFormula: $x$");
  });
});

describe("normalizeMath review cases", () => {
  it("keeps $ inside URLs", () => {
    const md = "见 https://example.com/odata/$metadata 和 <https://a.com/$x> 及 $y$";
    expect(normalizeMath(md)).toBe(md);
    expect(linkify(normalizeMath(md), { math: true })).toBe("见 <https://example.com/odata/$metadata> 和 <https://a.com/$x> 及 $y$");
  });

  it("strips quote prefixes from captured display math", () => {
    expect(normalizeMath("> \\[\n> a+b\n> \\]")).toBe("> $$\n> a+b\n> $$");
  });

  it("keeps explicit \\( \\) math out of the $ heuristics", () => {
    expect(normalizeMath("The \\(n\\)th term")).toBe("The $n$th term");
    expect(normalizeMath("a \\(x +\ny\\) b")).toBe("a $x +\ny$ b");
    expect(normalizeMath("价 \\(\\$5\\)")).toBe('价 ${\\char"24}5$');
  });

  it("protects table math without outer pipes or after inline code", () => {
    expect(normalizeMath("Name | Value\n--- | ---\nnorm | $|x|$")).toBe("Name | Value\n--- | ---\nnorm | $\\vert{}x\\vert{}$");
    expect(normalizeMath("| `c` | $|x|$ |\n| --- | --- |")).toBe("| `c` | $\\vert{}x\\vert{}$ |\n| --- | --- |");
  });
});

describe("linkify", () => {
  it("does not touch URLs inside math", () => {
    const md = "$\\text{see https://a.com/x}$";
    expect(linkify(md, { math: true })).toBe(md);
  });

  it("still links bare URLs outside math", () => {
    expect(linkify("见 https://a.com/x 和 $x$", { math: true })).toBe("见 <https://a.com/x> 和 $x$");
  });

  it("treats $ as text without math", () => {
    expect(linkify("$5 见 https://example.com/path。说明 $10")).toBe("$5 见 <https://example.com/path>。说明 $10");
  });
});

describe("Mermaid blocks", () => {
  it("are drawn by the Mermaid block, other fences stay code", async () => {
    const { renderToStaticMarkup } = await import("react-dom/server");
    const { createElement } = await import("react");
    const { Markdown } = await import("./Markdown");
    const html = renderToStaticMarkup(createElement(Markdown, { text: "```mermaid\ngraph TD; A-->B\n```\n\n```js\nlet a = 1\n```" }));
    expect(html.match(/data-mermaid/g)?.length).toBe(1);
    expect(html).toContain("graph TD; A--&gt;B"); // its source shows until the diagram is drawn
    expect(html).toContain("let a = 1");
  });

  it("code and diagram buttons show without hover on a touch screen, with a finger-sized target (#1996)", async () => {
    const { renderToStaticMarkup } = await import("react-dom/server");
    const { createElement } = await import("react");
    const { Markdown } = await import("./Markdown");
    // a code block's Copy is in its title bar, always shown (#2984); a diagram's buttons show on a touch screen
    const code = renderToStaticMarkup(createElement(Markdown, { text: "```js\nlet a = 1\n```" }));
    expect(code).toMatch(/data-code-bar[^]*aria-label="拷贝代码"[^>]*touch-hit/);
    expect(code).not.toContain("opacity-0");
    const diagram = renderToStaticMarkup(createElement(Markdown, { text: "```mermaid\ngraph TD; A-->B\n```" }));
    expect(diagram).toContain("[@media(hover:none)]:opacity-100");
    expect(diagram).toMatch(/aria-label="拷贝代码"[^>]*touch-hit/);
  });

  it("errors read in Chinese with the line, Mermaid's text kept", async () => {
    const { mermaidProblem } = await import("../lib/mermaid");
    const raw = "Parse error on line 3:\nflowchart TD A -->\n--------------^\nExpecting 'NODE', got 'EOF'";
    expect(mermaidProblem(new Error(raw))).toEqual({ summary: "这张图有语法错误（第 3 行）", detail: raw });
    expect(mermaidProblem(new Error("No diagram type detected")).summary).toBe("这张图画不出来");
  });
});


describe("conversation links", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("routes a machine file to its file endpoint instead of a site path", async () => {
    const { renderToStaticMarkup } = await import("react-dom/server");
    const { createElement } = await import("react");
    const { Markdown } = await import("./Markdown");
    vi.stubGlobal("localStorage", { getItem: () => "test-token" });
    for (const text of ["[报告](/workspace/my%20report.md)", "[报告](./my%20report.md)"]) {
      const html = renderToStaticMarkup(createElement(Markdown, { text, mid: "desktop", cwd: "/workspace" }));
      expect(html).toContain("/api/v1/machines/desktop/file/download?path=%2Fworkspace%2Fmy%20report.md");
      expect(html).not.toContain('href="/workspace/');
    }
  });

  it("keeps web links and file-reader-relative links on their existing routes", async () => {
    const { renderToStaticMarkup } = await import("react-dom/server");
    const { createElement } = await import("react");
    const { Markdown } = await import("./Markdown");
    const html = renderToStaticMarkup(createElement(Markdown, { text: "[站点](https://example.com/report.md) [章节](#heading)", mid: "desktop" }));
    expect(html).toContain('href="https://example.com/report.md"');
    expect(html).toContain('href="#heading"');
    expect(renderToStaticMarkup(createElement(Markdown, { text: "[另一个文件](./other.md)" }))).toContain('href="./other.md"');
  });

  it("passes machine context through both finished and streaming answers", async () => {
    const { renderToStaticMarkup } = await import("react-dom/server");
    const { createElement } = await import("react");
    const { Answer } = await import("../views/chat/SegmentView");
    vi.stubGlobal("localStorage", { getItem: () => "test-token" });
    const text = "[报告](/workspace/report.md)";
    for (const props of [{ streaming: text }, { item: { id: "answer", seq: 1, role: "assistant" as const, kind: "text" as const, text } }]) {
      const html = renderToStaticMarkup(createElement(Answer, { ...props, mid: "desktop", sid: "session" }));
      expect(html).toContain("/api/v1/machines/desktop/file/download?path=%2Fworkspace%2Freport.md");
    }
    // The first import of SegmentView pulls in the whole chat view graph; under the full suite's parallel load
    // that alone can pass the default 5 s, so this case gets room for the import, not for the rendering.
  }, 30_000);

  it("renders malformed conversation URLs without crashing the transcript", async () => {
    const { renderToStaticMarkup } = await import("react-dom/server");
    const { createElement } = await import("react");
    const { Markdown } = await import("./Markdown");
    for (const scheme of ["persona"]) {
      expect(() => renderToStaticMarkup(createElement(Markdown, { text: `[会话](${scheme}://session/desktop/%E0%A4%A)` }))).not.toThrow();
    }
  });
});

describe("Markdown tables (#5275)", () => {
  it("renders table rows with cells containing long links", async () => {
    const { renderToStaticMarkup } = await import("react-dom/server");
    const { createElement } = await import("react");
    const { Markdown } = await import("./Markdown");
    const md = "| 描述 | 链接 |\n| --- | --- |\n| 超长链接测试 | https://example.com/very/long/nested/path/that/exceeds/the/default/column/width/and/should/wrap/cleanly |";
    const html = renderToStaticMarkup(createElement(Markdown, { text: md }));
    expect(html).toContain("<table>");
    expect(html).toContain("<th>描述</th>");
    expect(html).toContain("<th>链接</th>");
    expect(html).toContain('<a href="https://example.com/very/long/nested/path/that/exceeds/the/default/column/width/and/should/wrap/cleanly"');
  });

  it("styles.css allows table cells to break long unbroken strings with overflow-wrap: anywhere", async () => {
    const { readFileSync } = await import("node:fs");
    const { join } = await import("node:path");
    const css = readFileSync(join(__dirname, "../styles.css"), "utf8");
    expect(css).toMatch(/\.prose-j th,\s*\.prose-j td\s*\{[^}]*overflow-wrap:\s*anywhere;/);
    // The copy button's wrap becomes the prose block's child, so the table would keep a top margin it used to lose.
    expect(css).toMatch(/\.prose-j > \.md-table:first-child > table\s*\{[^}]*margin-top:\s*0/);
    expect(css).toMatch(/\.prose-j > \.md-table:last-child > table\s*\{[^}]*margin-bottom:\s*0/);
  });
});

// A table on a phone (audit item 77): a column is as narrow as its words, and the copy button never sits on header words.
describe("Markdown table widths and the copy button (audit item 77)", () => {
  it("marks the cells short enough for one line; long ones keep their minimum and wrap", async () => {
    const { renderToStaticMarkup } = await import("react-dom/server");
    const { createElement } = await import("react");
    const { Markdown } = await import("./Markdown");
    const md = "| # | 事项 | 裁定 |\n| --- | --- | --- |\n| 1 | 小组件刷新频率 | 按小时刷新，夜间降到每三小时一次，省电优先 |\n| 2 | **图标** | 换成`描边`版本 |";
    const html = renderToStaticMarkup(createElement(Markdown, { text: md }));
    expect(html).toContain('<td data-fit="">1</td>');
    expect(html).toContain('<td data-fit=""><strong>图标</strong></td>');
    expect(html).toContain("<td>小组件刷新频率</td>");
    expect(html).toContain("<td>按小时刷新，夜间降到每三小时一次，省电优先</td>");
    // the header cells are left as they were (they never wrap)
    expect(html).toContain("<th>#</th>");
  });

  it("counts wide characters as wider, and a line break or a picture as never fitting", async () => {
    const { cellFits } = await import("./Markdown");
    const text = (value: string) => ({ type: "element", tagName: "td", children: [{ type: "text", value }] });
    expect(cellFits(text("中国气象局"))).toBe(true);
    expect(cellFits(text("中国气象局开放平台"))).toBe(false);
    expect(cellFits(text("1,496 ms"))).toBe(true);
    expect(cellFits(text("a sentence that wraps"))).toBe(false);
    expect(cellFits({ type: "element", tagName: "td", children: [{ type: "text", value: "a" }, { type: "element", tagName: "br", children: [] }] })).toBe(false);
    expect(cellFits({ type: "element", tagName: "td", children: [{ type: "element", tagName: "img", children: [] }] })).toBe(false);
    expect(cellFits(undefined)).toBe(false);
  });

  it("styles.css: no minimum for headers or short cells, 7em for long ones, and room for the button in the header row", async () => {
    const { readFileSync } = await import("node:fs");
    const { join } = await import("node:path");
    const css = readFileSync(join(__dirname, "../styles.css"), "utf8");
    const rule = (sel: RegExp) => css.match(new RegExp(`(?:^|\\n)${sel.source}\\s*\\{([^}]*)\\}`))?.[1] ?? "";
    expect(rule(/\.prose-j th,\s*\.prose-j td/)).not.toContain("min-width");
    expect(rule(/\.prose-j td/)).toMatch(/min-width:\s*7em/);
    expect(rule(/\.prose-j td\[data-fit\]/)).toMatch(/min-width:\s*0;\s*white-space:\s*nowrap/);
    expect(rule(/\.prose-j th(?!,)/)).not.toContain("min-width");
    expect(rule(/\.prose-j \.md-table th:last-child/)).toMatch(/padding-right:\s*2\.25rem/);
  });

  it("puts the copy button on a cap the header row's color, shown with it", async () => {
    const { renderToStaticMarkup } = await import("react-dom/server");
    const { createElement } = await import("react");
    const { Markdown } = await import("./Markdown");
    const html = renderToStaticMarkup(createElement(Markdown, { text: "| a | b |\n| --- | --- |\n| 1 | 2 |" }));
    const cap = /<span data-table-copy="[^"]*" class="([^"]*)"><button[^>]*aria-label="拷贝表格"/.exec(html);
    expect(cap).not.toBeNull();
    expect(cap![1]).toContain("to-inset");
    expect(cap![1]).toContain("rounded-tr-card");
    expect(cap![1]).toContain("opacity-0");
    expect(cap![1]).toContain("[@media(hover:none)]:opacity-100");
  });
});

// Finished blocks drawn again from markdown-cache.ts (#2465): a kept tree draws what a fresh one does, and a change of
// anything a tree is made from (machine, directory, formulas in or out, the path-link set) never draws another's.
describe("Markdown cache (#2465)", () => {
  const CORPUS = [
    "## 结果 Results\n\n| step | before | after |\n| --- | ---: | ---: |\n| parse | 1,496 ms | $O(n \\log n)$ |\n| 渲染 | **快** | `src/a.ts:12` |",
    "面积 $\\pi r^2$，水是 $\\ce{H2O}$，热量 $\\pu{123 kJ/mol}$，价格 $5\n\n$$\n\\sum_{i=1}^{n} t_i\n$$",
    "```ts\nconst a: number = 1 // one\n```\n\n```go\nfunc main() { fmt.Println(\"hi\") }\n```\n\n```python\nrows = [r for r in batch]  # keep\n```\n\n```nosuchlang\nplain $5\n```\n\n```\nno language\n```",
    "读 src/a.ts:12 和 `src/a.ts`，见 [报告](./report.md) 与 https://example.com/x。\n\n- 这是**「重要」**的事\n- > 引用 $x^2$",
  ];
  const there = async () => {
    const { askPaths, resetPathPresence } = await import("../lib/path-presence");
    resetPathPresence();
    api.mockImplementation(async (url: string) => ({
      stats: new URL(url, "http://x").searchParams.get("paths")!.split("\n").map((path) => ({ path, kind: path === "src/a.ts" ? "file" : "missing" })),
    }));
    askPaths("mac", "/repo", ["src/a.ts"]);
    await vi.waitFor(async () => expect((await import("../lib/path-presence")).pathStat("mac", "/repo", "src/a.ts")?.kind).toBe("file"));
    return resetPathPresence;
  };
  const draw = async () => {
    const { renderToStaticMarkup } = await import("react-dom/server");
    const { createElement } = await import("react");
    const { Markdown } = await import("./Markdown");
    const { markdownCache, forgetMarkdown } = await import("./markdown-cache");
    vi.stubGlobal("localStorage", { getItem: () => "test-token" });
    const html = (props: { text: string; mid?: string; cwd?: string }, cached = true) => {
      markdownCache.on = cached;
      try {
        return renderToStaticMarkup(createElement(Markdown, props));
      } finally {
        markdownCache.on = true;
      }
    };
    return { html, forgetMarkdown };
  };
  afterEach(() => vi.unstubAllGlobals());

  it("draws a kept tree, and the one it keeps, as the block drawn without the cache", async () => {
    const { loadMath } = await import("./stream/math");
    await loadMath();
    const forget = await there();
    const { html, forgetMarkdown } = await draw();
    forgetMarkdown();
    for (const text of CORPUS) {
      for (const props of [{ text }, { text, mid: "mac", cwd: "/repo" }]) {
        const fresh = html(props, false);
        expect(html(props)).toBe(fresh); // kept now
        expect(html(props)).toBe(fresh); // drawn from the cache
      }
    }
    expect(html({ text: CORPUS[3], mid: "mac", cwd: "/repo" })).toContain("file/download?path=%2Frepo%2Fsrc%2Fa.ts");
    forget();
  });

  it("draws a conversation opened again from its kept trees, without parsing its replies", async () => {
    const { html, forgetMarkdown } = await draw();
    forgetMarkdown();
    // each draw is a new root, as a conversation opened again mounts its replies anew (ChatView remounts)
    const open = () => CORPUS.map((text) => html({ text, mid: "mac", cwd: "/repo" }));
    const before = parses.n;
    const first = open();
    expect(parses.n - before).toBe(CORPUS.length);
    expect(open()).toEqual(first);
    expect(parses.n - before).toBe(CORPUS.length);
  });

  it("draws a block again without parsing it, and does not keep one block that would push out a chat", async () => {
    const { cachedMarkdown, forgetMarkdown } = await import("./markdown-cache");
    forgetMarkdown();
    const parsed = vi.fn();
    const plugins = [() => parsed];
    const opts = (children: string, remarkPlugins = plugins) => ({ children, remarkPlugins });
    const tree = cachedMarkdown("k", opts("# a"));
    expect(cachedMarkdown("k", opts("# a"))).toBe(tree);
    expect(parsed).toHaveBeenCalledTimes(1);
    // another remark plugin list (a reply read line by line, say) is another tree under the same key
    expect(cachedMarkdown("k", opts("# a", [...plugins]))).not.toBe(tree);
    expect(parsed).toHaveBeenCalledTimes(2);
    const long = "x".repeat(300_000);
    expect(cachedMarkdown(long, opts(long))).not.toBe(cachedMarkdown(long, opts(long)));
    expect(parsed).toHaveBeenCalledTimes(4);
  });

  it("keys the machine, the directory and the path-link set", async () => {
    const { html, forgetMarkdown } = await draw();
    forgetMarkdown();
    const text = CORPUS[3];
    const base = { text, mid: "mac", cwd: "/repo" };
    const before = html(base); // no answer about src/a.ts yet: plain text, kept
    expect(before).not.toContain("path=%2Frepo%2Fsrc%2Fa.ts");
    const forget = await there();
    const variants = [base, { ...base, mid: "mini" }, { ...base, cwd: "/other" }, { text }];
    const drawn = variants.map((v) => html(v));
    expect(drawn[0]).toContain("path=%2Frepo%2Fsrc%2Fa.ts"); // the link set changed: not the tree kept before
    expect(drawn).toEqual(variants.map((v) => html(v, false)));
    expect(variants.map((v) => html(v))).toEqual(drawn);
    expect(new Set([before, ...drawn]).size).toBe(variants.length + 1); // each of them changes what is drawn
    forget();
  });
});
