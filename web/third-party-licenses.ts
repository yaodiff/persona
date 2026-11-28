// The web build's third-party notices (THIRD_PARTY_NOTICES.md "Web client"): every npm package package-lock.json
// installs for the client, with its version, the license it is used under, its source and its license text, written
// to third-party-licenses.txt beside index.html. The build and third-party-licenses.test.ts fail on a package these
// lists do not cover yet.
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import type { Plugin } from "vite";

const WEB = fileURLToPath(new URL(".", import.meta.url));

/** Licenses the client may ship. A package under another one needs a decision here first. */
const ACCEPTED = new Set(["MIT", "ISC", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "MPL-2.0", "EPL-2.0", "Unlicense", "Zlib"]);

/** Packages offering a choice of license ("A OR B"), and the one Persona uses. */
export const CHOSEN: Record<string, string> = { dompurify: "Apache-2.0", elkjs: "EPL-2.0", jszip: "MIT" };

/** Packages whose package.json names no license, and the license their LICENSE file grants. */
export const UNDECLARED: Record<string, string> = { khroma: "MIT" };

/** Packages published without a license file: their entry points at their source. */
export const NO_LICENSE_FILE = new Set(["isarray", "rehype-katex", "remark-math"]);

export interface Package {
  name: string;
  version: string;
  /** The license expression the package declares. */
  declared?: string;
  /** The license Persona uses it under. */
  license?: string;
  dir: string;
}

interface LockEntry {
  version: string;
  license?: string;
  dev?: boolean;
  optional?: boolean;
  devOptional?: boolean;
}

/** Every lockfile package but the dev and optional ones (types, other platforms' binaries), once per version. */
export function shippedPackages(): Package[] {
  const lock = JSON.parse(readFileSync(join(WEB, "package-lock.json"), "utf8")) as { packages: Record<string, LockEntry> };
  const byId = new Map<string, Package>();
  for (const [path, entry] of Object.entries(lock.packages)) {
    if (!path || entry.dev || entry.optional || entry.devOptional) continue;
    const name = path.slice(path.lastIndexOf("node_modules/") + "node_modules/".length);
    const { version } = entry;
    const declared = entry.license?.replace(/^\((.*)\)$/, "$1");
    byId.set(`${name}@${version}`, { name, version, declared, license: CHOSEN[name] ?? declared ?? UNDECLARED[name], dir: join(WEB, path) });
  }
  return [...byId].sort(([a], [b]) => (a < b ? -1 : 1)).map(([, p]) => p);
}

/** What the lists above do not cover yet: a license not accepted (or a choice of licenses not made), a choice the
 * package no longer offers, a license file missing. The build fails on any. */
export function uncovered(packages: Package[]): string[] {
  return packages.flatMap((p) => {
    const id = `${p.name}@${p.version}`;
    const chosen = CHOSEN[p.name];
    return [
      !p.license?.split(" AND ").every((l) => ACCEPTED.has(l)) && `${id}: ${p.declared ?? "no license declared"} is not accepted`,
      chosen && !p.declared?.split(" OR ").includes(chosen) && `${id} no longer offers ${chosen}: ${p.declared ?? "no license declared"}`,
      !NO_LICENSE_FILE.has(p.name) && !licenseTexts(p.dir).length && `${id} is published without a license file`,
    ].filter((problem): problem is string => !!problem);
  });
}

// LICENSE-MPL (DOMPurify's other choice) and license-update.mjs (a script) are not the license used.
const LICENSE_FILE = /^(?:licen[cs]e|copying|notice)(?:\.(?:md|markdown|txt))?$/i;

/** The license files a package is published with (and an Apache NOTICE), in file name order. */
export function licenseTexts(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true })
    .filter((f) => f.isFile() && LICENSE_FILE.test(f.name))
    .map((f) => f.name)
    .sort()
    .map((name) => readFileSync(join(dir, name), "utf8").trim());
}

/** package.json's repository as a browsable https URL; the npm page when it names none. */
export function sourceUrl(repository: unknown, name: string, version: string): string {
  const raw = typeof repository === "string" ? repository : (repository as { url?: unknown } | undefined)?.url;
  if (typeof raw !== "string" || !raw) return `https://www.npmjs.com/package/${name}/v/${version}`;
  return raw
    .replace(/^git\+/, "")
    .replace(/^(?:git|ssh|http):\/\//, "https://")
    .replace(/^git@([^:/]+):/, "https://$1/")
    .replace(/^(https:\/\/)[^/@]+@/, "$1")
    .replace(/\.git$/, "")
    .replace(/^(?:github:)?([\w.-]+\/[\w.-]+)$/, "https://github.com/$1");
}

// The release scan (scripts/release/check_bundle_privacy.py) refuses any real email address in the hub binary, which
// embeds this file: authors' addresses are left out, their names and years stay in the copyright lines.
const ADDRESS = String.raw`(?:mailto:)?[\w.%+-]+@[\w-]+(?:\.[\w-]+)*\.[A-Za-z]{2,}`;
const EMAIL = new RegExp(String.raw`[ \t]*<${ADDRESS}>|[ \t]*\(${ADDRESS}\)|(?<=\()${ADDRESS},[ \t]*|[ \t]*${ADDRESS}`, "g");

/** The text without email addresses: " <a@b.c>" and " (a@b.c)" go whole, "(a@b.c, url)" keeps "(url)". */
export function withoutEmails(text: string): string {
  return text.replace(EMAIL, "");
}

export function notices(packages: Package[]): string {
  const entries = packages.map((p) => {
    if (!existsSync(p.dir)) throw new Error(`${p.name} ${p.version} is in package-lock.json but not installed: run npm ci in web/`);
    const { repository } = JSON.parse(readFileSync(join(p.dir, "package.json"), "utf8")) as { repository?: unknown };
    const texts = licenseTexts(p.dir);
    const offered = p.declared && p.declared !== p.license ? ` (offered: ${p.declared})` : "";
    return [
      "=".repeat(80),
      `${p.name} ${p.version}`,
      `License: ${p.license ?? "not declared"}${offered}`,
      `Source: ${sourceUrl(repository, p.name, p.version)}`,
      "",
      texts.length ? withoutEmails(texts.join("\n\n")) : "Published without a license file; its license text is at its source.",
    ].join("\n");
  });
  const head = [
    "Third-party software in the Persona web client",
    "",
    "The client's npm packages and bundled fonts, with the license each is used under, its source and the license",
    "text it is published with. Authors' email addresses are left out of the texts.",
  ].join("\n");
  const fontNotices = [
    ["Inter", "inter", "https://github.com/rsms/inter"],
    ["JetBrains Mono", "jetbrains-mono", "https://github.com/JetBrains/JetBrainsMono"],
  ].map(([name, slug, source]) => [
    "=".repeat(80),
    `${name} (Fontsource 5.3.0 Latin variable subset)`,
    "License: OFL-1.1",
    `Source: ${source}`,
    "",
    readFileSync(join(WEB, "src/ui/font-assets", `${slug}-OFL.txt`), "utf8").trimEnd(),
  ].join("\n"));
  return [head, ...entries, ...fontNotices].join("\n\n") + "\n";
}

export const thirdPartyLicenses: Plugin = {
  name: "third-party-licenses",
  apply: "build",
  generateBundle() {
    const packages = shippedPackages();
    const source = notices(packages);
    const problems = uncovered(packages);
    if (problems.length) this.error(`web/third-party-licenses.ts does not cover:\n${problems.join("\n")}`);
    this.emitFile({ type: "asset", fileName: "third-party-licenses.txt", source });
  },
};
