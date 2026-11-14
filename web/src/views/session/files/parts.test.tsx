// The empty state every page shares (Empty): its line under the title wraps between words, so an English sentence is
// never cut inside a word ("make sure t / he hub", web usability audit item 86); only a string too long for a line of
// its own (a path, an id) may break inside it. Chrome's line breaking itself is checked by web/e2e/shell-connection.mjs.
import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { Empty } from "./parts";

describe("Empty", () => {
  it("wraps its detail between words and breaks only an unbroken string that can't fit", () => {
    const html = renderToStaticMarkup(<Empty icon="hub" title="Offline" detail="Can't reach the hub. Check your network, or make sure the hub machine is on." />);
    const detail = /<div class="([^"]*)">Can&#x27;t reach the hub/.exec(html)?.[1].split(" ") ?? [];
    expect(detail).toContain("[overflow-wrap:anywhere]");
    expect(detail).not.toContain("break-all");
    expect(detail).not.toContain("break-words");
  });
});
