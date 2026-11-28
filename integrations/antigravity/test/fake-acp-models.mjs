import { appendFileSync, readFileSync } from 'node:fs'
import { createInterface } from 'node:readline'

const directory = process.argv[2]
if (process.argv.includes('--warm')) process.exit(0)
const settings = () => JSON.parse(readFileSync(`${directory}/catalog.json`, 'utf8'))
const nativeSettings = () => {
  try { return JSON.parse(readFileSync(`${directory}/settings/settings.json`, 'utf8')) } catch { return {} }
}
if (process.argv.includes('models')) {
  const catalog = settings()
  appendFileSync(`${directory}/models-calls.jsonl`, `${JSON.stringify({ at: Date.now() })}\n`)
  // An expired sign-in: agy models never answers (#11106).
  if (catalog.hang) setInterval(() => {}, 1 << 30)
  else {
  if (catalog.error) { console.error(catalog.error); process.exit(1) }
  console.error('Fetching available models...')
  for (const m of catalog.models) console.log(`${m.modelId}\t${m.name}`)
  process.exit(0)
  }
}
const id = '11111111-2222-3333-4444-555555555555'
const effortSuffix = /-(low|medium|high|xhigh|max)$/
let model = 'gemini-future-medium'
let effort = 'medium'
let mode = 'default'
const configuration = () => [
  { id: 'model', category: 'model', currentValue: model.replace(effortSuffix, '') },
  { id: 'thought_level', currentValue: effort },
  {
    id: 'mode',
    currentValue: mode,
    ...(settings().availableModes ? { options: settings().availableModes.map((entry) => ({ value: entry.id, name: entry.name || entry.id })) } : {}),
  },
]
let pendingPrompt
let pendingPermissions
let loaded = false
let settingsAgentMode
const send = (value) => process.stdout.write(`${JSON.stringify(value)}\n`)

const reportWindow = () => {
  const size = settings().windows?.[model]
  if (size !== undefined) send({ jsonrpc: '2.0', method: 'session/update', params: { sessionId: id, update: { sessionUpdate: 'usage_update', used: 0, size } } })
}

// Opt-in native settings updates: agy 1.5.12 reports default when it refuses auto.
const nativeAgentMode = () => {
  const requested = nativeSettings().agentMode || 'default'
  if (requested === 'auto' && settings().refuseAuto) return 'default'
  if (requested === 'acceptEdits' || requested === 'bypassPermissions') return 'accept-edits'
  return requested
}
const syncSettingsMode = () => {
  if (!loaded || !settings().settingsModeUpdates) return
  const requested = nativeSettings().agentMode || 'default'
  if (requested === settingsAgentMode) return
  settingsAgentMode = requested
  mode = nativeAgentMode()
  send({ jsonrpc: '2.0', method: 'session/update', params: { sessionId: id, update: { sessionUpdate: 'current_mode_update', currentModeId: mode } } })
}

const readPermissions = [
  { id: 'read-permission-git-status', toolCallId: 'git-status', title: 'run_command', rawInput: { command: 'git status' } },
  { id: 'read-permission-rg', toolCallId: 'rg', title: 'run_command', rawInput: { command: 'rg "agentMode" settings.json' } },
  { id: 'read-permission-mcp', toolCallId: 'mcp-read', title: 'mcp_persona_messages_read', rawInput: { limit: 1 } },
]
const mixedPermissions = [
  {
    id: 'read-permission-bash', toolCallId: 'bash-write', title: 'run_command', rawInput: { command: 'git add -A && gh pr merge 1' },
    options: [
      { optionId: 'reject', name: 'Reject', kind: 'reject_once' },
      { optionId: 'always', name: 'Always allow', kind: 'allow_always' },
    ],
  },
  {
    id: 'read-permission-edit', toolCallId: 'edit-file', title: 'edit_file', rawInput: { Path: '/tmp/a.go', Target: 'a', Replacement: 'b' },
    options: [
      { optionId: 'reject', name: 'Reject', kind: 'reject_once' },
      { optionId: 'once', name: 'Allow once', kind: 'allow_once' },
    ],
  },
  {
    id: 'read-permission-write', toolCallId: 'write-file', title: 'write_file', rawInput: { Path: '/tmp/b.go', Content: 'package b' },
    options: [
      { optionId: 'reject', name: 'Reject', kind: 'reject_always' },
      { optionId: 'always-write', name: 'Always allow', kind: 'allow_always' },
    ],
  },
]
const askPermissions = (request, permissions, result = { stopReason: 'end_turn' }) => {
  pendingPermissions = { promptId: request.id, open: new Set(), result }
  for (const permission of permissions) {
    pendingPermissions.open.add(permission.id)
    appendFileSync(`${directory}/calls.jsonl`, `${JSON.stringify({ method: 'fake/permission', id: permission.id })}\n`)
    send({ jsonrpc: '2.0', id: permission.id, method: 'session/request_permission', params: {
      sessionId: id,
      toolCall: { toolCallId: permission.toolCallId, title: permission.title, rawInput: permission.rawInput },
      options: permission.options ?? [
        { optionId: 'always', name: 'Always allow', kind: 'allow_always' },
        { optionId: 'allow', name: 'Allow', kind: 'allow_once' },
        { optionId: 'reject', name: 'Reject', kind: 'reject_once' },
      ],
    } })
  }
}
const answerPermission = (request) => {
  if (!pendingPermissions?.open.delete(request.id)) return false
  if (pendingPermissions.open.size === 0) {
    const { promptId, result } = pendingPermissions
    pendingPermissions = undefined
    send({ jsonrpc: '2.0', id: promptId, result })
  }
  return true
}
const complete = (request, result, error) => {
  const { method, params } = request
  if (request.id !== undefined) process.stdout.write(`${JSON.stringify({ jsonrpc: '2.0', id: request.id, ...(error ? { error } : { result }) })}\n`)
  if (method === 'session/set_mode' && settings().autoThenDefault && params.modeId === 'auto') {
    send({ jsonrpc: '2.0', method: 'session/update', params: { sessionId: id, update: { sessionUpdate: 'current_mode_update', currentModeId: 'auto' } } })
    setImmediate(() => send({ jsonrpc: '2.0', method: 'session/update', params: { sessionId: id, update: { sessionUpdate: 'current_mode_update', currentModeId: 'default' } } }))
  }
  if ((method === 'session/set_mode' || (method === 'session/set_model' && !settings().omitSetModelConfigUpdate)) && !error) process.stdout.write(`${JSON.stringify({ jsonrpc: '2.0', method: 'session/update', params: { sessionId: id, update: { sessionUpdate: 'config_option_update', configOptions: configuration() } } })}\n`)
}

createInterface({ input: process.stdin }).on('line', (line) => {
  const request = JSON.parse(line)
  appendFileSync(`${directory}/calls.jsonl`, `${line}\n`)
  const { method, params } = request
  if (request.id === 'check-permission' && !method) {
    send({ jsonrpc: '2.0', method: 'session/update', params: { sessionId: id, update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'OK' } } } })
    send({ jsonrpc: '2.0', id: pendingPrompt, result: { stopReason: 'end_turn' } })
    return
  }
  if (!method && answerPermission(request)) return
  if (!method) return
  let result = {}
  let error
  if (method === 'initialize') result = { protocolVersion: 1, agentInfo: { version: 'test' } }
  if (method === 'session/new' || method === 'session/resume') {
    loaded = true
    if (method === 'session/resume' && settings().resumeModel) {
      model = settings().resumeModel
      effort = effortSuffix.exec(model)?.[1]
    }
    if (method === 'session/resume' && settings().resetModeOnResume) mode = 'default'
    reportWindow()
    settingsAgentMode = nativeSettings().agentMode || 'default'
    if (method === 'session/resume' && settings().configUpdateOnResume) {
      const configOptions = settings().configUpdateOnResume === 'effortOnly'
        ? [{ id: 'thought_level', currentValue: effort }]
        : settings().configUpdateOnResume === 'modelOnly'
          ? [{ id: 'model', category: 'model', currentValue: model.replace(effortSuffix, '') }]
          : configuration()
      send({ jsonrpc: '2.0', method: 'session/update', params: { sessionId: id, update: { sessionUpdate: 'config_option_update', configOptions } } })
    }
    const modes = settings().availableModes ? { currentModeId: mode, availableModes: settings().availableModes } : { currentModeId: mode }
    result = {
      sessionId: id,
      models: { availableModels: settings().models, currentModelId: model },
      ...(method === 'session/resume' && settings().omitResumeModes ? {} : { modes }),
      ...(method === 'session/resume' && settings().omitResumeConfig ? {} : { configOptions: configuration() }),
    }
    if (method === 'session/resume' && settings().askPermissionOnResume) {
      askPermissions(request, readPermissions.slice(0, 1), result)
      return
    }
  }
  if (method === 'session/prompt') {
    if (settings().resumeModel) {
      appendFileSync(`${directory}/calls.jsonl`, `${JSON.stringify({ method: 'fake/prompt_model', model })}\n`)
    }
    syncSettingsMode()
    if (settings().refuseAutoOnPrompt) {
      mode = 'default'
      send({ jsonrpc: '2.0', method: 'session/update', params: { sessionId: id, update: { sessionUpdate: 'current_mode_update', currentModeId: 'default' } } })
    }
    const text = params.prompt?.find((block) => block.type === 'text')?.text?.trim()
    if (text === 'ask-read-permission' || text === 'ask-read-permissions' || text === 'ask-mixed-permissions') {
      const batch = text === 'ask-read-permission'
        ? readPermissions.slice(0, 1)
        : text === 'ask-mixed-permissions' ? mixedPermissions : readPermissions
      askPermissions(request, batch)
      return
    }
    if (settings().holdPrompt) return
    if (settings().promptError) error = { code: -32000, message: settings().promptError }
    else if (settings().askPermission) {
      pendingPrompt = request.id
      send({ jsonrpc: '2.0', id: 'check-permission', method: 'session/request_permission', params: { sessionId: id, toolCall: { title: 'run_command', input: { command: 'pwd' } }, options: [{ kind: 'allow_once', optionId: 'allow' }] } })
      return
    } else {
      send({ jsonrpc: '2.0', method: 'session/update', params: { sessionId: id, update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'OK' } } } })
      result = { stopReason: 'end_turn' }
    }
  }
  if (method === 'session/set_model') {
    if (!settings().models.some((entry) => entry.modelId === params.modelId)) error = { code: -32602, message: 'Model unavailable' }
    else {
      model = params.modelId
      effort = effortSuffix.exec(model)?.[1]
      reportWindow()
    }
  }
  // An Antigravity CLI from before session/set_mode (#14533) answers it as an unknown method.
  if (method === 'session/set_mode' && settings().noSetMode) {
    complete(request, undefined, { code: -32601, message: `Method not found: ${method}` })
    return
  }
  if (method === 'session/set_mode') {
    if (params.modeId === 'accept-edits' && settings().holdFallback) {
      const timer = setInterval(() => {
        if (!settings().releaseFallback) return
        clearInterval(timer)
        mode = params.modeId
        complete(request, {})
      }, 5)
      timer.unref?.()
      return
    }
    if (params.modeId === 'auto' && settings().rejectAuto) {
      error = { code: -32602, message: 'Auto mode is not available: it needs the OS terminal sandbox and may not be rolled out to you yet' }
    } else if (params.modeId === 'auto' && settings().autoThenDefault) {
      mode = 'auto'
      result = { modes: { currentModeId: mode }, configOptions: configuration() }
      mode = 'default'
    } else if (params.modeId === 'auto' && settings().refuseAuto) {
      mode = 'default'
      result = { modes: { currentModeId: mode }, configOptions: configuration() }
    } else mode = params.modeId
  }
  if (method === 'session/set_config_option') {
    const nextModel = settings().effortFallback?.[params.value] ?? `${model.replace(effortSuffix, '')}-${params.value}`
    if (!settings().models.some((entry) => entry.modelId === nextModel)) error = { code: -32602, message: 'Thinking level unavailable' }
    else { effort = params.value; model = nextModel; result = { configOptions: configuration() } }
  }
  complete(request, result, error)
})
