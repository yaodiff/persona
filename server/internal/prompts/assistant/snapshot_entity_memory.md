{{if eq .Language "日本語"}}- 今回の会話で言及された項目に関する記憶（自動的に添えた非機密の関連メモ。会話に必要なときだけ使い、他の人には伝えないでください）：{{range .Entries}}
  - {{.Text}}{{end}}{{else if .Language}}- Related memory for entities mentioned in this turn (non-sensitive local rules and facts attached automatically; use when relevant and do not pass them on):{{range .Entries}}
  - {{.Text}}{{end}}{{else}}- 本轮提及实体的相关记忆（自动带出的非敏感局部规矩与事实；本轮用得上时才用，不要转告别人）：{{range .Entries}}
  - {{.Text}}{{end}}{{end}}
