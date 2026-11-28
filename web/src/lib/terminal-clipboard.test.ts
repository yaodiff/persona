import { describe, expect, it, vi } from "vitest";
import {
  MAX_TERMINAL_UPLOAD,
  OSC52_MAX_CHARS,
  copyNotice,
  copyWith,
  dragHasFiles,
  escapePastePath,
  osc52Text,
  pastedPaths,
  pasteVerdict,
  refuseUpload,
  transferFiles,
  uploadName,
  uploadTimeoutMs,
  uploadingText,
} from "./terminal-clipboard";

describe("escapePastePath", () => {
  it("leaves a plain path alone", () => {
    expect(escapePastePath("/home/me/.persona/uploads/terminal/2026-11-04/ab12cd34-shot.png")).toBe(
      "/home/me/.persona/uploads/terminal/2026-11-04/ab12cd34-shot.png",
    );
    expect(escapePastePath("/a/b_c-d.e@f%g+h=i:j,k")).toBe("/a/b_c-d.e@f%g+h=i:j,k");
  });

  it("puts a backslash before spaces and shell characters", () => {
    expect(escapePastePath("/tmp/Screen Shot 1.png")).toBe("/tmp/Screen\\ Shot\\ 1.png");
    expect(escapePastePath("/tmp/a'b\"c$d`e(f)g&h;i|j<k>l*m?n!o#p~q{r}s[t]u\\v")).toBe(
      "/tmp/a\\'b\\\"c\\$d\\`e\\(f\\)g\\&h\\;i\\|j\\<k\\>l\\*m\\?n\\!o\\#p\\~q\\{r\\}s\\[t\\]u\\\\v",
    );
  });

  it("keeps non-ASCII letters, as a shell takes them in a word", () => {
    expect(escapePastePath("/tmp/会议 纪要.png")).toBe("/tmp/会议\\ 纪要.png");
    expect(escapePastePath("/tmp/😀.png")).toBe("/tmp/😀.png");
  });

  it("escapes a newline and a tab so they cannot end the command", () => {
    expect(escapePastePath("/tmp/a\nb\tc")).toBe("/tmp/a\\\nb\\\tc");
  });
});

describe("pastedPaths", () => {
  it("joins with spaces and ends with one", () => {
    expect(pastedPaths(["/a/x.png"])).toBe("/a/x.png ");
    expect(pastedPaths(["/a/x y.png", "/b/z.png"])).toBe("/a/x\\ y.png /b/z.png ");
  });
});

describe("transferFiles", () => {
  const file = (name: string) => new File(["x"], name);

  it("takes files first", () => {
    const a = file("a.png");
    expect(transferFiles({ files: [a] as unknown as FileList, items: [] as unknown as DataTransferItemList })).toEqual([a]);
  });

  it("falls back to the file items, and skips text ones", () => {
    const b = file("b.png");
    const items = [
      { kind: "string", getAsFile: () => null },
      { kind: "file", getAsFile: () => b },
      { kind: "file", getAsFile: () => null },
    ] as unknown as DataTransferItemList;
    expect(transferFiles({ files: [] as unknown as FileList, items })).toEqual([b]);
  });

  it("is empty for text and for nothing", () => {
    expect(transferFiles(null)).toEqual([]);
    expect(transferFiles(undefined)).toEqual([]);
    expect(transferFiles({ files: [] as unknown as FileList, items: [{ kind: "string", getAsFile: () => null }] as unknown as DataTransferItemList })).toEqual([]);
  });
});

describe("dragHasFiles", () => {
  it("looks for Files among the types", () => {
    expect(dragHasFiles({ types: ["text/plain", "Files"] })).toBe(true);
    expect(dragHasFiles({ types: ["text/plain"] })).toBe(false);
    expect(dragHasFiles(null)).toBe(false);
  });
});

describe("uploadName", () => {
  it("keeps a name that has an extension", () => {
    expect(uploadName({ name: "shot.png", type: "image/png" })).toBe("shot.png");
    expect(uploadName({ name: "notes.txt", type: "text/plain" })).toBe("notes.txt");
  });

  it("names a clipboard image from its type", () => {
    expect(uploadName({ name: "", type: "image/png" })).toBe("image.png");
    expect(uploadName({ name: "  ", type: "image/jpeg" })).toBe("image.jpg");
    expect(uploadName({ name: "image", type: "image/webp" })).toBe("image.webp");
  });

  it("does not invent an extension for a type it does not know", () => {
    expect(uploadName({ name: "Makefile", type: "" })).toBe("Makefile");
    expect(uploadName({ name: "", type: "" })).toBe("image");
  });
});

describe("refuseUpload", () => {
  it("lets a file through", () => {
    expect(refuseUpload({ name: "a.png", size: 1234 })).toBeNull();
    expect(refuseUpload({ name: "a.png", size: MAX_TERMINAL_UPLOAD })).toBeNull();
  });

  it("refuses an empty file and one over the limit", () => {
    expect(refuseUpload({ name: "a.png", size: 0 })).toContain("为空文件");
    expect(refuseUpload({ name: "big.mov", size: MAX_TERMINAL_UPLOAD + 1 })).toBe("「big.mov」超过 50 MB，未上传");
  });
});

describe("uploadTimeoutMs", () => {
  it("is a minute plus 10 ms per kilobyte", () => {
    expect(uploadTimeoutMs(0)).toBe(60_000);
    expect(uploadTimeoutMs(1_000_000)).toBe(70_000);
  });
});

describe("pasteVerdict", () => {
  it("pastes into the pane the files were meant for while it is driven", () => {
    expect(pasteVerdict({ disposed: false, driving: true })).toBe("ok");
  });

  it("drops the paste once the pane's feed is gone (another session, the panel closed)", () => {
    expect(pasteVerdict({ disposed: true, driving: true })).toBe("changed");
    expect(pasteVerdict({ disposed: true, driving: false })).toBe("changed");
  });

  it("drops it when the pane is the same but not driven any more", () => {
    expect(pasteVerdict({ disposed: false, driving: false })).toBe("watching");
  });
});

describe("uploadingText", () => {
  it("names the file, or the first and how many", () => {
    expect(uploadingText(["a.png"])).toBe("正在上传 a.png…");
    expect(uploadingText(["a.png", "b.png", "c.png"])).toBe("正在上传 a.png 等 3 个文件…");
    expect(uploadingText([])).toBe("正在上传 文件…");
  });
});

describe("copying", () => {
  it("uses the async clipboard first", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    const legacyCopy = vi.fn();
    expect(await copyWith("hi", { writeText, legacyCopy })).toBe("wrote");
    expect(writeText).toHaveBeenCalledWith("hi");
    expect(legacyCopy).not.toHaveBeenCalled();
  });

  it("falls back to the old way when the write is refused or there is no async clipboard", async () => {
    const refused = vi.fn().mockRejectedValue(new Error("NotAllowedError"));
    const legacyCopy = vi.fn().mockReturnValue(true);
    expect(await copyWith("hi", { writeText: refused, legacyCopy })).toBe("legacy");
    expect(await copyWith("hi", { legacyCopy })).toBe("legacy");
    expect(legacyCopy).toHaveBeenCalledTimes(2);
  });

  it("fails when neither works", async () => {
    expect(await copyWith("hi", { legacyCopy: () => false })).toBe("failed");
    expect(
      await copyWith("hi", {
        legacyCopy: () => {
          throw new Error("no");
        },
      }),
    ).toBe("failed");
  });

  it("says why on a page without HTTPS", () => {
    expect(copyNotice("wrote", true)).toEqual({ text: "已复制", tone: "ok" });
    expect(copyNotice("legacy", true).text).toBe("已复制");
    expect(copyNotice("legacy", false).text).toContain("未使用 HTTPS");
    expect(copyNotice("failed", false)).toMatchObject({ tone: "error", text: expect.stringContaining("HTTPS") });
    expect(copyNotice("failed", true)).toMatchObject({ tone: "error" });
  });

  it("copies an OSC 52 write unless it is empty or huge", () => {
    expect(osc52Text("hello")).toBe("hello");
    expect(osc52Text("")).toBeNull(); // undecodable payloads decode to "": they must not clear the clipboard
    expect(osc52Text("x".repeat(OSC52_MAX_CHARS + 1))).toBeNull();
  });
});
