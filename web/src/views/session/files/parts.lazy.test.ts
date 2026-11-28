/// <reference types="node" />
// The app shell (App.tsx) shows Empty and the palette's file results (an idle-tier page) show FileGlyph. Each lives in a
// file of its own, so neither carries the rest of parts.tsx (the back header, Git badges, Segmented, useWindow and the
// session-files-parts words): a runtime import of parts.tsx from App.tsx put all of it on every first screen (#17321).
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import startup from "../../../i18n/startup-namespaces.json";
import { Empty } from "./empty";
import { FileGlyph } from "./file-glyph";
import * as parts from "./parts";

const read = (file: string) => readFileSync(new URL(file, import.meta.url), "utf8");
const runtimeImportOf = (spec: string) => new RegExp(`^import\\s+(?!type\\b)[^;]*from\\s+["']${spec}["']`, "m");

describe("files panel parts", () => {
  it("keeps parts.tsx off the first screen and the palette", () => {
    expect(read("../../../App.tsx")).not.toMatch(runtimeImportOf("\\./views/session/files/parts"));
    expect(read("../../palette/FilesSection.tsx")).not.toMatch(runtimeImportOf("\\.\\./session/files/parts"));
    expect(read("./empty.tsx")).not.toMatch(runtimeImportOf("\\./parts"));
    expect(read("./file-glyph.tsx")).not.toMatch(runtimeImportOf("\\./parts"));
    expect(startup.filter((key) => key === "session-files-parts" || key.startsWith("session-files-parts."))).toEqual([]);
  });

  it("re-exports the same components from parts.tsx", () => {
    expect(parts.Empty).toBe(Empty);
    expect(parts.FileGlyph).toBe(FileGlyph);
  });
});
