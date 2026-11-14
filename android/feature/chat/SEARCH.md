# 会话内搜索与返回原位

入口在会话右上角「⋯ → 搜索」。全屏结果按正文、思考、工具名、工具输入、工具输出、系统事件分组，关键词高亮。点结果后展开对应工作行，定位并短暂描边；顶部「返回搜索前位置」恢复搜索前的消息键、像素偏移和工作行展开状态。重复搜索保留最初的返回位置。

搜索和聊天共用冻结的记录窗口，展开过的服务端折叠记录也参与搜索。历史页通过 `ChatRepository.loadOlder` 连续向前加载，失败或游标没有前进时停止，支持手动停止和继续加载。搜索页覆盖聊天时不标记消息已读。

`docs/protocol/protocol.md` 没有会话搜索接口，因此只在本地匹配已加载内容。更早记录来自已有的 `/items?before_seq=` 分页接口，不新增协议。Folded steps of folds already on screen are fetched for search without opening them (`ChatRepository.loadRange`, the same `from_seq` / `through_seq` pages). Truncated full text is still not searched. The page says so.

防抖、命中上限、描边时长和单次分页上限沿用 UX 3.3 的交互配置；搜索耗时、吞吐与内存占用未实测。

改动以 `ChatSearch*.kt` 和 `ChatListNavigation.kt` 为主。`ChatScreen.kt` 挂接入口、状态、导航及高亮容器；`ChatChrome.kt` 仅增加可选搜索回调和菜单项。`ChatContent.kt` 接收搜索命中以展开工具原始输入输出，`ChatState.kt` 提供当前窗口的可搜索记录。未改 App 外壳、iOS 或 hub。

关键测试在 `ChatSearchTest`，覆盖搜索范围、嵌套 JSON 字符串、字面匹配、高亮范围、命中上限、工具结果定位、返回锚点保留，以及连续分页上限和失败停止。

验证已通过：`:feature:chat:testDebugUnitTest`、`:core:chatlist:testDebugUnitTest`、`:app:assembleDebug` 和 `git diff --check`。新增两项关键测试均通过。未做模拟器深浅色截图验证。

本机默认的 Gradle daemon JDK 识别失败。验证时使用 Android Studio 内置 JDK，临时移开 `gradle-daemon-jvm.properties`，并传入 `-Pkotlin.compiler.execution.strategy=in-process` 避免 Kotlin daemon 写入沙箱外目录。构建结束已恢复原配置，没有提交构建配置改动。

中文说明已检查，中文句内半角标点和连续破折号命中均为零。
