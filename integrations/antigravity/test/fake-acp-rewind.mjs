import { appendFileSync, existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { createInterface } from 'node:readline'
import { DatabaseSync } from 'node:sqlite'

if (process.argv.includes('models')) {
  const dir = process.argv[2]
  let catalog
  try { catalog = JSON.parse(readFileSync(`${dir}/trajectory.json`, 'utf8')).models } catch {}
  if (Array.isArray(catalog) && catalog.length) {
    for (const m of catalog) console.log(`${m.modelId}\t${m.name}`)
  } else {
    console.log('test-model\tTest Model')
  }
  process.exit(0)
}

const directory = process.argv[2]
const log = process.argv[process.argv.indexOf('--log-file') + 1]
const calls = `${directory}/calls.jsonl`
const loaded = new Set()
const send = (message) => process.stdout.write(JSON.stringify({ jsonrpc: '2.0', ...message }) + '\n')
const server = createServer(async (req, res) => {
  // Agent port discovery probes listeners with GET /; it is not a native call and must not kill the fake.
  if (req.method !== 'POST') {
    res.writeHead(404).end('{"message":"Unknown native method"}')
    return
  }
  const chunks = []
  for await (const chunk of req) chunks.push(chunk)
  const params = JSON.parse(Buffer.concat(chunks).toString())
  const method = req.url.split('/').at(-1)
  appendFileSync(calls, JSON.stringify({ worker: process.pid, method, params }) + '\n')
  const snapshot = JSON.parse(readFileSync(`${directory}/trajectory.json`, 'utf8'))
  if (snapshot.fail && method === 'RevertToCascadeStep') {
    res.writeHead(409).end(JSON.stringify({ message: 'Native rewind refused' }))
    return
  }
  if (method === 'GetCascadeTrajectory') {
    if (snapshot.requireLoad && params.disableRehydration && !loaded.has(params.cascadeId)) {
      res.writeHead(404).end('{"code":"not_found","message":"Cascade is not loaded"}')
      return
    }
    const hold = snapshot.holdTrajectoryOnce
    if (hold) {
      delete snapshot.holdTrajectoryOnce
      writeFileSync(`${directory}/trajectory.json`, JSON.stringify(snapshot))
      writeFileSync(`${directory}/trajectory-held`, '')
      while (!existsSync(`${directory}/release-trajectory`)) {
        await new Promise((resolve) => setTimeout(resolve, 20))
      }
    }
    const holdAfter = snapshot.holdAfterRevertOnce && snapshot.afterRevertArmed
    if (holdAfter) {
      delete snapshot.holdAfterRevertOnce
      delete snapshot.afterRevertArmed
      writeFileSync(`${directory}/trajectory.json`, JSON.stringify(snapshot))
      writeFileSync(`${directory}/after-trajectory-held`, '')
      while (!existsSync(`${directory}/release-after-trajectory`)) {
        await new Promise((resolve) => setTimeout(resolve, 20))
      }
    }
    if (!snapshot.trajectory.steps?.length) delete snapshot.trajectory.steps
    if (snapshot.afterRevertBadLength) {
      delete snapshot.afterRevertBadLength
      const response = { ...snapshot, trajectory: { ...snapshot.trajectory, steps: [...(snapshot.trajectory.steps || []), { type: 'CORTEX_STEP_TYPE_SYSTEM_MESSAGE' }] } }
      writeFileSync(`${directory}/trajectory.json`, JSON.stringify(snapshot))
      res.end(JSON.stringify(response))
      return
    }
    if (hold) {
      res.end(JSON.stringify(snapshot), () => writeFileSync(`${directory}/trajectory-response-sent`, ''))
      return
    }
    if (holdAfter) {
      res.end(JSON.stringify(snapshot))
      return
    }
    res.end(JSON.stringify(snapshot))
    return
  }
  if (method === 'GetCascadeTrajectorySteps') {
    while (JSON.parse(readFileSync(`${directory}/trajectory.json`, 'utf8')).holdErrors) {
      await new Promise((resolve) => setTimeout(resolve, 20))
    }
    if (snapshot.stepsUnavailable) {
      res.writeHead(503).end('{"message":"Native service unavailable"}')
      return
    }
    if (snapshot.stepsTimeout) {
      res.writeHead(504).end('{"message":"Antigravity conversation operation timed out"}')
      return
    }
    if (snapshot.stepsNotFoundAlways || (snapshot.stepsNotFoundUnlessLoaded && !loaded.has(params.cascadeId))) {
      res.writeHead(404).end('{"code":"not_found","message":"not_found: trajectory not found (error ID: test-id)"}')
      return
    }
    // Like agy for a conversation another process created: no steps until it is loaded.
    if (snapshot.stepsRequireLoad && !loaded.has(params.cascadeId)) {
      res.end('{}')
      return
    }
    res.end(JSON.stringify({ steps: (snapshot.trajectory.steps || []).slice(params.stepOffset) }))
    return
  }
  if (method === 'ForkConversation' && snapshot.forkId) {
    while (snapshot.holdFork && !existsSync(`${directory}/release-fork`)) {
      await new Promise((resolve) => setTimeout(resolve, 20))
    }
    const newCascadeId = snapshot.forkId
    const forkedAtStepIndex = params.forkAtStepIndex === -1 ? snapshot.trajectory.steps.length - 1 : params.forkAtStepIndex
    const brain = `${directory}/config/brain`
    const from = `${brain}/${params.sourceCascadeId}/.system_generated/logs/transcript_full.jsonl`
    const to = `${brain}/${newCascadeId}/.system_generated/logs`
    if (!snapshot.delayForkExport) {
      mkdirSync(to, { recursive: true })
      writeFileSync(`${to}/transcript_full.jsonl`, readFileSync(from, 'utf8').trimEnd().split('\n').slice(0, forkedAtStepIndex + 1).join('\n') + '\n')
    }
    res.end(JSON.stringify({ newCascadeId, forkedAtStepIndex }))
    return
  }
  if (method === 'RevertToCascadeStep') {
    if (snapshot.holdRevert) {
      for (const update of [
        { sessionUpdate: 'agent_message_chunk', content: { text: 'Stale reply.' } },
        { sessionUpdate: 'tool_call', toolCallId: 'stale-tool', name: 'read_file' },
        { sessionUpdate: 'session_info_update', title: 'Rewind in progress' },
      ]) send({ method: 'session/update', params: { sessionId: params.cascadeId, update } })
      while (!existsSync(`${directory}/release-revert`)) {
        await new Promise((resolve) => setTimeout(resolve, 20))
      }
      delete snapshot.holdRevert
    }
    snapshot.trajectory.steps = (snapshot.trajectory.steps || []).slice(0, params.stepIndex + 1)
    if (snapshot.holdAfterRevertOnce) snapshot.afterRevertArmed = true
    if (snapshot.badAfterRevertLength) snapshot.afterRevertBadLength = true
    writeFileSync(`${directory}/trajectory.json`, JSON.stringify(snapshot))
    const db = new DatabaseSync(`${directory}/config/conversation_summaries.db`)
    db.prepare('UPDATE conversation_summaries SET step_count = ?, last_modified_time = ? WHERE conversation_id = ?')
      .run(params.stepIndex + 1, new Date().toISOString(), params.cascadeId)
    db.close()
    res.end('{"metadata":{}}')
    return
  }
  res.writeHead(404).end('{"message":"Unknown native method"}')
})
await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
const openService = () => {
  writeFileSync(log, `Language server listening on random port at ${server.address().port} for HTTP\n`)
  writeFileSync(`${directory}/service-port`, String(server.address().port))
}
if (!JSON.parse(readFileSync(`${directory}/trajectory.json`, 'utf8')).lazyService) openService()
const input = createInterface({ input: process.stdin })
let prompts = 0
let heldPrompt
let pendingPermission
input.on('line', (line) => {
  const req = JSON.parse(line)
  appendFileSync(calls, JSON.stringify({ worker: process.pid, ...req }) + '\n')
  if (!req.method && pendingPermission && req.id === pendingPermission.permId) {
    const done = pendingPermission
    pendingPermission = undefined
    send({ id: done.promptId, result: { stopReason: 'end_turn' } })
    return
  }
  if (req.method === 'session/cancel' && heldPrompt) {
    heldPrompt()
    heldPrompt = undefined
  }
  if (req.id === undefined) return
  if (req.method === 'session/list') openService()
  const snapshot = JSON.parse(readFileSync(`${directory}/trajectory.json`, 'utf8'))
  if (req.method === 'session/new') {
    send({ id: req.id, result: { sessionId: snapshot.newSessionId || snapshot.trajectory?.cascadeId } })
    return
  }
  if (req.method === 'session/set_model' && snapshot.invalidSessionOnModelOnce) {
    delete snapshot.invalidSessionOnModelOnce
    writeFileSync(`${directory}/trajectory.json`, JSON.stringify(snapshot))
    send({ id: req.id, error: { code: -32602, message: 'Session not found. Load it with session/load' } })
    return
  }
  if (req.method === 'session/set_model' && snapshot.exitOnModelOnce) {
    delete snapshot.exitOnModelOnce
    writeFileSync(`${directory}/trajectory.json`, JSON.stringify(snapshot))
    process.exit(1)
  }
  if (req.method === 'session/set_model' && Array.isArray(snapshot.rejectModels) && snapshot.rejectModels.includes(req.params?.modelId)) {
    send({ id: req.id, error: { code: -32000, message: `Unknown model ID: ${req.params.modelId}` } })
    return
  }
  if (req.method === 'session/set_config_option' && snapshot.invalidSessionOnConfigOnce) {
    delete snapshot.invalidSessionOnConfigOnce
    writeFileSync(`${directory}/trajectory.json`, JSON.stringify(snapshot))
    send({ id: req.id, error: { code: -32602, message: 'Session not found. Load it with session/load' } })
    return
  }
  if (req.method === 'session/set_config_option' && snapshot.exitOnConfigOnce) {
    delete snapshot.exitOnConfigOnce
    writeFileSync(`${directory}/trajectory.json`, JSON.stringify(snapshot))
    process.exit(1)
  }
  if (req.method === 'session/set_config_option' && Array.isArray(snapshot.rejectConfigOptions) && snapshot.rejectConfigOptions.includes(req.params?.configId)) {
    send({ id: req.id, error: { code: -32000, message: `Unsupported config option: ${req.params.configId}` } })
    return
  }
  if (req.method === 'session/resume' && snapshot.resumeError) {
    send({ id: req.id, error: { code: -32603, message: snapshot.resumeError } })
    return
  }
  if (req.method === 'session/resume' || req.method === 'session/load') {
    loaded.add(req.params.sessionId)
    if (snapshot.resumeDefaults) {
      const def = snapshot.resumeDefaults
      const res = {}
      if (def.model) res.models = { availableModels: snapshot.models || [], currentModelId: def.model }
      if (def.mode) res.modes = { currentModeId: def.mode }
      if (def.configOptions) res.configOptions = def.configOptions
      send({ id: req.id, result: res })
      return
    }
  }
  if (req.method === 'session/prompt' && snapshot.askPermissionOnPrompt && req.params.sessionId === snapshot.forkId) {
    const permId = `fork-perm-${++prompts}`
    pendingPermission = { permId, promptId: req.id }
    send({
      id: permId,
      method: 'session/request_permission',
      params: {
        sessionId: req.params.sessionId,
        toolCall: { toolCallId: permId, name: 'run_command', rawInput: { CommandLine: 'git status' } },
        options: [
          { optionId: 'always', name: 'Always allow', kind: 'allow_always' },
          { optionId: 'allow', name: 'Allow once', kind: 'allow_once' },
          { optionId: 'reject', name: 'Reject', kind: 'reject_once' },
        ],
      },
    })
    return
  }
  if (req.method === 'session/prompt' && snapshot.promptPlan) {
    const plan = snapshot.promptPlan[prompts++] || {}
    const sessionId = req.params.sessionId
    const update = (value) => send({ method: 'session/update', params: { sessionId, update: value } })
    if (plan.thinking) update({ sessionUpdate: 'agent_thought_chunk', content: { text: plan.thinking } })
    const tool = { sessionUpdate: 'tool_call_update', toolCallId: 'test-tool', status: plan.toolFailed ? 'failed' : 'completed' }
    if (plan.toolStart || plan.toolStartOnly) {
      update({ sessionUpdate: 'tool_call', toolCallId: 'test-tool', name: 'run_command', status: 'in_progress' })
      if (!plan.toolStartOnly) update(tool)
    }
    if (plan.reply && !plan.exportOnly) update({ sessionUpdate: 'agent_message_chunk', content: { text: plan.reply } })
    if (plan.tool && !plan.toolStart && !plan.toolStartOnly) update(tool)
    if (plan.lateTool) setTimeout(() => update(tool), plan.lateTool)
    if (plan.wakeOnFile) {
      const waitWake = () => {
        if (existsSync(`${directory}/${plan.wakeOnFile}`)) {
          update({ sessionUpdate: 'tool_call', toolCallId: 'wake-tool', name: 'run_command', status: 'in_progress' })
          return
        }
        setTimeout(waitWake, 20)
      }
      setTimeout(waitWake, 20)
    }
    if (plan.export || plan.flushReply) {
      const transcript = `${directory}/config/brain/${sessionId}/.system_generated/logs/transcript_full.jsonl`
      let index = readFileSync(transcript, 'utf8').trim().split('\n').length
      const now = new Date().toISOString()
      const created_at = plan.wholeSeconds ? now.replace(/\.\d{3}Z$/, 'Z') : now
      const append = (type, fields) => appendFileSync(transcript,
        JSON.stringify({ type, ...fields, step_index: index++, status: 'DONE', created_at }) + '\n')
      if (plan.flushReply) {
        append('USER_INPUT', { content: plan.flushReply.prompt })
        append('PLANNER_RESPONSE', { content: plan.flushReply.reply })
      }
      if (plan.export) {
        append('USER_INPUT', { content: (plan.planMode ? '/plan ' : '') + req.params.prompt[0].text })
        if (plan.undatedStep) appendFileSync(transcript,
          JSON.stringify({ type: 'SYSTEM_MESSAGE', content: 'System notice.', step_index: index++, status: 'DONE' }) + '\n')
        if (plan.error) append('ERROR_MESSAGE', { content: plan.error })
        if ((plan.reply !== undefined && !plan.streamOnly) || plan.thinking || plan.calls) {
          let content = plan.reply
          const extra = {}
          if (plan.reply && plan.truncateReply) {
            const { head, tail } = plan.truncateReply
            const omitted = Buffer.byteLength(plan.reply) - Buffer.byteLength(head) - Buffer.byteLength(tail)
            content = `${head}<truncated ${omitted} bytes>${tail}`
            extra.truncated_fields = ['content']
          }
          append('PLANNER_RESPONSE', { content, thinking: plan.thinking, tool_calls: plan.calls, ...extra })
        }
        if (plan.toolOutput !== undefined) {
          append('GENERIC', { content: plan.toolOutput, status: plan.toolStatus || 'DONE' })
        }
      }
    }
    if (plan.error && !plan.export) return send({ id: req.id, error: { code: -32000, message: plan.error } })
    const finish = () => send({ id: req.id, result: { stopReason: plan.stopReason || 'end_turn' } })
    if (plan.holdForCancel) heldPrompt = finish
    else finish()
    return
  }
  if (req.method === 'session/prompt' && snapshot.promptError) {
    process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: req.id, error: { code: -32000, message: snapshot.promptError } }) + '\n')
    return
  }
  if (req.method === 'session/prompt' && snapshot.promptReply) {
    send({ method: 'session/update', params: { sessionId: req.params.sessionId,
      update: { sessionUpdate: 'agent_message_chunk', content: { text: snapshot.promptReply } } } })
  }
  process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: req.id, result: {} }) + '\n')
})
input.on('close', () => server.close(() => process.exit(0)))
