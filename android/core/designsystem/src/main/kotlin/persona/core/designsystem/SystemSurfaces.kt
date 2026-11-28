package persona.core.designsystem

/**
 * The tokens for surfaces the system draws from the app's description, outside Compose's theme: the home-screen widget
 * (Glance) cannot read [PersonaTheme], so it takes the same palette and type scale from here, for the user's style and
 * for each of light and dark (the launcher switches between them with the system).
 */
object SystemSurfaces {
    fun palette(style: AppStyle, dark: Boolean): PersonaPalette = Palettes.of(style, dark)

    /** Palette for [style] in [dark] appearance with the user's chosen [color] preset layered on top. */
    fun palette(style: AppStyle, dark: Boolean, color: ThemeColor): PersonaPalette {
        val base = Palettes.of(style, dark)
        return ThemeColors.accent(color, persona = null)?.let { ThemeColors.palette(base, it) } ?: base
    }

    fun type(style: AppStyle): PersonaType = Types.of(style)
}
