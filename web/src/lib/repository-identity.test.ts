import { expect, it } from "vitest";
import fixtures from "../../../spec/fixtures/repository-remotes.json";
import { parseRepository } from "./repository-identity";

// Prefix + URL keeps Git transport users separate from email-like fixture text.
for (const { url, provider, identity, prefix = "" } of fixtures.valid) {
  it(`normalizes ${url}`, () => {
    expect(parseRepository(prefix + url, provider)).toEqual(identity);
    expect(parseRepository(identity.canonical_url, identity.provider)).toEqual(identity);
  });
}
for (const { url, provider, prefix = "" } of fixtures.invalid) {
  it(`refuses ${JSON.stringify(url)}`, () => expect(parseRepository(prefix + url, provider)).toBeUndefined());
}
