{{template "kb/minutes_meeting" .Meeting}}
各段笔记：
{{range .Parts}}
## 第 {{.Part}} 段（{{.From}}–{{.To}}）{{with .Where}}
地点：{{.}}{{end}}{{with .Attendees}}
在场：{{join . "、"}}{{end}}{{with .Points}}
要点：{{range .}}
- {{if .Photo}}[P{{.Photo}}]{{else}}[{{.Line}}]{{end}}{{with .Speaker}} 〔{{.}}〕{{end}}{{with .Topic}}〈{{.}}〉{{end}} {{.Text}}{{with .Who}}（{{.}}）{{end}}{{end}}{{end}}{{with .Decisions}}
决定：{{range .}}
- {{if .Photo}}[P{{.Photo}}]{{else}}[{{.Line}}]{{end}}{{with .Speaker}} 〔{{.}}〕{{end}}{{with .Topic}}〈{{.}}〉{{end}} {{.Text}}{{end}}{{end}}{{with .Actions}}
待办：{{range .}}
- {{if .Photo}}[P{{.Photo}}]{{else}}[{{.Line}}]{{end}} {{with .Who}}{{.}}：{{end}}{{.Text}}{{with .By}}（期限：{{.}}）{{end}}{{with .DueDate}}（{{.}}）{{end}}{{end}}{{end}}{{with .Open}}
未决：{{range .}}
- {{if .Photo}}[P{{.Photo}}]{{else}}[{{.Line}}]{{end}} {{.Text}}{{end}}{{end}}{{with .Debates}}
分歧：{{range .}}
- {{.Question}}{{with .Topic}}〈{{.}}〉{{end}}（{{if .Settled}}已谈拢{{else}}仍有分歧{{end}}）{{range .Positions}}
  - [{{.Line}}]{{with .Speaker}} 〔{{.}}〕{{end}}{{with .Holder}} (holder: {{.}}){{end}} {{.Stance}}{{with .Who}}（{{.}}）{{end}}{{with .Also}} [also: {{range $i, $a := .}}{{if $i}}, {{end}}[{{$a.Line}}]{{with $a.Speaker}} 〔{{.}}〕{{end}}{{with $a.Holder}} (holder: {{.}}){{end}}{{end}}]{{end}}{{end}}{{if .OutcomeLine}}
  - [{{.OutcomeLine}}] 结果：{{.OutcomeText}}{{end}}{{end}}{{end}}
{{end}}
