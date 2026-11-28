{{if eq .Language "English"}}{{if eq .Event "merged"}}✅ The delivery PR for the closed task "{{.Title}}" was merged{{else if eq .Event "closed"}}⌧ The delivery PR for the closed task "{{.Title}}" was closed without merging{{else if eq .Event "conflict"}}⚠️ The delivery PR for the closed task "{{.Title}}" has a merge conflict{{else}}🔁 The delivery PR for the closed task "{{.Title}}" received a changes-requested review at its current head{{end}}: {{.PRURL}}
The task stays closed and will not reopen; this is only a post-closure PR follow-up.
(This is a notice from Persona, not a message from the user. no reply is needed. Delegate another task only if this needs action.)
{{else if eq .Language "日本語"}}{{if eq .Event "merged"}}✅ クローズ済みタスク「{{.Title}}」の交付 PR がマージされました{{else if eq .Event "closed"}}⌧ クローズ済みタスク「{{.Title}}」の交付 PR がマージされずにクローズされました{{else if eq .Event "conflict"}}⚠️ クローズ済みタスク「{{.Title}}」の交付 PR でマージ競合が発生しています{{else}}🔁 クローズ済みタスク「{{.Title}}」の交付 PR の現在の head に changes-requested レビューが届きました{{end}}：{{.PRURL}}
タスクはクローズ済みのままで、再オープンしません。これはクローズ後の PR フォローアップです。
（これは Persona からの通知で、ユーザーからのメッセージではありません。返答は不要です。対応が必要な場合だけ別のタスクを依頼してください。）
{{else}}{{if eq .Event "merged"}}✅ 已关单任务「{{.Title}}」的交付 PR 已合并{{else if eq .Event "closed"}}⌧ 已关单任务「{{.Title}}」的交付 PR 已关闭（未合并）{{else if eq .Event "conflict"}}⚠️ 已关单任务「{{.Title}}」的交付 PR 出现合并冲突{{else}}🔁 已关单任务「{{.Title}}」的交付 PR 在当前 head 收到 changes-requested 评审{{end}}：{{.PRURL}}
任务保持已关单，不会被重开；这只是关单后的 PR 跟进信号。
（这是 Persona 的通知，不是用户发的消息。不用回应，需要处理时再派任务。）
{{end}}
