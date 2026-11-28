import { describe, expect, it } from "vitest";
import { cspFor, previewDocument, relativeRefs, sandboxFor } from "./html-preview";

describe("HTML preview policy", () => {
  it("keeps scripts and network off by default", () => {
    const off = { scripts: false, network: false };
    expect(sandboxFor(off)).toBe("allow-popups allow-popups-to-escape-sandbox");
    expect(cspFor(off)).toContain("default-src 'none'");
    expect(cspFor(off)).toContain("script-src 'none'");
    expect(sandboxFor({ scripts: true, network: false })).toContain("allow-scripts");
    expect(cspFor({ scripts: true, network: false })).toContain("script-src 'unsafe-inline'");
    expect(cspFor({ scripts: true, network: false })).toContain("default-src 'none'"); // inline scripts, still no network
    expect(cspFor({ scripts: false, network: true })).toBe("");
  });

  it.each([
    { scripts: false, network: false },
    { scripts: true, network: false },
    { scripts: false, network: true },
    { scripts: true, network: true },
  ])("keeps rendered HTML opaque with scripts=$scripts and network=$network", (policy) => {
    expect(sandboxFor(policy)).not.toContain("allow-same-origin");
  });

  it("puts the policy first, after the doctype", () => {
    const doc = previewDocument("<!DOCTYPE html><html><head><script src=x.js></script></head></html>", { scripts: false, network: false });
    expect(doc.startsWith('<!DOCTYPE html><meta http-equiv="Content-Security-Policy"')).toBe(true);
    expect(doc).toContain('<base target="_blank"><html>');
    expect(previewDocument("<p>hi</p>", { scripts: false, network: true })).toBe('<base target="_blank"><p>hi</p>');
  });

  it("counts references to files beside the page", () => {
    expect(relativeRefs(`<img src="a.png"><link href='style.css'><a href="https://x.dev">x</a><img src="data:image/png;base64,AA"><a href="#top">t</a><script src=//cdn/x.js></script>`)).toBe(2);
  });
});

describe("files beside the page", () => {
  it("knows what is relative and where it points", async () => {
    const { isRelativeRef, resolveRelative } = await import("./html-preview");
    expect(["a.png", "./css/s.css", "../img/x.png"].every(isRelativeRef)).toBe(true);
    expect(["https://x", "data:image/png;base64,", "#top", "/abs.png", "//cdn/x.js", "mailto:a@b", ""].some(isRelativeRef)).toBe(false);
    expect(resolveRelative("/Users/me/site", "img/../css/a%20b.css?v=2#x")).toBe("/Users/me/site/css/a b.css");
    expect(resolveRelative("/Users/me/site/pages", "../../logo.png")).toBe("/Users/me/logo.png");
  });

  it("rewrites url() and finds @import in style sheets", async () => {
    const { rewriteCssUrls, cssImports } = await import("./html-preview");
    expect(rewriteCssUrls(`a{background:url('bg.png')} b{src:url(data:x)} c{x:url( "f.woff2" )}`, (u) => (u.startsWith("data:") ? null : `R(${u})`))).toBe(
      `a{background:url("R(bg.png)")} b{src:url(data:x)} c{x:url("R(f.woff2)")}`,
    );
    expect(cssImports(`@import "base.css"; @import url(theme.css); a{}`)).toEqual(["base.css", "theme.css"]);
  });
});
