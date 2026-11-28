// @vitest-environment jsdom
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { EvidenceLink } from "./EvidenceLink";

describe("EvidenceLink URL validation", () => {
  it("renders valid PR/MR and external links", () => {
    const pr = renderToStaticMarkup(<EvidenceLink todoId="t1" link="https://github.com/org/repo/pull/12" />);
    expect(pr).toContain('href="https://github.com/org/repo/pull/12"');
    expect(pr).toContain("PR #12");
    expect(pr).toContain('rel="noopener noreferrer"');
  });

  it("drops links with credentials, backslash authority confusion, single-slash paths, or non-http schemes", () => {
    for (const bad of [
      "https://user:pass@example.com/org/repo/pull/12",
      "https://evil.example\\@example.com/org/repo/pull/12",
      "https:/org/repo/pull/12",
      "javascript:alert(1)",
      "https://example.com/\nfoo",
    ]) {
      expect(renderToStaticMarkup(<EvidenceLink todoId="t1" link={bad} />), bad).toBe("");
    }
  });
});
