import { join, resolve } from "node:path";
import { mkdir, writeFile } from "node:fs/promises";
import { app, nativeTheme } from "electron";

/** An explicit development-only main-process switch. The hub renderer has no IPC for this. */
export function installSmokeCapture(): void {
  const directory = process.env.PERSONA_DESKTOP_SMOKE;
  if (app.isPackaged || !directory || !process.argv.includes("--desktop-smoke")) return;
  const output = resolve(directory);
  let settled = false;
  const finish = async (result: { ok: boolean; text?: string; error?: string }, image?: Electron.NativeImage) => {
    if (settled) return;
    settled = true;
    try {
      await mkdir(output, { recursive: true });
      if (image) await writeFile(join(output, "desktop.png"), image.toPNG());
      await writeFile(join(output, "result.json"), JSON.stringify(result), "utf8");
      app.exit(result.ok ? 0 : 1);
    } catch { app.exit(2); }
  };
  const deadline = setTimeout(() => { void finish({ ok: false, error: "Smoke timed out waiting for the web UI" }); }, 45_000);
  void app.whenReady().then(() => {
    if (process.env.PERSONA_DESKTOP_SMOKE_THEME === "dark" || process.env.PERSONA_DESKTOP_SMOKE_THEME === "light") nativeTheme.themeSource = process.env.PERSONA_DESKTOP_SMOKE_THEME;
  });
  app.on("browser-window-created", (_event, window) => {
    const capture = () => {
      if (settled || window.isDestroyed()) return;
      void window.webContents.executeJavaScript(`(async () => { if (location.protocol === 'file:') { if (!window.desktopSetup || document.querySelector('#link-state').textContent.includes('…')) return {text: '', ready: false}; document.querySelector('details').open = true; await new Promise(resolve => setTimeout(resolve, 100)); } return {text: document.body.innerText, ready: document.body.innerText.includes('Let this browser in from a device') || !!document.querySelector('input[type="password"], #hub')}; })()`).then(async ({ text, ready }) => {
        if (!ready) { setTimeout(capture, 250); return; }
        clearTimeout(deadline);
        // The existing web login has a short entrance transition; capture its settled frame, not its transparent first frame.
        setTimeout(async () => {
          const image = await window.webContents.capturePage();
          await finish({ ok: true, text: text.slice(0, 1000) }, image);
        }, 800);
      }).catch(error => finish({ ok: false, error: String(error) }));
    };
    window.webContents.on("did-finish-load", () => { setTimeout(capture, 300); });
    window.webContents.on("did-fail-load", (_event, code, description) => { void finish({ ok: false, error: `${code}: ${description}` }); });
  });
}
