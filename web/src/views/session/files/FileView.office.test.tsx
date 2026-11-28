// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { connectModuleHub, registerModule, resetModulesForTest } from "../../../core/module-host";
import { forgetModuleSwitches, setModuleSwitchedOff } from "../../../core/module-switch";
import { initI18n, t } from "../../../i18n";
import { MODULES } from "../../../modules";
import { FileView } from "./FileView";

vi.hoisted(() => {
  window.matchMedia ??= ((media: string) => ({ matches: false, media, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia;
});
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const names = ["proposal.docx", "budget.xlsx", "slides.pptx", "accounts.numbers"];
const chunk = vi.fn();
const catalog = vi.fn();
const requested: string[] = [];
const coreOnly = process.env.PERSONA_WEB_MODULES === "none";
let features: string[];
let root: Root;
let host: HTMLDivElement;

function Folder() {
  const [path, open] = useState("/work");
  return <FileView mid="demo" path={path} onClose={() => open("/work")} onOpenPath={open} />;
}

beforeEach(async () => {
  localStorage.clear();
  forgetModuleSwitches();
  resetModulesForTest();
  features = ["modules", "module.office"];
  connectModuleHub(() => ({ loaded: true, features }), () => () => {});
  await initI18n("en"); // start with just the core catalogs, as a fresh browser does
  chunk.mockClear();
  catalog.mockClear();
  requested.length = 0;
  const office = MODULES.find((d) => d.id === "office");
  if (office) {
    registerModule({ ...office, locales: { en: () => { catalog(); return office.locales!.en(); } }, load: () => { chunk(); return office.load(); } });
  }
  vi.stubGlobal("fetch", vi.fn(async (input: string) => {
    const url = new URL(input, "http://localhost");
    requested.push(url.pathname);
    const path = url.searchParams.get("path");
    const body = url.pathname.endsWith("/convert") ? { path: "/preview.pdf" } : path === "/work"
      ? { path, name: "work", is_dir: true, entries: names }
      : { name: path?.split("/").pop(), path, stream: true, size: 1200 };
    return Response.json(body);
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const button = (text: string) => {
  const found = [...host.querySelectorAll("button")].find((b) => b.textContent === text);
  if (!found) throw new Error(`Missing button "${text}"`);
  return found;
};

it.each(coreOnly ? ["core-only build"] : ["hub off", "local switch off"])("%s lists every Office file and downloads the original without loading previews", async (mode) => {
  if (coreOnly) expect(MODULES).toHaveLength(0);
  if (mode === "hub off") features = ["modules"];
  if (mode === "local switch off") setModuleSwitchedOff("office", true);
  const downloads: [string, string | null][] = [];
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
    downloads.push([this.download, new URL(this.href).searchParams.get("path")]);
  });
  for (const name of names) {
    await act(async () => root.render(<Folder key={name} />));
    for (const entry of names) expect(button(entry)).toBeTruthy();
    await act(async () => button(name).click());
    expect(host.textContent).toContain(name);
    expect(host.textContent).toContain("No preview");
    await act(async () => button("Download").click());
    expect(downloads.at(-1)).toEqual([name, `/work/${name}`]);
  }
  expect(requested.every((url) => url.endsWith("/file"))).toBe(true);
  expect(chunk).not.toHaveBeenCalled();
  expect(catalog).not.toHaveBeenCalled();
  expect(t("office.opening")).toBe("office.opening");
});

it.skipIf(coreOnly)("loads the renderer and its language only when an Office file opens, then reacts to the switch", async () => {
  await act(async () => root.render(<Folder />));
  expect(chunk).not.toHaveBeenCalled();
  expect(catalog).not.toHaveBeenCalled();
  await act(async () => root.render(<FileView mid="demo" path="/work/notes.odt" onClose={() => {}} onOpenPath={() => {}} />));
  await vi.waitFor(async () => {
    await act(async () => { await vi.dynamicImportSettled(); });
    expect(host.querySelector("iframe")?.title).toBe("notes.odt");
  });
  expect(host.textContent).toContain("PDF converted by LibreOffice");
  expect(chunk).toHaveBeenCalledTimes(1);
  expect(catalog).toHaveBeenCalledTimes(1);
  act(() => { setModuleSwitchedOff("office", true); });
  expect(host.querySelector("iframe")).toBeNull();
  expect(host.textContent).toContain("No preview");
});
