import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { Collapse } from "./atoms";

describe("Collapse", () => {
  it("runs the grid's row between 0fr and 1fr, inert while closed", () => {
    const closed = renderToStaticMarkup(<Collapse open={false}><p>trace</p></Collapse>);
    expect(closed).toContain("grid-template-rows:0fr");
    expect(closed).toContain("inert");
    expect(closed).toContain("trace"); // not lazy: always there
    const open = renderToStaticMarkup(<Collapse open><p>trace</p></Collapse>);
    expect(open).toContain("grid-template-rows:1fr");
    expect(open).not.toContain("inert");
  });

  it("mounts a lazy collapse's children only when open", () => {
    expect(renderToStaticMarkup(<Collapse open={false} lazy><p>trace</p></Collapse>)).not.toContain("trace");
    expect(renderToStaticMarkup(<Collapse open lazy><p>trace</p></Collapse>)).toContain("trace");
  });

  it("mounts a folded sidebar group's rows only when it opens, unless told to keep them", () => {
    // a folded group with 400 Agents kept every row mounted, laid out on every page switch
    expect(renderToStaticMarkup(<Collapse open={false} sidebarFold><p>Agent 7</p></Collapse>)).not.toContain("Agent 7");
    expect(renderToStaticMarkup(<Collapse open sidebarFold><p>Agent 7</p></Collapse>)).toContain("Agent 7");
    expect(renderToStaticMarkup(<Collapse open={false} sidebarFold lazy={false}><p>Agent 7</p></Collapse>)).toContain("Agent 7");
  });

  it("clips a sidebar fold at its outer grid and other folds at their inner track", () => {
    const boxes = (markup: string) => [...markup.matchAll(/<div class="([^"]*)"/g)].map((match) => match[1]);
    const clip = "overflow-hidden [contain:layout_paint]";

    const normal = boxes(renderToStaticMarkup(<Collapse open><p>trace</p></Collapse>));
    expect(normal[0]).not.toContain(clip);
    expect(normal[1]).toContain(clip);

    const sidebar = boxes(renderToStaticMarkup(<Collapse open={false} sidebarFold><p>trace</p></Collapse>));
    expect(sidebar[0]).toContain(clip);
    expect(sidebar[1]).not.toContain(clip);

    const unclipped = boxes(renderToStaticMarkup(<Collapse open unclipped><p>trace</p></Collapse>));
    expect(unclipped[0]).not.toContain(clip);
    expect(unclipped[1]).not.toContain(clip);
    const closingUnclipped = boxes(renderToStaticMarkup(<Collapse open={false} unclipped><p>trace</p></Collapse>));
    expect(closingUnclipped[0]).not.toContain(clip);
    expect(closingUnclipped[1]).toContain(clip);

    const sidebarUnclipped = boxes(renderToStaticMarkup(<Collapse open sidebarFold unclipped><p>trace</p></Collapse>));
    expect(sidebarUnclipped[0]).not.toContain(clip);
    expect(sidebarUnclipped[1]).not.toContain(clip);
  });

  it("opts sidebar folds into the direction tokens and leaves every default fold on the legacy motion", () => {
    const normal = renderToStaticMarkup(<Collapse open><p>trace</p></Collapse>);
    expect(normal).toContain("duration-300");
    expect(normal).not.toContain("--sidebar-fold");
    expect(renderToStaticMarkup(<Collapse open={false}><p>trace</p></Collapse>)).toContain("opacity:0");

    const opening = renderToStaticMarkup(<Collapse open sidebarFold><p>trace</p></Collapse>);
    expect(opening).toContain("transition-duration:var(--sidebar-fold-open)");
    expect(opening).toContain("transition-timing-function:var(--sidebar-fold-open-ease)");
    expect(opening).not.toContain("duration-300");
    expect(opening).not.toContain("transition-[grid-template-rows,opacity]");

    const closing = renderToStaticMarkup(<Collapse open={false} sidebarFold><p>trace</p></Collapse>);
    expect(closing).toContain("transition-duration:var(--sidebar-fold-close)");
    expect(closing).toContain("transition-timing-function:var(--sidebar-fold-close-ease)");
    // The height masks fully visible content instead of fading a partly open group into an empty gap.
    expect(closing).toContain("opacity:1");
  });
});
