- headline：一句话说清这场会讨论了什么、结果如何，40 字左右。
- conclusions：核心结论，2 到 4 条，最重要的放前面：这场会最后落在哪里、定了什么。格式 {text, line}。依据照片的结论，只有议题里也有要点或决定引用了同一张照片时才填 photo；否则填 line，指向支持这条结论的那一行。
- topics：议题，按第一次讨论到的先后排，一般 2 到 8 个，最多 10 个。一个议题是一项工作或一个话题，比如一个模块、发布日期、性能。格式 {title, group, icon, points, decisions}：
  - title：议题名，12 字以内；
  - group：分组名，8 字左右。议题有 4 个或更多、而且能分成 2 到 4 组时才写，这时每个议题都要写；否则都写空字符串；
  - icon：照写下面的一个英文词：progress（进展）、schedule（排期）、release（发布）、performance（性能）、metrics（数据和指标）、tooling（工具）、quality（质量和缺陷）、design（设计）、people（人员）、customer（客户）、budget（预算）、risk（风险）、general（都不合适时）；
  - points：这个议题讨论的要点，按时间顺序，说清讨论了什么、有哪些观点或进展。格式 {text, line, who}，who 是提出或主讲的人；
  - decisions：这个议题里已经定下来的结论和决定，不包括讨论中的想法。格式 {text, line}。
  每个要点和决定都放进一个议题，不要写在 topics 外面。一个议题写 2 到 4 条，超过 5 条的拆成两个议题。
- debates：会上真正的分歧，最多 6 条；没有真正分歧时写空数组。只要会上有人对同一件事提出不同主张、明确反对或坚持不同方案，就算分歧——包括后面谈拢定下来的分歧（用 "settled" 记下，即使已写进 decisions 也要记录），也包括因待补数据或下次再议而暂未敲定的分歧；普通讨论、同步进展、提问确认、提建议后大家赞同、顺着同一方向完善方案都不算分歧，拿不准就不写。格式 {question, topic, status, positions, outcome}：
  - question：争议的核心问题，一句话，不加 **；
  - topic：所属议题名（对应 topics 里的一个 title，没有就写空字符串）；
  - status：固定写英文 "open"（散会时仍未谈拢）或 "settled"（会上曾有不同主张，后面谈拢定了下来）；
  - positions：2 到 4 个不同立场，格式 {{if .Speakers}}{stance, line, holder, also, who}{{else}}{stance, line, who}{{end}}。stance 是这一方的主张短句，里面不写人名{{if .Speakers}}或说话人编号{{end}}、不加 **；who 是持这个立场的人名，只有转写里明确说出或日历支持才填，否则写空字符串；line 必须是持这个立场的人自己最初提出该主张的那一行行号，即使后面有别人转述、复述或总结双方观点，也必须填最初提出者的行号，不要填转述或总结的那一行；不能引用照片；{{if .Speakers}}holder 是持这个立场的人的说话人编号（如 "S2"，通常就是 line 所在行的编号；如果引用的是别人转述，请填持方本人的编号；没有编号就写空字符串）；also 是可选数组（最多 2 项），只在还有其他说话人明确支持同一立场时填，每项格式 {line, holder}（不能引用照片，没有其他支持者时省略或写空数组）；{{if .Language}}所有文字字段中一律不要写说话人编号（如 S2、{{if eq .Language "日本語"}}話者 2{{else}}Speaker 2{{end}}）；{{else}}所有文字字段中一律不要写说话人编号（如 S2、发言人 2）；{{end}}{{end}}
  - outcome：只有 status 为 "settled" 时才填，格式 {text, line}，写清最后怎么定下来的，line 必须填最终敲定的那一行（与 topics/decisions 里对应决定的 line 相同），不能引用照片；status 为 "open" 时省略或写 null。
  仍未谈拢的分歧（status 为 "open"）不要再重复写进 open（未决问题）里。
