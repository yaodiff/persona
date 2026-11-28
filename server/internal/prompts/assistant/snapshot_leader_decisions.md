{{if eq .Language "日本語"}}- この方向および共通のユーザー決定（原文優先、上書きされるまで有効）：{{range .Entries}}
  - {{.Text}}{{end}}{{else if .Language}}- Active user decisions for this direction and global scope (original wording preserved until superseded):{{range .Entries}}
  - {{.Text}}{{end}}{{else}}- 本方向与全局的用户决定（保留原话，被新决定取代前持续有效）：{{range .Entries}}
  - {{.Text}}{{end}}{{end}}
