import { spawn } from "node:child_process";
import { join } from "node:path";
import { homedir } from "node:os";
import { existsSync } from "node:fs";
import { linkArguments, publicLinkStatus } from "./link-contract.cjs";
import type { LinkCommand } from "./link-contract.cjs";

/** Only the separately installed connector is accepted, never a renderer-provided executable or arbitrary PATH program. */
export function personaLinkExecutable(home = homedir()): string {
  return join(home, ".persona", "bin", process.platform === "win32" ? "persona-link.exe" : "persona-link");
}

export function linkInstalled(home?: string): boolean {
  return existsSync(personaLinkExecutable(home));
}
export interface CommandResult {
  readonly ok: boolean;
  readonly stdout: string;
  readonly stderr: string;
}

/** Fixed executable and argv: no shell, no arbitrary executable, no token on a command line. */
export function runPersonaLink(command: LinkCommand, executable = personaLinkExecutable()): Promise<CommandResult> {
  const parsed = linkArguments(command);
  if (!parsed) return Promise.resolve({ ok: false, stdout: "", stderr: "Unsupported Link command." });
  return new Promise(resolve => {
    const child = spawn(executable, parsed.args, { shell: false, env: { ...process.env }, stdio: ["pipe", "pipe", "pipe"] });
    let stdout = "", stderr = "";
    const timer = setTimeout(() => { child.kill("SIGTERM"); }, 120_000);
    child.stdout.on("data", data => { if (stdout.length < 64 * 1024) stdout += data.toString("utf8").slice(0, 64 * 1024 - stdout.length); });
    child.stderr.on("data", data => { if (stderr.length < 16 * 1024) stderr += data.toString("utf8").slice(0, 16 * 1024 - stderr.length); });
    child.on("error", error => { clearTimeout(timer); resolve({ ok: false, stdout, stderr: error.message }); });
    child.on("close", code => { clearTimeout(timer); resolve({ ok: code === 0, stdout, stderr }); });
    if (parsed.stdin) child.stdin.write(parsed.stdin);
    child.stdin.end();
  });
}

export async function linkStatus(): Promise<Record<string, unknown>> {
  const result = await runPersonaLink({ action: "status" });
  if (!linkInstalled()) return { available: false, healthy: false, detail: "Install Persona Link separately to run Agents on this computer." };
  try {
    return { ...publicLinkStatus(JSON.parse(result.stdout)), available: true, healthy: result.ok, detail: result.ok ? undefined : result.stderr };
  } catch {
    return { available: false, healthy: false, detail: result.stderr || "Install Persona Link to run Agents on this computer." };
  }
}
