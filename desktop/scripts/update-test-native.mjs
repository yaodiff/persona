// Bounded, injected command operations for update-test native ownership checks and cleanup.
// Production supplies run(); tests supply a double. No helper in this file invokes a platform command itself.
const isRegistryAbsent = result => result.code === 1 && /unable to find the specified (?:registry key|registry key or value)|cannot find the path specified/i.test(result.text ?? "");
export async function inspectWindowsRegistryKeys({ keys, run }) {
  const script = `$ErrorActionPreference='Stop'; ${JSON.stringify(keys)} | ForEach-Object { $key = $_ -replace '^HKCU\\\\', 'HKCU:\\'; [PSCustomObject]@{ Name = $_; Exists = Test-Path -LiteralPath $key; InstallLocation = (Get-ItemPropertyValue -LiteralPath $key -Name InstallLocation -ErrorAction SilentlyContinue) } } | ConvertTo-Json -Compress`;
  const { code, text } = await run("powershell", ["-NoProfile", "-NonInteractive", "-Command", script], { seconds: 60, quiet: true });
  if (code !== 0) throw new Error(`could not inspect this run's NSIS registry keys (exit ${code})`);
  const parsed = JSON.parse(text);
  const entries = Array.isArray(parsed) ? parsed : [parsed];
  return Object.fromEntries(entries.map(entry => [entry.Name, { exists: entry.Exists === true, installLocation: typeof entry.InstallLocation === "string" && entry.InstallLocation.length > 0 ? entry.InstallLocation : null }]));
}

/** A bounded diagnostic query; native NSIS behavior still requires execution on Windows. */
export async function readWindowsInstallLocations({ keys, run }) {
  const locations = {};
  for (const key of keys) {
    const query = await run("reg", ["query", key, "/v", "InstallLocation"], { seconds: 60, quiet: true })
      .catch(error => ({ code: -1, error }));
    // Exit 1 alone is ambiguous: access denied is not absence. Only the explicit not-found diagnostic qualifies.
    locations[key] = isRegistryAbsent(query)
      ? { exists: false }
      : query.code !== 0
        ? { error: query.error?.message ?? `exit ${query.code}${query.text ? `: ${query.text}` : ""}` }
        : { exists: true, ...parseWindowsInstallLocation(query.text) };
  }
  return locations;
}

export function parseWindowsInstallLocation(output) {
  const match = /(?:^|\r?\n)\s*InstallLocation\s+REG_SZ\s+([^\r\n]+)/.exec(output);
  return { installLocation: match?.[1].trim() || null };
}

export async function removeOwnedWindowsRegistryKeys({ keys, run }) {
  const failures = [];
  for (const key of keys) {
    const query = await run("reg", ["query", key], { seconds: 60, quiet: true }).catch(error => ({ code: -1, error }));
    if (query.code !== 0) {
      if (!isRegistryAbsent(query)) failures.push(`NSIS registry query failed for ${key}: ${query.error?.message ?? `exit ${query.code}${query.text ? `: ${query.text}` : ""}`}`);
      continue;
    }
    const result = await run("reg", ["delete", key, "/f"], { seconds: 60, quiet: true }).catch(error => ({ code: -1, error }));
    if (result.code !== 0) failures.push(`NSIS registry cleanup failed for ${key}: ${result.error?.message ?? `exit ${result.code}`}`);
  }
  for (const key of keys) {
    const after = await run("reg", ["query", key], { seconds: 60, quiet: true }).catch(error => ({ code: -1, error }));
    if (after.code === 0) failures.push(`NSIS registry key remains after cleanup: ${key}`);
    else if (!isRegistryAbsent(after)) failures.push(`NSIS registry verification failed for ${key}: ${after.error?.message ?? `exit ${after.code}${after.text ? `: ${after.text}` : ""}`}`);
  }
  return failures;
}

export async function removeOwnedShipItJob({ label, uid, run }) {
  const domain = `gui/${uid}`;
  const inspect = await run("launchctl", ["print", `${domain}/${label}`], { seconds: 30, quiet: true }).catch(error => ({ code: -1, error }));
  if (inspect.code !== 0) {
    if (inspect.code === 113) return [];
    return [`could not inspect this run's ShipIt job: ${inspect.error?.message ?? `exit ${inspect.code}`}`];
  }
  const removed = await run("launchctl", ["bootout", `${domain}/${label}`], { seconds: 30, quiet: true }).catch(error => ({ code: -1, error }));
  if (removed.code !== 0) return [`ShipIt job removal failed: ${removed.error?.message ?? `exit ${removed.code}`}`];
  const after = await run("launchctl", ["print", `${domain}/${label}`], { seconds: 30, quiet: true }).catch(error => ({ code: -1, error }));
  return after.code === 113 ? [] : [`ShipIt job remains registered after removal (query exit ${after.code})`];
}
