import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { SURFACE_MS } from "../../../lib/terminal-mode";
import { ModeToggle } from "./ModeToggle";
import { Surface } from "./Surface";

const html = (el: React.ReactElement) => renderToStaticMarkup(el);

describe("a surface of the terminal panel", () => {
  it("is there from the start without moving when nothing replaced it", () => {
    const h = html(
      <Surface leaving={false} enter={false} dir={1}>
        x
      </Surface>,
    );
    expect(h).toContain('data-surface="shown"');
    expect(h).toContain("opacity:1");
    expect(h).toContain("transform:none"); // no transform at rest: it would hold fixed-position menus inside the box
    expect(h).not.toContain("inert");
  });
  it("starts out of sight on the side it comes from when it replaced another", () => {
    const h = html(
      <Surface leaving={false} enter dir={1}>
        x
      </Surface>,
    );
    expect(h).toContain('data-surface="entering"');
    expect(h).toContain("opacity:0");
    expect(h).toContain("translateX(14px)");
    expect(h).toContain(`opacity ${SURFACE_MS}ms var(--ease-out-strong)`);
  });
  it("goes out the other way, unclickable and unfocusable", () => {
    const h = html(
      <Surface leaving enter={false} dir={1}>
        x
      </Surface>,
    );
    expect(h).toContain('data-surface="leaving"');
    expect(h).toContain("translateX(-14px)");
    expect(h).toContain("pointer-events:none");
    expect(h).toContain("inert");
    expect(h).toContain('aria-hidden="true"');
  });
});

describe("the switch in the panel's header", () => {
  it("is a radio group of the two modes with the shown one checked, words for a wide panel and icons for a narrow one", () => {
    const h = html(<ModeToggle mode="full" onChange={() => {}} />);
    expect(h).toContain('role="radiogroup"');
    expect(h).toContain('aria-label="终端界面"');
    expect(h).toMatch(/aria-checked="false"[^>]*aria-label="单个终端"/);
    expect(h).toMatch(/aria-checked="true"[^>]*aria-label="完整界面"/);
    expect(h).toContain(">单个终端</span>");
    expect(h).toContain(">完整界面</span>");
    expect(h).toContain("@md:hidden"); // the icon, where the words do not fit
    expect(h).toContain("whitespace-nowrap"); // a button's label is one line
  });
});
