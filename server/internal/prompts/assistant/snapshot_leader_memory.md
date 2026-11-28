{{if eq .Language "日本語"}}- この方向のプロジェクト記憶（{{.Project}}）：{{range .Entries}}
  - {{.Text}}{{end}}{{else if .Language}}- Direction project memory ({{.Project}}):{{range .Entries}}
  - {{.Text}}{{end}}{{else}}- 本方向绑定项目的扩展记忆（{{.Project}}）：{{range .Entries}}
  - {{.Text}}{{end}}{{end}}
