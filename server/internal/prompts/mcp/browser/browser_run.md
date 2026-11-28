Drive the browser (ego lite, Chromium) on one of the user's Macs: `script` is the body of an async JavaScript function run by ego-browser, with `task` (this conversation's own task space, kept from one call to the next) and `page` (its tab p1) at hand; `return` a JSON value. The browser is the user's: sites they signed in to in ego are signed in. Page API: `await page.goto(url)`, `page.snapshot()` (accessibility text with `[ref=N]`), `page.click("@N")` / `page.fill("@N", text)` / `page.press("Enter")`, also `text=…` / CSS targets, `page.waitForURL(…)`, `page.waitForSelector(…)`, `page.title()`, `page.url()`, `page.evaluate(fn, arg)` (runs in the page), `page.keyboard.type(text)`, `page.mouse.click(x, y)`; `task.newPage()` for another tab. A screenshot of the page after the script comes back with its result. Use browser_look to read a page first. Do not clear cookies, cache or storage of the whole profile (Network.clearBrowserCookies and the like): it signs the user out everywhere. Stop and tell the user when a site asks for a sign-in, a captcha or a payment. Writes may be confirmed on the phone first, if the user set that. Persona saves downloads in its managed browser downloads directory. To transfer a file to this conversation's computer, give `browser_fetch_download` the exact filename.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["machine", "script"],
  "properties": {
    "machine": {
      "type": "string",
      "description": "machine id or name"
    },
    "script": {
      "type": "string",
      "description": "the body of an async function; task and page are defined; return a JSON value"
    },
    "timeout_ms": {
      "type": "integer",
      "description": "default 60000, max 120000"
    }
  }
}
```
