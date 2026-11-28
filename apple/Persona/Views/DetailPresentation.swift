import SwiftUI

/// A detail page opened from a chat (a task, an agent, a todo): on a phone it rises as a sheet; in regular width (iPad,
/// iPhone Duo open) a sheet is the system's small form in the middle of the screen, so there it is pushed onto the
/// stack the chat is in and fills the detail column, as an agent opened from the Agents list does (#2816). The Mac and
/// Apple Vision Pro keep their sheets.
///
/// Both presentations stay attached whatever the width (only their bindings change): switching modifiers on a size
/// class change would rebuild the chat under them.
private struct DetailPresentation<Page: View>: ViewModifier {
    @Environment(\.horizontalSizeClass) private var widthClass
    @Binding var isPresented: Bool
    @ViewBuilder var page: () -> Page

    private var pushes: Bool {
        #if os(iOS)
        widthClass == .regular
        #else
        false
        #endif
    }

    func body(content: Content) -> some View {
        content
            .sheet(isPresented: Binding(get: { isPresented && !pushes }, set: { isPresented = $0 })) {
                page()
            }
            #if os(iOS)
            .navigationDestination(isPresented: Binding(get: { isPresented && pushes }, set: { isPresented = $0 })) {
                page().environment(\.detailPushed, true)
            }
            #endif
    }
}

extension EnvironmentValues {
    /// The page was pushed by `detailPresentation` (regular width), not risen as a sheet: no stack of its own (it is
    /// in the chat's), and its close goes back.
    @Entry var detailPushed = false
    /// The stack the chat is in takes a task's detail as a value on its path (`TaskDetailRoute`, #13156): the Sidebar
    /// layout's page in regular width, where a page pushed by view stayed in front of every root the sidebar picked.
    @Entry var taskDetailByValue = false
}

extension View {
    /// A detail page: a sheet on a phone, pushed into the detail column in regular width (`DetailPresentation`). The
    /// page wraps itself in a `NavigationStack` only when not `detailPushed` (`DetailStack`).
    func detailPresentation<Page: View>(isPresented: Binding<Bool>, @ViewBuilder page: @escaping () -> Page) -> some View {
        modifier(DetailPresentation(isPresented: isPresented, page: page))
    }

    /// The same, shown while `item` is set.
    func detailPresentation<Item, Page: View>(item: Binding<Item?>, @ViewBuilder page: @escaping (Item) -> Page) -> some View {
        modifier(ItemDetailPresentation(item: item, page: page))
    }
}

/// `detailPresentation(item:)`: the last item stays drawn while the page slides or sinks away after `item` is cleared.
private struct ItemDetailPresentation<Item, Page: View>: ViewModifier {
    @Binding var item: Item?
    var page: (Item) -> Page
    @State private var last: Item?

    func body(content: Content) -> some View {
        content
            .detailPresentation(isPresented: Binding(get: { item != nil }, set: { if !$0 { item = nil } })) {
                if let value = item ?? last { page(value) }
            }
            .onChange(of: item != nil) { if let item { last = item } }
    }
}

/// A detail page's own stack when it is a sheet; pushed (`detailPushed`), it is already in the chat's.
struct DetailStack<Content: View>: View {
    @Environment(\.detailPushed) private var pushed
    @ViewBuilder var content: Content

    var body: some View {
        if pushed { content } else { NavigationStack { content } }
    }
}
