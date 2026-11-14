import Foundation
import Testing
@testable import Persona

/// Same matter as… offers only eligible open matters, ordered and searched without touching view layout.
struct InboxSameMatterPickerTests {
    private func item(_ id: String, manager: String? = "manager-a", title: String? = nil,
                      parent: InboxParent? = nil) -> InboxItem {
        var item = InboxItem(id: id, source: "example", kind: "group", account: "example", accountName: "Example",
                             title: title ?? id, preview: "", count: 1)
        item.manager = manager
        item.parent = parent
        return item
    }

    private func parent(_ id: String) -> InboxParent {
        InboxParent(kind: "inbox", id: id)
    }

    private func candidates(_ items: [InboxItem], current: InboxItem, children: [InboxItem] = [], query: String = "") -> [String] {
        InboxSameMatter.candidates(items: items, item: current, children: children, query: query).map(\.id)
    }

    private func parentChain(_ prefix: String, links: Int, to current: InboxItem) -> [InboxItem] {
        precondition(links > 0)
        var rows = (0..<links).map { item("\(prefix)-\($0)") }
        for index in rows.indices {
            rows[index].parent = parent(index + 1 == links ? current.id : "\(prefix)-\(index + 1)")
        }
        return rows
    }

    @Test func candidatesKeepOnlyOpenItems() {
        let current = item("current")
        let open = item("open")
        var done = item("done")
        done.doneAt = Date(timeIntervalSince1970: 10)
        #expect(candidates([current, open, done], current: current) == ["open"])
    }

    @Test func candidatesExcludeSelfAndDirectChildren() {
        let current = item("current")
        let child = item("child")
        let other = item("other")
        #expect(candidates([other, child, current], current: current, children: [child]) == ["other"])
    }

    @Test func candidatesExcludeGrandchildrenAndGreatGrandchildren() {
        let current = item("current")
        let child = item("child", parent: parent("current"))
        let grandchild = item("grandchild", parent: parent("child"))
        let greatGrandchild = item("great-grandchild", parent: parent("grandchild"))
        let other = item("other")
        #expect(candidates([greatGrandchild, other, grandchild, child, current], current: current) == ["other"],
                "Every loaded descendant must be left out, not only the tree's direct children")
    }

    @Test func candidatesFollowParentsThroughDoneItems() {
        let current = item("current")
        var doneChild = item("done-child", parent: parent("current"))
        doneChild.doneAt = Date(timeIntervalSince1970: 10)
        let grandchild = item("grandchild", parent: parent("done-child"))
        let other = item("other")
        #expect(candidates([grandchild, other, doneChild, current], current: current) == ["other"],
                "A done item in the middle of a parent chain must not hide its descendants")
    }

    @Test func candidatesStopAtParentCycles() {
        let current = item("current")
        let first = item("first", parent: parent("second"))
        let second = item("second", parent: parent("first"))
        let selfParent = item("self-parent", parent: parent("self-parent"))
        #expect(candidates([selfParent, second, first, current], current: current) == ["first", "second", "self-parent"],
                "Corrupt cycles must terminate without excluding unrelated items")
    }

    @Test func candidatesStopAtTheParentDepthLimit() {
        let current = item("current")
        let atLimit = parentChain("at-limit", links: 50, to: current)
        let beyondLimit = parentChain("beyond-limit", links: 51, to: current)
        let ids = candidates(atLimit + beyondLimit + [current], current: current)
        #expect(!ids.contains("at-limit-0"), "A descendant exactly 50 parent links away must be excluded")
        #expect(ids.contains("beyond-limit-0"), "A chain deeper than 50 links must stop being followed")
    }

    @Test func parentLookupNeverBorrowsAnotherManagersItemWithTheSameID() {
        let current = item("current", manager: "manager-a")
        let candidate = item("candidate", manager: "manager-a", parent: parent("shared"))
        let localParent = item("shared", manager: "manager-a")
        let foreignParent = item("shared", manager: "manager-b", parent: parent("current"))
        #expect(candidates([foreignParent, localParent, candidate, current], current: current) == ["candidate", "shared"],
                "A same-id item in another manager must not change this manager's parent chain")
    }

    @Test func candidatesStayWithinTheCurrentManager() {
        let current = item("current", manager: "manager-a")
        let same = item("same", manager: "manager-a")
        let other = item("other", manager: "manager-b")
        let unowned = item("unowned", manager: nil)
        #expect(candidates([same, other, unowned], current: current) == ["same"])
    }

    @Test func aStampedThreadItemSelectsTheSameMergedListManager() {
        let currentInList = item("current", manager: "manager-a")
        let same = item("same", manager: "manager-a")
        let defaultManager = item("default", manager: nil)
        let unstamped = InboxTree(node: InboxParent(kind: "inbox", id: currentInList.id),
                                  item: item("current", manager: nil))
        let tree = InboxLine.stamping(unstamped, manager: currentInList.manager)
        let opened = tree.item ?? currentInList
        #expect(candidates([currentInList, same, defaultManager], current: opened, children: tree.children) == ["same"])
    }

    @Test func candidatesSortNewestFirstWithDateFallbacksAndIDTieBreak() {
        let current = item("current")
        var updated = item("newest")
        updated.updatedAt = Date(timeIntervalSince1970: 300)
        updated.startedAt = Date(timeIntervalSince1970: 1)

        var started = item("started")
        started.startedAt = Date(timeIntervalSince1970: 200)

        var older = item("older")
        older.updatedAt = Date(timeIntervalSince1970: 100)
        older.startedAt = Date(timeIntervalSince1970: 999) // `updatedAt` takes precedence over a newer start.

        let a = item("a")
        let b = item("b")
        #expect(candidates([b, older, a, started, updated], current: current) == ["newest", "started", "older", "a", "b"])
    }

    @Test func candidatesSearchTitlesCaseInsensitively() {
        let current = item("current")
        let match = item("match", title: "Quarterly PLAN")
        let other = item("other", title: "Something else")
        #expect(candidates([match, other], current: current, query: "plan") == ["match"])
        #expect(candidates([match, other], current: current, query: "  plan\n") == ["match"])
        #expect(candidates([match, other], current: current, query: "   ") == ["match", "other"])
    }

    @Test func candidatesSearchPlacesCaseInsensitively() {
        let current = item("current")
        var match = item("match", title: "One")
        match.`where` = "Project ROOM"
        let other = item("other", title: "Two")
        #expect(candidates([match, other], current: current, query: "room") == ["match"])
    }

    @Test func candidatesDoNotSearchSenderOrPreview() {
        let current = item("current")
        var sender = item("sender")
        sender.sender = "Needle"
        var preview = item("preview")
        preview.preview = "Needle"
        #expect(candidates([sender, preview], current: current, query: "needle").isEmpty)
    }

    @Test func everyParentConflictUsesTheSameLocalizedMessage() {
        let expected = String(localized: "This link isn't possible. Choose something else.")
        let conflicts = [
            APIError(status: 409, message: "parent would create a cycle", fields: ["code": "conflict"]),
            APIError(status: 409, message: "parent depth exceeds the limit", fields: ["code": "conflict"]),
            APIError(status: 409, message: "another refusal", fields: ["code": "something_else"]),
            APIError(status: 409, message: "no structured code")
        ]
        for conflict in conflicts {
            let message = InboxParentChange.conflictMessage(for: conflict)
            #expect(message == expected, "Every 409 must use the generic localized message without inspecting its code or text")
            #expect(message != conflict.message, "The hub's English error must not be shown")
        }
    }

    @Test func otherParentFailuresKeepTheExistingErrorPresentation() {
        let notFound = APIError(status: 404, message: "missing")
        #expect(InboxParentChange.conflictMessage(for: notFound) == nil,
                "Non-conflict API errors must continue through model.show")
        let other = NSError(domain: "example", code: 409)
        #expect(InboxParentChange.conflictMessage(for: other) == nil,
                "Only an APIError's HTTP status identifies a parent conflict")
    }

    @Test func locationLineJoinsDifferentPlaceAndSender() {
        var candidate = item("item")
        candidate.`where` = "Project room"
        candidate.sender = "Alex"
        candidate.preview = "Latest text is not a location"
        #expect(InboxSameMatter.locationLine(candidate) == "Project room · Alex")
    }

    @Test func locationLineSaysAnEqualPlaceAndSenderOnce() {
        var candidate = item("item")
        candidate.`where` = "Alex"
        candidate.sender = "Alex"
        #expect(InboxSameMatter.locationLine(candidate) == "Alex")
    }

    @Test func locationLineFallsBackToTheOneNonemptyValue() {
        var place = item("place")
        place.`where` = "Project room"
        place.sender = ""
        #expect(InboxSameMatter.locationLine(place) == "Project room")

        var sender = item("sender")
        sender.`where` = ""
        sender.sender = "Alex"
        #expect(InboxSameMatter.locationLine(sender) == "Alex")
    }

    @Test func locationLineIsNilWithoutPlaceOrSender() {
        var candidate = item("item")
        candidate.preview = "Latest text is never a fallback"
        #expect(InboxSameMatter.locationLine(candidate) == nil)
        candidate.`where` = ""
        candidate.sender = ""
        #expect(InboxSameMatter.locationLine(candidate) == nil)
        candidate.`where` = "  "
        candidate.sender = "\n"
        #expect(InboxSameMatter.locationLine(candidate) == nil)
    }
}
