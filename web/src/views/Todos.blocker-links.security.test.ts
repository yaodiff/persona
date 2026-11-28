import { describe, expect, it } from "vitest";
import { activeBlockerLink, validateExternalPRUrl } from "./Todos";

describe("Todos external blocker URL helpers", () => {
  it("accepts credential-free https PR/MR links and rejects http, userinfo, backslash, and single-slash URLs", () => {
    expect(activeBlockerLink("https://example.com/org/repo/pull/42")).toEqual({
      url: "https://example.com/org/repo/pull/42",
      label: "org/repo#42",
    });
    expect(validateExternalPRUrl("https://example.com/org/repo/-/merge_requests/9")).toBe(
      "https://example.com/org/repo/-/merge_requests/9",
    );

    for (const bad of [
      "http://example.com/org/repo/pull/42",
      "https://user:pass@example.com/org/repo/pull/42",
      "https://evil.example\\@example.com/org/repo/pull/42",
      "https:/org/repo/pull/42",
      "javascript:alert(1)",
    ]) {
      expect(activeBlockerLink(bad), bad).toBeNull();
      expect(validateExternalPRUrl(bad), bad).toBeNull();
    }
  });
});
