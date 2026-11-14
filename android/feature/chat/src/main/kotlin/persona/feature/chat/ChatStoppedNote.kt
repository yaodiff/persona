package persona.feature.chat

import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import persona.core.designsystem.*

/** A stopped Agent keeps its transcript and shell; only the terminal's explicit Resume starts it again. */
@Composable
internal fun ChatStoppedNote(terminal: (() -> Unit)?, resume: (() -> Unit)?) {
    Column(Modifier.fillMaxWidth().testTag("chat-stopped").padding(horizontal = pagePadding, vertical = Tokens.Space.s)) {
        Text(stringResource(persona.core.designsystem.R.string.designsystem_agent_stopped),
            style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
        Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            terminal?.let { JTextButton(stringResource(R.string.chat_chrome_terminal), it) }
            resume?.let { JTextButton(stringResource(R.string.chat_stopped_resume), it) }
        }
    }
}
