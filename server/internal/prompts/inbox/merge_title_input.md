合并在一起的几件事（最新的在前）：
{{range .Items}}
类型：{{.Kind}}{{with .Source}}（{{.}}）{{end}}
标题：{{.Title}}{{with .Sender}}
来自：{{.}}{{end}}{{with .Summary}}
摘要：{{.}}{{end}}{{with .Preview}}
内容：{{.}}{{end}}
{{end}}
