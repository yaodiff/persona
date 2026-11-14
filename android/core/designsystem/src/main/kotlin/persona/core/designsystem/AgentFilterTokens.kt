package persona.core.designsystem

/** Final E geometry, composed from existing tokens; a 42dp frame in Android's 48dp touch row. */
internal object AgentFilterTokens {
    val height = Tokens.MinTouch.android - Tokens.Space.Standard.sm
    val chipHeight = Tokens.Size.slimBar - Tokens.Space.Standard.s
    val groupWidth = Tokens.MinTouch.android
    val separatorHeight = Tokens.Size.iconXS + Tokens.Space.Standard.xxs
    val stroke = Tokens.Size.line
    val inset get() = Tokens.Space.sm
    val gap get() = Tokens.Space.xs
    val chipPadding get() = Tokens.Space.s + Tokens.Space.xxs
    val contentGap get() = Tokens.Space.xs + Tokens.Size.line
    val dot = Tokens.Size.dotS
    val icon = Tokens.Size.iconXS
    val fadeEnd = Tokens.Size.badge
    const val collapsedCharacters = 2
    const val focusDelayMillis = 90L
    const val focusFraction = 0.45f
    const val selectedFillAlpha = 0.15f
}
