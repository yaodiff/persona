// Optional task ownership must not increase the first-screen catalog budget.
import { readFileSync } from "node:fs";
import { expect, it } from "vitest";

it("keeps closed-task notices and recovery controls outside the first screen", async () => {
  const source = readFileSync(new URL("./ChatView.tsx", import.meta.url), "utf8");
  const namespaces = JSON.parse(readFileSync(new URL("../../i18n/startup-namespaces.json", import.meta.url), "utf8"));
  expect(source).toMatch(/lazy\(\(\) => import\("\.\/ClosedTaskNotice"\)\)/);
  expect(source).not.toMatch(/^import[^;]*from\s+["']\.\/ClosedTaskNotice["']/m);
  expect(source).toMatch(/lazy\(\(\) => import\("\.\/StoppedAgentLine"\)/);
  expect(source).not.toMatch(/^import[^;]*from\s+["']\.\/StoppedAgentLine["']/m);
  expect(source).toMatch(/lazy\(\(\) => import\("\.\.\/\.\.\/ui\/ArchivedNotice"\)\)/);
  expect(namespaces).not.toContain("chat-closed");
  const startup = JSON.parse(readFileSync(new URL("../../i18n/locales/en/chat-chatview.json", import.meta.url), "utf8"));
  const deferred = JSON.parse(readFileSync(new URL("../../i18n/locales/en/chat-closed.json", import.meta.url), "utf8"));
  expect(startup).not.toHaveProperty("archived_notice");
  expect(startup).not.toHaveProperty("resume");
  expect(startup).not.toHaveProperty("resume_failed");
  expect(startup).not.toHaveProperty("restore");
  expect(startup).not.toHaveProperty("terminal");
  expect(deferred.archived).toBeTypeOf("string");
  expect(deferred.resume).toBeTypeOf("string");
  expect(deferred.resume_failed).toBeTypeOf("string");
  const stopped = await import("./StoppedAgentLine");
  expect(stopped.default).toBe(stopped.StoppedAgentLine);
  expect(typeof (await import("./ClosedTaskNotice")).default).toBe("function");
});
