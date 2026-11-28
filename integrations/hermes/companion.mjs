#!/usr/bin/env node
import { parseArgs } from 'node:util'
import { createHermesRuntime } from './lib/runtime.js'

const { values } = parseArgs({ options: {
  socket: { type: 'string' }, 'hermes-bin': { type: 'string' },
} })
const runtime = createHermesRuntime({
  bin: values['hermes-bin'], socketPath: values.socket,
  log: (message) => process.stderr.write(`persona-hermes: ${message}\n`),
})
const alive = setInterval(() => {}, 60_000)
for (const signal of ['SIGTERM', 'SIGINT']) process.once(signal, () => {
  clearInterval(alive)
  void runtime.stop().finally(() => process.exit(0))
})
try {
  await runtime.start()
} catch (error) {
  console.error(`persona-hermes: ${error.message}`)
  clearInterval(alive)
  await runtime.stop()
  process.exitCode = 1
}
