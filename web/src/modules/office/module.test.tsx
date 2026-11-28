// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import JSZip from "jszip";
import { expect, it, vi } from "vitest";
import { office } from "./module";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

it("falls back to the embedded preview when the workbook reader cannot open a Numbers file", async () => {
  const zip = new JSZip();
  zip.file("QuickLook/Preview.pdf", "%PDF-1.4");
  const data = await zip.generateAsync({ type: "base64" });
  const { FilePreview } = await office.load();
  if (!FilePreview) throw new Error("missing file preview");
  const host = document.createElement("div");
  const root = createRoot(host);
  vi.stubGlobal("Worker", class {
    onmessage?: (event: { data: { error: string } }) => void;
    postMessage() { this.onmessage?.({ data: { error: "unsupported workbook" } }); }
    terminate() {}
  });
  const create = vi.fn(() => "blob:numbers-preview");
  const revoke = vi.fn();
  vi.stubGlobal("URL", class extends URL { static createObjectURL = create; static revokeObjectURL = revoke; });
  try {
    const file = { name: "Budget.numbers", path: "/Budget.numbers", size: atob(data).length, data, is_dir: false, truncated: false };
    await act(async () => root.render(<FilePreview mid="demo" file={file} onDownload={() => {}} />));
    expect(host.querySelector("iframe")?.getAttribute("src")).toBe("blob:numbers-preview");
    expect(create).toHaveBeenCalledOnce();
  } finally {
    act(() => root.unmount());
    vi.unstubAllGlobals();
  }
  expect(revoke).toHaveBeenCalledWith("blob:numbers-preview");
});

it("tameLinks keeps canonical credential-free http(s) URLs and strips unsafe or userinfo links", async () => {
  const { tameLinks } = await import("./OfficeView");
  const root = document.createElement("div");
  root.innerHTML =
    '<a id="ok" href="https://example.com/doc?x=1">ok</a>' +
    '<a id="cred" href="https://user:pass@evil.example/doc">cred</a>' +
    '<a id="bs" href="https://evil.example\\@trusted.example/doc">bs</a>' +
    '<a id="js" href="javascript:alert(1)" target="_top" rel="opener">js</a>';
  tameLinks(root);
  const ok = root.querySelector("#ok")!;
  expect(ok.getAttribute("href")).toBe("https://example.com/doc?x=1");
  expect(ok.getAttribute("target")).toBe("_blank");
  expect(ok.getAttribute("rel")).toBe("noopener noreferrer");
  for (const id of ["#cred", "#bs", "#js"]) {
    const el = root.querySelector(id)!;
    expect(el.hasAttribute("href"), id).toBe(false);
    expect(el.hasAttribute("target"), id).toBe(false);
    expect(el.hasAttribute("rel"), id).toBe(false);
  }
});
