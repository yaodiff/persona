import assert from "node:assert/strict";
import test from "node:test";
import { homedir } from "node:os";
import { join } from "node:path";
import { personaLinkExecutable } from "../src/link.js";

const name = process.platform === "win32" ? "persona-link.exe" : "persona-link";

test("the connector is a fixed separate installation, not a renderer-chosen executable", () => {
  assert.equal(personaLinkExecutable(join("home", "example")), join("home", "example", ".persona", "bin", name));
});

test("the connector is looked up in the person's home directory on every platform", () => {
  // Windows has no HOME variable by default; an empty home would make the path relative to the working directory.
  assert.equal(personaLinkExecutable(), join(homedir(), ".persona", "bin", name));
  assert.notEqual(homedir(), "");
});
