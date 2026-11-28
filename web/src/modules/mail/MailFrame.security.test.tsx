// @vitest-environment jsdom
import "./test-register";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { InboxItem } from "../../lib/inbox";
import type { MailMessage } from "./mail";

const openLink = vi.hoisted(() => vi.fn());
vi.mock("../../lib/links", async (orig) => ({
  ...(await orig<typeof import("../../lib/links")>()),
  openLink,
}));

const answers = new Map<string, (v: unknown) => void>();
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: vi.fn((path: string) => {
    if (path === "/mail/accounts") return Promise.resolve({ accounts: [{ id: "acc", email: "me@example.com" }] });
    if (path.includes("/flags")) return Promise.resolve({});
    return new Promise((resolve) => answers.set(path.replace(/\?.*$/, ""), resolve));
  }),
}));
vi.mock("../../lib/translate", async (orig) => ({
  ...(await orig<typeof import("../../lib/translate")>()),
  useTranslatePrefs: () => ({ enabled: false, target: "zh-Hans" }),
}));

import { buildMailSrcDoc, FullMailBody, safeMailHref } from "./FullMailBody";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const item = (): InboxItem =>
  ({
    id: "mail:acc:sec:1",
    source: "mail",
    kind: "mail",
    account: "acc",
    title: "Security check",
    count: 1,
    tier: "action",
    version: "v1",
    read: true,
    mail: { account_id: "acc", mailbox: "INBOX", uids: [1], from: { email: "sender@example.com" } },
  }) as InboxItem;

const mail = (html: string): MailMessage =>
  ({
    uid: 1,
    subject: "Security check",
    from: { email: "sender@example.com" },
    html,
    message_id: "<sec@example.com>",
  }) as MailMessage;

let host: HTMLDivElement;
let root: Root;
const realResizeObserver = globalThis.ResizeObserver;

beforeEach(() => {
  answers.clear();
  openLink.mockReset();
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  globalThis.ResizeObserver = realResizeObserver;
  vi.restoreAllMocks();
});

describe("MailFrame HTML isolation and link hardening", () => {
  it("adds strict CSP and no-referrer, strips dangerous tags, and blocks same-origin or unsafe subresources", () => {
    const raw = `
      <html><head>
        <meta http-equiv="refresh" content="0;url=https://evil.example/phish">
        <meta name="referrer" content="unsafe-url">
        <base href="https://evil.example/">
        <link rel="stylesheet" href="/api/v1/secret.css">
        <style>
          @import url("https://evil.example/track.css");
          .rel { background-image: url("/api/v1/logout"); }
          .same { background-image: url("https://hub.example/api/v1/me"); }
          .esc { background-image: \\75 rl("/api/v1/escaped"); }
          .ext { background-image: url("https://cdn.example/banner.png"); }
        </style>
        <script>window.pwned = true;</script>
      </head>
      <body style="background-image: url('/api/v1/body')">
        <iframe src="https://evil.example"></iframe>
        <img id="rel" src="/api/v1/secret.png" srcset="/api/v1/1x.png 1x, https://cdn.example/2x.png 2x" onerror="alert(1)">
        <img id="same" src="https://hub.example/api/v1/secret.png">
        <img id="svg" src="data:image/svg+xml,<svg onload=alert(1)>">
        <img id="ok-ext" src="https://cdn.example/photo.jpg">
        <img id="ok-data" src="data:image/png;base64,iVBORw0KGgo=">
        <img id="ok-cid" src="cid:inline-1@example.com">
        <a id="good" href="https://example.com/path?a=1" rel="opener" target="_self">good</a>
        <a id="mail" href="mailto:user@example.com" rel="opener" target="_blank">mail</a>
        <a id="hash" href="#section-2">section</a>
        <a id="js" href="javascript:alert(1)">js</a>
        <a id="cred" href="https://user:pass@evil.example/login">cred</a>
        <a id="slash" href="https:/admin">slash</a>
        <a id="rel-link" href="/api/v1/secret">rel-link</a>
        <a id="bad-mail" href="mailto://user@evil.example">bad-mail</a>
      </body></html>
    `;
    const srcDoc = buildMailSrcDoc(raw, "https://hub.example");
    const parsed = new DOMParser().parseFromString(srcDoc, "text/html");

    const csp = parsed.head.querySelector('meta[http-equiv="Content-Security-Policy"]')?.getAttribute("content") ?? "";
    expect(csp).toContain("default-src 'none'");
    expect(csp).toContain("base-uri 'none'");
    expect(csp).toContain("form-action 'none'");
    expect(parsed.head.querySelector('meta[name="referrer"]')?.getAttribute("content")).toBe("no-referrer");

    expect(parsed.querySelector('meta[http-equiv="refresh"]')).toBeNull();
    expect(parsed.querySelector("base")).toBeNull();
    expect(parsed.querySelector("link")).toBeNull();
    expect(parsed.querySelector("script")).toBeNull();
    expect(parsed.querySelector("iframe")).toBeNull();

    const styleText = [...parsed.head.querySelectorAll("style")].map((s) => s.textContent).join("\n");
    expect(styleText).not.toContain("@import");
    expect(styleText).not.toContain("/api/v1/");
    expect(styleText).not.toContain("hub.example");
    expect(styleText).toContain("https://cdn.example/banner.png");
    expect(parsed.body.getAttribute("style") ?? "").not.toContain("/api/v1/");

    const relImg = parsed.querySelector("#rel")!;
    expect(relImg.hasAttribute("src")).toBe(false);
    expect(relImg.hasAttribute("onerror")).toBe(false);
    expect(relImg.getAttribute("srcset")).toBe("https://cdn.example/2x.png 2x");
    expect(parsed.querySelector("#same")!.hasAttribute("src")).toBe(false);
    expect(parsed.querySelector("#svg")!.hasAttribute("src")).toBe(false);
    expect(parsed.querySelector("#ok-ext")!.getAttribute("src")).toBe("https://cdn.example/photo.jpg");
    expect(parsed.querySelector("#ok-data")!.getAttribute("src")).toBe("data:image/png;base64,iVBORw0KGgo=");
    expect(parsed.querySelector("#ok-cid")!.getAttribute("src")).toBe("cid:inline-1@example.com");

    const good = parsed.querySelector("#good")!;
    expect(good.getAttribute("href")).toBe("https://example.com/path?a=1");
    expect(good.getAttribute("target")).toBe("_blank");
    expect(good.getAttribute("rel")).toBe("noopener noreferrer");

    const mailLink = parsed.querySelector("#mail")!;
    expect(mailLink.getAttribute("href")).toBe("mailto:user@example.com");
    expect(mailLink.hasAttribute("target")).toBe(false);
    expect(mailLink.getAttribute("rel")).toBe("noopener noreferrer");

    expect(parsed.querySelector("#hash")!.getAttribute("href")).toBe("#section-2");
    for (const id of ["#js", "#cred", "#slash", "#rel-link", "#bad-mail"]) {
      expect(parsed.querySelector(id)!.hasAttribute("href"), id).toBe(false);
    }
  });

  it("sets referrerPolicy=no-referrer and validates both click and middle-click (auxclick)", async () => {
    act(() => root.render(<FullMailBody item={item()} />));
    await act(async () => {
      answers.get("/mail/messages/acc/1")!({ message: mail('<p><a id="ok" href="https://example.com/doc">doc</a></p>') });
    });

    const iframe = host.querySelector<HTMLIFrameElement>("iframe")!;
    expect(iframe.getAttribute("referrerpolicy")).toBe("no-referrer");

    // Populate the jsdom iframe document so click/auxclick listeners fire against real anchors.
    const doc = iframe.contentDocument!;
    doc.body.innerHTML =
      '<a id="ok" href="https://example.com/doc">doc</a>' +
      '<a id="bad" href="https://user:pass@evil.example/doc">bad</a>' +
      '<a id="rel" href="/api/v1/secret">rel</a>';
    act(() => iframe.dispatchEvent(new Event("load")));

    doc.querySelector("#ok")!.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 }));
    expect(openLink).toHaveBeenCalledTimes(1);
    expect(openLink).toHaveBeenLastCalledWith("https://example.com/doc", undefined, undefined);

    doc.querySelector("#ok")!.dispatchEvent(new MouseEvent("auxclick", { bubbles: true, cancelable: true, button: 1 }));
    expect(openLink).toHaveBeenCalledTimes(2);

    // Unsafe hrefs are blocked even if injected dynamically into the document.
    doc.querySelector("#bad")!.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 }));
    doc.querySelector("#rel")!.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 }));
    expect(openLink).toHaveBeenCalledTimes(2);
    expect(safeMailHref("https://evil.example\\@trusted.example")).toBeUndefined();
    expect(safeMailHref("mailto:team@example.com?subject=hi")).toBe("mailto:team@example.com?subject=hi");

    // Nested child element click uses closest("a[href], area[href]"), reload replaces prior listeners, and unmount removes both click & auxclick listeners.
    const removeSpy = vi.spyOn(doc, "removeEventListener");
    act(() => iframe.dispatchEvent(new Event("load")));
    expect(removeSpy.mock.calls.some(([ev]) => ev === "click")).toBe(true);
    expect(removeSpy.mock.calls.some(([ev]) => ev === "auxclick")).toBe(true);
    removeSpy.mockClear();

    doc.body.innerHTML = '<a id="nest" href="https://example.com/nested"><span id="child">click</span></a>';
    doc.querySelector("#child")!.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 }));
    expect(openLink).toHaveBeenCalledTimes(3);
    expect(openLink).toHaveBeenLastCalledWith("https://example.com/nested", undefined, undefined);

    act(() => root.unmount());
    expect(removeSpy.mock.calls.some(([ev]) => ev === "click")).toBe(true);
    expect(removeSpy.mock.calls.some(([ev]) => ev === "auxclick")).toBe(true);
    const afterHost = doc.body ?? doc.documentElement;
    if (afterHost) {
      const afterLink = doc.createElement("a");
      afterLink.setAttribute("href", "https://example.com/after-unmount");
      afterHost.appendChild(afterLink);
      afterLink.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 }));
      afterLink.dispatchEvent(new MouseEvent("auxclick", { bubbles: true, cancelable: true, button: 1 }));
    }
    expect(openLink).toHaveBeenCalledTimes(3);
  });

  it("blocks Declarative Shadow DOM, CSS image-set/EOF-url/CRLF-hex bypasses, SVG SMIL/paint attrs, srcset data trailing comma, and legacy media attrs", () => {
    const raw = `
      <html><head>
        <style>
          .is1 { background-image: image-set("/api/v1/secret" 1x); }
          .is2 { background-image: -webkit-image-set("/api/v1/secret" 1x, "https://cdn.example/ok.png" 2x); }
          .is3 { background-image: image-set("https://cdn.example/ext.png" 1x); }
          .imgfn { background-image: image("/api/v1/secret", red); }
          .srcfn { background-image: src("/api/v1/secret"); }
          .crlf { background-image: \\75\r\nrl("/api/v1/crlf"); }
          .eof1 { background-image: url('/api/v1/eof-unclosed' }
          .eof2 { background-image: url(/api/v1/eof-bare
        </style>
        <style>.breakout { content: "</style><img src='/api/v1/breakout'>"; }</style>
      </head>
      <body>
        <template shadowrootmode="open"><img src="/api/v1/shadow"><style>div{background:url(/api/v1/shadow-css)}</style></template>
        <svg>
          <image id="svg-img" href="https://cdn.example/pic.png"><set attributeName="href" to="/api/v1/smil" begin="0s"/></image>
          <a id="svg-good" xlink:href="https://example.com/svg"><text>svg link</text><animate attributeName="href" to="javascript:alert(1)"/></a>
          <a id="svg-bad" xlink:href="javascript:alert(1)"><text>bad svg</text></a>
          <rect id="svg-cursor" cursor="url(/api/v1/cursor), auto" filter="url(/api/v1/filter)" fill="url(#grad1)"/>
        </svg>
        <img id="data-comma" srcset="data:image/png;base64,AA==, 2x">
        <img id="data-two-commas" srcset="data:image/png;base64,AA==,/api/v1/extra">
        <picture><source id="pic-src" srcset="/api/v1/s.png 1x, https://cdn.example/s.png 2x"></picture>
        <video id="vid" poster="/api/v1/poster.png"></video>
        <table><tr><td id="cell" background="/api/v1/bg.png"></td></tr></table>
        <form id="frm" action="/api/v1/submit"><input id="img-inp" type="image" src="/api/v1/btn.png"></form>
        <map name="m">
          <area id="area-ok" href="https://example.com/map" rel="opener">
          <area id="area-bad" href="javascript:alert(1)">
        </map>
        <a id="ctrl-hash" href="#bad\nanchor">ctrl</a>
      </body></html>
    `;
    const srcDoc = buildMailSrcDoc(raw, "https://hub.example/");
    const parsed = new DOMParser().parseFromString(srcDoc, "text/html");

    expect(parsed.querySelector("template")).toBeNull();
    expect(parsed.querySelector("set")).toBeNull();
    expect(parsed.querySelector("animate")).toBeNull();

    const styles = [...parsed.querySelectorAll("style")].map((s) => s.textContent ?? "").join("\n");
    expect(styles).not.toContain("/api/v1/");
    expect(styles).toContain("https://cdn.example/ext.png");
    expect(parsed.querySelector('img[src="/api/v1/breakout"]')).toBeNull();

    const rect = parsed.querySelector("#svg-cursor")!;
    expect(rect.getAttribute("cursor")).not.toContain("/api/v1/");
    expect(rect.getAttribute("filter")).not.toContain("/api/v1/");
    expect(rect.getAttribute("fill")).toBe('url("#grad1")');

    const svgGood = parsed.querySelector("#svg-good")!;
    expect(svgGood.getAttribute("href")).toBe("https://example.com/svg");
    expect(svgGood.getAttribute("rel")).toBe("noopener noreferrer");
    expect(svgGood.hasAttribute("xlink:href")).toBe(false);
    expect(parsed.querySelector("#svg-bad")!.hasAttribute("href")).toBe(false);

    expect(parsed.querySelector("#data-comma")!.hasAttribute("srcset")).toBe(false);
    expect(parsed.querySelector("#data-two-commas")!.hasAttribute("srcset")).toBe(false);
    expect(parsed.querySelector("#pic-src")!.getAttribute("srcset")).toBe("https://cdn.example/s.png 2x");
    expect(parsed.querySelector("#vid")!.hasAttribute("poster")).toBe(false);
    expect(parsed.querySelector("#cell")!.hasAttribute("background")).toBe(false);
    expect(parsed.querySelector("#frm")!.hasAttribute("action")).toBe(false);
    expect(parsed.querySelector("#img-inp")!.hasAttribute("src")).toBe(false);

    const areaOk = parsed.querySelector("#area-ok")!;
    expect(areaOk.getAttribute("href")).toBe("https://example.com/map");
    expect(areaOk.getAttribute("target")).toBe("_blank");
    expect(areaOk.getAttribute("rel")).toBe("noopener noreferrer");
    expect(parsed.querySelector("#area-bad")!.hasAttribute("href")).toBe(false);
    expect(parsed.querySelector("#ctrl-hash")!.hasAttribute("href")).toBe(false);
  });
});
