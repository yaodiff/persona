{{.Owner}}确认了，Agent 已经开了（{{.Machine}} 上的会话 {{.Session}}），指令{{if .Edited}}（{{.Owner}}改过）{{end}}：
{{.Text}}

它停下来时，你会收到它最后说的话或失败原因，那时再交建议。现在调 inbox_suggest 只给 recap 更新总结。
