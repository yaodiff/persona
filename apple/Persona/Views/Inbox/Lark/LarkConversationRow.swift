import SwiftUI

/// A chat in the Messages list (#392): avatar, name and time, the last message and its unread count. The iPhone's tab
/// (`LarkConversationsView`) and the Mac's Messages section (`MacLarkMessagesPage`, #3162) draw the same row.
struct LarkConversationRow: View {
    let conversation: LarkConversation
    let display: LarkMessageDisplay

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            LarkAvatarImage(name: conversation.name, url: conversation.picture, account: conversation.account, size: Theme.Size.avatarXL)
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack(spacing: Theme.Space.s) {
                    Text(conversation.name).font(Theme.Font.bodyStrong).foregroundStyle(.primary).lineLimit(1)
                    Spacer(minLength: 0)
                    if let date = conversation.updatedAt {
                        Text(Fmt.relative(date)).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
                HStack(spacing: Theme.Space.s) {
                    Text(display.summary(conversation.preview)).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
                    Spacer(minLength: 0)
                    if conversation.badge > 0 { CountBadge(count: conversation.badge) }
                    if conversation.muted { Image(systemName: Theme.Icon.muted).font(Theme.Font.caption).foregroundStyle(.secondary) }
                }
            }
        }
        .padding(.vertical, Theme.Space.s)
    }
}

/// One chip per signed-in account, its unread count beside the name; the chosen one is filled.
struct LarkAccountChips: View {
    let choice: LarkAccountChoice

    var body: some View {
        ScrollView(.horizontal) {
            HStack(spacing: Theme.Space.s) {
                ForEach(choice.accounts) { account in
                    FilterChip(title: account.title, count: account.unread > 0 ? account.unread : nil,
                               on: account.id == choice.selectedID) { choice.select(account.id) }
                        .accessibilityIdentifier("feishu-account-\(account.id)")
                }
            }
            .padding(.horizontal, Theme.Space.page)
        }
        .accessibilityLabel("Lark account")
    }
}

/// A personal Agent in the Messages list, drawn like a chat: its own face, its name with its service's (Grok Bot, Muse…)
/// beside it, the time and its latest message, and its unread count.
struct LarkAgentRow: View {
    let session: PersonalAgentSession

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            PersonalAgentAvatar(session: session, size: Theme.Size.avatarXL, showsCharge: true)
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack(spacing: Theme.Space.s) {
                    Text(session.title).font(Theme.Font.bodyStrong).foregroundStyle(.primary).lineLimit(1)
                    Text(PersonalAgentArrange.providerLabel(session.provider))
                        .font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                        .padding(.horizontal, Theme.Space.s).padding(.vertical, Theme.Space.xxs)
                        .background(Theme.Fill.well, in: Capsule())
                        .layoutPriority(-1)
                    if let charge = session.charge { PersonalAgentBattery(charge: charge) }
                    Spacer(minLength: 0)
                    if session.rankTimeMs > 0 {
                        Text(Fmt.relative(session.rankTime)).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
                HStack(spacing: Theme.Space.s) {
                    Text(LarkAgentFeed.preview(session) ?? String(localized: "AI Assistant", comment: "A personal Agent's row in the Messages list without a latest message"))
                        .font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
                    Spacer(minLength: 0)
                    if session.unreadCount > 0 { CountBadge(count: session.unreadCount) }
                }
            }
        }
        .padding(.vertical, Theme.Space.s)
        .accessibilityElement(children: .combine)
    }
}
