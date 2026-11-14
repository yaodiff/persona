#!/bin/sh
# Install personad (docs/operations/release.md "Publishing personad"). scripts/release/publish.sh serves this file as <site>/install.sh with
# PERSONA_DL filled in:
#
#   curl -fsSL <site>/install.sh | sh
#
# Puts the binary for this system at ~/.persona/bin/personad (where the hub looks for it on every computer it reaches
# over ssh), after checking it against the published SHA256SUMS; on the computer that runs the hub, persona-hub and
# persona-manager beside it too (docs/server/server.md "Programs"). PERSONA_DL=<url> installs from somewhere else.
#
# One hub is enough for a home (docs/archive/operations/plan-hub-center.md section 0); every other computer gets the node service: the
# resident node and the connector persona-link, paired with the hub (docs/operations/link.md "Install the node service"):
#
#   curl -fsSL <site>/install.sh | sh -s -- --node --pair '<link from personad hub pair --machine <id>>'   # this one connects to the hub
#   curl -fsSL <site>/install.sh | sh -s -- --node --listen [:18663] [--addr <address the hub reaches this one at>]  # the hub connects here
#
# --pair and --listen imply --node (PERSONA_ROLE=node, PERSONA_PAIR, PERSONA_LISTEN do the same). Without a role and with
# a terminal it asks, and says so when it hears a hub on this LAN already (mDNS). --hub is the machine that runs the hub.
#
# On the machine that runs the hub it also asks who may open the hub's admin UI (docs/operations/admin.md): with a terminal it
# asks; without one, give it as `sh -s -- --admin-access lan` (or PERSONA_ADMIN_ACCESS=lan), else lan applies and a
# line says so. --admin-cidrs a,b (PERSONA_ADMIN_CIDRS) goes with cidrs.
# Then herdr (the default engine, docs/integrations/engines.md) if this computer has none: herdr's own installer puts it in
# ~/.local/bin, where the node looks. PERSONA_SKIP_HERDR=1 leaves it out (Orca / Herdr App machines); a failed herdr
# install only warns. scripts/deploy/ensure-herdr.sh does the same for deploy.sh: keep the two in step.
set -eu

# >>> install language (scripts/deploy.sh reads this block with admin access; keep it POSIX sh and self-contained)
# install_language [locale]: print one of en, zh-Hans or ja. With no argument it reads LC_ALL, then LANG.
install_language() {
	_install_locale=${1:-${LC_ALL:-${LANG:-}}}
	case "$_install_locale" in
	zh* | ZH*) printf '%s' zh-Hans ;;
	ja* | JA*) printf '%s' ja ;;
	*) printf '%s' en ;;
	esac
}

# install_text <key>: print the text for key in INSTALL_LANG. Keys and all three translations live in this block.
# Catalog entries are printed as data: install_format, not this lookup, substitutes their placeholders.
INSTALL_TEXT_KEYS="admin_help admin_choose admin_invalid_choice admin_cidrs admin_any_confirm admin_any_fallback admin_reminder admin_reminder_if_hub lang_invalid unknown_option pair_hub_conflict pair_listen_conflict role_invalid admin_access_invalid state_conflict python_required state_symlink_unavailable legacy_command_directory artifact_missing unsupported_os unsupported_arch gzip_required fetch_required checksum_tool_required download_failed checksum_missing checksum_mismatch unpack_failed empty_binary create_dest_failed write_failed install_failed release_program_required release_program_kept node_link_required unknown_version program_installed not_on_path herdr_already herdr_installing herdr_installed herdr_failed node_connect_help node_connect_choose node_pair_prompt mac_connector node_switch_warning node_stays_warning hub_found hub_one_only install_node_confirm run_hub_confirm connect_node_confirm hub_setup_next hub_setup_incomplete hub_upgraded hub_run_next messaging_connectors other_computers"
# shellcheck disable=SC2183 # Catalog entries are format strings; install_format supplies their arguments.
install_text() {
	case "$INSTALL_LANG:$1" in
	en:admin_help) cat <<'TEXT'
Who can open the hub admin page (<hub address>/admin, for adding computers, managing devices and changing the network)?
  1) loopback  Only the hub machine itself (inside Docker, only the container). Other computers need an SSH tunnel. Safest, least convenient
  2) lan       This machine and the hub's local network (default). Available at home or the office, not from the internet or other networks
  3) cidrs     This machine and the networks you list, such as a VPN network. Public networks in the list still require TLS
  4) any       Anywhere through the TLS port (the local network still works). Exposed to the internet and protected only by the admin password and login rate limiting; without TLS this is the same as lan
Change it later on the hub with personad hub admin access <scope>. The web page can narrow access, but cannot widen it.
TEXT
		;;
	zh-Hans:admin_help) cat <<'TEXT'
hub 管理界面（<hub 地址>/admin：加电脑、管设备、改网络）谁能打开？
  1) loopback  只有 hub 这台机器自己（Docker 里是容器自己），别的电脑要用 ssh 隧道。最安全，用着最麻烦
  2) lan       本机 + 和 hub 同一网段的局域网（默认）。家里 / 办公室的局域网里都能打开，公网和别的网段打不开
  3) cidrs     本机 + 你列出的网段，例如 VPN 的网段；其中的公网网段也只能经 TLS
  4) any       任何地方经 TLS 端口都能打开（局域网照旧）。暴露在公网上，只靠管理员密码和登录限流挡着；没开 TLS 时等于 lan
以后在 hub 机器上用 personad hub admin access <范围> 改；网页里只能收窄，不能放宽。
TEXT
		;;
	ja:admin_help) cat <<'TEXT'
hub 管理画面（<hub のアドレス>/admin：コンピュータ、端末、ネットワークの管理）を誰が開けますか？
  1) loopback  hub のコンピュータだけ（Docker ではコンテナだけ）。ほかのコンピュータには SSH トンネルが必要です。最も安全ですが不便です
  2) lan       このコンピュータと hub と同じ LAN（既定）。自宅や職場の LAN から開けますが、インターネットや別のネットワークからは開けません
  3) cidrs     このコンピュータと、VPN など指定したネットワーク。指定した公開ネットワークでも TLS が必要です
  4) any       TLS ポートを通してどこからでも開けます（LAN も利用可能）。インターネットに公開され、管理者パスワードとログイン制限だけで保護されます。TLS がなければ lan と同じです
後で hub 上の personad hub admin access <範囲> で変更できます。Web 画面では範囲を狭めることだけができます。
TEXT
		;;
	en:admin_choose) printf '%s' 'Choose one [1-4, Enter = 2 lan] ' ;;
	zh-Hans:admin_choose) printf '%s' '选哪个？[1-4，回车 = 2 lan] ' ;;
	ja:admin_choose) printf '%s' '選んでください [1-4、Enter = 2 lan] ' ;;
	en:admin_invalid_choice) printf '%s' 'Enter a number from 1 to 4' ;;
	zh-Hans:admin_invalid_choice) printf '%s' '请输入 1 到 4' ;;
	ja:admin_invalid_choice) printf '%s' '1 から 4 を入力してください' ;;
	en:admin_cidrs) printf '%s' 'Networks, separated by commas (for example 100.64.0.0/10,192.0.2.0/24): ' ;;
	zh-Hans:admin_cidrs) printf '%s' '网段，逗号分隔（例如 100.64.0.0/10,192.0.2.0/24）：' ;;
	ja:admin_cidrs) printf '%s' 'ネットワークをカンマで区切って入力してください（例：100.64.0.0/10,192.0.2.0/24）：' ;;
	en:admin_any_confirm) printf '%s' 'The admin page will be open to the internet. Enter any to confirm; anything else uses lan: ' ;;
	zh-Hans:admin_any_confirm) printf '%s' '管理界面会对公网开放。输入 any 确认，别的改用 lan：' ;;
	ja:admin_any_confirm) printf '%s' '管理画面がインターネットに公開されます。確認するには any、それ以外なら lan を使用します：' ;;
	en:admin_any_fallback) printf '%s' 'Not confirmed; using lan' ;;
	zh-Hans:admin_any_fallback) printf '%s' '没有确认，改用 lan' ;;
	ja:admin_any_fallback) printf '%s' '確認されなかったため lan を使用します' ;;
	en:admin_reminder) printf '%s' 'Reminder: the admin page (/admin) access was not chosen; using lan (the hub machine and its local network). Change it on the hub with personad hub admin access <loopback|lan|cidrs|any>' ;;
	zh-Hans:admin_reminder) printf '%s' '提醒：hub 管理界面（/admin）的访问范围没有选，用默认的 lan（hub 本机和同一局域网）；要改在 hub 机器上运行 personad hub admin access <loopback|lan|cidrs|any>' ;;
	ja:admin_reminder) printf '%s' 'お知らせ：管理画面（/admin）のアクセス範囲が未選択のため、lan（hub と同じ LAN）を使用します。変更するには hub で personad hub admin access <loopback|lan|cidrs|any> を実行してください' ;;
	en:admin_reminder_if_hub) printf '%s' 'Reminder: if this computer runs the hub, the admin page (/admin) access was not chosen; using lan (the hub machine and its local network). Change it on the hub with personad hub admin access <loopback|lan|cidrs|any>' ;;
	zh-Hans:admin_reminder_if_hub) printf '%s' '提醒：如果这台跑 hub，hub 管理界面（/admin）的访问范围没有选，用默认的 lan（hub 本机和同一局域网）；要改在 hub 机器上运行 personad hub admin access <loopback|lan|cidrs|any>' ;;
	ja:admin_reminder_if_hub) printf '%s' 'お知らせ：このコンピュータで hub を動かす場合、管理画面（/admin）のアクセス範囲は未選択のため lan を使用します。変更するには hub で personad hub admin access <loopback|lan|cidrs|any> を実行してください' ;;
	en:lang_invalid) printf '%s' 'personad: --lang must be en, zh-Hans or ja' ;;
	zh-Hans:lang_invalid) printf '%s' 'personad：--lang 必须是 en、zh-Hans 或 ja' ;;
	ja:lang_invalid) printf '%s' 'personad：--lang は en、zh-Hans、ja のいずれかを指定してください' ;;
	en:unknown_option) printf '%s' 'personad: unknown option %s (--node [--pair <link> [--machine <id>] | --listen [addr] [--addr <host>]], --hub, --admin-access <loopback|lan|cidrs|any>, --admin-cidrs a,b, --lang <en|zh-Hans|ja>)' ;;
	zh-Hans:unknown_option) printf '%s' 'personad：未知选项 %s（可用：--node [--pair <链接> [--machine <id>] | --listen [地址] [--addr <主机>]]、--hub、--admin-access <loopback|lan|cidrs|any>、--admin-cidrs a,b、--lang <en|zh-Hans|ja>）' ;;
	ja:unknown_option) printf '%s' 'personad：不明なオプション %s（使用可能：--node [--pair <リンク> [--machine <id>] | --listen [アドレス] [--addr <ホスト>]]、--hub、--admin-access <loopback|lan|cidrs|any>、--admin-cidrs a,b、--lang <en|zh-Hans|ja>）' ;;
	en:pair_hub_conflict) printf '%s' 'personad: --pair / --listen are for the node service, not --hub' ;;
	zh-Hans:pair_hub_conflict) printf '%s' 'personad：--pair / --listen 用于 node 服务，不能和 --hub 一起使用' ;;
	ja:pair_hub_conflict) printf '%s' 'personad：--pair / --listen は node サービス用で、--hub では使用できません' ;;
	en:pair_listen_conflict) printf '%s' 'personad: use either --pair (this computer connects to the hub) or --listen (the hub connects here), not both' ;;
	zh-Hans:pair_listen_conflict) printf '%s' 'personad：--pair（这台电脑连接 hub）和 --listen（hub 连接这台电脑）只能选一个' ;;
	ja:pair_listen_conflict) printf '%s' 'personad：--pair（このコンピュータから hub へ接続）と --listen（hub から接続）のどちらか一方を使用してください' ;;
	en:role_invalid) printf '%s' 'personad: PERSONA_ROLE must be hub or node' ;;
	zh-Hans:role_invalid) printf '%s' 'personad：PERSONA_ROLE 必须是 hub 或 node' ;;
	ja:role_invalid) printf '%s' 'personad：PERSONA_ROLE は hub または node でなければなりません' ;;
	en:admin_access_invalid) printf '%s' 'personad: --admin-access must be loopback, lan, cidrs (with --admin-cidrs) or any' ;;
	zh-Hans:admin_access_invalid) printf '%s' 'personad：--admin-access 必须是 loopback、lan、cidrs（并提供 --admin-cidrs）或 any' ;;
	ja:admin_access_invalid) printf '%s' 'personad：--admin-access は loopback、lan、cidrs（--admin-cidrs が必要）、any のいずれかです' ;;
	en:state_conflict) printf '%s' 'personad: independent state directories; set PERSONA_HOME explicitly' ;;
	zh-Hans:state_conflict) printf '%s' 'personad：检测到相互独立的状态目录；请明确设置 PERSONA_HOME' ;;
	ja:state_conflict) printf '%s' 'personad：独立した状態ディレクトリが複数あります。PERSONA_HOME を明示してください' ;;
	en:python_required) printf '%s' 'personad: python3 is required to resolve existing service profiles safely' ;;
	zh-Hans:python_required) printf '%s' 'personad：安全解析现有服务配置需要 python3' ;;
	ja:python_required) printf '%s' 'personad：既存のサービス設定を安全に解決するには python3 が必要です' ;;
	en:state_symlink_unavailable) printf '%s' 'personad: unavailable state symlink %s: restore its target before upgrading' ;;
	zh-Hans:state_symlink_unavailable) printf '%s' 'personad：状态目录符号链接 %s 不可用；请先恢复其目标再升级' ;;
	ja:state_symlink_unavailable) printf '%s' 'personad：状態ディレクトリのシンボリックリンク %s を利用できません。アップグレード前にリンク先を復元してください' ;;
	en:legacy_command_directory) printf '%s' 'personad: legacy command is a directory: %s' ;;
	zh-Hans:legacy_command_directory) printf '%s' 'personad：旧命令路径是目录：%s' ;;
	ja:legacy_command_directory) printf '%s' 'personad：旧コマンドのパスがディレクトリです：%s' ;;
	en:artifact_missing) printf '%s' 'personad: %s is not in SHA256SUMS' ;;
	zh-Hans:artifact_missing) printf '%s' 'personad：SHA256SUMS 中没有 %s' ;;
	ja:artifact_missing) printf '%s' 'personad：SHA256SUMS に %s がありません' ;;
	en:unsupported_os) printf '%s' 'personad: no build for %s (Linux and macOS only)' ;;
	zh-Hans:unsupported_os) printf '%s' 'personad：没有适用于 %s 的构建（仅支持 Linux 和 macOS）' ;;
	ja:unsupported_os) printf '%s' 'personad：%s 用のビルドはありません（Linux と macOS のみ対応）' ;;
	en:unsupported_arch) printf '%s' 'personad: no build for %s' ;;
	zh-Hans:unsupported_arch) printf '%s' 'personad：没有适用于 %s 的构建' ;;
	ja:unsupported_arch) printf '%s' 'personad：%s 用のビルドはありません' ;;
	en:gzip_required) printf '%s' 'personad: gzip is required to unpack the download' ;;
	zh-Hans:gzip_required) printf '%s' 'personad：解压下载内容需要 gzip' ;;
	ja:gzip_required) printf '%s' 'personad：ダウンロードしたファイルの展開には gzip が必要です' ;;
	en:fetch_required) printf '%s' 'personad: curl or wget is required' ;;
	zh-Hans:fetch_required) printf '%s' 'personad：需要 curl 或 wget' ;;
	ja:fetch_required) printf '%s' 'personad：curl または wget が必要です' ;;
	en:checksum_tool_required) printf '%s' 'personad: sha256sum or shasum is required to check the download' ;;
	zh-Hans:checksum_tool_required) printf '%s' 'personad：校验下载内容需要 sha256sum 或 shasum' ;;
	ja:checksum_tool_required) printf '%s' 'personad：ダウンロードの検証には sha256sum または shasum が必要です' ;;
	en:download_failed) printf '%s' 'personad: could not download %s' ;;
	zh-Hans:download_failed) printf '%s' 'personad：无法下载 %s' ;;
	ja:download_failed) printf '%s' 'personad：%s をダウンロードできませんでした' ;;
	en:checksum_missing) printf '%s' 'personad: %s is not in SHA256SUMS' ;;
	zh-Hans:checksum_missing) printf '%s' 'personad：SHA256SUMS 中没有 %s' ;;
	ja:checksum_missing) printf '%s' 'personad：SHA256SUMS に %s がありません' ;;
	en:checksum_mismatch) printf '%s' 'personad: checksum mismatch for %s: not installed' ;;
	zh-Hans:checksum_mismatch) printf '%s' 'personad：%s 的校验和不匹配，未安装' ;;
	ja:checksum_mismatch) printf '%s' 'personad：%s のチェックサムが一致しないため、インストールしませんでした' ;;
	en:unpack_failed) printf '%s' 'personad: could not unpack %s: not installed' ;;
	zh-Hans:unpack_failed) printf '%s' 'personad：无法解压 %s，未安装' ;;
	ja:unpack_failed) printf '%s' 'personad：%s を展開できなかったため、インストールしませんでした' ;;
	en:empty_binary) printf '%s' 'personad: %s contains an empty binary: not installed' ;;
	zh-Hans:empty_binary) printf '%s' 'personad：%s 中的二进制文件为空，未安装' ;;
	ja:empty_binary) printf '%s' 'personad：%s 内のバイナリが空のため、インストールしませんでした' ;;
	en:create_dest_failed) printf '%s' 'personad: could not create %s: not installed' ;;
	zh-Hans:create_dest_failed) printf '%s' 'personad：无法创建 %s，未安装' ;;
	ja:create_dest_failed) printf '%s' 'personad：%s を作成できなかったため、インストールしませんでした' ;;
	en:write_failed) printf '%s' 'personad: could not write %s: not installed' ;;
	zh-Hans:write_failed) printf '%s' 'personad：无法写入 %s，未安装' ;;
	ja:write_failed) printf '%s' 'personad：%s に書き込めなかったため、インストールしませんでした' ;;
	en:install_failed) printf '%s' 'personad: could not install %s' ;;
	zh-Hans:install_failed) printf '%s' 'personad：无法安装 %s' ;;
	ja:install_failed) printf '%s' 'personad：%s をインストールできませんでした' ;;
	en:release_program_required) printf '%s' 'personad: this release has no %s for %s-%s, and %s runs %s: it would stay older than personad. Not installed; install a release that has it' ;;
	zh-Hans:release_program_required) printf '%s' 'personad：此版本没有适用于 %s（%s-%s）的构建，但 %s 正在运行 %s；它会比 personad 更旧。未安装，请改用包含该程序的版本' ;;
	ja:release_program_required) printf '%s' 'personad：このリリースには %s（%s-%s）がありませんが、%s が %s を実行しているため personad より古いままになります。インストールを中止しました。このプログラムを含むリリースを使用してください' ;;
	en:release_program_kept) printf '%s' 'This release has no %s for %s-%s: %s stays as it was (no service runs it)' ;;
	zh-Hans:release_program_kept) printf '%s' '此版本没有适用于 %s（%s-%s）的构建；%s 保持不变（没有服务运行它）' ;;
	ja:release_program_kept) printf '%s' 'このリリースには %s（%s-%s）がないため、%s はそのまま残します（実行中のサービスはありません）' ;;
	en:node_link_required) printf '%s' 'personad: the node service needs Persona Link (persona-link)' ;;
	zh-Hans:node_link_required) printf '%s' 'personad：node 服务需要 Persona Link（persona-link）' ;;
	ja:node_link_required) printf '%s' 'personad：node サービスには Persona Link（persona-link）が必要です' ;;
	en:unknown_version) printf '%s' 'unknown' ;;
	zh-Hans:unknown_version) printf '%s' '版本未知' ;;
	ja:unknown_version) printf '%s' 'バージョン不明' ;;
	en:program_installed) printf '%s' '%s %s installed at %s' ;;
	zh-Hans:program_installed) printf '%s' '%s %s 已安装到 %s' ;;
	ja:program_installed) printf '%s' '%s %s を %s にインストールしました' ;;
	en:not_on_path) printf '%s' '(not on PATH: run it as %s, or add %s to PATH)' ;;
	zh-Hans:not_on_path) printf '%s' '（不在 PATH 中：请运行 %s，或把 %s 加入 PATH）' ;;
	ja:not_on_path) printf '%s' '（PATH にありません：%s として実行するか、%s を PATH に追加してください）' ;;
	en:herdr_already) printf '%s' 'herdr: already installed' ;;
	zh-Hans:herdr_already) printf '%s' 'herdr：已经安装' ;;
	ja:herdr_already) printf '%s' 'herdr：インストール済みです' ;;
	en:herdr_installing) printf '%s' 'herdr: not found, installing it (PERSONA_SKIP_HERDR=1 skips this)' ;;
	zh-Hans:herdr_installing) printf '%s' 'herdr：未找到，正在安装（PERSONA_SKIP_HERDR=1 可跳过）' ;;
	ja:herdr_installing) printf '%s' 'herdr：見つからないためインストールします（PERSONA_SKIP_HERDR=1 で省略できます）' ;;
	en:herdr_installed) printf '%s' 'herdr: installed' ;;
	zh-Hans:herdr_installed) printf '%s' 'herdr：安装完成' ;;
	ja:herdr_installed) printf '%s' 'herdr：インストールしました' ;;
	en:herdr_failed) printf '%s' 'personad: warning: could not install herdr; install it yourself (https://herdr.dev) before using Agents on this computer' ;;
	zh-Hans:herdr_failed) printf '%s' 'personad：警告：无法安装 herdr；在这台电脑上使用 Agents 前，请自行安装（https://herdr.dev）' ;;
	ja:herdr_failed) printf '%s' 'personad：警告：herdr をインストールできませんでした。このコンピュータで Agents を使う前に https://herdr.dev からインストールしてください' ;;
	en:node_connect_help) printf '%s' 'How should this computer connect to the hub?\n  1) This computer connects to the hub: run personad hub pair --machine <this computer id> --add on the hub and paste the link here (laptops and computers behind NAT)\n  2) The hub connects here: listen on this computer and paste the printed link into Hub Admin > Computers > Add Computer > Hub connects here (servers with a stable address)' ;;
	zh-Hans:node_connect_help) printf '%s' '这台电脑怎么和 hub 连？\n  1) 我去连 hub：在 hub 机器上运行 personad hub pair --machine <这台的 id> --add，把拿到的链接粘贴过来（笔记本、在 NAT 后面的电脑）\n  2) 让 hub 来连：这台监听一个端口，打印一条链接粘贴到 hub 管理界面「电脑 › 添加电脑 › hub 来连」（服务器、地址固定的电脑）' ;;
	ja:node_connect_help) printf '%s' 'このコンピュータを hub にどう接続しますか？\n  1) このコンピュータから hub へ接続：hub で personad hub pair --machine <このコンピュータの id> --add を実行し、リンクを貼り付けます（ノート PC、NAT 内のコンピュータ）\n  2) hub からこのコンピュータへ接続：ここでポートを待ち受け、表示されたリンクを hub 管理画面の「コンピュータ › コンピュータを追加 › hub から接続」に貼り付けます（固定アドレスのサーバー）' ;;
	en:node_connect_choose) printf '%s' 'Choose one [1-2, Enter = 1] ' ;;
	zh-Hans:node_connect_choose) printf '%s' '选哪个？[1-2，回车 = 1] ' ;;
	ja:node_connect_choose) printf '%s' '選んでください [1-2、Enter = 1] ' ;;
	en:node_pair_prompt) printf '%s' 'Paste the pairing link (Enter to skip and run persona-link pair later): ' ;;
	zh-Hans:node_pair_prompt) printf '%s' '粘贴配对链接（回车跳过，以后再 persona-link pair）：' ;;
	ja:node_pair_prompt) printf '%s' 'ペアリングリンクを貼り付けてください（Enter で省略し、後で persona-link pair を実行）：' ;;
	en:mac_connector) printf '%s' 'The independent Persona Link service is installed and starts at login; the Electron desktop is optional. Check its status with %s status.' ;;
	zh-Hans:mac_connector) printf '%s' '独立 Persona Link 服务已安装，会在登录时启动，不依赖 Electron 桌面应用。用 %s status 查看状态。' ;;
	ja:mac_connector) printf '%s' '独立した Persona Link サービスをインストールし、ログイン時に開始するように設定しました。Electron デスクトップは不要です。%s status で状態を確認できます。' ;;
	en:node_switch_warning) printf '%s' 'personad: warning: the node service still runs persona-node; run %s node switch --to personad (on a Mac, in Terminal on the Mac)' ;;
	zh-Hans:node_switch_warning) printf '%s' 'personad：警告：node 服务仍在运行 persona-node；请运行 %s node switch --to personad（Mac 上请在终端运行）' ;;
	ja:node_switch_warning) printf '%s' 'personad：警告：node サービスはまだ persona-node を実行しています。%s node switch --to personad を実行してください（Mac ではターミナルで実行）' ;;
	en:node_stays_warning) printf '%s' 'personad: warning: the node service stays on personad (see above); retry with %s node switch (on a Mac, in Terminal on the Mac)' ;;
	zh-Hans:node_stays_warning) printf '%s' 'personad：警告：node 服务继续使用 personad（见上文）；请重试 %s node switch（Mac 上请在终端运行）' ;;
	ja:node_stays_warning) printf '%s' 'personad：警告：node サービスは personad のままです（上記参照）。%s node switch を再実行してください（Mac ではターミナルで実行）' ;;
	en:hub_found) printf '%s' 'A hub was found on the local network:' ;;
	zh-Hans:hub_found) printf '%s' '局域网里已经有 hub：' ;;
	ja:hub_found) printf '%s' 'LAN 上に hub が見つかりました：' ;;
	en:hub_one_only) printf '%s' 'A home needs only one hub. This computer only needs the node service (resident node and connector).' ;;
	zh-Hans:hub_one_only) printf '%s' '全家只要一台 hub；这台装 node 服务（常驻 node + 连接器）就够了。' ;;
	ja:hub_one_only) printf '%s' '家庭には hub が 1 台あれば十分です。このコンピュータには node サービス（常駐 node とコネクタ）だけをインストールします。' ;;
	en:install_node_confirm) printf '%s' 'Install the node service? [Y/n] ' ;;
	zh-Hans:install_node_confirm) printf '%s' '装成 node 服务？[Y/n] ' ;;
	ja:install_node_confirm) printf '%s' 'node サービスをインストールしますか？ [Y/n] ' ;;
	en:run_hub_confirm) printf '%s' 'Should this computer run the hub? [y/N] ' ;;
	zh-Hans:run_hub_confirm) printf '%s' '这台电脑要跑 hub 吗？[y/N] ' ;;
	ja:run_hub_confirm) printf '%s' 'このコンピュータで hub を動かしますか？ [y/N] ' ;;
	en:connect_node_confirm) printf '%s' 'Install the node service and connect it to your hub? [Y/n] ' ;;
	zh-Hans:connect_node_confirm) printf '%s' '装成 node 服务、连到你的 hub？[Y/n] ' ;;
	ja:connect_node_confirm) printf '%s' 'node サービスをインストールして hub に接続しますか？ [Y/n] ' ;;
	en:hub_setup_next) printf '%s' 'Next, set up this computer as the hub (%s hub setup):' ;;
	zh-Hans:hub_setup_next) printf '%s' '接着把这台电脑设成 hub（%s hub setup）：' ;;
	ja:hub_setup_next) printf '%s' '次に、このコンピュータを hub として設定します（%s hub setup）：' ;;
	en:hub_setup_incomplete) printf '%s' 'The hub is not ready yet. Follow the Next steps above, then run %s hub setup --lang %s to continue; completed steps will not be repeated.' ;;
	zh-Hans:hub_setup_incomplete) printf '%s' 'hub 还没装好：按上面的「下一步」处理后，运行 %s hub setup --lang %s 接着装（已经做好的不会重做）。' ;;
	ja:hub_setup_incomplete) printf '%s' 'hub の設定はまだ完了していません。上の「次の手順」に従い、%s hub setup --lang %s を実行して続けてください。完了済みの手順は繰り返されません。' ;;
	en:hub_upgraded) printf '%s' 'This computer is already the hub. personad was upgraded; restart the hub to use the new version (Linux: systemctl --user restart personad). To check the setup, run: %s hub setup' ;;
	zh-Hans:hub_upgraded) printf '%s' '这台已经是 hub，personad 升级好了；重启 hub 后用上新版本（Linux：systemctl --user restart personad）。要检查一遍：%s hub setup' ;;
	ja:hub_upgraded) printf '%s' 'このコンピュータはすでに hub です。personad を更新しました。新しい版を使うには hub を再起動してください（Linux：systemctl --user restart personad）。設定確認：%s hub setup' ;;
	en:hub_run_next) printf '%s' 'To run the hub on this computer, run %s hub setup --lang %s (or repeat the install command with --hub). A home needs only one hub.' ;;
	zh-Hans:hub_run_next) printf '%s' '要让这台电脑跑 hub：运行 %s hub setup --lang %s（或重跑安装命令并加 --hub）。全家只要一台 hub。' ;;
	ja:hub_run_next) printf '%s' 'このコンピュータで hub を動かすには、%s hub setup --lang %s を実行してください（または --hub を付けてインストールを再実行）。家庭には hub が 1 台あれば十分です。' ;;
	en:messaging_connectors) printf '%s' 'Messaging connectors are installed from the hub extension marketplace.' ;;
	zh-Hans:messaging_connectors) printf '%s' '消息连接器从 hub 的扩展市场安装。' ;;
	ja:messaging_connectors) printf '%s' 'メッセージ用コネクタは hub の拡張機能マーケットからインストールします。' ;;
	en:other_computers) printf '%s' "On other computers, install the node service by adding --node --pair '<link printed by personad hub pair --machine <id>>' to the install command (or use --listen so the hub connects to it)." ;;
	zh-Hans:other_computers) printf '%s' "其他电脑装 node 服务：安装命令加 --node --pair '<personad hub pair --machine <id> 打印的链接>'（或 --listen：让 hub 来连）。" ;;
	ja:other_computers) printf '%s' "ほかのコンピュータには、インストールコマンドに --node --pair '<personad hub pair --machine <id> が表示したリンク>' を追加して node サービスをインストールします（または --listen で hub から接続）。" ;;
	*) return 1 ;;
	esac
}

# say / warn / prompt <key> [printf arguments...]: localized stdout, stderr and /dev/tty output.
install_format() {
	_install_key=$1
	shift
	_install_format=$(install_text "$_install_key") || {
		printf 'personad: missing installer text %s/%s\n' "$INSTALL_LANG" "$_install_key" >&2
		return 70
	}
	# The format string is trusted repository text; dynamic values are always separate arguments.
	printf "$_install_format" "$@"
}

say() {
	install_format "$@"
	printf '\n'
}

warn() {
	install_format "$@" >&2
	printf '\n' >&2
}

prompt() {
	install_format "$@" >/dev/tty
}

install_text_check() {
	_install_old_lang=${INSTALL_LANG:-}
	for INSTALL_LANG in en zh-Hans ja; do
		for _install_check_key in $INSTALL_TEXT_KEYS; do install_text "$_install_check_key" >/dev/null || return 1; done
	done
	INSTALL_LANG=$_install_old_lang
}
# <<< install language

INSTALL_LANG=$(install_language)
install_lang_seen=
install_lang_value=
install_lang_next=
for install_arg do
	if [ -n "$install_lang_next" ]; then
		install_lang_seen=1
		install_lang_value=$install_arg
		install_lang_next=
		continue
	fi
	case "$install_arg" in
	--lang) install_lang_next=1 ;;
	--lang=*) install_lang_seen=1; install_lang_value=${install_arg#*=} ;;
	esac
done
if [ -n "$install_lang_next" ] || { [ -n "$install_lang_seen" ] && [ -z "$install_lang_value" ]; }; then
	warn lang_invalid
	exit 2
fi
if [ -n "$install_lang_seen" ]; then
	case "$install_lang_value" in en | zh-Hans | ja) INSTALL_LANG=$install_lang_value ;; *) warn lang_invalid; exit 2 ;; esac
fi

# Standalone compatibility adapter: preserve explicit empty canonical overrides.
for suffix in ADMIN_ACCESS ADMIN_CIDRS ROLE PAIR MACHINE LISTEN DL HERDR_INSTALL HOME WITH SKIP_HERDR; do
    eval 'present=${PERSONA_'"$suffix"'+x}'
    eval 'legacy_present=${JARVIS_'"$suffix"'+x}'
    if [ "$present" != x ] && [ "$legacy_present" = x ]; then
        eval 'value=${JARVIS_'"$suffix"'-}'
        export "PERSONA_$suffix=$value"
    fi
done
LEGACY_ROOT="$HOME/.jarvis"

# >>> admin access (scripts/deploy.sh reads this block from here: keep it POSIX sh and self-contained)
admin_access_help() {
	install_text admin_help
	printf '\n'
}

# admin_access_valid <access> <cidrs>: the scope is one of the four, and the networks look like networks.
admin_access_valid() {
	case "$1" in loopback | lan | any) [ -z "$2" ] ;; cidrs) [ -n "$2" ] && case "$2" in *[!0-9A-Fa-f.:/,]*) false ;; *) true ;; esac ;; *) false ;; esac
}

# ask_admin_access: ADMIN_ACCESS (and ADMIN_CIDRS) from the terminal; returns 1 when there is none (curl | sh still has
# /dev/tty when run by a person; CI and cron do not).
ask_admin_access() {
	(: </dev/tty) 2>/dev/null || return 1
	admin_access_help >/dev/tty
	while :; do
		prompt admin_choose
		read -r answer </dev/tty || return 1
		case "$answer" in
		'' | 2 | lan) ADMIN_ACCESS=lan ;;
		1 | loopback) ADMIN_ACCESS=loopback ;;
		3 | cidrs) ADMIN_ACCESS=cidrs ;;
		4 | any) ADMIN_ACCESS=any ;;
		*) install_text admin_invalid_choice >/dev/tty && printf '\n' >/dev/tty && continue ;;
		esac
		break
	done
	ADMIN_CIDRS=
	while [ "$ADMIN_ACCESS" = cidrs ] && ! admin_access_valid cidrs "$ADMIN_CIDRS"; do
		prompt admin_cidrs
		read -r ADMIN_CIDRS </dev/tty || return 1
		ADMIN_CIDRS=$(printf '%s' "$ADMIN_CIDRS" | tr -d ' ')
	done
	if [ "$ADMIN_ACCESS" = any ]; then
		prompt admin_any_confirm
		read -r confirm </dev/tty || return 1
		[ "$confirm" = any ] || { install_text admin_any_fallback >/dev/tty && printf '\n' >/dev/tty && ADMIN_ACCESS=lan; }
	fi
	return 0
}

# admin_access_reminder [if-hub]: the one line printed when nobody chose (no terminal, no option).
admin_access_reminder() {
	if [ -n "${1:-}" ]; then warn admin_reminder_if_hub; else warn admin_reminder; fi
}
# <<< admin access

ADMIN_ACCESS="${PERSONA_ADMIN_ACCESS:-}"
ADMIN_CIDRS="${PERSONA_ADMIN_CIDRS:-}"
ROLE="${PERSONA_ROLE:-}"
PAIR="${PERSONA_PAIR:-}"
MACHINE="${PERSONA_MACHINE:-}"
LISTEN="${PERSONA_LISTEN:-}"
LISTEN_SET="${PERSONA_LISTEN:+1}"
while [ $# -gt 0 ]; do
	case "$1" in
	--admin-access) ADMIN_ACCESS="${2:-}" && shift ;;
	--admin-access=*) ADMIN_ACCESS="${1#*=}" ;;
	--admin-cidrs) ADMIN_CIDRS="${2:-}" && shift ;;
	--admin-cidrs=*) ADMIN_CIDRS="${1#*=}" ;;
	--node) ROLE=node ;;
	--hub) ROLE=hub ;;
	--pair) PAIR="${2:-}" && shift ;;
	--pair=*) PAIR="${1#*=}" ;;
	--machine) MACHINE="${2:-}" && shift ;;
	--machine=*) MACHINE="${1#*=}" ;;
	--listen)
		LISTEN_SET=1
		case "${2:-}" in '' | -*) ;; *) LISTEN="$2" && shift ;; esac ;;
	--listen=*) LISTEN_SET=1 && LISTEN="${1#*=}" ;;
	--addr) ADDRS="${ADDRS:-} ${2:-}" && LISTEN_SET=1 && shift ;;
	--addr=*) ADDRS="${ADDRS:-} ${1#*=}" && LISTEN_SET=1 ;;
	--lang) shift ;;
	--lang=*) ;;
	*) warn unknown_option "$1" && exit 2 ;;
	esac
	shift
done
if [ -n "$PAIR" ] || [ -n "$LISTEN_SET" ]; then
	[ "$ROLE" != hub ] || { warn pair_hub_conflict && exit 2; }
	ROLE=node
fi
if [ -n "$PAIR" ] && [ -n "$LISTEN_SET" ]; then
	warn pair_listen_conflict
	exit 2
fi
case "$ROLE" in '' | hub | node) ;; *) warn role_invalid && exit 2 ;; esac
if [ -n "$ADMIN_ACCESS" ] && ! admin_access_valid "$ADMIN_ACCESS" "$ADMIN_CIDRS"; then
	warn admin_access_invalid
	exit 2
fi

lexical_path() {
	rest="$1"
	result=
	while [ -n "$rest" ]; do
		part="${rest%%/*}"
		if [ "$rest" = "$part" ]; then rest=; else rest="${rest#*/}"; fi
		case "$part" in '' | .) ;; ..) result="${result%/*}" ;; *) result="$result/$part" ;; esac
	done
	printf '%s' "${result:-/}"
}

DL="${PERSONA_DL:-@PERSONA_DL@}"
HERDR_INSTALL="${PERSONA_HERDR_INSTALL:-https://herdr.dev/install.sh}"
STATE="${PERSONA_HOME:-}"
if [ -z "$STATE" ]; then
    if { [ -e "$HOME/.persona" ] || [ -L "$HOME/.persona" ]; } && { [ -e "$LEGACY_ROOT" ] || [ -L "$LEGACY_ROOT" ]; }; then
        [ "$HOME/.persona" -ef "$LEGACY_ROOT" ] || { warn state_conflict; exit 1; }
    fi
    if [ -e "$LEGACY_ROOT" ] || [ -L "$LEGACY_ROOT" ]; then STATE="$LEGACY_ROOT"; else STATE="$HOME/.persona"; fi
fi
# The published installer embeds the same credential-free service resolver used by deploy.sh.
IDENTITY_CODE='@PERSONA_IDENTITY@'
identity_script="$(dirname "$0")/../persona_identity.py"
if [ -f "$identity_script" ]; then
    resolved=$(python3 "$identity_script" --service "${ROLE:-auto}") || exit $?
    STATE="$resolved"
elif [ "$IDENTITY_CODE" != '@''PERSONA_IDENTITY@' ]; then
    command -v python3 >/dev/null 2>&1 || { warn python_required; exit 1; }
    resolved=$(python3 -c "import base64; exec(base64.b64decode('$IDENTITY_CODE'))" --service "${ROLE:-auto}") || exit $?
    STATE="$resolved"
fi
case "$STATE" in '~') STATE="$HOME" ;; '~/'*) STATE="$HOME/${STATE#\~/}" ;; esac
case "$STATE" in /*) ;; *) STATE="$HOME/$STATE" ;; esac
STATE="$(lexical_path "$STATE")"
DEST="$STATE/bin"
export PERSONA_HOME="$STATE" JARVIS_HOME="$STATE"

fail() {
	warn "$@"
	exit 1
}

prepare_state() {
	if [ -L "$STATE" ] && [ ! -d "$STATE" ]; then
		fail state_symlink_unavailable "$STATE"
	fi
    mkdir -p "$STATE"
    case "$STATE" in
    "$HOME/.persona") [ -e "$LEGACY_ROOT" ] || [ -L "$LEGACY_ROOT" ] || ln -s .persona "$LEGACY_ROOT" ;;
    "$LEGACY_ROOT") [ -e "$HOME/.persona" ] || [ -L "$HOME/.persona" ] || ln -s .jarvis "$HOME/.persona" ;;
    esac
}

compat_command() {
    case "$1" in personad) old=jarvisd ;; persona-link) old=jarvis-link ;; *) return ;; esac
    [ ! -d "$DEST/$old" ] || fail legacy_command_directory "$DEST/$old"
    ln -sf "$1" "$DEST/$old"
}

artifact_name() {
    current="$1"
    case "$current" in personad-*) old="jarvisd-${current#personad-}" ;; persona-link-*) old="jarvis-link-${current#persona-link-}" ;; *) old="$current" ;; esac
    for candidate in "$current" "$old"; do
        if awk -v n="$candidate.gz" '$2 == n || $2 == "*" n { found=1 } END { exit !found }' "$tmp/SHA256SUMS"; then
            printf '%s' "$candidate"
            return
        fi
    done
    fail artifact_missing "$current.gz"
}

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) fail unsupported_os "$(uname -s)" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail unsupported_arch "$(uname -m)" ;;
esac
name="personad-$os-$arch"
archive="$name.gz"
command -v gzip >/dev/null 2>&1 || fail gzip_required

if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -q "$1" -O "$2"; }
else
	fail fetch_required
fi
if command -v sha256sum >/dev/null 2>&1; then
	sum() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
	sum() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
	fail checksum_tool_required
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fetch "$DL/SHA256SUMS" "$tmp/SHA256SUMS" || fail download_failed "$DL/SHA256SUMS"

# get <name> <installed as>: downloads <name>.gz, checks it against SHA256SUMS and puts it in $DEST.
get() {
	fetch_checked "$1"
	place "$1" "$2"
}

# fetch_checked <name>: downloads <name>.gz, checks it against SHA256SUMS and unpacks it to $tmp/<name>.
fetch_checked() {
    source=$(artifact_name "$1") || return 1
	archive="$source.gz"
	fetch "$DL/$archive" "$tmp/$archive" || fail download_failed "$DL/$archive"
	want="$(awk -v n="$archive" '$2 == n || $2 == "*" n { print $1 }' "$tmp/SHA256SUMS")"
	[ -n "$want" ] || fail checksum_missing "$archive"
	[ "$(sum "$tmp/$archive")" = "$want" ] || fail checksum_mismatch "$archive"
	gzip -dc "$tmp/$archive" > "$tmp/$1" || fail unpack_failed "$archive"
	[ -s "$tmp/$1" ] || fail empty_binary "$archive"
}

# place <name> <installed as>: puts $tmp/<name> (fetch_checked) in $DEST.
place() {
	prepare_state
	mkdir -p "$DEST" || fail create_dest_failed "$DEST"
	# Copied beside the old one first, then renamed over it: the temporary directory can be another file system, and a
	# resident node running $DEST/$2 must never find it missing or half written.
	cp "$tmp/$1" "$DEST/.$2.new" && chmod 755 "$DEST/.$2.new" || fail write_failed "$DEST/.$2.new"
	mv -f "$DEST/.$2.new" "$DEST/$2" || fail install_failed "$DEST/$2"
    compat_command "$2"
}

# The device's own program (docs/operations/link.md "The device's program"): the node service on a device runs persona-node,
# at $DEST/persona-node. A computer that runs a hub or a manager keeps its node on personad, which their services run
# too and which updates with the node (hosts_hub_or_manager).
has_node_build() { awk -v n="persona-node-$os-$arch.gz" '$2 == n || $2 == "*" n { found = 1 } END { exit !found }' "$tmp/SHA256SUMS"; }
hosts_hub_or_manager() {
	[ -f "$STATE/hub.json" ] && return 0
	for f in "$STATE"/managers/*/manager.json; do
		if [ -f "$f" ]; then return 0; fi
	done
	return 1
}
# get_node_program: persona-node at $DEST/persona-node, beside whatever else is there (personad stays).
get_node_program() {
	get "persona-node-$os-$arch" persona-node
	say program_installed persona-node "$("$DEST/persona-node" version 2>/dev/null || install_text unknown_version)" "$DEST/persona-node"
}

# The hub's and the built-in manager's own programs (docs/server/server.md "Programs"), on the computer that runs the
# hub: the same build as personad, installed with it, so that a service that runs one never runs an older one. A
# release without one leaves the file here as it was, unless a service runs it: then nothing is installed (the service
# would go on starting the older one beside the new personad). Other computers do not get them.
HUB_PROGRAMS="persona-hub persona-manager"
in_release() { awk -v n="$1.gz" '$2 == n || $2 == "*" n { found = 1 } END { exit !found }' "$tmp/SHA256SUMS"; }
# runs_hub: this computer runs the hub (--hub, a hub.json), or has the hub's programs from before.
runs_hub() {
	if [ "$ROLE" = hub ] || [ -f "$STATE/hub.json" ]; then return 0; fi
	for p in $HUB_PROGRAMS; do
		if [ -e "$DEST/$p" ]; then return 0; fi
	done
	return 1
}
# unit_forms <path>: the ways a systemd unit names <path>: as is, with systemd's %% and $$, and inside quotes as the
# hub's setup and hub-systemd.sh write it (\\ and \" too). One per line.
unit_forms() { printf '%s\n' "$1" "$1" "$1" | sed '2s/%/%%/g; 2s/\$/$$/g; 3s/[\\"]/\\&/g; 3s/%/%%/g; 3s/\$/$$/g'; }
# service_runs <program>: prints a file here that names $DEST/<program> (in a form of unit_forms, or under %h), or fails
# when there is none: a systemd unit or drop-in with it in an ExecStart line (any section; key and = may have spaces
# around them), or a launchd property list with it in a <string>. Followed by a space, a quote, "<" or the line's end:
# persona-hub.prev is not persona-hub. Wider than the hub's own check (docs/operations/hub-update.md "Programs swapped
# together"): a unit a later drop-in took back to personad, a disabled one and a wrapper (sh -c '...') count too.
service_runs() {
	forms=$(unit_forms "$DEST/$1")
	case $DEST in "$HOME"/*) forms="$forms
$(unit_forms "${DEST#"$HOME"}/$1" | sed 's/^/%h/')" ;; esac
	config="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user" data="${XDG_DATA_HOME:-$HOME/.local/share}/systemd/user"
	for f in "$config"/*.service "$config"/*.service.d/*.conf /etc/systemd/user/*.service /etc/systemd/user/*.service.d/*.conf \
		"$data"/*.service "$data"/*.service.d/*.conf /usr/lib/systemd/user/*.service /usr/lib/systemd/user/*.service.d/*.conf \
		/etc/systemd/system/*.service /etc/systemd/system/*.service.d/*.conf /usr/lib/systemd/system/*.service \
		/usr/lib/systemd/system/*.service.d/*.conf \
		"$HOME"/Library/LaunchAgents/*.plist /Library/LaunchAgents/*.plist /Library/LaunchDaemons/*.plist; do
		[ -f "$f" ] || continue
		# The paths go in through the environment: awk -v would read backslashes in them as escapes. In bytes (LC_ALL=C):
		# macOS awk stops on a binary property list in a UTF-8 locale and prints its bytes; a byte match is the same here.
		if SERVICE_FORMS="$forms" LC_ALL=C awk '
			function names(s, p,  i, c) {
				i = index(s, p)
				if (p == "" || !i) return 0
				c = substr(s, i + length(p), 1)
				return c == "" || c == " " || c == "\t" || c == "\"" || c == "'\''" || c == "<"
			}
			BEGIN { n = split(ENVIRON["SERVICE_FORMS"], form, "\n") }
			/^[ \t]*ExecStart[ \t]*=/ || /<string>/ { for (k = 1; k <= n; k++) if (names($0, form[k])) { found = 1; exit } }
			END { exit !found }' "$f"; then
			echo "$f"
			return 0
		fi
	done
	return 1
}
# hub_programs: the ones this release has, as words; fails when it lacks one that a service here runs.
hub_programs() {
	for p in $HUB_PROGRAMS; do
		if in_release "$p-$os-$arch"; then
			printf '%s ' "$p"
		elif [ -e "$DEST/$p" ] && by=$(service_runs "$p"); then
			fail release_program_required "$p" "$os" "$arch" "$by" "$DEST/$p"
		elif [ -e "$DEST/$p" ]; then
			warn release_program_kept "$p" "$os" "$arch" "$DEST/$p"
		fi
	done
}
# get_hub_programs [personad]: the hub's programs (and personad first with "personad"), each downloaded and checked
# before any is put in place. Once only.
get_hub_programs() {
	[ -z "${HUB_PROGRAMS_FETCHED:-}" ] || return 0
	programs=$(hub_programs) || exit 1
	if [ "${1:-}" = personad ]; then fetch_checked "$name"; fi
	for p in $programs; do fetch_checked "$p-$os-$arch"; done
	if [ "${1:-}" = personad ]; then place "$name" personad; fi
	for p in $programs; do
		place "$p-$os-$arch" "$p"
		say program_installed "$p" "$("$DEST/$p" version 2>/dev/null || install_text unknown_version)" "$DEST/$p"
	done
	HUB_PROGRAMS_FETCHED=1
}

# The node service given up front (--node) on a device: persona-node and Persona Link, no personad. Everything else:
# personad, as before (the questions below may add persona-node: node_service); with the hub's programs on the
# computer that runs the hub.
if [ "$ROLE" = node ] && has_node_build && ! hosts_hub_or_manager; then
	get_node_program
	program=persona-node
	# A personad here from before (the node service may still run it) is brought up to date too: it is what the
	# service goes back to when the switch to persona-node does not work out.
	if [ -e "$DEST/personad" ]; then get "$name" personad; fi
elif runs_hub; then
	get_hub_programs personad
	program=personad
	version="$("$DEST/personad" version 2>/dev/null || install_text unknown_version)"
	say program_installed personad "$version" "$DEST/personad"
else
	get "$name" personad
	program=personad
	version="$("$DEST/personad" version 2>/dev/null || install_text unknown_version)"
	say program_installed personad "$version" "$DEST/personad"
fi
case ":$PATH:" in
*":$DEST:"*) ;;
*) say not_on_path "$DEST/$program" "$DEST" ;;
esac

# The places the node looks besides PATH (server/internal/node/node.go: ssh sessions get a minimal PATH).
has_herdr() {
	command -v herdr >/dev/null 2>&1 && return 0
	for d in "$HOME/.local/bin" "$HOME/bin" /opt/homebrew/bin /usr/local/bin; do
		[ -x "$d/herdr" ] && return 0
	done
	return 1
}
if [ "${PERSONA_SKIP_HERDR:-}" = 1 ]; then
	:
elif has_herdr; then
	say herdr_already
else
	say herdr_installing
	if fetch "$HERDR_INSTALL" "$tmp/herdr-install.sh" && sh "$tmp/herdr-install.sh" && has_herdr; then
		say herdr_installed
	else
		warn herdr_failed
	fi
fi
# fetch_link puts persona-link (the connector, Persona Link) at $DEST/persona-link, checked against SHA256SUMS like
# personad. Only the canonical command is installed.
fetch_link() {
	[ -z "${LINK_FETCHED:-}" ] || return 0
    lname=$(artifact_name "persona-link-$os-$arch") || return 1
	larchive="$lname.gz"
	fetch "$DL/$larchive" "$tmp/$larchive" || { warn download_failed "$DL/$larchive" && return 1; }
	lwant="$(awk -v n="$larchive" '$2 == n || $2 == "*" n { print $1 }' "$tmp/SHA256SUMS")"
	[ -n "$lwant" ] || { warn checksum_missing "$larchive" && return 1; }
	[ "$(sum "$tmp/$larchive")" = "$lwant" ] || { warn checksum_mismatch "$larchive" && return 1; }
	gzip -dc "$tmp/$larchive" > "$tmp/$lname" && [ -s "$tmp/$lname" ] || { warn unpack_failed "$larchive" && return 1; }
	cp "$tmp/$lname" "$DEST/.persona-link.new" && chmod 755 "$DEST/.persona-link.new" && mv -f "$DEST/.persona-link.new" "$DEST/persona-link" ||
		{ warn install_failed "$DEST/persona-link" && return 1; }
    compat_command persona-link
	LINK_FETCHED=1
	say program_installed persona-link "$("$DEST/persona-link" version 2>/dev/null || install_text unknown_version)" "$DEST/persona-link"
}

# node_service: the node service, chosen at the questions below: with the device's own program when the release has it.
node_service() {
	if has_node_build && ! hosts_hub_or_manager; then get_node_program; fi
	install_node
}

# install_node makes this computer the node service: the resident node and the connector, paired with the hub
# (docs/operations/link.md). persona-link does the work; this only picks how it pairs.
install_node() {
	fetch_link || fail node_link_required
	if [ -z "$PAIR" ] && [ -z "$LISTEN_SET" ] && (: </dev/tty) 2>/dev/null; then
		install_format node_connect_help >/dev/tty
		printf '\n' >/dev/tty
		prompt node_connect_choose
		read -r how </dev/tty || how=
		case "$how" in
		2) LISTEN_SET=1 ;;
		*)
			prompt node_pair_prompt
			read -r PAIR </dev/tty || PAIR=
			;;
		esac
	fi
	set --
	if [ -n "$PAIR" ]; then
		set -- --pair "$PAIR"
		[ -z "$MACHINE" ] || set -- "$@" --machine "$MACHINE"
	elif [ -n "$LISTEN_SET" ]; then
		set -- --listen
		[ -z "$LISTEN" ] || set -- "$@" "$LISTEN"
		for a in ${ADDRS:-}; do set -- "$@" --addr "$a"; done # addresses have no spaces
	fi
	if [ "$os" = darwin ]; then
		# The same CLI pairs and installs the independent connector and resident-node services. It needs no desktop app.
		"$DEST/persona-link" install "$@" || return 1
		switch_node
		say mac_connector "$DEST/persona-link"
		return 0
	fi
	"$DEST/persona-link" install "$@" || return 1
	switch_node
}

# node_runs_persona_node: this computer's node service (the systemd unit or launchd agent, whoever set it up) runs persona-node.
node_runs_persona_node() {
	for f in "${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/personad-node.service" "$HOME/Library/LaunchAgents/com.persona.node.plist"; do
		if [ -f "$f" ] && grep -q '/persona-node[" <]' "$f"; then return 0; fi
	done
	return 1
}
# unswitch_node: a computer that is to run a hub keeps its node on personad, which updates with the node: a node service
# that runs persona-node moves back (persona-link node switch --to personad, with the release's persona-link, or the one
# here when that cannot be downloaded).
unswitch_node() {
	node_runs_persona_node || return 0
	if { fetch_link || [ -x "$DEST/persona-link" ]; } && "$DEST/persona-link" node switch --to personad; then return 0; fi
	if [ -x "$DEST/persona-link" ]; then link_cmd="$DEST/persona-link"; else link_cmd="persona-link (install Persona Link first)"; fi
	warn node_switch_warning "$link_cmd"
	return 0
}

# switch_node: a node service that runs personad on a device moves over to persona-node (persona-link node switch:
# whole or not at all, back on personad when persona-node does not answer). A new service runs persona-node already.
switch_node() {
	if [ -x "$DEST/persona-node" ] && ! hosts_hub_or_manager; then
		"$DEST/persona-link" node switch || warn node_stays_warning "$DEST/persona-link"
	fi
	return 0
}

# Who may open the admin UI, on the machine that runs the hub (one with a hub.json, or one the person says is).
set_admin_access() {
	if [ "$ADMIN_ACCESS" = cidrs ]; then
		"$DEST/personad" hub admin access cidrs --cidrs "$ADMIN_CIDRS"
	else
		"$DEST/personad" hub admin access "$ADMIN_ACCESS"
	fi
}
if [ "$ROLE" = node ]; then
	install_node
	exit $?
fi
if [ -n "$ADMIN_ACCESS" ]; then
	set_admin_access
elif [ -f "$STATE/hub.json" ]; then
	if [ -z "$("$DEST/personad" hub admin access --get 2>/dev/null || true)" ]; then
		if ask_admin_access; then set_admin_access; else admin_access_reminder; fi
	fi
elif [ "$ROLE" = hub ]; then
	if ask_admin_access; then set_admin_access; else admin_access_reminder; fi
elif (: </dev/tty) 2>/dev/null; then
	# A hub on this LAN already: this computer only needs the node service (docs/archive/operations/plan-hub-center.md section 0).
	found=
	if fetch_link 2>/dev/null; then found="$("$DEST/persona-link" discover --wait 3 2>/dev/null || true)"; fi
	if [ -n "$found" ]; then
		{
			say hub_found
			echo "$found" | sed 's/^/  /'
			say hub_one_only
		} >/dev/tty
		prompt install_node_confirm
		read -r as_node </dev/tty || as_node=
		case "$as_node" in n | N | no) ;; *) node_service && exit 0 || exit 1 ;; esac
	fi
	prompt run_hub_confirm
	read -r is_hub </dev/tty || is_hub=
	case "$is_hub" in
	y | Y | yes)
		ROLE=hub
		if ask_admin_access; then set_admin_access; else admin_access_reminder; fi
		;;
	*)
		prompt connect_node_confirm
		read -r as_node </dev/tty || as_node=
		case "$as_node" in n | N | no) ;; *) node_service && exit 0 || exit 1 ;; esac
		;;
	esac
else
	admin_access_reminder if-hub
fi

# The machine that runs the hub: personad hub setup does the rest (hub.json, the database, the service, the phone's
# QR code and the admin URL, docs/assistant/onboarding.md 4.1). Running it again is safe, so an upgrade with --hub runs it too.
# PERSONA_INSTALL_LANG: a new hub starts in the language this installer ran in (hub.json initial_locale, #5519); an
# environment variable, which an older personad ignores rather than refusing an unknown flag.
if [ "$ROLE" = hub ]; then
	unswitch_node
	get_hub_programs # the person said so at the question above, after personad was installed
	echo ""
	say hub_setup_next "$DEST/personad"
	if PERSONA_INSTALL_LANG="$INSTALL_LANG" "$DEST/personad" hub setup; then
		exit 0
	fi
	warn hub_setup_incomplete "$DEST/personad" "$INSTALL_LANG"
	exit 1
fi
# A hub that has run has a token in hub.json; set_admin_access above may have written one with admin.access only.
if [ -f "$STATE/hub.json" ] && grep -Eq '"token"[[:space:]]*:[[:space:]]*"[^"]' "$STATE/hub.json"; then
	say hub_upgraded "$DEST/personad"
	exit 0
fi
say hub_run_next "$DEST/personad" "$INSTALL_LANG"
say messaging_connectors
say other_computers
