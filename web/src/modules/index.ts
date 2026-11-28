// The module list (#13434, docs/web/web.md "Modules"): every optional feature module of the web app, the only place
// their ids appear outside src/modules/<id>. Each entry is a static descriptor (module.ts: what it contributes, its hub
// feature flag) whose code is a dynamic import(), so importing this list loads no module's code and none of its strings.
// App.tsx imports it once, before the first route is read. Adding a module: docs/web/web.md "Adding a module".
import { registerModule, registeredModules, type ModuleDescriptor } from "../core/module-host";
import { health } from "./health/module";
import { lark } from "./lark/module";
import { mail } from "./mail/module";
import { office } from "./office/module";
import { recording } from "./recording/module";
import { STANDALONE_MODULES } from "./standalone";
import { voice } from "./voice/module";

export const MODULES: readonly ModuleDescriptor[] = [health, voice, recording, lark, office, mail, ...STANDALONE_MODULES];

/**
 * Registers each module once (a second call, a hot reload or a test importing the list again, is a no-op). This runs
 * when App.tsx loads, before anything draws, so a descriptor the host refuses must not take the app down with it: in
 * a production build it is logged and that module is left out (absent, as if switched off), and the rest register.
 * In development and tests (`strict`) the refusal throws, so a bad module cannot land.
 */
export function registerModules(list: readonly ModuleDescriptor[] = MODULES, strict = !import.meta.env.PROD, report: (message: string, error: unknown) => void = console.error): void {
  for (const m of list) {
    if (registeredModules().includes(m)) continue;
    try {
      registerModule(m);
    } catch (e) {
      if (strict) throw e;
      report(`module "${m.id}" was not registered and is left out`, e);
    }
  }
}

registerModules();
