import assert from 'node:assert/strict'
import { test } from 'node:test'
import { extractBackground } from '../lib/background.js'

const conversation = '11111111-2222-4333-8444-555555555555'
const id = `${conversation}/task-1`
const started = '2026-06-01T10:00:00Z'
const fired = '2026-06-01T10:01:01Z'

function bgStep(step_index, status, created_at, taskNum, description, extra = {}) {
  return {
    step_index,
    type: 'GENERIC',
    status,
    created_at,
    content: `Created At: ${created_at}\nTool is running as a background task with task id: ${conversation}/task-${taskNum}\nTask Description: ${description}`,
    ...extra,
  }
}

function systemMessage(step_index, created_at, content) {
  return { step_index, type: 'SYSTEM_MESSAGE', created_at, content }
}

const cases = [
  { name: 'timer duration', args: { DurationSeconds: 60 }, description: 'Check status', completed: true },
  { name: 'timer description', completed: true },
  { name: 'cron expression', args: { CronExpression: '0 * * * *' } },
  { name: 'cron description', args: { DurationSeconds: '60' }, description: 'Cron: 0 * * * *' },
  { name: 'cron iteration', args: { DurationSeconds: '60' }, message: 'Check again (iteration 1)' },
  { name: 'timer prompt containing still running', args: { DurationSeconds: '60' }, message: 'Check build progress if still running', completed: true },
  { name: 'heartbeat', args: { DurationSeconds: '60' }, message: 'Task is still running' },
  { name: 'heartbeat with notice prefix', args: { DurationSeconds: '60' }, message: '[Notice] Task is still running' },
  { name: 'heartbeat with task id', args: { DurationSeconds: '60' }, message: 'Task id "task-3" is still running' },
  { name: 'different sender', sender: `${id}0` },
]

for (const { name, args, description = 'Timer: 60s', sender = id, message = 'Check again', completed } of cases) {
  test(`AGY schedule message: ${name}`, () => {
    const { running, ended } = extractBackground([
      { type: 'PLANNER_RESPONSE', tool_calls: [{ name: 'schedule', args }] },
      { type: 'GENERIC', status: 'RUNNING', created_at: started,
        content: `Tool is running as a background task with task id: ${id}\nTask Description: ${description}` },
      { type: 'SYSTEM_MESSAGE', created_at: fired,
        content: `<SYSTEM_MESSAGE>\n[Message] timestamp=${started} sender=${sender} priority=MESSAGE_PRIORITY_HIGH content=${message}\n</SYSTEM_MESSAGE>` },
    ], conversation)
    const task = { id, kind: 'monitor', description, started_at: started }
    assert.deepEqual(running, completed ? [] : [task])
    assert.deepEqual(ended, completed ? [{ ...task, status: 'completed', ended_at: fired }] : [])
  })
}

test('AGY server restart notice kills prior running tasks and keeps later tasks running', () => {
  const completedAt = '2026-06-01T10:01:00Z'
  const restartedAt = '2026-06-01T10:05:00Z'
  const resumedAt = '2026-06-01T10:06:00Z'
  const notice = `The following is a <SYSTEM_MESSAGE> not actually sent by the user.\n\n<SYSTEM_MESSAGE>\n[Message] timestamp=${restartedAt} sender=system priority=MESSAGE_PRIORITY_LOW content=[Notice] All your subagents and background tasks have been stopped due to server restart.\n</SYSTEM_MESSAGE>`
  const { running, ended } = extractBackground([
    {
      step_index: 0,
      type: 'PLANNER_RESPONSE',
      created_at: started,
      tool_calls: [
        { name: 'run_command', args: { CommandLine: 'npm test' } },
        { name: 'schedule', args: { CronExpression: '*/5 * * * *' } },
        { name: 'run_workflow' },
      ],
    },
    bgStep(1, 'RUNNING', started, 1, 'Run tests'),
    bgStep(2, 'RUNNING', started, 2, 'Cron: */5 * * * * Poll'),
    bgStep(3, 'DONE', started, 3, 'Already completed', { updated_at: completedAt }),
    systemMessage(4, restartedAt, notice),
    {
      step_index: 5,
      type: 'PLANNER_RESPONSE',
      created_at: resumedAt,
      tool_calls: [{ name: 'run_command', args: { CommandLine: 'npm run build' } }],
    },
    bgStep(6, 'RUNNING', resumedAt, 4, 'Build after restart'),
  ], conversation)
  assert.deepEqual(running.map(t => t.id), [`${conversation}/task-4`])
  assert.deepEqual(ended.map(t => [t.id, t.status, t.ended_at]), [
    [`${conversation}/task-3`, 'completed', completedAt],
    [`${conversation}/task-1`, 'killed', restartedAt],
    [`${conversation}/task-2`, 'killed', restartedAt],
  ])
})

test('AGY server restart notice handles bare notices, message timestamp fallback, same-timestamp order, and ignores non-restart messages', () => {
  const restartedAt = '2026-06-01T10:05:00Z'
  const secondRestartAt = '2026-06-01T10:10:00Z'
  const afterSecondAt = '2026-06-01T10:11:00Z'

  // Bare notice with preamble wrapper.
  {
    const bare = 'The following is a <SYSTEM_MESSAGE> not actually sent by the user.\n\n<SYSTEM_MESSAGE>\n[Notice] All your subagents and background tasks have been stopped due to server restart.\n</SYSTEM_MESSAGE>'
    const { running, ended } = extractBackground([
      bgStep(0, 'RUNNING', started, 1, 'Prior task'),
      systemMessage(1, restartedAt, bare),
    ], conversation)
    assert.deepEqual(running, [])
    assert.deepEqual(ended.map(t => [t.id, t.status, t.ended_at]), [[`${conversation}/task-1`, 'killed', restartedAt]])
  }

  // Fallback to [Message] timestamp when step.created_at is omitted.
  {
    const wrapped = `<SYSTEM_MESSAGE>\n[Message] timestamp=${restartedAt} sender=system priority=MESSAGE_PRIORITY_LOW content=[Notice] All your subagents and background tasks have been stopped due to server restart.\n</SYSTEM_MESSAGE>`
    const { running, ended } = extractBackground([
      bgStep(0, 'RUNNING', started, 1, 'Prior task'),
      { step_index: 1, type: 'SYSTEM_MESSAGE', content: wrapped },
    ], conversation)
    assert.deepEqual(running, [])
    assert.deepEqual(ended.map(t => [t.id, t.status, t.ended_at]), [[`${conversation}/task-1`, 'killed', restartedAt]])
  }

  // Same-timestamp tie-break and multiple restarts preserve earliest restart per task.
  {
    const msg = (ts) => `<SYSTEM_MESSAGE>\n[Message] timestamp=${ts} sender=system priority=MESSAGE_PRIORITY_LOW content=[Notice] All your subagents and background tasks have been stopped due to server restart.\n</SYSTEM_MESSAGE>`
    const { running, ended } = extractBackground([
      bgStep(0, 'RUNNING', restartedAt, 1, 'Before first restart same ts'),
      systemMessage(1, restartedAt, msg(restartedAt)),
      bgStep(2, 'RUNNING', restartedAt, 2, 'After first restart same ts'),
      systemMessage(3, secondRestartAt, msg(secondRestartAt)),
      bgStep(4, 'RUNNING', afterSecondAt, 3, 'After second restart'),
    ], conversation)
    assert.deepEqual(running.map(t => t.id), [`${conversation}/task-3`])
    assert.deepEqual(ended.map(t => [t.id, t.status, t.ended_at]), [
      [`${conversation}/task-1`, 'killed', restartedAt],
      [`${conversation}/task-2`, 'killed', secondRestartAt],
    ])
  }

  // Quoted restart text from a task sender or a subagent idle report must not kill running tasks.
  {
    const fromTask = `<SYSTEM_MESSAGE>\n[Message] timestamp=${restartedAt} sender=${conversation}/task-1 priority=MESSAGE_PRIORITY_HIGH content=[Notice] All your subagents and background tasks have been stopped due to server restart.\n</SYSTEM_MESSAGE>`
    const fromSubagentReport = `<SYSTEM_MESSAGE>\n[Message] timestamp=${restartedAt} sender=system priority=MESSAGE_PRIORITY_HIGH content=Subagent 00000000-0000-4000-8000-000000000002 has gone idle. This was the subagent's most recent response: All your subagents and background tasks have been stopped due to server restart.\n</SYSTEM_MESSAGE>`
    const { running, ended } = extractBackground([
      bgStep(0, 'RUNNING', started, 1, 'Echo test output'),
      bgStep(1, 'RUNNING', started, 2, 'Still alive'),
      systemMessage(2, restartedAt, fromTask),
      systemMessage(3, restartedAt, fromSubagentReport),
    ], conversation)
    assert.deepEqual(running.map(t => t.id), [`${conversation}/task-1`, `${conversation}/task-2`])
    assert.deepEqual(ended, [])
  }
})

test('AGY manage_task(list) reconciliation validates complete inventories and ignores unsafe outputs', () => {
  const listed = '2026-06-01T10:00:02Z'
  const later = '2026-06-01T10:00:03Z'
  const empty = 'No background tasks are currently running.'
  const partial = 'You have 1 background task(s) currently running:\n- Check status (`task-2`)'
  const full = `You have 2 background task(s) currently running:\n- Wait for input (\`${conversation}/task-1\`)\n- Check status (\`task-2\`)`
  const listCases = [
    { name: 'empty inventory with Output header', args: { Action: 'list' }, status: 'DONE', content: `Created At: ${listed}\nCompleted At: ${listed}\nOutput:\n${empty}`, wantEnded: 2 },
    { name: 'empty inventory without Output header', args: { Action: 'list' }, status: 'DONE', content: `Created At: ${listed}\nCompleted At: ${listed}\n${empty}`, wantEnded: 2 },
    { name: 'listed task stays running (JSON-encoded action)', args: { Action: JSON.stringify('list') }, status: 'DONE', content: `Created At: ${listed}\nCompleted At: ${listed}\nOutput:\n${partial}`, wantEnded: 1 },
    { name: 'all tasks listed', args: { Action: 'list' }, status: 'DONE', content: `Created At: ${listed}\nCompleted At: ${listed}\nOutput:\n${full}`, wantEnded: 0 },
    { name: 'status is not a list', args: { Action: 'status' }, status: 'DONE', content: empty, wantEnded: 0 },
    { name: 'failed list status', args: { Action: 'list' }, status: 'ERROR', content: empty, wantEnded: 0 },
    { name: 'failed list error field', args: { Action: 'list' }, status: 'DONE', error: 'tool failed', content: empty, wantEnded: 0 },
    { name: 'truncated content', args: { Action: 'list' }, status: 'DONE', content: partial, truncated_fields: ['content'], wantEnded: 0 },
    { name: 'truncated planner tool_calls', args: { Action: 'list' }, status: 'DONE', content: empty, plannerTruncated: ['tool_calls'], wantEnded: 0 },
    { name: 'missing row', args: { Action: 'list' }, status: 'DONE', content: partial.replace('have 1', 'have 2'), wantEnded: 0 },
    { name: 'duplicate row', args: { Action: 'list' }, status: 'DONE', content: `${partial}\n- Check status (\`task-2\`)`, wantEnded: 0 },
    { name: 'foreign inventory', args: { Action: 'list' }, status: 'DONE', content: partial.replace('task-2', 'another-conversation/task-2'), wantEnded: 0 },
    { name: 'mixed inventory', args: { Action: 'list' }, status: 'DONE', content: full.replace('(`task-2`)', '(`another-conversation/task-2`)'), wantEnded: 0 },
    { name: 'filtered TaskId', args: { Action: 'list', TaskId: 'task-2' }, status: 'DONE', content: partial, wantEnded: 0 },
    { name: 'unknown selector', args: { Action: 'list', Scope: 'current-turn' }, status: 'DONE', content: empty, wantEnded: 0 },
    { name: 'empty TaskId string', args: { Action: 'list', TaskId: '' }, status: 'DONE', content: empty, wantEnded: 2 },
    { name: 'null TaskId', args: { Action: 'list', TaskId: null }, status: 'DONE', content: empty, wantEnded: 2 },
    { name: 'unknown output', args: { Action: 'list' }, status: 'DONE', content: 'Unable to list tasks', wantEnded: 0 },
  ]

  for (const tc of listCases) {
    const planner = {
      step_index: 2,
      type: 'PLANNER_RESPONSE',
      created_at: listed,
      tool_calls: [{ name: 'run_command' }, { name: 'manage_task', args: tc.args }],
      ...(tc.plannerTruncated ? { truncated_fields: tc.plannerTruncated } : {}),
    }
    const result = {
      step_index: 4,
      type: 'GENERIC',
      status: tc.status,
      created_at: listed,
      content: tc.content,
      ...(tc.error ? { error: tc.error } : {}),
      ...(tc.truncated_fields ? { truncated_fields: tc.truncated_fields } : {}),
    }
    const { running, ended } = extractBackground([
      bgStep(0, 'RUNNING', started, 1, 'Wait for input'),
      bgStep(1, 'RUNNING', started, 2, 'Check status'),
      planner,
      result,
      bgStep(3, 'RUNNING', later, 3, 'Started after list'),
    ], conversation)
    assert.equal(ended.length, tc.wantEnded, `${tc.name}: ended count`)
    assert.equal(running.length, 3 - tc.wantEnded, `${tc.name}: running count`)
    assert.equal(running[running.length - 1].id, `${conversation}/task-3`, `${tc.name}: later task kept running`)
    for (let i = 0; i < ended.length; i += 1) {
      assert.equal(ended[i].id, `${conversation}/task-${i + 1}`, `${tc.name}: ended id`)
      assert.equal(ended[i].status, 'completed', `${tc.name}: ended status`)
      assert.equal(ended[i].ended_at, listed, `${tc.name}: ended_at`)
    }
  }
})

test('AGY manage_task(list) respects slot ownership, call time, and newer task updates', () => {
  const first = '2026-06-01T10:00:01Z'
  const second = '2026-06-01T10:00:02Z'
  const written = '2026-06-01T10:00:03Z'
  const empty = 'No background tasks are currently running.'
  const present = 'You have 1 background task(s) currently running:\n- Wait for input (`task-1`)'
  const listCall = { name: 'manage_task', args: { Action: 'list' } }
  const planner = (step_index, created_at, tool_calls) => ({ step_index, type: 'PLANNER_RESPONSE', created_at, tool_calls })
  const result = (step_index, created_at, content) => ({ step_index, type: 'GENERIC', status: 'DONE', created_at, content })

  const ownershipCases = [
    {
      name: 'late result keeps its native slot',
      steps: [planner(1, first, [listCall]), planner(3, second, [listCall]), result(2, written, empty)],
      endedAt: first,
    },
    {
      name: 'late result after user input',
      steps: [planner(1, first, [listCall]), { step_index: 3, type: 'USER_INPUT' }, result(2, written, empty)],
      endedAt: first,
    },
    {
      name: 'old result in newer planner slot',
      steps: [planner(1, first, [listCall]), planner(3, written, [listCall]), result(4, second, empty)],
      endedAt: '',
    },
    {
      name: 'old tool timestamp in newer planner slot',
      steps: [
        planner(1, first, [listCall]),
        planner(3, second, [listCall]),
        result(4, written, `Created At: ${first}\nCompleted At: ${written}\nOutput:\n${empty}`),
      ],
      endedAt: '',
    },
    {
      name: 'overlapping planner slots',
      steps: [planner(1, first, [{ name: 'run_command' }, listCall]), planner(2, second, [listCall]), result(3, written, empty)],
      endedAt: '',
    },
    {
      name: 'newer snapshot wins over later export',
      steps: [planner(1, first, [listCall]), planner(3, second, [listCall]), result(4, second, present), result(2, written, empty)],
      endedAt: '',
    },
    {
      name: 'repeated omission preserves first completion',
      steps: [planner(1, first, [listCall]), planner(3, second, [listCall]), result(4, second, empty), result(2, written, empty)],
      endedAt: first,
    },
  ]

  for (const tc of ownershipCases) {
    const { running, ended } = extractBackground([
      bgStep(0, 'RUNNING', started, 1, 'Wait for input'),
      ...tc.steps,
    ], conversation)
    if (!tc.endedAt) {
      assert.equal(running.length, 1, `${tc.name}: expected running`)
      assert.equal(ended.length, 0, `${tc.name}: expected no ended`)
    } else {
      assert.equal(running.length, 0, `${tc.name}: expected no running`)
      assert.deepEqual(ended.map(t => [t.id, t.status, t.ended_at]), [[`${conversation}/task-1`, 'completed', tc.endedAt]], tc.name)
    }
  }

  // Call time and later heartbeat / updated_at keep active tasks running.
  const heartbeat = `[Message] timestamp=${second} sender=${conversation}/task-3 priority=MESSAGE_PRIORITY_HIGH content=Task is still running`
  const { running, ended } = extractBackground([
    bgStep(0, 'RUNNING', started, 1, 'Omitted older task'),
    bgStep(1, 'RUNNING', started, 2, 'Task with later update', { updated_at: second }),
    bgStep(2, 'RUNNING', started, 3, 'Task with later heartbeat'),
    planner(3, first, [{ name: 'run_command' }, listCall]),
    bgStep(4, 'RUNNING', second, 4, 'Started after the list call'),
    result(5, written, empty),
    systemMessage(6, second, heartbeat),
  ], conversation)
  assert.deepEqual(ended.map(t => [t.id, t.status, t.ended_at]), [[`${conversation}/task-1`, 'completed', first]])
  assert.deepEqual(running.map(t => t.id), [
    `${conversation}/task-2`,
    `${conversation}/task-3`,
    `${conversation}/task-4`,
  ])
})

test('AGY reconciles earliest event between manage_task(list) and server restart', () => {
  const listTime = '2026-06-01T10:02:00Z'
  const afterList = '2026-06-01T10:03:00Z'
  const restartTime = '2026-06-01T10:05:00Z'
  const laterListTime = '2026-06-01T10:06:00Z'
  const listCall = { name: 'manage_task', args: { Action: 'list' } }
  const restartMsg = `<SYSTEM_MESSAGE>\n[Message] timestamp=${restartTime} sender=system priority=MESSAGE_PRIORITY_LOW content=[Notice] All your subagents and background tasks have been stopped due to server restart.\n</SYSTEM_MESSAGE>`

  // List omission before restart ends task-1 as completed; restart ends task-2 as killed.
  {
    const { running, ended } = extractBackground([
      bgStep(0, 'RUNNING', started, 1, 'Already gone before list'),
      { step_index: 1, type: 'PLANNER_RESPONSE', created_at: listTime, tool_calls: [listCall] },
      { step_index: 2, type: 'GENERIC', status: 'DONE', created_at: listTime, content: 'No background tasks are currently running.' },
      bgStep(3, 'RUNNING', afterList, 2, 'Running at restart'),
      systemMessage(4, restartTime, restartMsg),
    ], conversation)
    assert.deepEqual(running, [])
    assert.deepEqual(ended.map(t => [t.id, t.status, t.ended_at]), [
      [`${conversation}/task-1`, 'completed', listTime],
      [`${conversation}/task-2`, 'killed', restartTime],
    ])
  }

  // Server restart before empty list preserves killed status at restart time.
  {
    const { running, ended } = extractBackground([
      bgStep(0, 'RUNNING', started, 1, 'Killed by restart'),
      systemMessage(1, restartTime, restartMsg),
      { step_index: 2, type: 'PLANNER_RESPONSE', created_at: laterListTime, tool_calls: [listCall] },
      { step_index: 3, type: 'GENERIC', status: 'DONE', created_at: laterListTime, content: 'No background tasks are currently running.' },
    ], conversation)
    assert.deepEqual(running, [])
    assert.deepEqual(ended.map(t => [t.id, t.status, t.ended_at]), [
      [`${conversation}/task-1`, 'killed', restartTime],
    ])
  }
})

test('AGY TimerCondition early cancellation reconciles matching messages and guards active tasks', () => {
  const received = '2026-06-01T10:00:02Z'
  const sender = `${conversation}/task-2`
  const subagent = '22222222-3333-4444-8555-666666666666'
  const cases = [
    { name: 'any sender (task finish)', args: { DurationSeconds: 180, TimerCondition: 'any' }, msgSender: sender, msgBody: `Task id "${sender}" finished with result: ok`, at: received, canceled: true },
    { name: 'any sender (JSON-encoded compact args)', args: { DurationSeconds: '180', TimerCondition: JSON.stringify('any') }, msgSender: sender, msgBody: 'Done', at: received, canceled: true },
    { name: 'any sender (subagent message)', args: { DurationSeconds: 60, TimerCondition: 'any' }, msgSender: subagent, msgBody: 'Subagent finished', at: received, canceled: true },
    { name: 'specific subagent idle system notice', args: { DurationSeconds: 60, TimerCondition: subagent }, msgSender: 'system', msgBody: `Subagent ${subagent} has gone idle. This was the subagent's most recent response: Done`, at: received, canceled: true },
    { name: 'specific full task sender', args: { DurationSeconds: 60, TimerCondition: sender }, msgSender: sender, msgBody: 'Ready', at: received, canceled: true },
    { name: 'specific short task sender', args: { DurationSeconds: 60, TimerCondition: 'task-2' }, msgSender: sender, msgBody: 'Ready', at: received, canceled: true },
    { name: 'different sender', args: { DurationSeconds: 60, TimerCondition: sender }, msgSender: `${conversation}/task-3`, msgBody: 'Ready', at: received, canceled: false },
    { name: 'TimerCondition never', args: { DurationSeconds: 60, TimerCondition: 'never' }, msgSender: sender, msgBody: 'Ready', at: received, canceled: false },
    { name: 'TimerCondition omitted', args: { DurationSeconds: 60 }, msgSender: sender, msgBody: 'Ready', at: received, canceled: false },
    { name: 'message before timer start', args: { DurationSeconds: 60, TimerCondition: 'any' }, msgSender: sender, msgBody: 'Ready', at: '2026-06-01T09:59:59Z', canceled: false },
    { name: 'message after timer deadline', args: { DurationSeconds: 60, TimerCondition: 'any' }, msgSender: sender, msgBody: 'Ready', at: '2026-06-01T10:01:01Z', canceled: false },
    { name: 'own heartbeat does not cancel', args: { DurationSeconds: 60, TimerCondition: 'any' }, msgSender: id, msgBody: 'Task is still running', at: received, canceled: false },
    { name: 'other task heartbeat does not cancel any', args: { DurationSeconds: 60, TimerCondition: 'any' }, msgSender: sender, msgBody: '[Notice] Task id "task-2" is still running', at: received, canceled: false },
    { name: 'watched task heartbeat does not cancel', args: { DurationSeconds: 60, TimerCondition: 'task-2' }, msgSender: sender, msgBody: 'Task is STILL RUNNING', at: received, canceled: false },
    { name: 'cron ignores TimerCondition', args: { CronExpression: '*/5 * * * *', TimerCondition: 'any' }, description: 'Cron: */5 * * * *', msgSender: sender, msgBody: 'Ready', at: received, canceled: false },
  ]
  for (const tc of cases) {
    const desc = tc.description ?? 'Timer: 3m, Prompt: Check status'
    const msg = `<SYSTEM_MESSAGE>\n[Message] timestamp=${tc.at} sender=${tc.msgSender} priority=MESSAGE_PRIORITY_HIGH content=${tc.msgBody}\n</SYSTEM_MESSAGE>`
    const { running, ended } = extractBackground([
      { step_index: 0, type: 'PLANNER_RESPONSE', created_at: started, tool_calls: [{ name: 'schedule', args: tc.args }] },
      bgStep(1, 'RUNNING', started, 1, desc),
      systemMessage(2, tc.at, msg),
    ], conversation)
    if (tc.canceled) {
      assert.deepEqual(running, [], `${tc.name}: expected running empty`)
      assert.deepEqual(ended.map(t => [t.id, t.status, t.ended_at]), [[id, 'killed', tc.at]], tc.name)
    } else {
      assert.deepEqual(running.map(t => t.id), [id], `${tc.name}: expected timer running`)
      assert.deepEqual(ended, [], `${tc.name}: expected ended empty`)
    }
  }

  // Later liveness proof (listed in manage_task(list)) prevents condition cancellation from falsely killing a live timer.
  {
    const listedAt = '2026-06-01T10:00:05Z'
    const msg = `<SYSTEM_MESSAGE>\n[Message] timestamp=${received} sender=${sender} priority=MESSAGE_PRIORITY_HIGH content=Ready\n</SYSTEM_MESSAGE>`
    const listJSON = `You have 1 background task(s) currently running:\n${JSON.stringify([{ taskId: id, toolName: 'schedule' }])}`
    const { running, ended } = extractBackground([
      { step_index: 0, type: 'PLANNER_RESPONSE', created_at: started, tool_calls: [{ name: 'schedule', args: { DurationSeconds: 60, TimerCondition: 'any' } }] },
      bgStep(1, 'RUNNING', started, 1, 'Timer: 60s'),
      systemMessage(2, received, msg),
      { step_index: 3, type: 'PLANNER_RESPONSE', created_at: listedAt, tool_calls: [{ name: 'manage_task', args: { Action: 'list' } }] },
      { step_index: 4, type: 'GENERIC', status: 'DONE', created_at: listedAt, content: listJSON },
    ], conversation)
    assert.deepEqual(running.map(t => t.id), [id])
    assert.deepEqual(ended, [])
  }

  // Earliest event wins between TimerCondition and server restart.
  {
    const beforeRestart = '2026-06-01T10:01:00Z'
    const restartAt = '2026-06-01T10:02:00Z'
    const afterRestart = '2026-06-01T10:03:00Z'
    const msg = (at, s, body) => `<SYSTEM_MESSAGE>\n[Message] timestamp=${at} sender=${s} priority=MESSAGE_PRIORITY_HIGH content=${body}\n</SYSTEM_MESSAGE>`
    const { running, ended } = extractBackground([
      {
        step_index: 0,
        type: 'PLANNER_RESPONSE',
        created_at: started,
        tool_calls: [
          { name: 'schedule', args: { DurationSeconds: 600, TimerCondition: 'early' } },
          { name: 'schedule', args: { DurationSeconds: 600, TimerCondition: 'late' } },
        ],
      },
      bgStep(1, 'RUNNING', started, 1, 'Timer: 10m'),
      bgStep(2, 'RUNNING', started, 2, 'Timer: 10m'),
      systemMessage(3, beforeRestart, msg(beforeRestart, 'early', 'Ready')),
      systemMessage(4, restartAt, msg(restartAt, 'system', '[Notice] All your subagents and background tasks have been stopped due to server restart.')),
      systemMessage(5, afterRestart, msg(afterRestart, 'late', 'Ready')),
    ], conversation)
    assert.deepEqual(running, [])
    assert.deepEqual(ended.map(t => [t.id, t.status, t.ended_at]), [
      [`${conversation}/task-1`, 'killed', beforeRestart],
      [`${conversation}/task-2`, 'killed', restartAt],
    ])
  }

  // Out-of-order late-exported timer GENERIC step still reconciles against a later-timestamped condition message.
  {
    const msg = `<SYSTEM_MESSAGE>\n[Message] timestamp=${received} sender=${sender} priority=MESSAGE_PRIORITY_HIGH content=Ready\n</SYSTEM_MESSAGE>`
    const { running, ended } = extractBackground([
      { step_index: 0, type: 'PLANNER_RESPONSE', created_at: started, tool_calls: [{ name: 'schedule', args: { DurationSeconds: 60, TimerCondition: 'any' } }] },
      systemMessage(2, received, msg),
      bgStep(1, 'RUNNING', started, 1, 'Timer: 60s'),
    ], conversation)
    assert.deepEqual(running, [])
    assert.deepEqual(ended.map(t => [t.id, t.status, t.ended_at]), [[id, 'killed', received]])
  }
})

test('AGY manage_task(list) reconciles real JSON array inventories and rejects malformed JSON', () => {
  const listed = '2026-06-01T10:00:02Z'
  const later = '2026-06-01T10:00:03Z'
  const dupHeaders = `Created At: ${listed}\nCompleted At: ${listed}\nCreated At: ${listed}\nCompleted At: ${listed}\n`
  const oneJSON = `You have 1 background task(s) currently running:\n${JSON.stringify([{ taskId: `${conversation}/task-2`, toolName: 'run_command', description: 'npm test' }])}`
  const shortJSON = `You have 1 background task(s) currently running:\n${JSON.stringify([{ taskId: 'task-2', toolName: 'run_command' }])}`
  const twoJSON = `You have 2 background task(s) currently running:\n${JSON.stringify([{ taskId: `${conversation}/task-1` }, { taskId: 'task-2' }])}`

  const cases = [
    { name: 'JSON array with duplicated headers keeps listed task and ends omitted', content: dupHeaders + oneJSON, wantEnded: 1 },
    { name: 'JSON array with short taskId', content: shortJSON, wantEnded: 1 },
    { name: 'JSON array with all tasks listed', content: dupHeaders + twoJSON, wantEnded: 0 },
    { name: 'malformed JSON array fails closed', content: 'You have 1 background task(s) currently running:\n[{"taskId":', wantEnded: 0 },
    { name: 'JSON array count mismatch fails closed', content: oneJSON.replace('have 1', 'have 2'), wantEnded: 0 },
    { name: 'JSON array duplicate taskId fails closed', content: `You have 2 background task(s) currently running:\n${JSON.stringify([{ taskId: 'task-2' }, { taskId: `${conversation}/task-2` }])}`, wantEnded: 0 },
    { name: 'JSON array foreign taskId fails closed', content: `You have 1 background task(s) currently running:\n${JSON.stringify([{ taskId: 'other-conv/task-2' }])}`, wantEnded: 0 },
    { name: 'JSON array missing taskId fails closed', content: `You have 1 background task(s) currently running:\n${JSON.stringify([{ toolName: 'run_command' }])}`, wantEnded: 0 },
  ]
  for (const tc of cases) {
    const { running, ended } = extractBackground([
      bgStep(0, 'RUNNING', started, 1, 'Ghost timer'),
      bgStep(1, 'RUNNING', started, 2, 'Active command'),
      { step_index: 2, type: 'PLANNER_RESPONSE', created_at: listed, tool_calls: [{ name: 'manage_task', args: { Action: JSON.stringify('list') } }] },
      { step_index: 3, type: 'GENERIC', status: 'DONE', created_at: listed, content: tc.content },
      bgStep(4, 'RUNNING', later, 3, 'Started after list'),
    ], conversation)
    assert.equal(ended.length, tc.wantEnded, `${tc.name}: ended count`)
    assert.equal(running.length, 3 - tc.wantEnded, `${tc.name}: running count`)
    if (tc.wantEnded === 1) {
      assert.deepEqual(ended.map(t => [t.id, t.status, t.ended_at]), [[`${conversation}/task-1`, 'completed', listed]], tc.name)
      assert.deepEqual(running.map(t => t.id), [`${conversation}/task-2`, `${conversation}/task-3`], tc.name)
    }
  }
})

test('AGY manage_task(status) reconciles terminal statuses and refreshes liveness for RUNNING', () => {
  const checked = '2026-06-01T10:00:05Z'
  const dupHeaders = `Created At: ${checked}\nCompleted At: ${checked}\nCreated At: ${checked}\nCompleted At: ${checked}\n`
  const statusCases = [
    { name: 'DONE ends requested task as completed', reported: 'DONE', wantStatus: 'completed' },
    { name: 'ERROR ends requested task as failed', reported: 'ERROR', wantStatus: 'failed' },
    { name: 'FAILED ends requested task as failed', reported: 'FAILED', wantStatus: 'failed' },
    { name: 'CANCELED ends requested task as killed', reported: 'CANCELED', wantStatus: 'killed' },
    { name: 'CANCELLED ends requested task as killed', reported: 'CANCELLED', wantStatus: 'killed' },
    { name: 'RUNNING keeps task alive', reported: 'RUNNING', wantStatus: '' },
  ]
  for (const tc of statusCases) {
    const { running, ended } = extractBackground([
      bgStep(0, 'RUNNING', started, 1, 'Checked task'),
      bgStep(1, 'RUNNING', started, 2, 'Unrelated task'),
      { step_index: 2, type: 'PLANNER_RESPONSE', created_at: checked, tool_calls: [{ name: 'manage_task', args: { Action: JSON.stringify('status'), TaskId: JSON.stringify(id) } }] },
      { step_index: 3, type: 'GENERIC', status: 'DONE', created_at: checked, content: `${dupHeaders}Task: ${id}\nStatus: ${tc.reported}\nLog: file:///tmp/task-1.log\nLog output:\nhello` },
    ], conversation)
    if (tc.wantStatus) {
      assert.deepEqual(running.map(t => t.id), [`${conversation}/task-2`], tc.name)
      assert.deepEqual(ended.map(t => [t.id, t.status, t.ended_at]), [[id, tc.wantStatus, checked]], tc.name)
    } else {
      assert.deepEqual(running.map(t => t.id), [id, `${conversation}/task-2`], tc.name)
      assert.deepEqual(ended, [], tc.name)
    }
  }

  // Mismatched Task line or errored status step fails closed.
  for (const [name, step] of [
    ['mismatched task id', { step_index: 2, type: 'GENERIC', status: 'DONE', created_at: checked, content: `Task: ${conversation}/task-2\nStatus: DONE` }],
    ['errored status step', { step_index: 2, type: 'GENERIC', status: 'ERROR', created_at: checked, content: `Task: ${id}\nStatus: DONE` }],
    ['older created header', { step_index: 2, type: 'GENERIC', status: 'DONE', created_at: checked, content: `Created At: ${started}\nCompleted At: ${checked}\nTask: ${id}\nStatus: DONE` }],
  ]) {
    const { running, ended } = extractBackground([
      bgStep(0, 'RUNNING', started, 1, 'Checked task'),
      { step_index: 1, type: 'PLANNER_RESPONSE', created_at: checked, tool_calls: [{ name: 'manage_task', args: { Action: 'status', TaskId: id } }] },
      step,
    ], conversation)
    assert.deepEqual(running.map(t => t.id), [id], name)
    assert.deepEqual(ended, [], name)
  }

  // Status: RUNNING refreshes lastSeen so an earlier list omission does not kill it.
  {
    const listed = '2026-06-01T10:00:02Z'
    const { running, ended } = extractBackground([
      bgStep(0, 'RUNNING', started, 1, 'Alive task'),
      { step_index: 1, type: 'PLANNER_RESPONSE', created_at: listed, tool_calls: [{ name: 'manage_task', args: { Action: 'list' } }] },
      { step_index: 2, type: 'GENERIC', status: 'DONE', created_at: listed, content: 'No background tasks are currently running.' },
      { step_index: 3, type: 'PLANNER_RESPONSE', created_at: checked, tool_calls: [{ name: 'manage_task', args: { Action: 'status', TaskId: id } }] },
      { step_index: 4, type: 'GENERIC', status: 'DONE', created_at: checked, content: `Task: ${id}\nStatus: RUNNING` },
    ], conversation)
    assert.deepEqual(running.map(t => t.id), [id])
    assert.deepEqual(ended, [])
  }
})

test('AGY one-shot timer expires after duration + grace while cron and shell stay running', () => {
  const startMs = Date.parse(started)
  const deadline60 = startMs + 60_000
  const expiry60 = deadline60 + 60_000
  const deadline90 = startMs + 90_000
  const expiry90 = deadline90 + 60_000
  const steps = [
    {
      step_index: 0,
      type: 'PLANNER_RESPONSE',
      created_at: started,
      tool_calls: [
        { name: 'schedule', args: { DurationSeconds: 60 } },
        { name: 'schedule', args: { DurationSeconds: 60, CronExpression: '0 * * * *' } },
        { name: 'run_command', args: { CommandLine: 'npm run watch' } },
      ],
    },
    bgStep(1, 'RUNNING', started, 1, 'Timer: 60s, Prompt: Check CI'),
    bgStep(2, 'RUNNING', started, 2, 'Cron: 0 * * * *'),
    bgStep(3, 'RUNNING', started, 3, 'Watch build'),
    // Fallback timer without planner args (e.g. truncated tool_calls): parses "Timer: 1m 30s" from description.
    bgStep(9, 'RUNNING', started, 4, 'Timer: 1m 30s, Prompt: Follow up'),
  ]

  const before = extractBackground(steps, conversation, expiry60 - 1)
  assert.deepEqual(before.running.map(t => t.id), [
    `${conversation}/task-1`, `${conversation}/task-2`, `${conversation}/task-3`, `${conversation}/task-4`,
  ])
  assert.equal(before.nextExpiry, expiry60)

  const atFirst = extractBackground(steps, conversation, expiry60)
  assert.deepEqual(atFirst.running.map(t => t.id), [
    `${conversation}/task-2`, `${conversation}/task-3`, `${conversation}/task-4`,
  ])
  assert.deepEqual(atFirst.ended.map(t => [t.id, t.status, Date.parse(t.ended_at)]), [
    [`${conversation}/task-1`, 'completed', deadline60],
  ])
  assert.equal(atFirst.nextExpiry, expiry90)

  const atSecond = extractBackground(steps, conversation, expiry90)
  assert.deepEqual(atSecond.running.map(t => t.id), [
    `${conversation}/task-2`, `${conversation}/task-3`,
  ])
  assert.deepEqual(atSecond.ended.map(t => [t.id, t.status, Date.parse(t.ended_at)]), [
    [`${conversation}/task-1`, 'completed', deadline60],
    [`${conversation}/task-4`, 'completed', deadline90],
  ])
  assert.equal(atSecond.nextExpiry, undefined)
})

test('AGY schedule MaxIterations completes on final iteration while unlimited cron keeps running', () => {
  const t1 = '2026-06-01T10:05:00Z'
  const t2 = '2026-06-01T10:10:00Z'
  const iterMsg = (taskNum, at, n) => systemMessage(
    10 + n,
    at,
    `<SYSTEM_MESSAGE>\n[Message] timestamp=${at} sender=${conversation}/task-${taskNum} priority=MESSAGE_PRIORITY_HIGH content=Poll status (iteration ${n})\n</SYSTEM_MESSAGE>`,
  )
  const baseSteps = [
    {
      step_index: 0,
      type: 'PLANNER_RESPONSE',
      created_at: started,
      tool_calls: [
        { name: 'schedule', args: { CronExpression: '*/5 * * * *', MaxIterations: '2' } },
        { name: 'schedule', args: { CronExpression: '*/5 * * * *' } },
      ],
    },
    bgStep(1, 'RUNNING', started, 1, 'Cron: */5 * * * * (max 2)'),
    bgStep(2, 'RUNNING', started, 2, 'Cron: */5 * * * * (unlimited)'),
    iterMsg(1, t1, 1),
    iterMsg(2, t1, 1),
  ]

  const after1 = extractBackground(baseSteps, conversation)
  assert.deepEqual(after1.running.map(t => t.id), [`${conversation}/task-1`, `${conversation}/task-2`])
  assert.deepEqual(after1.ended, [])

  const after2 = extractBackground([...baseSteps, iterMsg(1, t2, 2), iterMsg(2, t2, 2)], conversation)
  assert.deepEqual(after2.running.map(t => t.id), [`${conversation}/task-2`])
  assert.deepEqual(after2.ended.map(t => [t.id, t.status, t.ended_at]), [[`${conversation}/task-1`, 'completed', t2]])
})

test('AGY manage_subagents reconciles agent tasks without affecting shell, monitor, or workflow tasks', () => {
  const actAt = '2026-06-01T10:02:00Z'
  const afterAt = '2026-06-01T10:03:00Z'
  const sub1 = 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeee1'
  const sub2 = 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeee2'
  const agentStep = (idx, num, subId, at = started) => ({
    step_index: idx,
    type: 'GENERIC',
    status: 'RUNNING',
    created_at: at,
    content: `Created At: ${at}\nTool is running as a background task with task id: ${conversation}/task-${num}\nTask Description: Subagent ${subId}`,
  })
  const base = [
    {
      step_index: 0,
      type: 'PLANNER_RESPONSE',
      created_at: started,
      tool_calls: [
        { name: 'run_command', args: { CommandLine: 'npm test' } },
        { name: 'schedule', args: { CronExpression: '*/5 * * * *' } },
        { name: 'run_workflow' },
        { name: 'invoke_subagent' },
        { name: 'invoke_subagent' },
      ],
    },
    bgStep(1, 'RUNNING', started, 1, 'Run tests'),
    bgStep(2, 'RUNNING', started, 2, 'Cron: */5 * * * *'),
    bgStep(3, 'RUNNING', started, 3, 'Workflow'),
    agentStep(4, 4, sub1),
    agentStep(5, 5, sub2),
  ]

  // kill_all kills only prior agent tasks.
  {
    const { running, ended } = extractBackground([
      ...base,
      { step_index: 6, type: 'PLANNER_RESPONSE', created_at: actAt, tool_calls: [{ name: 'manage_subagents', args: { Action: JSON.stringify('kill_all') } }] },
      { step_index: 7, type: 'GENERIC', status: 'DONE', created_at: actAt, content: 'Successfully killed 2 subagent(s) and their descendants.' },
      { step_index: 8, type: 'PLANNER_RESPONSE', created_at: afterAt, tool_calls: [{ name: 'invoke_subagent' }] },
      agentStep(9, 6, 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeee3', afterAt),
    ], conversation)
    assert.deepEqual(running.map(t => t.id), [
      `${conversation}/task-1`, `${conversation}/task-2`, `${conversation}/task-3`, `${conversation}/task-6`,
    ])
    assert.deepEqual(ended.map(t => [t.id, t.status, t.ended_at]), [
      [`${conversation}/task-4`, 'killed', actAt],
      [`${conversation}/task-5`, 'killed', actAt],
    ])
  }

  // kill with specific ConversationIds kills only matching agent task.
  {
    const { running, ended } = extractBackground([
      ...base,
      { step_index: 6, type: 'PLANNER_RESPONSE', created_at: actAt, tool_calls: [{ name: 'manage_subagents', args: { Action: 'kill', ConversationIds: [sub1] } }] },
      { step_index: 7, type: 'GENERIC', status: 'DONE', created_at: actAt, content: 'Successfully killed 1 subagent(s).' },
    ], conversation)
    assert.deepEqual(running.map(t => t.id), [
      `${conversation}/task-1`, `${conversation}/task-2`, `${conversation}/task-3`, `${conversation}/task-5`,
    ])
    assert.deepEqual(ended.map(t => [t.id, t.status, t.ended_at]), [
      [`${conversation}/task-4`, 'killed', actAt],
    ])
  }

  // list with 0 active subagents completes prior agent tasks without touching non-agent tasks.
  {
    const { running, ended } = extractBackground([
      ...base,
      { step_index: 6, type: 'PLANNER_RESPONSE', created_at: actAt, tool_calls: [{ name: 'manage_subagents', args: { Action: 'list' } }] },
      { step_index: 7, type: 'GENERIC', status: 'DONE', created_at: actAt, content: `Created At: ${actAt}\nCompleted At: ${actAt}\nYou have 0 active subagent(s):\n[]` },
    ], conversation)
    assert.deepEqual(running.map(t => t.id), [
      `${conversation}/task-1`, `${conversation}/task-2`, `${conversation}/task-3`,
    ])
    assert.deepEqual(ended.map(t => [t.id, t.status, t.ended_at]), [
      [`${conversation}/task-4`, 'completed', actAt],
      [`${conversation}/task-5`, 'completed', actAt],
    ])
  }

  // Failed manage_subagents call leaves all tasks running.
  {
    const { running, ended } = extractBackground([
      ...base,
      { step_index: 6, type: 'PLANNER_RESPONSE', created_at: actAt, tool_calls: [{ name: 'manage_subagents', args: { Action: 'kill_all' } }] },
      { step_index: 7, type: 'GENERIC', status: 'ERROR', created_at: actAt, content: 'permission denied' },
    ], conversation)
    assert.equal(running.length, 5)
    assert.deepEqual(ended, [])
  }
})
