// Pure local preview. The server reparses every save; shared fixtures pin parity.
export type RepositoryIdentity = { provider: string; instance_url: string; path: string; canonical_url: string };

const githubPages = new Set("tree blob raw blame commit commits pull pulls issues actions releases tags branches compare discussions wiki projects settings security network deployments packages graphs stargazers watchers forks".split(" "));
const credentialKeys = new Set(["token", "access_token", "private_token", "auth", "authorization", "password", "api_key", "apikey", "signature"]);

export function parseRepository(remote: string, provider = ""): RepositoryIdentity | undefined {
  if (!remote || remote.length > 2048 || /[\s\\]/u.test(remote)) return;
  if (remote.startsWith("git@") && !remote.includes("://")) {
    const scp = /^git@([^/:@]+):(.+)$/.exec(remote);
    if (!scp) return;
    remote = `ssh://git@${scp[1]}/${scp[2]}`;
  }
  // Preserve raw path components: URL would silently erase dot traversal.
  const match = /^(https?|ssh):\/\/([^/?#]+)(\/[^?#]*)?(\?[^#]*)?(#.*)?$/.exec(remote);
  if (!match) return;
  let [, scheme, authority, rawPath = "", query = "", fragment = ""] = match;
  if (scheme === "ssh") {
    if (!authority.startsWith("git@") || query || fragment) return;
    authority = authority.slice(4);
  }
  if (authority.includes("@") || authority.includes("%")) return;
  const hostPort = /^(\[[0-9a-fA-F:.]+\]|[^:]+)(?::([0-9]+))?$/.exec(authority);
  if (!hostPort) return;
  let host = hostPort[1].toLowerCase(), port = hostPort[2] ?? "";
  try {
    // Only validate the authority here, keeping the raw path above intact.
    const parsed = new URL(`https://${host}`);
    if (host.startsWith("[")) host = parsed.hostname;
    else if (parsed.hostname !== host || host.endsWith(".") || host.length > 253) return;
    if (!host.startsWith("[") && !/^\d+\.\d+\.\d+\.\d+$/.test(host)
      && !host.split(".").every((label) => /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(label))) return;
    if (query.includes(";") || /%(?![0-9a-f]{2})/i.test(query + fragment)) return;
    for (const key of new URLSearchParams(query).keys()) if (credentialKeys.has(key.toLowerCase())) return;
    for (const part of rawPath.split("/")) {
      if (decodeURIComponent(part).split("/").some((value) => value === "." || value === ".." || /[\\\0\r\n]/.test(value))) return;
    }
  } catch { return; }
  if (scheme === "ssh") {
    const alternative = host === "ssh.github.com" || host === "altssh.gitlab.com";
    if (port && port !== "22" && !(alternative && port === "443")) return;
    if (host === "ssh.github.com") host = "github.com";
    else if (host === "altssh.gitlab.com") host = "gitlab.com";
    scheme = "https"; port = "";
  } else if (host === "ssh.github.com" || host === "altssh.gitlab.com") return;
  const api = host === "api.github.com";
  if (host === "www.github.com" || api) host = "github.com";
  if (port && port !== (scheme === "http" ? "80" : "443")) {
    const number = Number(port);
    if (number < 1024 || number > 65535 || String(number) !== port) return;
  } else port = "";
  if ((host === "github.com" || host === "gitlab.com") && !port) scheme = "https";
  const known = host === "github.com" ? "github" : host === "gitlab.com" ? "gitlab" : "";
  provider ||= known;
  if (!["github", "gitlab"].includes(provider) || (known && known !== provider)) return;
  let parts = rawPath.replace(/^\//, "").replace(/\/$/, "").split("/");
  if (api) {
    if (parts.length < 3 || parts[0] !== "repos") return;
    parts = parts.slice(1);
  }
  if (match[1] !== "ssh") {
    if (provider === "github" && parts.length > 2) {
      if (!githubPages.has(parts[2])) return;
      parts = parts.slice(0, 2);
    } else if (provider === "gitlab" && parts.includes("-")) parts = parts.slice(0, parts.indexOf("-"));
  }
  parts[parts.length - 1] = parts.at(-1)!.replace(/\.git$/i, "");
  const path = parts.join("/").toLowerCase();
  if (parts.length < 2 || parts.length > 20 || (provider === "github" && parts.length !== 2) || path.length > 512
    || !parts.every((part) => /^[a-zA-Z0-9_][a-zA-Z0-9_.-]*$/.test(part) && !part.toLowerCase().endsWith(".git"))) return;
  const instance_url = `${scheme}://${host}${port ? `:${port}` : ""}`;
  return { provider, instance_url, path, canonical_url: `${instance_url}/${path}` };
}
