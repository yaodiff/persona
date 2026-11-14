import { spawn } from "node:child_process";
import { mkdtemp, mkdir, readFile, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { tmpdir } from "node:os";
import electron from "electron";

/** Development-only smoke: a real Electron process against a synthetic hub, not a production bypass. */
const root = fileURLToPath(new URL("..", import.meta.url));
const output = resolve(process.argv[2] ?? join(root, "release", "smoke"));
const hub = process.env.PERSONA_HUB_URL ?? "http://127.0.0.1:18760/";
if (!hub.startsWith("http://127.0.0.1:") && !hub.startsWith("http://localhost:")) throw new Error("The smoke hub must be a local synthetic endpoint.");
await mkdir(output, { recursive: true });
const profile = await mkdtemp(join(tmpdir(), "persona-desktop-smoke-"));
const child = spawn(electron, ["--no-sandbox", "--disable-gpu", `--user-data-dir=${profile}`, "--desktop-smoke", join(root, "dist", "main.js")], {
  cwd: root,
  env: { ...process.env, PERSONA_HUB_URL: hub, PERSONA_DESKTOP_DISABLE_UPDATE: "1", PERSONA_DESKTOP_SMOKE: output },
  stdio: ["ignore", "pipe", "pipe"],
});
let log = "";
child.stdout.on("data", data => { log += data; });
child.stderr.on("data", data => { log += data; });
const timeout = setTimeout(() => { child.kill("SIGTERM"); }, 60_000);
const exit = await new Promise(resolve => child.once("close", (code, signal) => resolve({ code, signal })));
clearTimeout(timeout);
await writeFile(join(output, "electron.log"), log.replaceAll(profile, "<temporary profile>"), "utf8");
let verdict;
try { verdict = JSON.parse(await readFile(join(output, "result.json"), "utf8")); } catch { verdict = { ok: false, error: "No smoke result" }; }
console.log(JSON.stringify({ exit, verdict, screenshot: join(output, "desktop.png") }));
if (exit.code !== 0 || verdict.ok !== true) process.exitCode = 1;
