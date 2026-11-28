// @vitest-environment jsdom
// URL safety regressions across Feishu doc renderer/markdown, doc comments, Lark media/extras/profile helpers,
// Lark sync OAuth navigation, extension homepages, links.linkHref, and task repository bindings.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { linkHref } from "../../lib/links";
import { safeApiPath } from "../../lib/safe-media-url";
import { cardButtons, profileRows } from "../../lib/lark-chat";
import { leaveFor } from "../../lib/lark-sync";
import { mediaURL, prepareMarkdown, type DocNode } from "./feishu-doc";
import { DocTree } from "./DocContent";
import { DocCommentsView } from "./DocComments";
import { openMedia, safeMediaURL } from "../inbox/LarkThread";
import { EmojiImage, FilePart, VideoPart, VoicePart } from "../inbox/LarkExtras";
import { Information } from "../settings/ExtensionSettingsPage";
import { TodoRepositoryBindings } from "../../ui/TaskRepositoryBindingsEditor";
import type { HubExtensionDetail } from "../../lib/extensions";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.restoreAllMocks();
});

describe("shared safeApiPath, linkHref, and Feishu doc prepareMarkdown / mediaURL hardening", () => {
  it("blocks encoded, intra-API, fragment, and backslash traversal while preserving valid %2F IDs and query values", () => {
    for (const bad of [
      "api/v1/foo",
      "/api/v1/../admin",
      "/api/v1/lark/media/../../admin",
      "/api/v1/lark/raw/..?foo=1",
      "/api/v1/lark/raw/%2e%2e?foo=1",
      "/api/v1/lark/raw/..#x",
      "/api/v1/lark/raw/..%2fadmin",
      "/api/v1/lark/raw/%2e%2e%2fadmin",
      "/api/v1/lark/raw/%252e%252e/admin",
      "/api/v1/lark/raw/foo%5cbar",
      "/api/v1/lark/media/%255c..%255cadmin",
      "/api/v1/lark/raw//admin",
      "/api/v1/lark/raw/ok#frag",
    ]) {
      expect(safeApiPath(bad), bad).toBeUndefined();
      expect(linkHref(bad), bad).toBeUndefined();
    }

    expect(safeApiPath("/api/v1/mail/threads/a%2Fb")).toBe("/api/v1/mail/threads/a%2Fb");
    expect(safeApiPath("/api/v1/lark/media/50%25off.png")).toBe("/api/v1/lark/media/50%25off.png");
    expect(safeApiPath("/api/v1/machines/m/file/download?path=%2Ftmp%2F..%2Ffoo")).toBe("/api/v1/machines/m/file/download?path=%2Ftmp%2F..%2Ffoo");
    expect(linkHref("/api/v1/mail/threads/a%2Fb")).toBe("/api/v1/mail/threads/a%2Fb");
    expect(linkHref("/api/v1/lark/media/50%25off.png")).toBe("/api/v1/lark/media/50%25off.png");
  });

  it("rejects unsafe schemes, relative authority confusion, and traversal in mediaURL and prepareMarkdown", () => {
    for (const bad of [
      "javascript:alert(1)",
      "data:image/svg+xml;base64,PHN2Zz4=",
      "https:/admin",
      "https://user:pw@evil.example/a.png",
      "/api/v1/../admin",
      "/api/v1/lark/raw/..?x=1",
      "/api/v1/lark/raw/%2e%2e?x=1",
      "/api/v1/lark/raw/..#x",
      "/api/v1/lark/raw/..%2fadmin",
      "/api/v1/lark/raw/%252e%252e/admin",
      "/api/v1/lark/raw/foo%5cbar",
      "http://127.0.0.1:3000/api/../../admin",
      "http://127.0.0.1:3000/api/%2e%2e/%2e%2e/admin",
      "http://127.0.0.1:3000/api/..#frag",
    ]) {
      expect(mediaURL(bad, undefined, "tok"), bad).toBe("");
    }
    expect(mediaURL("http://127.0.0.1:3000/api/documents/media/abc", "m-1", "tok")).toMatch(/\/api\/v1\/lark\/raw\/documents\/media\/abc\?manager=m-1&token=tok$/);
    expect(mediaURL("https://example.com/pic.png", undefined, "tok")).toBe("https://example.com/pic.png");

    const md = [
      "![bad1](http://127.0.0.1:3000/api/../../admin)",
      "![bad2](/api/v1/lark/raw/..%2fadmin)",
      "![bad3](/api/v1/lark/raw/..#frag)",
      "[bad4](/api/v1/lark/raw/%252e%252e/admin)",
      "![ok](<http://127.0.0.1:3000/api/documents/media/abc?x=1>)",
    ].join("\n");
    const out = prepareMarkdown(md, "", "secret-tok");
    expect(out).not.toContain("admin");
    expect(out).not.toContain("#frag");
    expect(out).toContain("![ok](</api/v1/lark/raw/documents/media/abc?x=1&token=secret-tok>)");
    expect(out.match(/secret-tok/g)).toHaveLength(1);
  });

  it("drops unsafe and untrusted /api/ hrefs across DocTree nodes while keeping file token downloads", async () => {
    const doc: DocNode = {
      type: "doc",
      content: [
        { type: "image", attrs: { src: "javascript:alert(1)" } },
        { type: "bookmark", attrs: { href: "javascript:alert(1)", title: "Bad bookmark" } },
        { type: "bookmark", attrs: { href: "/api/v1/settings", title: "API bookmark" } },
        { type: "embedLink", attrs: { href: "https:/admin", title: "Bad embed" } },
        { type: "unsupported", attrs: { sourceUrl: "http:bad" } },
        { type: "file", attrs: { token: "..", name: "Traversal file" } },
        { type: "file", attrs: { token: "file_tok_1", name: "Valid file" } },
        {
          type: "paragraph",
          content: [
            { type: "mention", attrs: { kind: "doc", label: "Bad mention", href: "javascript:alert(1)" } },
            { type: "mention", attrs: { kind: "doc", label: "API mention", href: "/api/v1/settings" } },
            { type: "text", text: "Bad mark", marks: [{ type: "link", attrs: { href: "https:/admin" } }] },
            { type: "text", text: "API mark", marks: [{ type: "link", attrs: { href: "/api/v1/settings" } }] },
            { type: "text", text: "Good mark", marks: [{ type: "link", attrs: { href: "https://example.com/ok" } }] },
          ],
        },
      ],
    };
    await act(async () => root.render(<DocTree doc={doc} />));
    const anchors = [...host.querySelectorAll("a")];
    expect(anchors.map((a) => a.getAttribute("href"))).toEqual([
      expect.stringContaining("/api/v1/lark/raw/documents/drive/files/file_tok_1/download"),
      "https://example.com/ok",
    ]);
    expect(host.querySelectorAll("img")).toHaveLength(0);
    expect(host.textContent).toContain("Traversal file");
    expect(host.textContent).toContain("API bookmark");
  });

  it("refuses unsafe and /api/ hrefs in DocComments replies while preserving safe links", async () => {
    await act(async () =>
      root.render(
        <DocCommentsView
          url="https://example.com/doc"
          writes={false}
          resolved={false}
          onResolved={() => {}}
          onBack={() => {}}
          focus={0}
          model={{
            threads: [
              {
                id: "t1",
                whole: true,
                quote: "",
                resolved: false,
                replies: [
                  {
                    id: "r1",
                    author: { name: "Alice" },
                    createdAt: "2026-01-01T00:00:00Z",
                    content: [
                      { kind: "text", text: "Bad", href: "javascript:alert(1)" },
                      { kind: "text", text: "Rel", href: "https:/admin" },
                      { kind: "text", text: "Api", href: "/api/v1/settings" },
                      { kind: "text", text: "Good", href: "https://example.com/doc" },
                    ],
                  },
                ],
              },
            ],
            loadError: "",
            reload: () => {},
            target: { kind: "document" },
            setTarget: () => {},
            draft: "",
            setDraft: () => {},
            pickPerson: () => {},
            sending: false,
            resolving: new Set(),
            send: async () => {},
            resolve: async () => {},
          }}
        />,
      ),
    );
    const anchors = [...host.querySelectorAll("a")];
    expect(anchors).toHaveLength(1);
    expect(anchors[0].getAttribute("href")).toBe("https://example.com/doc");
    expect(host.textContent).toContain("Bad");
    expect(host.textContent).toContain("Rel");
    expect(host.textContent).toContain("Api");
  });
});

describe("Lark media, LarkExtras, profile, OAuth leaveFor, extension homepage, and task repo bindings", () => {
  it("guards openMedia and safeMediaURL against unsafe schemes, blob:, and traversal", () => {
    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
    for (const bad of [
      "javascript:alert(1)",
      "https:/admin",
      "https://u:p@evil.example/a.png",
      "blob:https://hub.example/x",
      "/api/../admin",
      "/api/v1/lark/media/../../admin",
      "/api/v1/lark/media/%252e%252e/admin",
      "//evil.example/api/x",
    ]) {
      expect(safeMediaURL(bad)).toBeUndefined();
      openMedia(bad);
    }
    expect(openSpy).not.toHaveBeenCalled();

    openMedia("/api/v1/lark/media/img_1?token=k");
    expect(openSpy).toHaveBeenCalledExactlyOnceWith("/api/v1/lark/media/img_1?token=k", "_blank", "noopener,noreferrer");
  });

  it("blocks traversal and unsafe URLs in LarkExtras FilePart, VoicePart, VideoPart, and EmojiImage", async () => {
    await act(async () =>
      root.render(
        <div>
          <FilePart p={{ kind: "file", text: "bad.pdf", url: "/api/v1/lark/media/../../admin" }} />
          <FilePart p={{ kind: "file", text: "ok.pdf", url: "/api/v1/lark/media/file_1" }} />
          <VoicePart p={{ kind: "media", url: "/api/v1/lark/media/%252e%252e/admin" }} />
          <VideoPart p={{ kind: "media", url: "/api/v1/lark/media/..%2fadmin", metadata: { posterUrl: "/api/v1/lark/media/../../admin" } }} />
          <EmojiImage name="bad_rel" size={16} catalog={{ groups: [], emojis: { bad_rel: { url: "https:/admin", label: "bad" } } }} />
          <EmojiImage name="bad_api" size={16} catalog={{ groups: [], emojis: { bad_api: { url: "/api/v1/lark/../../admin", label: "bad2" } } }} />
          <EmojiImage name="good" size={16} catalog={{ groups: [], emojis: { good: { url: "https://example.com/e.png", label: "ok" } } }} />
        </div>,
      ),
    );
    const fileLinks = [...host.querySelectorAll("a")];
    expect(fileLinks).toHaveLength(1);
    expect(fileLinks[0].getAttribute("href")).toMatch(/^\/api\/v1\/lark\/media\/file_1/);
    const buttons = [...host.querySelectorAll("button")];
    expect(buttons.every((b) => b.disabled)).toBe(true);
    const imgs = [...host.querySelectorAll("img")];
    expect(imgs).toHaveLength(1);
    expect(imgs[0].getAttribute("src")).toBe("https://example.com/e.png");
  });

  it("rejects single-slash relative and credentialed URLs in profileRows and cardButtons icons", () => {
    const rows = profileRows(
      {
        fields: [
          { key: "a", label: "Bad1", value: "v1", url: "https:/admin" },
          { key: "b", label: "Bad2", value: "v2", url: "https://user:pw@evil.example" },
          { key: "c", label: "Good", value: "v3", url: "https://example.com/u" },
        ],
      },
      {},
    );
    expect(rows.map((r) => r.url)).toEqual([undefined, undefined, "https://example.com/u"]);

    const btns = cardButtons({
      body: {
        children: [
          { tag: "button", url: "https://example.com/a", children: [{ tag: "custom_icon", image: { url: "https:/admin" } }, { tag: "plain_text", text: "A" }] },
          { tag: "button", url: "https://example.com/b", children: [{ tag: "custom_icon", image: { url: "/api/v1/lark/../../admin" } }, { tag: "plain_text", text: "B" }] },
          { tag: "button", url: "https://example.com/c", children: [{ tag: "custom_icon", image: { url: "https://example.com/icon.png" } }, { tag: "plain_text", text: "C" }] },
        ],
      },
    });
    expect(btns.map((b) => b.icon?.image)).toEqual([undefined, undefined, "https://example.com/icon.png"]);
  });

  it("refuses non-https, relative, and credentialed URLs in leaveFor", () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign });
    try {
      for (const bad of ["javascript:alert(1)", "http://example.com/auth", "https:/admin", "https://user:pw@evil.example/auth"]) {
        leaveFor(bad);
      }
      expect(assign).not.toHaveBeenCalled();

      leaveFor("https://accounts.example.com/authorize?state=ok");
      expect(assign).toHaveBeenCalledExactlyOnceWith("https://accounts.example.com/authorize?state=ok");
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("rejects single-slash https:/admin and userinfo homepages in ExtensionSettingsPage Information", async () => {
    const baseDetail = {
      id: "ext-1",
      name: "Demo",
      version: "1.0.0",
      source: "installed",
      origin: "market:official",
    } as HubExtensionDetail;

    for (const bad of ["https:/admin", "https://user:pw@evil.example", "http://example.com", "javascript:alert(1)"]) {
      await act(async () => root.render(<Information d={{ ...baseDetail, homepage: bad }} />));
      expect(host.querySelector("a")).toBeNull();
    }
    await act(async () => root.render(<Information d={{ ...baseDetail, homepage: "https://example.com/ext" }} />));
    expect(host.querySelector("a")?.getAttribute("href")).toBe("https://example.com/ext");
  });

  it("omits unsafe blocking_pr and repository URLs in TodoRepositoryBindings", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({
      revision: 1,
      repositories: [
        {
          repo_id: "r1",
          role: "target",
          source_snapshot: {
            identity: { provider: "github", instance_url: "https://github.com", path: "org/repo", canonical_url: "javascript:alert(1)" },
          },
          links: [],
        },
      ],
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    try {
      await act(async () =>
        root.render(
          <TodoRepositoryBindings
            manager="m1"
            owner="o1"
            todoId="t-1"
            version={1}
            todosV2
            repoGate={{
              state: "blocked",
              ready_count: 0,
              target_count: 1,
              reference_count: 0,
              blocking_repo: "org/repo",
              blocking_pr: "javascript:alert(1)",
            }}
          />,
        ),
      );
      for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
      expect(host.querySelector('[data-testid="todo-detail-repo-gate-pr"]')).toBeNull();
      expect(host.querySelector('a[href^="javascript:"]')).toBeNull();
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
