import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { defaultStateRoot, legacyEnvName } from '../../runtime-sdk/lib/compat.js'
import { stateDir } from '../../runtime-sdk/lib/identity.js'

export function daemonCandidates(home, options) {
  const roots = [stateDir(home), join(home, '.persona'), join(home, '.jarvis')]
  return [options.personadBin, options.jarvisdBin, ...roots.flatMap((root) => [join(root, 'bin', 'personad'), join(root, 'bin', 'jarvisd')])]
}
export function daemonBinary(home, options) {
  return daemonCandidates(home, options).find((p) => p && existsSync(p)) || join(defaultStateRoot(home), 'bin', 'personad')
}
const MCP_PROXY = fileURLToPath(new URL('../mcp-proxy.mjs', import.meta.url))
export function mcpServers(bin, pid, assistant) {
  const env = []
  for (const [name, value] of [['PERSONA_SESSION_ID', pid], ...(assistant ? [['PERSONA_ASSISTANT', '1']] : [])]) {
    env.push({ name, value }, { name: legacyEnvName(name), value })
  }
  // One server, not one per MCP_SERVER_NAMES: agy counts every declaration against its tool budget. An assistant
  // chat's tools go through mcp-proxy.mjs, so the ones agy defers stay reachable (#7333).
  if (!assistant) return [{ name: 'persona', command: bin, args: ['mcp'], env }]
  return [{ name: 'persona', command: process.execPath, args: [MCP_PROXY, bin, 'mcp'], env }]
}
export function assistantSearchPaths(home) {
  return [...new Set([join(stateDir(home), 'assistant', '.bin'), join(home, '.jarvis', 'assistant', '.bin'), join(home, '.local', 'bin')])]
}
