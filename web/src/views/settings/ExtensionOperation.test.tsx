// @vitest-environment jsdom
// The steps of an install (#3277): each step with its state, the download's bar, where it failed and why, and a retry
// only when the hub says it can.
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { ExtensionOperation } from "../../lib/extension-operations";
import { OperationSteps } from "./ExtensionOperation";

const base: ExtensionOperation = {
  id: "op1",
  kind: "install",
  extension: "larkext",
  state: "running",
  step: "download",
  steps: [
    { id: "download", state: "running", bytes: 8 << 20, total: 16 << 20 },
    { id: "verify", state: "pending" },
    { id: "nodes", state: "skipped" },
  ],
  retryable: false,
  started_at: "2026-11-06T00:00:00Z",
  updated_at: "2026-11-06T00:00:01Z",
};

describe("OperationSteps", () => {
  it("draws the steps it takes, the download as a bar", () => {
    const html = renderToStaticMarkup(<OperationSteps op={base} />);
    expect(html).toContain("下载");
    expect(html).toContain("校验");
    expect(html).not.toContain("同步到电脑"); // skipped
    expect(html).toContain('aria-valuenow="50"');
    expect(html).toContain("8.0 MB / 16.0 MB");
  });

  it("shows where it stopped, why, and a retry when it can", () => {
    const failed: ExtensionOperation = {
      ...base,
      state: "failed",
      error: "下载卡住了",
      error_code: "download_stalled",
      retryable: true,
      steps: [{ id: "download", state: "failed", bytes: 1 << 20, total: 16 << 20 }, { id: "verify", state: "pending" }],
    };
    const html = renderToStaticMarkup(<OperationSteps op={failed} onRetry={async () => {}} />);
    expect(html).toContain("在「下载」步骤中断");
    expect(html).toContain("原因：下载卡住了");
    expect(html).toContain("重试");
    expect(renderToStaticMarkup(<OperationSteps op={{ ...failed, retryable: false }} onRetry={async () => {}} />)).not.toContain("重试");
  });

  it("lists the tools step's programs, the machine's own and the one downloading with its bar (#4507)", () => {
    const op: ExtensionOperation = {
      ...base,
      step: "tools",
      steps: [
        { id: "download", state: "done", bytes: 1 << 20 },
        { id: "tools", state: "running", bytes: 3 << 20, total: 12 << 20, tools: [{ name: "node", state: "done", reused: true }, { name: "example-cli", state: "running" }] },
        { id: "install", state: "pending" },
      ],
    };
    const html = renderToStaticMarkup(<OperationSteps op={op} />);
    expect(html).toContain("安装命令行工具");
    expect(html).toContain("node");
    expect(html).toContain("使用本机已有版本");
    expect(html).toContain("example-cli");
    expect(html).toContain('aria-label="example-cli"');
    expect(html).toContain('aria-valuenow="25"');
    const failed = renderToStaticMarkup(<OperationSteps op={{ ...op, state: "failed", step: "tools", error: "example-cli：校验不对", error_code: "tools_failed" }} />);
    expect(failed).toContain("命令行工具安装失败：example-cli：校验不对");
  });
});
