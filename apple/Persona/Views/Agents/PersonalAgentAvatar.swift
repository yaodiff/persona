import SwiftUI

extension PersonalAgentSession {
    /// Hub-relative avatar URL (the hub rasterizes SVGs to PNG); the version keys LarkMedia's memory and disk cache.
    var avatarPath: String? {
        guard hasAvatar == true else { return nil }
        return APIClient.agentPath(id) + "/avatar?v=" + (avatarVersion ?? "")
    }
}

/// A personal agent's avatar loaded with the hub's bearer auth through LarkMedia, or the title's first letter.
struct PersonalAgentAvatar: View {
    let session: PersonalAgentSession
    var size: CGFloat = 32
    /// Draw what is left of its allowance as a ring around it (`PersonalAgentChargeRing`), when the source says.
    var showsCharge = false
    @State private var image: UIImage?

    var body: some View {
        ZStack {
            Circle().fill(Color.secondary.opacity(0.15))
            if let image {
                Image(uiImage: image).resizable().scaledToFill()
            } else {
                Text(initial).font(.system(size: size * 0.45, weight: .medium)).foregroundStyle(.secondary)
            }
        }
        .frame(width: size, height: size)
        .clipShape(Circle())
        .modifier(PersonalAgentChargeRing(charge: showsCharge ? session.charge : nil, size: size))
        .accessibilityHidden(true)
        .task(id: session.avatarPath) {
            guard let path = session.avatarPath else { image = nil; return }
            if let hit = LarkMedia.shared.cached(path) { image = hit; return }
            image = await LarkMedia.shared.image(path, maxPixel: 256)
        }
    }

    private var initial: String {
        let t = session.title.trimmingCharacters(in: .whitespacesAndNewlines)
        return t.first.map { String($0).uppercased() } ?? "?"
    }
}
