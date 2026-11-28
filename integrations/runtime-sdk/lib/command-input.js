import { RpcError } from './wire.js'

const refusals = new Set(['prompt_changed', 'command_input_refused', 'command_input_unreachable', 'command_input_line'])

// Drivers may reuse the same row for live inventory, start and history.
export function withoutInputFields(row) {
  if (!Object.hasOwn(row ?? {}, 'waiting_inputs') && !Object.hasOwn(row ?? {}, 'input_binding')) return row
  const { waiting_inputs, input_binding, ...session } = row
  return session
}

// A start or fork can return row fields directly or under session.
export function publicSessionResult(result) {
  const reply = withoutInputFields(result)
  const session = withoutInputFields(reply?.session)
  return session === reply?.session ? reply : { ...reply, session }
}

export function hasLiveWaits(row) {
  return Array.isArray(row.waiting_inputs) && row.waiting_inputs.length > 0 &&
    !row.archived && !row.deleted &&
    !['interrupted', 'offline', 'archived', 'deleted'].includes(row.status)
}

function inputError(code) {
  // Peer serializes an error's message and data. Both can contain the owner's line.
  return new RpcError(code, code)
}

/**
 * @param {{input?: import('../index.js').InputHandler}} driver
 * @param {import('../index.js').SessionInput} request
 * @returns {Promise<import('../index.js').TypedInput>}
 */
export async function commandInput(driver, { session_id, task, wait_id, line }) {
  if (typeof driver.input !== 'function') throw inputError('command_input_unreachable')
  if (![session_id, task, wait_id].every(id => typeof id === 'string' && id.length > 0)) {
    throw inputError('prompt_changed')
  }
  // Match CommandInputLineProblem: a single UTF-8 line, with tabs allowed.
  if (typeof line !== 'string' || !line || Buffer.byteLength(line) > 4096 ||
      !line.isWellFormed() || /[\x00-\x08\x0a-\x1f\x7f-\x9f\u2028\u2029]/u.test(line)) {
    throw inputError('command_input_line')
  }
  let result
  try {
    result = await driver.input({ session_id, task, wait_id, line })
  } catch (error) {
    // Read the code once. A driver-supplied getter may itself throw.
    let code
    try { code = error?.code } catch {}
    throw inputError(refusals.has(code) ? code : 'command_input_unconfirmed')
  }
  try {
    if (result?.typed === true) return { typed: true }
  } catch {
    // An unreadable acknowledgement leaves the outcome unknown.
  }
  throw inputError('command_input_unconfirmed')
}
