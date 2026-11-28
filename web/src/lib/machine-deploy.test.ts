import { describe, expect, it } from "vitest";
import { machineDeployment } from "./machine-deploy";
import type { Machine } from "./types";

const machine = (fields: Partial<Machine> = {}): Machine => ({ id: "computer-example", name: "示例电脑", online: true, node_mismatch: true, ...fields });

describe("machine deployment", () => {
  it("retains old hub deployment for SSH machines, including offline recovery", () => {
    for (const kind of ["ssh", undefined]) {
      for (const online of [true, false]) {
        expect(machineDeployment(machine({ kind, online }))).toMatchObject({ visible: true, supported: true, canDeploy: true, busy: false });
      }
    }
  });

  it.each(["link", "service"])("requires advertised capability for %s nodes", (kind) => {
    const legacy = machineDeployment(machine({ kind }));
    expect(legacy).toMatchObject({ supported: false, canDeploy: false });
    expect(legacy.reason).toContain("手动更新 node");
    expect(legacy.reason).toContain("hub");
    expect(machineDeployment(machine({ kind, can_deploy: true }))).toMatchObject({ supported: true, canDeploy: true });
  });

  it("honors explicit rejection, even for legacy-compatible SSH", () => {
    const reason = "请先在这台电脑上本地升级 node，再从 hub 更新。";
    for (const kind of ["ssh", "link", "service", undefined]) {
      expect(machineDeployment(machine({ kind, can_deploy: false, deploy_reason: reason }))).toMatchObject({ supported: false, canDeploy: false, reason });
    }
  });

  it("never offers to redeploy the hub's local node", () => {
    expect(machineDeployment(machine({ kind: "local" }))).toMatchObject({ supported: false, canDeploy: false });
    expect(machineDeployment(machine({ kind: "local", can_deploy: true })).reason).toContain("本机更新 hub");
  });

  it.each(["link", "service"])("waits for %s nodes to reconnect before starting", (kind) => {
    expect(machineDeployment(machine({ kind, can_deploy: true, online: false }))).toMatchObject({ supported: true, canDeploy: false, busy: false });
    expect(machineDeployment(machine({ kind, can_deploy: true, online: false })).reason).toContain("离线");
  });

  it.each([
    ["waiting_idle", "等待空闲", "Agent 空闲"],
    ["transferring", "传输中", "传输到这台电脑"],
    ["draining", "待空闲重启", "等待当前任务完成后重启"],
    ["restarting", "重启中", "重启"],
    ["confirming", "确认中", "确认其与 hub 版本一致"],
  ] as const)("keeps %s visible through offline/version transitions and prevents duplicate deployments", (deploy_phase, label, text) => {
    const result = machineDeployment(machine({ kind: "link", can_deploy: false, online: false, node_mismatch: false, deploy_phase }));
    expect(result).toMatchObject({ visible: true, canDeploy: false, busy: true, label });
    expect(result.message).toContain(text);
  });

  it("treats only restart and confirmation disconnects as expected, and restores recovery after failure", () => {
    const offline = machine({ kind: "link", online: false, can_deploy: true });
    for (const deploy_phase of ["waiting_idle", "transferring", "draining", undefined] as const) {
      expect(machineDeployment({ ...offline, deploying: true, deploy_phase }).expectedDisconnect).toBe(false);
    }
    for (const deploy_phase of ["restarting", "confirming"] as const) {
      expect(machineDeployment({ ...offline, deploying: true, deploy_phase }).expectedDisconnect).toBe(true);
      expect(machineDeployment({ ...offline, online: true, deploying: true, deploy_phase }).expectedDisconnect).toBe(false);
      expect(machineDeployment({ ...offline, deploying: false, deploy_phase, deploy_error: "等待新版本确认超时" }).expectedDisconnect).toBe(false);
    }
    expect(machineDeployment({ ...offline, deploying: false }).expectedDisconnect).toBe(false);
  });

  it("keeps old hubs busy until they report completion without claiming version confirmation", () => {
    const result = machineDeployment(machine({ deploying: true, node_mismatch: false }));
    expect(result).toMatchObject({ visible: true, canDeploy: false, busy: true, label: "部署中" });
    expect(result.message).toContain("完成后将确认其与 hub 版本一致");
  });

  it("preserves errors even without a mismatch or while capability is unavailable", () => {
    const result = machineDeployment(machine({ kind: "link", can_deploy: false, node_mismatch: false, deploy_error: "当前 node 不支持远程更新" }));
    expect(result).toMatchObject({ visible: true, canDeploy: false, error: "上次部署失败：当前 node 不支持远程更新" });
    expect(result.reason).toContain("手动更新");
  });

  it("allows retry after a failed supported deployment and retains errors during a retry", () => {
    const failed = machine({ kind: "service", can_deploy: true, deploy_error: "未收到新版本确认" });
    expect(machineDeployment(failed)).toMatchObject({ visible: true, canDeploy: true, label: "重新部署" });
    expect(machineDeployment({ ...failed, deploying: true, deploy_phase: "confirming" })).toMatchObject({ busy: true, canDeploy: false, error: "上次部署失败：未收到新版本确认" });
  });

  it("clears the banner only when the hub reports no pending phase, mismatch or error", () => {
    expect(machineDeployment(machine({ node_mismatch: false, can_deploy: true }))).toMatchObject({ visible: false, busy: false });
    expect(machineDeployment(machine({ node_mismatch: true, can_deploy: true, deploy_phase: undefined, deploying: false }))).toMatchObject({ visible: true, busy: false, canDeploy: true });
  });
});
