Act on one of the user's Macs through Cua Driver, in one call: either `steps` (Cua's run_steps: actions in order, stopping at the first failure) or `script` (Cua's run_script: JavaScript with loops and checks). Apps act in the background without taking focus.
- steps: `[{"click": {"app": "Calculator", "role": "button", "name": "7"}}, {"press_key": {"app": "Calculator", "key": "return"}}]`. A step names its target by role/name (no earlier read needed) or by an `element_token` from computer_state. Other actions: double_click, right_click, type_text {text}, set_value {value}, hotkey {keys}, scroll, launch_app {name}. Add `observe: {}` to get what changed in the window after the last step (no screenshot).
- script: the body of an async function with one API, `cua`: `const app = await cua.getApp("Calculator"); await app.click({role: "button", name: "7"}); const [d] = await app.query({role: "statictext"}); return d.value;`. Also `cua.launch(name)`, `app.typeText(text, target?)`, `app.pressKey("cmd+s")`, `app.waitFor({…})`, `app.getState()`. Its return value comes back; `timeout_ms` up to 120000.
- An app with more than one process: pass `pid` (and `window_id`) on computer_run itself and leave `app` out of the steps, which then act in that window; a step's own `app` would have Cua Driver choose the window again. Such an app is never guessed at: a call naming it otherwise is refused with its processes' windows, and so is a script naming it (`cua.getApp` cannot tell them apart).
Read first with computer_state when unsure what is on screen, and read again with computer_state for a screenshot to show the user the result. Writes may be confirmed on the phone first, if the user set that.

Never System Settings or macOS's permission and password prompts: a call that names one or would act in one is refused, and the user does that themselves on the Mac or through the live screen in the Persona app. Only they are off limits: with System Settings open, steps on other apps (by name or element token, keys to a named app) still run, but a point where System Settings is on top, keys with no app while it is in front, and scripts are refused, so prefer element tokens and steps then. While a permission or password prompt is on the screen nothing runs until the user answers it. Name every app a script touches with a literal `cua.getApp("…")` or `cua.launch("…")`; a script that names them otherwise, or names none, is refused. Steps naming no app run one at a time.

On a machine whose computer-use backend is LCU, `script` is that runtime's JavaScript and `steps` are translated into it. Per-app, session, always, and site confirmations are asked on the phone or the web, on the same cards as other computer-use approvals. A refusal is the user's. Do not retry it and do not try to approve it yourself.

```json
{
  "type": "object",
  "required": ["machine"],
  "properties": {
    "machine": {
      "type": "string",
      "description": "machine id or name"
    },
    "steps": {
      "type": "array",
      "items": {
        "type": "object"
      },
      "description": "Cua run_steps steps"
    },
    "app": {
      "type": "string",
      "description": "default app for steps that name none"
    },
    "pid": {
      "type": "integer",
      "description": "default process for steps that name no app, when the app has several (steps only)"
    },
    "window_id": {
      "type": "integer",
      "description": "default window for steps that name no app (steps only)"
    },
    "observe": {
      "type": "object",
      "description": "after the steps, read what changed in the window (get_window_state options; {} for defaults)"
    },
    "script": {
      "type": "string",
      "description": "Cua run_script JavaScript"
    },
    "timeout_ms": {
      "type": "integer",
      "description": "script wall time, default 30000, max 120000"
    }
  }
}
```
