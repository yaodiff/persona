"""Browser Use input, from repository root: browser-use < web/e2e/repositories-browser.py.

Requires the existing mock hub and Vite on isolated ports 25434/25435 (see web.md).
Uses the actual assistant settings. Only repository HTTP replies are synthetic;
provider verification and persistence are covered by Go tests, not this fixture.
Never run against a real hub. Screenshots stay outside Git.
"""
import os
import time
from pathlib import Path

new_tab("http://127.0.0.1:25435/")
wait_for_load()
assert page_info()["url"].startswith("http://127.0.0.1:25435/"), "Use the isolated mock Vite tab"
evidence = Path(os.environ.get("REPOSITORY_EVIDENCE_DIR", "/tmp/persona-repositories-evidence"))
evidence.mkdir(parents=True, exist_ok=True)


def eventually(expression):
    deadline = time.monotonic() + 8
    while time.monotonic() < deadline:
        if js(expression):
            return
        time.sleep(0.05)  # bounded condition polling, not a rendering assumption
    raise AssertionError(expression)


def control(name, role="button"):
    assert page_info()["url"].startswith("http://127.0.0.1:25435/"), "Mock tab lost"
    eventually("!document.querySelector('[data-closing]')")
    nodes = cdp("Accessibility.getFullAXTree")["nodes"]
    node = next(n for n in nodes if n.get("role", {}).get("value") == role and n.get("name", {}).get("value") == name)
    cdp("DOM.scrollIntoViewIfNeeded", backendNodeId=node["backendDOMNodeId"])
    target = cdp("DOM.resolveNode", backendNodeId=node["backendDOMNodeId"])["object"]["objectId"]
    deadline, previous = time.monotonic() + 8, None
    while time.monotonic() < deadline:
        box = cdp("DOM.getBoxModel", backendNodeId=node["backendDOMNodeId"])["model"]["content"]
        x, y = sum(box[0::2]) / 4, sum(box[1::2]) / 4
        clear = cdp("Runtime.callFunctionOn", objectId=target, functionDeclaration=f"function(){{return this.contains(document.elementFromPoint({x},{y}));}}", returnByValue=True)["result"].get("value")
        if box == previous and clear:
            click_at_xy(x, y)
            return
        previous = box
        time.sleep(0.05)
    raise AssertionError(f"Control remained covered or moving: {name}")


def fill(name, value):
    control(name, "textbox")
    js("document.activeElement.select(); true")
    type_text(value)


def shot(name):
    assert page_info()["url"].startswith("http://127.0.0.1:25435/"), "Mock tab lost"
    cdp("Runtime.evaluate", expression="new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))", awaitPromise=True)
    eventually("document.getAnimations().every(a=>a.playState!=='running'||a.effect?.getTiming().iterations===Infinity)")
    capture_screenshot(str(evidence / f"{name}.png"))


js("localStorage.setItem('persona.token','synthetic-browser-only'); localStorage.setItem('persona.language','en'); true")
new_tab("http://127.0.0.1:25435/settings?tab=persona")
cdp("Emulation.setDeviceMetricsOverride", width=1365, height=757, deviceScaleFactor=1, mobile=False)
wait_for_load()
eventually("!!document.querySelector('[data-settings-group=repositories]')")
js("""(() => {
  document.documentElement.classList.remove('dark');
  const original = window.fetch.bind(window);
  const state = window.repoFixture = {rows:[], calls:[], removed:null, pending:null, fail:false};
  window.fetch = async (url, init={}) => {
    const path=String(url).split('/api/v1')[1];
    if (!path?.startsWith('/assistant/repositories')) return original(url,init);
    const method=init.method||'GET', body=init.body?JSON.parse(init.body):{};
    state.calls.push({path,method,body});
    const manager=new Headers(init.headers).get('X-Persona-Manager');
    const identity = () => {
      const remote=body.url.replace(/^git@([^:]+):/, 'https://$1/').replace(/\\.git$/, '');
      const parsed=new URL(remote);
      return {provider:body.provider||'gitlab', instance_url:parsed.origin,
        path:parsed.pathname.slice(1).toLowerCase(),canonical_url:remote.toLowerCase()};
    };
    const result={manager};
    if (path.endsWith('/preview')) result.identity=identity();
    else if (path.endsWith('/restore')) {state.rows=[state.removed];result.repository=state.removed;}
    else if (method==='GET') result.repositories=state.rows;
    else if (method==='DELETE') {state.removed=state.rows[0];state.rows=[];result.receipt='synthetic-undo';}
    else {
      await new Promise(resolve=>state.pending=resolve);
      state.pending=null;
      if(state.fail) return new Response(JSON.stringify({code:'repository_unavailable',
        error:'Existence could not be confirmed. The repository may be private, hidden or unavailable.'}),
        {status:422,headers:{'Content-Type':'application/json'}});
      const record={...identity(),id:'repo_synthetic',name:body.name,aliases:body.aliases,
        purpose:body.purpose,verified_at:'2026-11-12T00:00:00Z'};
      state.rows=[record];result.repository=record;
    }
    return new Response(JSON.stringify(result),{headers:{'Content-Type':'application/json'}});
  };
  return true;
})()""")
control("Manage repositories")
eventually("document.body.innerText.includes('No repositories bound')")
shot("light-empty")
control("Bind repository")
fill("Repository URL", "git@gitlab.example.com:example/team/sample.git")
control("Provider", "combobox")
press_key("End")
press_key("Enter")
fill("Name (optional)", "Sample service")
fill("Aliases (comma-separated, optional)", "demo, service")
fill("Purpose (optional)", "Public-safe example service for repository identity resolution.")
eventually("document.querySelector('[role=dialog]').innerText.includes('Parsed locally; existence not yet checked')")
assert js("window.repoFixture.calls.every(call=>!call.path.endsWith('/preview'))")
assert js("document.querySelector('[data-repository-preview]').innerText.includes('https://gitlab.example.com/example/team/sample')")
shot("light-preview")
control("Check existence and save")
eventually("!!window.repoFixture.pending")
assert js("document.querySelector('[role=dialog]').innerText.includes('Checking repository existence')")
shot("light-pending")
js("window.repoFixture.pending(); true")
eventually("!!document.querySelector('[data-repository=repo_synthetic]')")
shot("light-saved")
js("document.documentElement.classList.add('dark'); true")
control("Edit Sample service")
fill("Name (optional)", "Sample service updated")
control("Save changes")
eventually("!!window.repoFixture.pending")
assert js("document.querySelector('[role=dialog][aria-modal=true]').innerText.includes('Saving repository details')")
js("window.repoFixture.pending(); true")
eventually("document.querySelector('[data-repository]').innerText.includes('Sample service updated')")
control("Edit Sample service updated")
fill("Repository URL", "https://gitlab.example.com/example/team/hidden")
eventually("document.querySelector('[role=dialog][aria-modal=true]').innerText.includes('example/team/hidden')")
js("window.repoFixture.fail=true; true")
control("Save changes")
eventually("!!window.repoFixture.pending")
js("window.repoFixture.pending(); true")
eventually("!!document.querySelector('[role=dialog][aria-modal=true] [role=alert]')")
assert js("window.repoFixture.rows[0].path==='example/team/sample'")
eventually("!document.body.innerText.includes('Repository saved')")
js("document.querySelector('[role=dialog][aria-modal=true] [role=alert]').scrollIntoView({block:'nearest'}); true")
shot("dark-failure")
control("Cancel")
control("Unbind Sample service updated")
eventually("!!document.querySelector('[role=dialog][aria-modal=true]')")
shot("dark-unbind")
control("Unbind repository")
eventually("document.body.innerText.includes('No repositories bound')")
control("Undo")
eventually("!!document.querySelector('[data-repository=repo_synthetic]')")
shot("dark-restored")
cdp("Emulation.setDeviceMetricsOverride", width=390, height=844, deviceScaleFactor=1, mobile=False)
control("Edit Sample service updated")
eventually("!!document.querySelector('[role=dialog][aria-modal=true]')")
shot("dark-narrow")
assert js("document.documentElement.scrollWidth<=390")
js("document.documentElement.dataset.textSize='large'; true")
shot("dark-large")
js("document.documentElement.classList.remove('dark'); true")
shot("light-large")
assert js("document.documentElement.scrollWidth<=390")
print({"result": "PASS", "screenshots": str(evidence), "requests": js("window.repoFixture.calls")})
