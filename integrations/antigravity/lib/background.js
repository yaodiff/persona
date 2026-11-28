const startPrefix = 'Tool is running as a background task with task id:'
const endNotice = /\bTask id "([^"\n]+)" (?:was )?(finished|failed|canceled|cancelled|killed|stopped)\b/i
const taskMessage = /^[ \t]*\[Message\] timestamp=(\S+) sender=(\S+) priority=\S+ content=([\s\S]*)/m
const cronIteration = /\(iteration ([0-9]+)\)$/
const timerDescRe = /^Timer:\s*((?:\d+(?:\.\d+)?[hms]\s*)+)(?:,|$)/i
const timerUnitRe = /(\d+(?:\.\d+)?)([hms])/gi
const heartbeatNotice = /^(?:\[Notice\]\s+)?Task(?:\s+id\s+"[^"\n]+")?\s+is\s+still\s+running\b/i
const serverRestartNotice = /^(?:\[Notice\]\s+)?All your subagents and background tasks have been stopped due to server restart\b/i
const subagentIdleRe = /^Subagent ([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}) has gone idle\b/i
const genericHeaderRe = /^(?:Created At: \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})\r?\nCompleted At: \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})(?:\r?\n|$))+/
const emptyTaskList = 'No background tasks are currently running.'
const taskListHeader = /^You have ([1-9][0-9]*) background task\(s\) currently running:$/
const taskListRow = /^- .* \(`([^`\s]+)`\)$/
const taskStatusHeader = /^Task:\s*(\S+)\r?\nStatus:\s*([A-Za-z_]+)(?:\r?\n|$)/
const emptySubagentList = /^You have 0 active subagent\(s\):(?:\s*\[\])?$/
const embeddedUuidRe = /\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b/gi
const exactUuidRe = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/
const terminalStatuses = new Map([
  ['DONE', 'completed'], ['COMPLETED', 'completed'], ['FINISHED', 'completed'],
  ['ERROR', 'failed'], ['FAILED', 'failed'],
  ['CANCELED', 'killed'], ['CANCELLED', 'killed'], ['KILLED', 'killed'], ['STOPPED', 'killed'],
])
const kinds = new Map([['schedule', 'monitor'], ['run_workflow', 'workflow'], ['invoke_subagent', 'agent']])
const timerExpiryGraceMs = 60_000
const retainedForMs = 24 * 60 * 60 * 1000
const endedLimit = 20
const runningLimit = 50
const taskName = /^task-[0-9]+$/
const liveStep = new Set(['RUNNING', 'PENDING', 'IN_PROGRESS', 'QUEUED'])

// Step parsing can replace a GENERIC result with its final output. Keep the
// original task notice as private metadata so the outcome still has an ID.
export const backgroundStart = Symbol('backgroundStart')

export function backgroundNotice(content) {
  if (typeof content !== 'string') return ''
  content = content.trim()
  if (content.startsWith('Created At:')) {
    const newline = content.indexOf('\n')
    content = newline < 0 ? '' : content.slice(newline + 1).trimStart()
  }
  return content.startsWith(startPrefix) ? content.slice(startPrefix.length) : ''
}

export function priorBackgroundNotice(step) {
  if (step[backgroundStart]) return step[backgroundStart]
  if (liveStep.has(String(step.status).trim().toUpperCase()) && backgroundNotice(step.content)) return step.content
  return ''
}

function toolString(value) {
  if (typeof value !== 'string') return ''
  if (value.startsWith('"')) {
    try { return JSON.parse(value) } catch { /* The argument was already decoded. */ }
  }
  return value
}

function toolNumber(value) {
  if (typeof value === 'number') return Number.isFinite(value) ? value : Number.NaN
  if (typeof value !== 'string') return Number.NaN
  const text = toolString(value).trim()
  if (!text) return Number.NaN
  const n = Number(text)
  return Number.isFinite(n) ? n : Number.NaN
}

function toolStringArray(value) {
  if (Array.isArray(value)) return value.filter(v => typeof v === 'string')
  if (typeof value === 'string') {
    try {
      const parsed = JSON.parse(value)
      if (Array.isArray(parsed)) return parsed.filter(v => typeof v === 'string')
    } catch { /* Ignore non-JSON string values. */ }
  }
  return []
}

function parseTimerDescriptionMs(description) {
  if (typeof description !== 'string') return 0
  const m = timerDescRe.exec(description.trim())
  if (!m) return 0
  let totalSec = 0
  for (const unit of m[1].matchAll(timerUnitRe)) {
    const n = Number(unit[1])
    if (!Number.isFinite(n) || n <= 0) continue
    const mult = unit[2].toLowerCase() === 'h' ? 3600 : unit[2].toLowerCase() === 'm' ? 60 : 1
    totalSec += n * mult
  }
  return totalSec > 0 && totalSec < 1e9 ? Math.round(totalSec * 1000) : 0
}

function scheduleConfig(description, call, conversation) {
  const args = call?.name === 'schedule' && call.args && typeof call.args === 'object' ? call.args : undefined
  const rawMax = toolNumber(args?.MaxIterations)
  const maxIterations = Number.isInteger(rawMax) && rawMax > 0 ? rawMax : 0
  if (description?.startsWith('Cron:') || toolString(args?.CronExpression).trim()) {
    return { oneShot: false, condition: '', durationMs: 0, maxIterations }
  }
  const hasDuration = args?.DurationSeconds != null
  if (hasDuration || description?.startsWith('Timer:')) {
    let condition = toolString(args?.TimerCondition).trim()
    const lower = condition.toLowerCase()
    if (lower === 'never') condition = ''
    else if (lower === 'any') condition = 'any'
    else if (condition) condition = exactUuidRe.test(lower) ? lower : qualifiedTaskId(conversation, condition)
    const seconds = toolNumber(args?.DurationSeconds)
    const durationMs = seconds > 0 && seconds < 1e9
      ? Math.round(seconds * 1000)
      : parseTimerDescriptionMs(description)
    return { oneShot: true, condition, durationMs, maxIterations: 0 }
  }
  return { oneShot: false, condition: '', durationMs: 0, maxIterations }
}

function parseTime(value) {
  if (typeof value !== 'string' || !value) return Number.NaN
  return Date.parse(value)
}

function qualifiedTaskId(conversation, id) {
  return taskName.test(id) ? `${conversation}/${id}` : id
}

function isOwnTaskId(conversation, id) {
  const prefix = conversation + '/'
  return id.startsWith(prefix) && taskName.test(id.slice(prefix.length))
}

function toolOutput(content) {
  if (typeof content !== 'string') return ''
  if (content.startsWith('Created At: ')) {
    const match = genericHeaderRe.exec(content)
    if (match) return content.slice(match[0].length).replace(/^Output:\r?\n/, '')
  }
  const outIdx = content.indexOf('\nOutput:\n')
  return outIdx >= 0 && content.startsWith('Created At:') ? content.slice(outIdx + '\nOutput:\n'.length) : content
}

function systemMessageBody(content) {
  let trimmed = String(content ?? '').trim()
  const open = trimmed.lastIndexOf('<SYSTEM_MESSAGE>')
  if (open >= 0) trimmed = trimmed.slice(open + '<SYSTEM_MESSAGE>'.length)
  return trimmed.trim().replace(/<\/SYSTEM_MESSAGE>\s*$/, '').trim()
}

function serverRestartEvent(content, message, createdAt, order) {
  if (message) {
    if (message[2] !== 'system') return null
    const body = message[3].trim().replace(/<\/SYSTEM_MESSAGE>\s*$/, '').trim()
    if (!serverRestartNotice.test(body)) return null
    const at = parseTime(createdAt)
    if (Number.isFinite(at)) return { at, rawAt: createdAt, order }
    const fallback = parseTime(message[1])
    return { at: fallback, rawAt: Number.isFinite(fallback) ? message[1] : createdAt, order }
  }
  if (!serverRestartNotice.test(systemMessageBody(content))) return null
  return { at: parseTime(createdAt), rawAt: createdAt, order }
}

function validToolResult(step, slot, calledAt, at) {
  if (step.error || String(step.status ?? '').toUpperCase() !== 'DONE' ||
    !Number.isInteger(step.step_index) || step.step_index < 0 ||
    !Number.isInteger(slot.planner?.step_index) || slot.planner.step_index < 0 ||
    !Number.isFinite(calledAt) || !Number.isFinite(at) || at < calledAt ||
    slot.planner.truncated_fields?.includes('tool_calls')) {
    return false
  }
  // The tool's timestamp can predate the result step when an old result is exported late.
  if (typeof step.content === 'string' && step.content.startsWith('Created At: ')) {
    const newline = step.content.indexOf('\n')
    const created = (newline < 0 ? step.content : step.content.slice(0, newline)).slice('Created At: '.length).trim()
    const started = parseTime(created)
    if (!Number.isFinite(started) || started < calledAt) return false
  }
  return true
}

// Accepts complete, unfiltered manage_task(Action="list") results for this conversation.
function taskInventory(step, slot, calledAt, at, conversation) {
  const call = slot?.tool
  if (call?.name !== 'manage_task' || !validToolResult(step, slot, calledAt, at) ||
    step.truncated_fields?.includes('content')) {
    return null
  }
  const args = call.args
  if (!args || typeof args !== 'object' || Array.isArray(args) ||
    toolString(args.Action).toLowerCase() !== 'list') {
    return null
  }
  // A nonempty selector or an unknown option may make the inventory a subset.
  for (const [key, value] of Object.entries(args)) {
    if (key === 'Action') continue
    if (key === 'TaskId' && (value == null || (typeof value === 'string' && toolString(value) === ''))) continue
    return null
  }
  const output = toolOutput(step.content).trim()
  if (output === emptyTaskList) return new Set()
  const newline = output.indexOf('\n')
  if (newline < 0) return null
  const header = taskListHeader.exec(output.slice(0, newline).trim())
  if (!header) return null
  const count = Number(header[1])
  const body = output.slice(newline + 1).trim()
  const ids = new Set()
  if (body.startsWith('[')) {
    let items
    try { items = JSON.parse(body) } catch { return null }
    if (!Array.isArray(items) || items.length !== count) return null
    for (const item of items) {
      if (!item || typeof item !== 'object' || typeof item.taskId !== 'string') return null
      const id = qualifiedTaskId(conversation, item.taskId.trim())
      if (!isOwnTaskId(conversation, id) || ids.has(id)) return null
      ids.add(id)
    }
    return ids.size === count ? ids : null
  }
  for (const line of body.split('\n')) {
    const match = taskListRow.exec(line.trim())
    if (!match) return null
    const id = qualifiedTaskId(conversation, match[1])
    if (!isOwnTaskId(conversation, id) || ids.has(id)) return null
    ids.add(id)
  }
  return ids.size === count ? ids : null
}

// Accepts authoritative manage_task(Action="status", TaskId=...) results matching the requested task.
function taskStatusObservation(step, slot, calledAt, at, conversation) {
  const call = slot?.tool
  if (call?.name !== 'manage_task' || !validToolResult(step, slot, calledAt, at)) return null
  const args = call.args
  if (!args || typeof args !== 'object' || Array.isArray(args) ||
    toolString(args.Action).toLowerCase() !== 'status') {
    return null
  }
  const requested = qualifiedTaskId(conversation, toolString(args.TaskId).trim())
  if (!isOwnTaskId(conversation, requested)) return null
  const match = taskStatusHeader.exec(toolOutput(step.content).trimStart())
  if (!match) return null
  const reported = qualifiedTaskId(conversation, match[1].trim())
  if (reported !== requested) return null
  const rawStatus = match[2].toUpperCase()
  if (liveStep.has(rawStatus)) return { id: requested, live: true }
  const status = terminalStatuses.get(rawStatus)
  return status ? { id: requested, status } : null
}

// Accepts successful manage_subagents actions scoped strictly to agent-kind background tasks.
function subagentAction(step, slot, calledAt, at) {
  const call = slot?.tool
  if (call?.name !== 'manage_subagents' || !validToolResult(step, slot, calledAt, at)) return null
  const args = call.args
  if (!args || typeof args !== 'object' || Array.isArray(args)) return null
  const action = toolString(args.Action).trim().toLowerCase()
  if (action === 'kill_all') return { kind: 'kill_all' }
  if (action === 'kill') {
    const ids = []
    for (const raw of toolStringArray(args.ConversationIds)) {
      const m = raw.trim().toLowerCase()
      if (exactUuidRe.test(m)) ids.push(m)
    }
    return ids.length ? { kind: 'kill', ids } : null
  }
  if (action === 'list' && !step.truncated_fields?.includes('content') &&
    emptySubagentList.test(toolOutput(step.content).trim())) {
    return { kind: 'empty_list' }
  }
  return null
}

function recordSeen(task, at, order) {
  if (Number.isFinite(at)) {
    if (!Number.isFinite(task.lastSeen) || at > task.lastSeen) {
      task.lastSeen = at
      task.lastSeenOrder = order
    } else if (at === task.lastSeen && order > task.lastSeenOrder) {
      task.lastSeenOrder = order
    }
  } else if (!Number.isFinite(task.lastSeen) && order > task.lastSeenOrder) {
    task.lastSeenOrder = order
  }
}

function recordMapSeen(map, key, ev) {
  const prev = map.get(key)
  if (!prev || ev.at > prev.at || (ev.at === prev.at && ev.order > prev.order)) map.set(key, ev)
}

function compareEvents(a, b) {
  const aTimed = Number.isFinite(a.at)
  const bTimed = Number.isFinite(b.at)
  if (aTimed && bTimed && a.at !== b.at) return a.at - b.at
  return a.order - b.order
}

function eventAfterTask(ev, lastSeen, lastSeenOrder) {
  if (Number.isFinite(ev.at) && Number.isFinite(lastSeen)) {
    return ev.at > lastSeen || (ev.at === lastSeen && ev.order > lastSeenOrder)
  }
  return ev.order > lastSeenOrder
}

function finish(task, status, at, inferred = false) {
  if (!task || (task.status && (!task.inferred || inferred))) return
  task.status = status
  task.ended_at = at
  task.inferred = inferred
}

function firstForeignUuid(text, conversation) {
  if (typeof text !== 'string' || !text) return ''
  const own = conversation.toLowerCase()
  for (const m of text.matchAll(embeddedUuidRe)) {
    const id = m[0].toLowerCase()
    if (id !== own) return id
  }
  return ''
}

export function extractBackground(steps, conversation, now = Number.NaN) {
  const tasks = new Map()
  const pending = new Map()
  const restarts = []
  const messages = []
  const inventories = []
  const inventorySeen = new Map()
  const statusSeen = new Map()
  const statusEnds = new Map()
  const subagentKillAlls = []
  const subagentKills = new Map()
  const subagentEmptyLists = []
  let index = -1
  let order = -1
  for (const step of steps) {
    order += 1
    index = Number.isInteger(step?.step_index) && step.step_index >= 0 ? step.step_index : index + 1
    const at = parseTime(step?.created_at)
    const slot = pending.get(index)
    pending.delete(index)
    switch (step?.type) {
      case 'PLANNER_RESPONSE': {
        const calls = Array.isArray(step.tool_calls) ? step.tool_calls : []
        for (let offset = 0; offset < calls.length; offset += 1) {
          const call = calls[offset]
          // Late results retain their native step index. An overlapping claim makes the slot ambiguous.
          const target = index + 1 + offset
          if (pending.has(target)) pending.set(target, null)
          else pending.set(target, { planner: step, plannerOrder: order, tool: call })
          if (call?.name === 'manage_task' && toolString(call.args?.Action).toLowerCase() === 'kill') {
            finish(tasks.get(qualifiedTaskId(conversation, toolString(call.args?.TaskId))), 'killed', step.created_at)
          }
        }
        break
      }
      case 'SYSTEM_MESSAGE': {
        const content = step.content ?? ''
        const notice = endNotice.exec(content)
        if (notice) {
          const verb = notice[2].toLowerCase()
          const status = verb === 'finished' ? 'completed' : verb === 'failed' ? 'failed' : 'killed'
          finish(tasks.get(qualifiedTaskId(conversation, notice[1])), status, step.created_at)
        }
        const message = taskMessage.exec(content)
        const restart = serverRestartEvent(content, message, step.created_at, order)
        if (restart) {
          restarts.push(restart)
          break
        }
        if (message) {
          const rawSender = message[2]
          const sender = exactUuidRe.test(rawSender.toLowerCase()) ? rawSender.toLowerCase() : qualifiedTaskId(conversation, rawSender)
          const body = message[3].trim().replace(/<\/SYSTEM_MESSAGE>\s*$/, '').trim()
          const heartbeat = heartbeatNotice.test(body)
          const msgAt = Number.isFinite(at) ? at : parseTime(message[1])
          const msgRawAt = Number.isFinite(at) ? step.created_at : (Number.isFinite(msgAt) ? message[1] : step.created_at)
          const task = tasks.get(sender)
          if (task) {
            recordSeen(task, msgAt, order)
            if (!notice) {
              const iter = cronIteration.exec(body)
              if (iter) {
                task.oneShot = false
                task.condition = ''
                task.deadline = Number.NaN
                if (task.maxIterations > 0 && Number(iter[1]) >= task.maxIterations) {
                  finish(task, 'completed', msgRawAt, true)
                }
              } else if (task.oneShot && !heartbeat) {
                finish(task, 'completed', msgRawAt)
              }
            }
          }
          if (!heartbeat && Number.isFinite(msgAt)) {
            const idleMatch = sender === 'system' ? subagentIdleRe.exec(body) : null
            messages.push({
              at: msgAt,
              rawAt: msgRawAt,
              order,
              sender,
              subagentId: idleMatch ? idleMatch[1].toLowerCase() : '',
            })
          }
        }
        break
      }
      case 'GENERIC': {
        let call
        if (slot?.planner) {
          const calledAt = parseTime(slot.planner.created_at)
          if (!Number.isFinite(at) || !Number.isFinite(calledAt) || at >= calledAt) {
            call = slot.tool
          }
          const ev = { at: calledAt, rawAt: slot.planner.created_at, order: slot.plannerOrder }
          const ids = taskInventory(step, slot, calledAt, at, conversation)
          if (ids) {
            inventories.push(ev)
            for (const id of ids) recordMapSeen(inventorySeen, id, ev)
          }
          const obs = taskStatusObservation(step, slot, calledAt, at, conversation)
          if (obs) {
            if (obs.live) recordMapSeen(statusSeen, obs.id, ev)
            else {
              let list = statusEnds.get(obs.id)
              if (!list) statusEnds.set(obs.id, list = [])
              list.push({ ...ev, status: obs.status })
            }
          }
          const subAct = subagentAction(step, slot, calledAt, at)
          if (subAct) {
            if (subAct.kind === 'kill_all') subagentKillAlls.push({ ...ev, status: 'killed' })
            else if (subAct.kind === 'empty_list') subagentEmptyLists.push(ev)
            else if (subAct.kind === 'kill') {
              for (const subId of subAct.ids) {
                let list = subagentKills.get(subId)
                if (!list) subagentKills.set(subId, list = [])
                list.push({ ...ev, status: 'killed' })
              }
            }
          }
        }
        const notice = backgroundNotice(step[backgroundStart] || step.content)
        if (!notice) break
        const [first, ...details] = notice.split('\n')
        const id = first.trim()
        if (!isOwnTaskId(conversation, id)) break
        let task = tasks.get(id)
        if (!task) {
          const detailText = details.join('\n')
          const description = details.map(line => line.trim()).find(line => line.startsWith('Task Description:'))
          const kind = kinds.get(call?.name) ?? 'shell'
          task = {
            id,
            kind,
            started_at: step.created_at,
            lastSeen: Number.NaN,
            lastSeenOrder: order,
          }
          if (description) task.description = description.slice('Task Description:'.length).trim()
          const cfg = scheduleConfig(task.description, call, conversation)
          task.oneShot = cfg.oneShot
          task.condition = cfg.condition
          task.maxIterations = cfg.maxIterations
          task.deadline = cfg.oneShot && cfg.durationMs > 0 && Number.isFinite(at) ? at + cfg.durationMs : Number.NaN
          if (kind === 'agent') task.subagentId = firstForeignUuid(detailText, conversation)
          if (call?.name === 'run_command') task.command = toolString(call.args?.CommandLine) || toolString(call.args?.command)
          tasks.set(id, task)
        }
        recordSeen(task, at, order)
        const updatedAt = parseTime(step.updated_at)
        if (Number.isFinite(updatedAt)) recordSeen(task, updatedAt, order)
        const status = terminalStatuses.get(String(step.status).toUpperCase())
        if (status) finish(task, status, step.updated_at || step.created_at, true)
        break
      }
    }
  }
  inventories.sort(compareEvents)
  subagentEmptyLists.sort(compareEvents)
  const running = [], ended = []
  let nextExpiry = Number.NaN
  const pickEarliest = (best, ev, status) => {
    const candidate = status ? { ...ev, status } : ev
    return !best || compareEvents(candidate, best) < 0 ? candidate : best
  }
  for (const record of tasks.values()) {
    if (!record.status) {
      let lastSeen = record.lastSeen
      let lastSeenOrder = record.lastSeenOrder
      for (const seenMap of [inventorySeen, statusSeen]) {
        const seen = seenMap.get(record.id)
        if (seen && (!Number.isFinite(lastSeen) || seen.at > lastSeen ||
          (seen.at === lastSeen && seen.order > lastSeenOrder))) {
          lastSeen = seen.at
          lastSeenOrder = seen.order
        }
      }
      const startedAt = parseTime(record.started_at)
      let best
      for (const ev of restarts) {
        if (eventAfterTask(ev, lastSeen, lastSeenOrder)) best = pickEarliest(best, ev, 'killed')
      }
      if (Number.isFinite(startedAt) && Number.isFinite(lastSeen)) {
        const inventory = inventories.find(ev => ev.at > lastSeen)
        if (inventory) best = pickEarliest(best, inventory, 'completed')
      }
      if (record.oneShot && record.condition && Number.isFinite(startedAt)) {
        for (const ev of messages) {
          if (ev.sender === record.id) continue
          if (record.condition !== 'any' && record.condition !== ev.sender &&
            (!ev.subagentId || record.condition !== ev.subagentId)) continue
          if (Number.isFinite(record.deadline) && ev.at >= record.deadline) continue
          if (eventAfterTask(ev, lastSeen, lastSeenOrder)) best = pickEarliest(best, ev, 'killed')
        }
      }
      for (const ev of statusEnds.get(record.id) ?? []) {
        if (eventAfterTask(ev, lastSeen, lastSeenOrder)) best = pickEarliest(best, ev)
      }
      if (record.kind === 'agent') {
        for (const ev of subagentKillAlls) {
          if (eventAfterTask(ev, lastSeen, lastSeenOrder)) best = pickEarliest(best, ev)
        }
        if (record.subagentId) {
          for (const ev of subagentKills.get(record.subagentId) ?? []) {
            if (eventAfterTask(ev, lastSeen, lastSeenOrder)) best = pickEarliest(best, ev)
          }
        }
        if (Number.isFinite(startedAt) && Number.isFinite(lastSeen)) {
          const emptySub = subagentEmptyLists.find(ev => ev.at > lastSeen)
          if (emptySub) best = pickEarliest(best, emptySub, 'completed')
        }
      }
      if (best) finish(record, best.status, best.rawAt, true)
    }
    if (!record.status && record.oneShot && Number.isFinite(record.deadline) && Number.isFinite(now)) {
      // Allow time for the timer's own completion message to reach the export.
      const expiry = record.deadline + timerExpiryGraceMs
      if (now >= expiry) {
        finish(record, 'completed', new Date(record.deadline).toISOString(), true)
      } else if (!Number.isFinite(nextExpiry) || expiry < nextExpiry) {
        nextExpiry = expiry
      }
    }
    const {
      inferred, oneShot, condition, maxIterations, deadline, subagentId,
      lastSeen, lastSeenOrder, ...task
    } = record
    if (task.status) {
      if (Number.isFinite(Date.parse(task.ended_at))) ended.push(task)
    } else if (running.length < runningLimit) {
      running.push(task)
    }
  }
  ended.sort((a, b) => Date.parse(a.ended_at) - Date.parse(b.ended_at))
  return {
    running,
    ended: ended.slice(-endedLimit),
    ...(Number.isFinite(nextExpiry) ? { nextExpiry } : {}),
  }
}

// The node attaches log paths from its own local transcript after validating
// that each path belongs to the conversation and task.
export function backgroundRow(tasks, live, now = Date.now()) {
  const ended = tasks.ended.filter(task => now - Date.parse(task.ended_at) < retainedForMs)
  return {
    ...(live && tasks.running.length ? { background: tasks.running } : {}),
    ...(ended.length ? { background_ended: ended } : {}),
  }
}
