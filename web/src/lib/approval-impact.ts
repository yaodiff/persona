// The web approval card describes the requested action. Only recognized local operations skip its extra step.
import { isCommandPrompt } from "./command-ask";
import type { Interaction } from "./types";

export type ApprovalImpact = "push" | "publish" | "deploy" | "remote_read" | "send" | "external" | "unknown";
type Impact = ApprovalImpact | null;

// Only paths with an unambiguous local spelling qualify for one-tap approval. File contents are not paths.
function localPath(path: unknown): path is string {
  if (typeof path !== "string" || !path.trim()) return false;
  const value = path.trim().replaceAll("\\", "/");
  return !value.startsWith("//") && (/^[a-z]:\//i.test(value) || !/^[^/]+:/.test(value));
}

// Card cwd can be the conversation's starting directory. A relative file needs the operation's own directory.
function localTarget(path: unknown, cwd: unknown): boolean {
  if (!localPath(path)) return false;
  const absolute = /^(?:\/|[a-z]:[\\/])/i;
  return absolute.test(path) || (localPath(cwd) && absolute.test(cwd));
}

function patchImpact(input: unknown, cwd: unknown): Impact {
  if (typeof input !== "string") return "unknown";
  const lines = input.trim().split(/\r?\n/);
  if (lines.shift() !== "*** Begin Patch" || lines.pop() !== "*** End Patch") return "unknown";
  let files = 0;
  for (const line of lines) {
    if (!line.startsWith("*** ") || line === "*** End of File") continue;
    const path = /^\*\*\* (?:(?:Add|Delete|Update) File|Move to): (.+)$/.exec(line)?.[1];
    if (!localTarget(path, cwd)) return "unknown";
    files++;
  }
  return files ? null : "unknown";
}

function commandImpact([program, ...args]: string[]): Impact {
  const [verb, action] = args;
  if (program === "git") {
    let i = 0;
    let configured = false;
    while (args[i] === "-C" || args[i] === "-c") {
      if (args[i] === "-C" && !localPath(args[i + 1])) return "unknown";
      if (args[i] === "-c") configured = true;
      i += 2;
    }
    const gitVerb = args[i];
    const flags = args.slice(i + 1);
    if (gitVerb === "push") return "push";
    if (gitVerb === "send-email") return "send";
    if (configured) return "unknown";
    if (gitVerb === "status" && flags.every(a => /^(?:-s|-b|-sb|--short|--branch|--porcelain)$/.test(a))) return null;
    if ((gitVerb === "diff" || gitVerb === "log") && flags.every(a => /^(?:--stat|--name-only|--oneline|--cached|--staged|-\d+)$/.test(a))) return null;
    return "unknown";
  }
  if ((program === "docker" && verb === "push") ||
      (["npm", "pnpm", "yarn", "cargo"].includes(program) && verb === "publish") ||
      (program === "gh" && verb === "release" && action === "create")) return "publish";
  if ((["vercel", "netlify", "firebase", "wrangler"].includes(program) && verb === "deploy") ||
      (program === "kubectl" && (["apply", "delete", "replace", "set"].includes(verb) || (verb === "rollout" && ["restart", "undo", "pause", "resume"].includes(action)))) ||
      (program === "terraform" && ["apply", "destroy"].includes(verb))) return "deploy";
  if (program === "kubectl" && verb === "rollout" && ["status", "history"].includes(action)) return "remote_read";
  if (["mail", "mailx", "sendmail"].includes(program) ||
      (["gh", "glab"].includes(program) && ["issue", "pr", "mr"].includes(verb) && ["comment", "review", "create"].includes(action)) ||
      (program === "lark-cli" && verb === "im" && ["+send", "+reply"].includes(action))) return "send";
  if (program === "echo" || program === "pwd") return null;
  if (["cd", "ls", "cat", "head", "tail", "wc", "mkdir", "touch"].includes(program)) {
    return args.every(localPath) ? null : "unknown";
  }
  return "unknown";
}

// Read literal shell words, joined quotes and simple command lists. Anything requiring shell evaluation,
// including substitutions, redirection or an unmatched quote, must still go through confirmation.
function shellImpact(source: string): Impact {
  const normalized = source.replace(/^[ \t\n]+|[ \t\n]+$/g, "").replace(/\$\{IFS\}|\$IFS\b/g, " ");
  const tokens = /[ \t]+|&&|\|\||[;|\n]|(?:[\w./:@%,+=*-]+|'[^']*'|"[^"\\$`]*"|\\[\w-])+/gy;
  const words: string[] = [];
  let impact: Impact = null;
  let offset = 0;
  for (const match of normalized.matchAll(tokens)) {
    const [token] = match;
    offset += token.length;
    if (/^[ \t]/.test(token)) continue;
    if (/^[;&|\n]/.test(token)) {
      if (!words.length) return "unknown";
      impact ??= commandImpact(words);
      words.length = 0;
    } else {
      words.push(token.replace(/'([^']*)'|"([^"]*)"|\\([\w-])/g, (_, single, double, escaped) => single ?? double ?? escaped));
    }
  }
  if (offset !== normalized.length || (!words.length && !normalized.endsWith(";"))) return "unknown";
  if (words.length) impact ??= commandImpact(words);
  return impact ?? (normalized !== source && source.includes("$") ? "unknown" : null);
}

export function approvalImpact(ix: Interaction): Impact {
  if (ix.kind === "screen" && isCommandPrompt(ix.dialog)) {
    // The node reconstructs wrapped rows for display. They can explain a known effect, never prove locality.
    const displayed = ix.dialog?.command;
    return typeof displayed === "string" ? shellImpact(displayed) ?? "unknown" : "unknown";
  }
  if (ix.kind !== "permission") return null;
  if (ix.editable_text !== undefined) return "send";
  if (ix.outward_write) return "external";
  const tool = ix.tool_name ?? "";
  const input = ix.tool_input;
  if (input?.workdir !== undefined && !localPath(input.workdir)) return "unknown";
  if (input?.cwd !== undefined && !localPath(input.cwd)) return "unknown";
  const cwd = input?.workdir ?? input?.cwd;
  if (cwd === undefined && ix.cwd && !localPath(ix.cwd)) return "unknown";
  if (tool === "Edit") {
    // Codex sends just its first file in tool_input. The hub's remembered scopes can identify an external
    // destination, but they aren't the edit payload and may be absent (or omit a move destination).
    for (const key of ix.always_keys ?? []) {
      const target = /^codex-(?:edit|root):(.+)$/.exec(key)?.[1];
      if (target && !localPath(target)) return "external";
    }
    if (typeof input?.old_string !== "string" || typeof input.new_string !== "string") return "unknown";
  }
  if (["Read", "Write", "Edit", "MultiEdit"].includes(tool)) return localTarget(input?.file_path, cwd) ? null : "unknown";
  if (tool === "NotebookEdit") return localTarget(input?.notebook_path, cwd) ? null : "unknown";
  if (tool === "apply_patch") return patchImpact(input?.input, cwd);
  if (tool === "Grep" || tool === "Glob") return localTarget(input?.path ?? ".", cwd) ? null : "unknown";
  if (["Bash", "exec_command", "shell"].includes(tool)) {
    if (input?.shell || input?.env) return "unknown";
    if (input?.cmd !== undefined && input.command !== undefined) return "unknown";
    const cmd = input?.command ?? input?.cmd;
    if (typeof cmd === "string") return shellImpact(cmd);
    if (Array.isArray(cmd) && cmd.length && cmd.every(arg => typeof arg === "string")) {
      if (["sh", "bash", "/bin/sh", "/bin/bash"].includes(cmd[0]) && /^-l?c$/.test(cmd[1]) && cmd.length === 3) return shellImpact(cmd[2]);
      return commandImpact(cmd);
    }
    return "unknown";
  }
  const name = tool.split("__").pop()?.toLowerCase() ?? "";
  if (/(^|_)(push|push_files)($|_)/.test(name)) return "push";
  if (/(^|_)deploy($|_)/.test(name)) return "deploy";
  if (/(^|_)publish($|_)/.test(name)) return "publish";
  if (/(^|_)(send|reply|forward|comment|post_message)($|_)/.test(name)) return "send";
  return "unknown";
}

/** The negative answer in a command prompt read from the terminal (its own option key must be sent verbatim). */
export function declinesCommand(ix: Interaction, key: string): boolean {
  const option = ix.dialog?.options?.find(o => o.key === key && !o.kind);
  if (!option) return false;
  const label = option.label.trim().toLowerCase();
  // Short labels are terminal data. The longer forms are screen.ParseDialog and CodexOption.Role refusals.
  return ["no", "deny", "decline", "否", "拒绝", "いいえ", "拒否", "no, exit"].includes(label) || // i18n-ignore
    /^(?:no, and tell (?:claude|codex) what to do differently|no, (?:but )?continue without|cancel this request)\b/.test(label);
}
