import { describe, expect, it } from "vitest";
import serializedEdit from "./testdata/codex-file-approval.json";
import { approvalImpact } from "./approval-impact";
import type { Interaction } from "./types";

const card = (tool_name: string, tool_input: Interaction["tool_input"] = {}): Interaction =>
  ({ id: "pending", machine_id: "m", session_id: "s", kind: "permission", tool_name, tool_input });

describe("the effect of a tool approval", () => {
  it.each([
    ["git push origin feature", "push"], ["g'it' p\"us\"h origin feature", "push"],
    ["git${IFS}push origin feature", "push"], ["git -C repo push", "push"],
    ["cd repo && git push", "push"], ["git -C repo status", null], ["ls;", null],
    ["ls &&", "unknown"], ["git -C //shared/repo status", "unknown"], ["terraform destroy", "deploy"], ["terraform apply", "deploy"], ["npm publish", "publish"],
    ["kubectl apply -f demo.yml", "deploy"], ["kubectl rollout restart deployment/example", "deploy"],
    ["kubectl rollout status deployment/example", "remote_read"], ["kubectl rollout history deployment/example", "remote_read"], ["mail team@example.com", "send"],
    ["gh issue comment 3 --body 'Ready'", "send"], ["ls -la", null], ["git status", null],
    ["pwd && ls | wc -l", null], ["echo 'git push'", null],
    ["sh -c 'custom-script'", "unknown"], ["tool-from-extension", "unknown"],
    ["ls $(custom-script)", "unknown"], ["ls; 'unfinished", "unknown"],
    ["cat <(custom-script)", "unknown"], ["npm test", "unknown"], ["", "unknown"],
  ])("%s", (command, expected) => {
    expect(approvalImpact(card("Bash", { command }))).toBe(expected);
  });

  it("leaves uncertain escaped line endings for review", () => {
    for (const slashes of [1, 2, 3]) {
      const command = "echo sample " + "\\".repeat(slashes) + "\n" + "pwd";
      expect(approvalImpact(card("Bash", { command }))).toBe("unknown");
    }
  });

  it("requires a known local destination for every file a tool or patch can change", () => {
    for (const tool of ["Write", "Edit", "MultiEdit", "Read"]) {
      expect(approvalImpact(card(tool, { file_path: String.raw`\\server\share\draft.txt` }))).toBe("unknown");
      expect(approvalImpact(card(tool, { file_path: "smb://server/share/draft.txt" }))).toBe("unknown");
      expect(approvalImpact(card(tool, { file_path: "/project/draft.txt", old_string: "a", new_string: "b" }))).toBeNull();
      expect(approvalImpact(card(tool))).toBe("unknown");
    }
    expect(approvalImpact(card("NotebookEdit", { notebook_path: "//server/share/draft.ipynb" }))).toBe("unknown");
    const patch = "*** Begin Patch\n*** Update File: draft.txt\n*** Move to: ";
    expect(approvalImpact(card("apply_patch", { workdir: "/project", input: patch + "done.txt\n*** End Patch" }))).toBeNull();
    expect(approvalImpact(card("apply_patch", { workdir: "/project", input: patch + "//server/share/done.txt\n*** End Patch" }))).toBe("unknown");
    expect(approvalImpact(card("apply_patch", { input: "unrecognized patch" }))).toBe("unknown");
  });

  it("checks the working directory for relative targets and requires complete operation data", () => {
    const write = card("Write", { file_path: "draft.txt", content: "text" });
    expect(approvalImpact({ ...write, cwd: "//shared/project" })).toBe("unknown");
    expect(approvalImpact(write)).toBe("unknown");
    expect(approvalImpact(card("Write", { ...write.tool_input, workdir: "/project" }))).toBeNull();
    expect(approvalImpact(card("Write", { ...write.tool_input, workdir: "//shared/project" }))).toBe("unknown");
    expect(approvalImpact(card("Edit", { file_path: "/project/first.txt" }))).toBe("unknown");
  });

  it("uses every available Codex destination and treats missing scopes as incomplete", () => {
    // Captured from item/fileChange/requestApproval after item/started supplied one local and one shared path.
    const edit = { ...card("Edit"), ...serializedEdit };
    expect(approvalImpact(edit)).toBe("external");
    expect(approvalImpact({ ...edit, always_keys: undefined })).toBe("unknown");
    expect(approvalImpact({ ...edit, always_keys: [edit.always_keys[0]] })).toBe("unknown");
  });

  it("keeps argv boundaries and honors the command tool's alternative input", () => {
    expect(approvalImpact(card("shell", { command: ["bash", "-lc", "git push"] }))).toBe("push");
    expect(approvalImpact(card("shell", { command: ["echo", "git push"] }))).toBeNull();
    expect(approvalImpact(card("exec_command", { cmd: "npm publish" }))).toBe("publish");
    expect(approvalImpact(card("shell", { command: ["ls", 1] }))).toBe("unknown");
    expect(approvalImpact(card("Bash", { description: "Only reads a file" }))).toBe("unknown");
  });

  it("uses structured tool names and trusted outgoing-message fields, independent of the summary", () => {
    expect(approvalImpact(card("mcp__github__push_files"))).toBe("push");
    expect(approvalImpact(card("mcp__host__deploy"))).toBe("deploy");
    expect(approvalImpact(card("mail_send"))).toBe("send");
    expect(approvalImpact({ ...card("Bash"), outward_write: true })).toBe("external");
    expect(approvalImpact({ ...card("Bash"), editable_text: "Hello" })).toBe("send");
    expect(approvalImpact(card("Write", { file_path: "/project/draft.txt", content: "git push" }))).toBeNull();
    expect(approvalImpact(card("mcp__custom__Read"))).toBe("unknown");
  });
});
