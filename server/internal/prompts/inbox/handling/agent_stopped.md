{{if .Failed}}Agent 失败了{{else}}Agent 停了{{end}}（{{.Machine}} 上的会话 {{.Session}}）。{{if .Failed}}失败原因{{else}}它最后说的{{end}}：

<persona-context kind="agent_result" title="{{if .Failed}}Agent 的失败原因{{else}}Agent 的最后一句{{end}}">
{{if .Text}}{{ctx .Text}}{{else}}（没有最后的话）{{end}}
</persona-context>

读完接着处理这件事：调 inbox_suggest 交下一步建议，同时更新 recap。
