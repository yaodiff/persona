Retrieve a completed `browser_run` download by its exact filename in Persona's managed browser downloads directory on the named personal Mac. If this conversation runs on another connected personal computer, Persona copies the file there and returns its local path. On the same computer, it returns the original path. Files up to 50 MiB are supported. The user may be asked to confirm first.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["machine", "filename"],
  "properties": {
    "machine": {
      "type": "string",
      "description": "id or name of the browser machine"
    },
    "filename": {
      "type": "string",
      "description": "the exact filename in Persona's browser downloads directory, without a directory path"
    }
  }
}
```
