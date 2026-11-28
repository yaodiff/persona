import Foundation

/// Previous, Next and answers on a terminal dialog of several questions (#2970, ScreenStepsLogic). Every one goes out as
/// the card's answer, so the node checks the dialog before it presses anything, and the hub reads the screen again after
/// it: the question the terminal shows next comes as a card of its own. Nothing new is asked of the node or the hub.
extension AppModel {
    /// The step the card's dialog is on, when it can be told.
    func screenStep(_ ix: Interaction, _ d: ScreenParser.Dialog) -> Int? {
        ScreenStepsLogic.current(d, trail: screenTrails[ix.sessionKey], now: .now)
    }

    /// The card waits for the next one of its dialog after this device answered it.
    func isMoving(_ ix: Interaction) -> Bool { screenMoves[ix.sessionKey]?.card.uid == ix.uid }

    /// Previous / Next: ← or → once.
    func stepScreen(_ ix: Interaction, by move: Int) async {
        await answerScreen(ix, Resolution(keys: [move < 0 ? "left" : "right"]), move: move, answer: nil)
    }

    /// "Answer X" on the review: back with ←, one card at a time, until that question shows.
    func walkScreen(_ ix: Interaction, to step: Int) async {
        screenTrails[ix.sessionKey]?.walkTo = step
        await stepScreen(ix, by: -1)
    }

    /// An answer (or a key) from a card of a dialog with several questions. The trail notes where the next card should
    /// be; the session's other screen cards may come again (a question gone back to keeps the id it had, and this device
    /// ended it when it answered); the card stays, greyed, until the next one comes (`waits`: one is expected).
    func answerScreen(_ ix: Interaction, _ r: Resolution, move: Int, answer: String?, waits: Bool = true) async {
        let key = ix.sessionKey
        if let d = ix.screenDialog, d.steps.count > 1 {
            let now = Date.now
            let labels = d.steps.map(\.label)
            let trail = screenTrails[key].flatMap { $0.labels == labels ? $0 : nil } ?? ScreenTrail(labels: labels)
            let from = d.review ? d.steps.count : ScreenStepsLogic.current(d, trail: trail, now: now)
            screenTrails[key] = ScreenStepsLogic.sent(trail, from: from, move: move, answer: answer, now: now)
            if let prefix = ScreenStepsLogic.screenPrefix(ix.uid) {
                retainEndedInteractions { !$0.hasPrefix(prefix) || $0 == ix.uid }
            }
            if waits {
                screenMoves[key] = ScreenMove(card: ix, at: now)
                Task { [weak self] in
                    try? await Task.sleep(for: .seconds(ScreenStepsLogic.moveFor))
                    await self?.moveTimedOut(ix)
                }
            }
        }
        await resolve(ix, r)
        // Not sent (no network, refused): the card is back as it was.
        if interactions[ix.uid] != nil, isMoving(ix) { screenMoves[key] = nil }
    }

    /// A card came: the one answered before it in its session gives way, its dialog's trail notes the question, and
    /// a walk back to a question goes on. `live`: pushed now (not found in a full list).
    func noteScreenCard(_ ix: Interaction, live: Bool) {
        guard ix.kind == .screen else { return }
        let key = ix.sessionKey
        if screenMoves[key] != nil { screenMoves[key] = nil }
        guard let d = ix.screenDialog, d.steps.count > 1 else { return }
        var t = ScreenStepsLogic.seen(d, trail: screenTrails[key], now: .now)
        if let to = t.walkTo {
            if live, !d.review, let c = ScreenStepsLogic.current(d, trail: t, now: .now), c > to {
                screenTrails[key] = t
                Task { await stepScreen(ix, by: -1) }
                return
            }
            t.walkTo = nil
        }
        if screenTrails[key] != t { screenTrails[key] = t }
    }

    /// No next card came: the card answered comes back if the hub asks it again (the keys did not take), and the user
    /// is told to look at the terminal.
    private func moveTimedOut(_ ix: Interaction) async {
        let key = ix.sessionKey
        guard isMoving(ix) else { return }
        screenMoves[key] = nil
        screenTrails[key]?.walkTo = nil
        forgetEndedInteraction(ix)
        toast = Toast(String(localized: "The terminal didn't move to another question. Check it with View Terminal."), style: .error)
        let asked = Date.now
        guard let ixs = try? await api(ix.group)?.interactions() else { return }
        replaceInteractions(ix.group, ixs, askedAt: asked)
    }
}
