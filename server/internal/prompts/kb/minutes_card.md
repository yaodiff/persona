- headline：一句话说清这场会讨论了什么、结果如何，40 字左右。
- conclusions：核心结论，2 到 4 条，最重要的放前面：这场会最后落在哪里、定了什么。格式 {text, line}。依据照片的结论，只有议题里也有要点或决定引用了同一张照片时才填 photo；否则填 line，指向支持这条结论的那一行。
- topics：议题，按第一次讨论到的先后排，一般 2 到 8 个，最多 10 个。一个议题是一项工作或一个话题，比如一个模块、发布日期、性能。格式 {title, group, icon, points, decisions}：
  - title：议题名，12 字以内；
  - group：分组名，8 字左右。议题有 4 个或更多、而且能分成 2 到 4 组时才写，这时每个议题都要写；否则都写空字符串；
  - icon：照写下面的一个英文词：progress（进展）、schedule（排期）、release（发布）、performance（性能）、metrics（数据和指标）、tooling（工具）、quality（质量和缺陷）、design（设计）、people（人员）、customer（客户）、budget（预算）、risk（风险）、general（都不合适时）；
  - points：这个议题讨论的要点，按时间顺序，说清讨论了什么、有哪些观点或进展。格式 {text, line, who}，who 是提出或主讲的人；
  - decisions：这个议题里已经定下来的结论和决定，不包括讨论中的想法。格式 {text, line}。
  每个要点和决定都放进一个议题，不要写在 topics 外面。一个议题写 2 到 4 条，超过 5 条的拆成两个议题。
