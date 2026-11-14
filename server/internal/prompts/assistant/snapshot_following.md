{{if .FollowingTotal}}- 在跟的事（{{.FollowingTotal}} 件）：{{if .Omit.Following}}这份快照放不下，没列出来，收件箱在 App 里看{{else}}{{range .Following}}
  - 「{{.Title}}」；在哪：{{if .Where}}{{.Where}}{{else}}没写{{end}}；等谁：{{if .Who}}{{.Who}}{{else}}没写{{end}}；下一步：{{if .Next}}{{.Next}}{{else}}没写{{end}}；再看：{{if .CheckAt.IsZero}}{{if .CheckDue}}到了再看的时间{{else}}没定时间{{end}}{{else}}{{.CheckAt.Format "01-02 15:04"}}{{if .CheckDue}}；到了再看的时间{{end}}{{end}}{{if .FollowUpAsk}}；看过 3 次没动静{{end}}{{end}}{{if gt .FollowingTotal (len .Following)}}
  - 还有 {{.FollowingLeft}} 件，收件箱在 App 里看{{end}}{{end}}{{end}}
