// The feature-module boundary (#13434, #13430 section 6; docs/web/web.md "Modules"), over all of src (`npm run lint`'s
// second pass): Persona Core never reaches into a module, and a module reaches another only when its descriptor names it
// in `needs`. Only the module list (src/modules/index.ts, and standalone.ts, its part main.tsx imports) and the module
// host (src/core/module-host.ts) join them.
//
//   - outside src/modules: no import of a module's files (`modules/<id>/…`), static or dynamic; the list itself
//     (`./modules`, `../modules`) is the way in. Tests may reach a module's files to test them.
//   - src/core: not even the list: the host knows no module.
//   - src/modules/<id>: no import of another module's files, unless its module.ts names that module in `needs`, and
//     never of the list.
//
// The disables of these rules (and of persona-modules/boundary below) are counted by
// scripts/ci/web_module_boundary_ratchet.py: the count can only go down.
import { readdirSync, readFileSync } from "node:fs";
import { basename, dirname, isAbsolute, join, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import parser from "@typescript-eslint/parser";
import hooks from "eslint-plugin-react-hooks";

const MODULES_DIR = new URL("./src/modules/", import.meta.url);

/** The modules (src/modules/<id>/module.ts) and the modules each names in `needs`. */
export function moduleNeeds() {
  return readdirSync(MODULES_DIR, { withFileTypes: true })
    .filter((e) => e.isDirectory() && readdirSync(new URL(`${e.name}/`, MODULES_DIR)).includes("module.ts"))
    .map((e) => {
      const source = readFileSync(new URL(`${e.name}/module.ts`, MODULES_DIR), "utf8");
      const needs = /\bneeds:\s*\[([^\]]*)\]/.exec(source)?.[1] ?? "";
      return { id: e.name, needs: [...needs.matchAll(/["']([a-z][a-z0-9-]*)["']/g)].map((m) => m[1]) };
    });
}

// the list is index.ts, and standalone.ts, its part main.tsx imports (src/modules/standalone.ts says why)
const INTO_A_MODULE = "(^|/)modules/(?!(index|standalone)$)[^/]+";
const outside = "Persona Core reaches a module only through the module list (src/modules/index.ts) and the module host (src/core/module-host.ts): docs/web/web.md \"Modules\".";
const inCore = "src/core knows no module: the module list registers them with the host (docs/web/web.md \"Modules\").";

/** no-restricted-imports and the same paths through a dynamic import(). */
function forbid(regexes, message) {
  const source = regexes.map((r) => `(${r})`).join("|");
  return {
    "no-restricted-imports": ["error", { patterns: regexes.map((regex) => ({ regex, message })) }],
    "no-restricted-syntax": ["error", { selector: `ImportExpression[source.value=/${source.replace(/\//g, "\\/")}/]`, message }],
  };
}

// The same boundary by where an import lands rather than how it is spelled: the specifier (or a template literal's
// static start) is resolved against the importing file, so `../../modules`, `../`, `./x/../../other` and
// import(`../modules/health/${x}`) are judged as what they reach. A template literal whose static start could still
// reach src/modules is refused: the boundary has to be readable.
const SRC = fileURLToPath(new URL("./src/", import.meta.url));
const MODULES = join(SRC, "modules");
const NEEDS = new Map(moduleNeeds().map(({ id, needs }) => [id, needs]));
const TEST = /\.test\.[cm]?[jt]sx?$/;

/** What a path under src reaches: the list, a module, somewhere under src/modules that can't be told, or nothing of them. */
function landing(abs, partial, incomplete = false) {
  const rel = relative(MODULES, abs);
  if (rel.startsWith("..") || isAbsolute(rel)) {
    if (!partial) return null;
    // a partial path (a template literal's start) that is an ancestor of src/modules may still reach into it, and so
    // may one whose last, unfinished segment begins a directory on the way (`../mod${x}`)
    if (!relative(abs, MODULES).startsWith("..")) return { kind: "unknown" };
    const toModules = relative(dirname(abs), MODULES);
    return incomplete && !toModules.startsWith("..") && toModules.split(sep)[0].startsWith(basename(abs)) ? { kind: "unknown" } : null;
  }
  if (rel === "") return partial ? { kind: "unknown" } : { kind: "list" };
  const [first, ...rest] = rel.split(sep);
  if (!rest.length && /^(index|standalone)(\.[cm]?[jt]sx?)?$/.test(first)) return { kind: "list" };
  if (incomplete && !rest.length) return { kind: "unknown" }; // `modules/hea${x}`: which module is not written
  return NEEDS.has(first) ? { kind: "module", id: first } : { kind: "unknown" };
}

/** Where the importing file is: in a module, in src/core, elsewhere in src, or the list's own directory. */
function placeOf(file) {
  const rel = relative(MODULES, file);
  if (!rel.startsWith("..") && !isAbsolute(rel)) {
    const parts = rel.split(sep);
    return parts.length > 1 ? { kind: "module", id: parts[0] } : { kind: "modules-root" };
  }
  const core = relative(join(SRC, "core"), file);
  if (!core.startsWith("..") && !isAbsolute(core)) return { kind: "core" };
  return { kind: "outside", test: TEST.test(file) };
}

function verdict(place, target) {
  if (!target || place.kind === "modules-root") return null;
  if (place.kind === "module") {
    if (target.kind === "module" && (target.id === place.id || (NEEDS.get(place.id) ?? []).includes(target.id))) return null;
    if (target.kind === "list") return `module "${place.id}" may not import the module list: the list imports the modules (docs/web/web.md "Modules").`;
    return `module "${place.id}" may import only the modules its module.ts names in \`needs\` (docs/web/web.md "Modules").`;
  }
  if (place.kind === "core") return inCore;
  if (place.test || target.kind === "list") return null;
  return outside;
}

const boundary = {
  meta: { type: "problem", schema: [] },
  create(context) {
    const file = context.filename ?? context.getFilename();
    const place = placeOf(file);
    const check = (node, source) => {
      if (!source) return;
      let spec, partial = false;
      if (source.type === "Literal" && typeof source.value === "string") spec = source.value;
      else if (source.type === "TemplateLiteral") {
        spec = source.quasis[0].value.cooked ?? "";
        partial = source.expressions.length > 0;
      } else return;
      if (!spec.startsWith(".")) return;
      const incomplete = partial && !spec.endsWith("/") && !spec.endsWith(".");
      const message = verdict(place, landing(resolve(dirname(file), spec), partial, incomplete));
      if (message) context.report({ node: source, message });
    };
    return {
      ImportDeclaration: (n) => check(n, n.source),
      ExportNamedDeclaration: (n) => check(n, n.source),
      ExportAllDeclaration: (n) => check(n, n.source),
      TSImportType: (n) => check(n, n.source ?? n.argument?.literal),
      ImportExpression: (n) => check(n, n.source),
    };
  },
};

// react-hooks only so the code's own disable comments for its rules resolve; none of its rules is on here
const base = {
  plugins: { "react-hooks": hooks, "persona-modules": { rules: { boundary } } },
  linterOptions: { reportUnusedDisableDirectives: false },
  languageOptions: { parser, parserOptions: { ecmaVersion: "latest", sourceType: "module", ecmaFeatures: { jsx: true } } },
};

const LIST = ["(^|/)modules/?$", "(^|/)modules/(index|standalone)(\\.[jt]sx?)?$"];

export default [
  { ...base, files: ["src/**/*.ts", "src/**/*.tsx"], rules: { "persona-modules/boundary": "error" } },
  { ...base, files: ["src/**/*.ts", "src/**/*.tsx"], ignores: ["src/modules/**", "src/**/*.test.ts", "src/**/*.test.tsx"], rules: { "persona-modules/boundary": "error", ...forbid([INTO_A_MODULE], outside) } },
  { ...base, files: ["src/core/**/*.ts", "src/core/**/*.tsx"], rules: { "persona-modules/boundary": "error", ...forbid([INTO_A_MODULE, ...LIST], inCore) } },
  ...moduleNeeds().map(({ id, needs }) => {
    const allowed = [id, ...needs].join("|");
    const message = `module "${id}" may import only the modules its module.ts names in \`needs\`, and never the module list (docs/web/web.md "Modules").`;
    return {
      ...base,
      files: [`src/modules/${id}/**/*.ts`, `src/modules/${id}/**/*.tsx`],
      rules: {
        "persona-modules/boundary": "error",
        // spelled paths through src/modules; relative ones (`../other`, `../`) persona-modules/boundary resolves
        ...forbid([`(^|/)modules/(?!(${allowed})(/|$))[^/]+`, ...LIST], message),
      },
    };
  }),
];
