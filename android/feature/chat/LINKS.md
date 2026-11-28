# 会话内链接

`ChatScreen` 已接入 `ChatLinkHost`。网页从底部铺满，拖动标题栏或把手可停在六成，再下拉关闭；右侧按钮也能切换档位。六成时给会话和输入框留出空间，无遮罩，网页内容区保留自己的滚动手势。标题栏是设计系统的件：返回（网页后退，到头了就关闭）、标题和网址、收起 / 铺满、⋯（复制链接、重新加载、在浏览器打开、分享）、关闭。

裸链接、Markdown、用户消息、工具代码和交互正文统一使用 App 内入口。链接按项目约定在中文、空白处结束，去掉尾部标点和不成对的括号。网页新窗口回到当前 WebView。长按（或大屏右键）对话里的 Markdown 链接与普通网址会弹出锚定下拉菜单（`ChatLinkMenu` / `JPointerMenu`），提供在 Persona 中打开、在浏览器打开、复制链接、分享（端口转发与非网页链接仅保留适用项），不与消息长按菜单或文字选择冲突。

## 链接协议（2026-11-02 按主线旧 App 补齐，`ChatLinks.kind`）

- `http(s)`：在卡片里打开。
- `persona://session/<机器>/<会话>`、`persona://tab/persona|agents|inbox`（`assistant` 当 persona）、`persona://assistant`、`persona://inbox`：只做跳转（打开那个对话 / 切 tab，开着的卡片先收起），由外壳经 `LocalChatAppLinks` 接到导航。`persona://pair`、`persona://open` 在对话里点了不认（外部打开时才配对 / 直达）。
- `mailto:`、`tel:`、其他 App 的协议、`intent:`：先确认（「写邮件？」「拨打电话？」「用其他 App 打开？」），再交给系统；`intent:` 去掉 component、selector、clip data 和所有 URI 授权标志，数据是 `persona:` / `intent:` / `content:` / `file:` 等或目标是本 App 的一律丢掉（`externalIntent`）。没有能打开的 App 时提示。
- `content:` `file:` `javascript:` `data:` `about:` `blob:` `vbscript:`：不打开，弹「无法打开链接」。
- 网页里的非 http 跳转：只有用户真点了、页面在屏幕上时才走上面的确认（预加载的页面和脚本拉不起 App），`persona:` 链接不从网页进来。

## 选中文字

对话里选区菜单最前面是「AI 修改」和「作为上下文」；没有对话的地方（外壳的卡片：知识库、Persona 首页之外的页面；收件箱打开的网页）只有「交给 Persona」：写一句要它做什么（可以不写，默认「看看上面这段。」），进 Persona 主对话并切到 Persona tab。菜单项在 `onPrepareActionMode` 里加、`SHOW_AS_ACTION_ALWAYS`、放在 Chromium 那一组的最小序号，才排在「复制」前面（旧 App 的经验）。

卡片里的原生飞书文档 / 公司圈帖子（hub 有飞书扩展时）选区菜单在「复制」后面加同样的项（`feature/inbox` 的 `LarkSelectable`），头部另有「作为上下文」按钮带上整篇（文档的正文是和 iOS、网页一样的标记「（整篇）」，Agent 自己去读；公司圈帖子带帖子正文）。

「AI 修改」可以不写要求（默认「按上面选中的内容修改。」）；「作为上下文」放进输入区，出站消息落入 outbox 后才清理，只有上下文时正文是「参考上面的内容。」。引用标签和 iOS 的 `ContextRef` 逐字一样：属性转义 `& " < >`，正文不转义、只去掉 `</persona-context>`；多个上下文之间一个换行，正文前空一行。读回时按 iOS 的规则解码实体，以前转义过正文的消息也读得回来。

## WebView 池

池随会话页面释放，最多 4 个页面（屏幕上的一个 + 最多 3 个预加载），LRU 淘汰，系统 HTTP 缓存（`LOAD_DEFAULT`，遵循服务端 `Cache-Control` / `ETag` 语义；端口转发经 `forget(url)` 使用 `LOAD_NO_CACHE`）、DOM 存储和第三方 Cookie（页面里嵌的登录要用）。只预加载 15 分钟内 Agent 回复里的前 3 个链接（最后 40 条里，主机没有点的不算，直链音视频文件不预加载；hub 有飞书扩展时卡片原生打开的飞书文档、公司圈链接也不算，文档改成预读进阅读器的缓存，见 `docs/android/android.md`「Lark through extensions」），列表停止滚动后预启动引擎，支持时调用预渲染 API，否则普通加载；后台页面暂停并在支持时静音，切回可见并挂载窗口后唤醒因后台暂停而挂起的 `<video>` 加载。`WebChromeClient` 接入 `onShowCustomView` / `onHideCustomView` / `getDefaultVideoPoster`，由 `ChatLinkHost` 通过 `JFullScreen` 承载全屏视频并在返回键、页面切换、释放与销毁时清理。渲染进程崩溃或被杀只丢掉那一个页面（`onRenderProcessGone`），卡片显示「网页意外退出了」和「重新载入」；系统内存紧张（`onTrimMemory`）时销毁不在屏幕上的页面。池容量和等待时间是实现配置，性能未实测。API 说明见 [Android WebViewCompat](https://developer.android.com/reference/androidx/webkit/WebViewCompat)。

## 当前 App 接线

`ShellChat` 与助手内嵌入口均使用已带 ChatLinkHost 的 ChatScreen，飞书会话挂 LarkLinkHost，外壳（`app/…/ui/ShellLinks.kt` 的 `ShellLinkHost`）为知识库等页面提供应用内链接入口，同时提供 `persona://` 的跳转和收件箱网页的「交给 Persona」（`feature/inbox` 的 `LocalInboxWebSelection`）。会话内选区上下文和 AI 修改已走现有草稿／发送器，随会话隔离。

## 验证与边界

`ChatLinksTest` 覆盖中文链接边界、Markdown 保护、和 iOS 逐字对照的标签与默认文字、读回旧格式、预加载的新鲜度和主机、协议分类、`persona://` 解析（含拒绝 pair / open）、外跳确认文案和 intent 目标的拒绝规则。

网页登录状态来自 WebView，不注入 hub 凭据。限制内嵌登录的 OAuth 站点需用户从菜单打开浏览器，尚未接 Custom Tabs。选中菜单的实际顺序、渲染进程崩溃后的恢复要在真机 / 模拟器上确认（Chromium 每个版本填菜单的方式可能不同）；跨 iframe 的选区、性能未实测。

## Reopening a link (ordinary pages and forwarded ports)

- An ordinary http(s) link serves one opening. When the card releases its view (closed, replaced by another link, or left), `ChatWebViewPool.release` destroys that view and at once preloads a fresh one for the link's original URL, as iOS does. The next tap gets a view that never showed anything: the link's own URL, no in-page history, no earlier title, failure or progress. Capacity stays at four; a loopback address (`localhost`, `127.0.0.1`, `::1`, `*.localhost`, with or without a port) is destroyed without a replacement and is never preloaded.
- A forwarded port keeps its own rule: the host calls `forget(local address)` before opening, a view on screen reloads in place, one kept aside is destroyed, and a port is never preloaded onto the phone's own loopback. Such a view is released without being destroyed, so hot reload on an open card is untouched.
- Release looks the view up by identity. A second release of the same view, a late release after trimming or a renderer loss, and a release of a view the pool no longer holds do nothing, so they never destroy the replacement. Only the card's `DisposableEffect(view)` releases; the page slot (`ChatLinkWebSlot`) leaving composition just detaches the view, so a slot that comes back with the same view is not blank. The effect depends on the view only: a change of text selection actions updates the view in place.
- A view becomes host-owned only when `SideEffect` calls `activate` after its composition commits. Before that, only the newest acquisition is pending and protected; replacing it makes an earlier, potentially abandoned acquisition evictable again. The committed page and newest pending page survive trimming, repeated abandoned compositions cannot accumulate permanent `held` entries, and the four-view cap does not destroy the active page.
- A destroyed pool is closed: `acquire` returns null, `preload` and `release` do nothing. The host closes it before its page's release runs (its effect is declared after the card's, and Compose forgets effects in reverse order), so leaving the chat does not build a replacement page for a pool that is about to go. That order is Compose's convention, not measured here.
- Preloading after a release is not filtered like the Agent-reply preload (last 15 minutes, three links, no native Feishu links). Closing a card can therefore request the original URL again, also for a link opened from somewhere that never preloads it, a Feishu link opened as a web page, and a one-time link (a sign-in callback, a confirmation link). iOS does the same; this wave does not change the policy. Whether that matters for one-time links, and the extra request and its cost, are open and unmeasured.
- What is shared is data, not pages: the cookie store and the disk cache belong to the WebView package and outlive every renderer, so a login stays. The pool never clears cookies, the cache or history globally; destroying a view only ends its own renderer.
- Tests: `ChatWebViewPoolTest` (which instance the pool hands out, when it destroys, creates or refuses a view, committed versus uncommitted ownership and the bounded pending path, on recording views: they do not exercise in-page history, real cookies or the disk cache), `ChatLinkReopenTest` (the real host with the WebView feature probes fixed to unsupported through `LocalChatWebFeatures`), `ChatLinkWebSlotTest` (the slot leaving and returning). Robolectric has no WebView implementation behind these probes, so the pool takes them through `ChatWebFeatures`; production uses `WebViewFeature`.
- Device evidence: `scripts/app/android_link_reopen_check.py` and `scripts/app/android_adaptive_scenes/chat-link-reopen.json`. `reopen` checks history and response timing; `cookie` requires two post-login responses to carry the same one-time session, which later `/login` preloads cannot reissue; `cache` requires a unique cacheable script to execute on reopen without a second network request. The cache result is a shared HTTP cache within this process, not proof of disk persistence after a process restart. Not verified: renderer process counts, phone-loopback traffic, per-reopen request counts, or cookie and disk stores after process death.

## HTML5 video playback and HTTP caching

- `ChatWebViewPool` configures `WebSettings.LOAD_DEFAULT` on every new WebView so standard HTTP `Cache-Control` / `ETag` headers apply: cacheable assets stay fast from the shared HTTP cache, while pages with `Cache-Control: no-store` / `no-cache` and HTML5 `<video>` byte-range requests are never served stale from disk. Forwarded ports (`forget(url)`) keep `WebSettings.LOAD_NO_CACHE`.
- `WebChromeClient` implements `onShowCustomView`, `onHideCustomView`, and a 1x1 transparent `getDefaultVideoPoster`. `ChatLinkHost` displays active custom views via `JFullScreen` over `PersonaTheme.palette.mediaBackdrop`, exits fullscreen before navigating back or closing the card, and `ChatWebViewPool` detaches the custom view and notifies `CustomViewCallback.onCustomViewHidden()` once on hide, navigation, page switch, release, renderer loss, trim, and pool destruction. Background/inactive views cannot present fullscreen custom views.
- Direct audio/video file URLs skip background preloading. For preloaded HTML pages paused while detached, `ChatWebViewPool` tracks pause state (avoiding redundant `onResume()` calls across Compose recompositions) and triggers a one-shot `<video>` `load()` recovery on stalled unplayed elements (`readyState < 2`, excluding `preload="none"`, `srcObject`, and `blob:` / `mediastream:` sources) once the view is active, resumed, attached to a window, and finished loading.

## Chrome-aligned page rendering

- `ChatWebViewPool` keeps algorithmic darkening and view-level force dark off (`setAlgorithmicDarkeningAllowed(settings, false)`, `settings.isAlgorithmicDarkeningAllowed = false` on API 33+, `isForceDarkAllowed = false`) so pages without dark-mode CSS are not heuristically inverted (including images and QR codes), while pages declaring `prefers-color-scheme: dark` still follow the app's dark theme (`values-night/themes.xml` `android:isLightTheme=false`).
- Viewport and pinch-zoom settings match Chrome and `HtmlPreview`: `useWideViewPort = true`, `loadWithOverviewMode = true`, `setSupportZoom(true)`, `builtInZoomControls = true`, and `displayZoomControls = false`.
- `mixedContentMode` uses `WebSettings.MIXED_CONTENT_COMPATIBILITY_MODE` so passive mixed resources (such as HTTP images and QR codes on HTTPS pages) render while active mixed content (HTTP scripts, stylesheets, iframes) remains blocked.

## File downloads (`ChatWebDownloads`)

- `ChatWebViewPool` registers a `DownloadListener` on every `ChatSelectionWebView`, gated to the active, unpaused, mounted (`parent != null`), non-speculative foreground view and strictly `http(s)` URLs (`ChatLinks.webUrl(url) != null`). Preloaded/background pages never start downloads; loopback/non-canonical numeric URLs and direct download extensions skip background preloading, and a preloaded extensionless URL that suppressed a download reloads cleanly when acquired in the foreground.
- Before enqueueing into Android's system `DownloadManager` (public `Environment.DIRECTORY_DOWNLOADS`, `VISIBILITY_VISIBLE_NOTIFY_COMPLETED`), `ChatWebDownloader.Platform` runs a fail-closed security preflight on `Dispatchers.IO`:
  - Static host classification (`classifyWebDownloadHost`) canonicalizes IPv6 literals (compressed/expanded, zone-stripped, IPv4-mapped `::ffff:0:0/96`, deprecated IPv4-compatible `::/96`, IPv4-translated `::ffff:0:0:0/96`, and NAT64 `64:ff9b::/96`) and parses WHATWG/POSIX `inet_aton` numeric IPv4 spellings (shorthand `127.1`, integer `2130706433`, hex `0x7f000001`, octal `0177.0.0.1`, `0.0.0.0`/`0`). Loopback/unspecified targets are rejected with an in-app notice, and other non-canonical/malformed numeric hosts fail closed as refused while normal domains and canonical non-loopback LAN/Tailscale dotted-quad IPv4 or IPv6 literals remain allowed.
  - Per-hop DNS resolution (`WebDownloadHostResolver`) rejects any host that resolves to a loopback or unspecified address on any returned record (failing closed if resolution fails or returns empty).
  - Manual redirect tracking (`instanceFollowRedirects = false`, up to 5 hops via `HEAD`, falling back to `GET` with `Range: bytes=0-0` on `403`/`405`/`501`) re-validates `http(s)` scheme, host safety, and DNS on every hop, scopes cookies strictly to same-origin hops (never forwarding cookies across origins), fails closed on redirect loops, missing `Location`, non-2xx responses, or network errors, and hands only the verified terminal URL to `DownloadManager`. Because `DownloadManager` runs in a separate system process and resolves DNS again when fetching, preflight cannot eliminate post-preflight DNS rebinding TOCTOU.
- Filenames prefer RFC 5987 `filename*=` then `URLUtil.guessFileName`, strip traversal/control/unpaired-surrogate characters, and cap the final UTF-8 byte length at `180` bytes on Unicode code-point boundaries (leaving headroom below the 255-byte filesystem component limit for `DownloadManager` suffixes while preserving extensions).
- Repeated triggers are bounded per WebView by `ChatWebDownloadGuard` (canonical-URL in-flight deduplication, 3-second post-completion cooldown, max 3 concurrent in-flight downloads per view; active coroutines are cancelled and guard state is cleared when the view is disposed, released, or destroyed).
- Known file sizes (`contentLength` from WebView or `Content-Length` / `Content-Range` total length from preflight) are capped at `512 MiB` with a localized error notice; unknown sizes (`<= 0`, such as chunked transfer encoding without `Content-Length`) are permitted because Android's `DownloadManager` does not expose a streaming byte-limit API.
- Only when `DownloadManager` itself is unavailable or rejects an already verified terminal URL does the handler fall back to opening that validated terminal URL in the system browser or showing a failure notice.
