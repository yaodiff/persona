import Testing
import UIKit
@testable import Persona

/// The Agents search field's words stop growing at the top bars' size (#10165): its capsule is 44 pt and its band 54 pt
/// at every text size, so what is typed and the placeholder must fit them. The field in the list's row is made at launch,
/// before the window's cap is set, and kept the system's AX5 words (53 pt) in the capsule.
@MainActor struct AgentsSearchFieldTextSizeTests {
    /// iOS 18's shorter navigation clearance stops the bar one size earlier.
    private var topBarCap: UIContentSizeCategory {
        if #available(iOS 26.0, *) { .extraExtraExtraLarge } else { .extraExtraLarge }
    }

    /// A field as `AgentsSearchField.makeUIView` makes it: the search bar's own, taken out of the bar.
    private func field() -> AgentsSearchField.Holder {
        let bar = UISearchBar()
        bar.placeholder = "Search"
        let field = bar.searchTextField
        field.removeFromSuperview()
        return AgentsSearchField.Holder(field: field, bar: bar)
    }

    /// `view` in a window of that text size, laid out.
    private func show(_ view: UIView, at size: UIContentSizeCategory) throws -> UIWindow {
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: 390, height: 120)
        window.traitOverrides.preferredContentSizeCategory = size
        view.frame = CGRect(x: 0, y: 0, width: 360, height: Theme.Size.searchField)
        window.addSubview(view)
        window.isHidden = false
        window.layoutIfNeeded()
        return window
    }

    private func body(at size: UIContentSizeCategory) -> CGFloat {
        UIFont.preferredFont(forTextStyle: .body, compatibleWith: UITraitCollection(preferredContentSizeCategory: size)).pointSize
    }

    private func placeholderFont(_ field: UISearchTextField) throws -> UIFont {
        let placeholder = try #require(field.attributedPlaceholder)
        #expect(placeholder.length > 0)
        return try #require(placeholder.attribute(.font, at: 0, effectiveRange: nil) as? UIFont)
    }

    /// What was seen: the field's own size already the window's capped one when it came into the window, its words still
    /// the system's AX5 size from when it was made. UIKit follows only a change of the field's size, so they stayed.
    @Test func wordsMadeAtAnotherSizeAreFittedInTheWindow() throws {
        let holder = field()
        holder.traitOverrides.preferredContentSizeCategory = topBarCap
        holder.updateTraitsIfNeeded()
        let made = body(at: .accessibilityExtraExtraExtraLarge)
        holder.field.font = try #require(holder.field.font).withSize(made)
        let placeholder = NSMutableAttributedString(attributedString: try #require(holder.field.attributedPlaceholder))
        placeholder.addAttribute(.font, value: try placeholderFont(holder.field).withSize(made), range: NSRange(location: 0, length: placeholder.length))
        holder.field.attributedPlaceholder = placeholder
        let window = try show(holder, at: topBarCap)
        #expect(holder.field.font?.pointSize == body(at: topBarCap))
        #expect(try placeholderFont(holder.field).pointSize == body(at: topBarCap))
        window.isHidden = true
    }

    /// Past the top bars' size the words stay at it, whatever the window says, and fit the capsule.
    @Test(arguments: [UIContentSizeCategory.extraExtraExtraLarge, .accessibilityMedium, .accessibilityExtraExtraExtraLarge])
    func theWordsStopAtTheTopBarsSize(_ size: UIContentSizeCategory) throws {
        let holder = field()
        let window = try show(holder, at: size)
        let font = try #require(holder.field.font)
        #expect(font.pointSize == body(at: topBarCap))
        #expect(font.lineHeight < Theme.Size.searchField)
        #expect(try placeholderFont(holder.field).pointSize == body(at: topBarCap))
        window.isHidden = true
    }

    /// Up to the cap nothing changes: the words are the search bar's own at that size, in its face.
    @Test(arguments: [UIContentSizeCategory.extraSmall, .large, .extraExtraLarge, .extraExtraExtraLarge])
    func upToTheCapTheWordsAreTheBars(_ size: UIContentSizeCategory) throws {
        let bar = UISearchBar()
        bar.placeholder = "Search"
        let barWindow = try show(bar, at: min(size, topBarCap))
        let holder = field()
        let window = try show(holder, at: size)
        let font = try #require(holder.field.font), reference = try #require(bar.searchTextField.font)
        #expect(font.fontName == reference.fontName)
        #expect(font.pointSize == reference.pointSize)
        let placeholder = try placeholderFont(holder.field), referencePlaceholder = try placeholderFont(bar.searchTextField)
        #expect(placeholder.fontName == referencePlaceholder.fontName)
        #expect(placeholder.pointSize == referencePlaceholder.pointSize)
        window.isHidden = true
        barWindow.isHidden = true
    }

    /// The text size changed with the page open (Control Center, Settings): the words follow, up to the cap.
    @Test func theWordsFollowAChangeOfTheTextSize() throws {
        let holder = field()
        let window = try show(holder, at: .large)
        for (size, expected) in [(UIContentSizeCategory.extraExtraLarge, UIContentSizeCategory.extraExtraLarge),
                                 (.accessibilityExtraExtraExtraLarge, topBarCap), (.small, .small)] {
            window.traitOverrides.preferredContentSizeCategory = size
            window.layoutIfNeeded()
            holder.updateTraitsIfNeeded()
            #expect(holder.field.font?.pointSize == body(at: expected), "at \(size.rawValue)")
            #expect(try placeholderFont(holder.field).pointSize == body(at: expected), "at \(size.rawValue)")
        }
        window.isHidden = true
    }

    @Test func theCapIsTheTopBars() {
        #expect(AgentsSearchField.largestTypeSize == UIContentSizeCategory(Theme.Shell.topBarLargestTypeSize))
        #expect(AgentsSearchField.largestTypeSize == topBarCap)
        #expect(AgentsSearchField.textSize(for: .large) == .large)
        #expect(AgentsSearchField.textSize(for: .accessibilityExtraExtraExtraLarge) == AgentsSearchField.largestTypeSize)
    }
}
