package persona.android.assist

import android.content.Context
import android.view.View
import android.widget.FrameLayout
import androidx.activity.OnBackPressedCallback
import androidx.activity.findViewTreeOnBackPressedDispatcherOwner
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.ViewModel
import androidx.lifecycle.ViewModelProvider
import androidx.lifecycle.findViewTreeLifecycleOwner
import androidx.lifecycle.findViewTreeViewModelStoreOwner
import androidx.savedstate.findViewTreeSavedStateRegistryOwner
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class AssistSessionLifecycleOwnerTest {
    private val context: Context get() = ApplicationProvider.getApplicationContext()

    class TrackingViewModel : ViewModel() {
        var cleared = false
        override fun onCleared() {
            cleared = true
        }
    }

    @Test
    fun attachesAllViewTreeOwnersAndSupportsRepeatedShowHideUntilDestroy() {
        var fallbackBacks = 0
        val owner = AssistSessionLifecycleOwner(fallbackOnBackPressed = { fallbackBacks++ })
        val decor = FrameLayout(context)
        val content = View(context)
        decor.addView(content)

        owner.onCreate()
        assertEquals(Lifecycle.State.CREATED, owner.lifecycle.currentState)

        owner.attachTo(decor, content)
        assertSame(owner, decor.findViewTreeLifecycleOwner())
        assertSame(owner, decor.findViewTreeSavedStateRegistryOwner())
        assertSame(owner, decor.findViewTreeViewModelStoreOwner())
        assertSame(owner, decor.findViewTreeOnBackPressedDispatcherOwner())
        assertSame(owner, content.findViewTreeLifecycleOwner())
        assertSame(owner, content.findViewTreeSavedStateRegistryOwner())
        assertSame(owner, content.findViewTreeViewModelStoreOwner())
        assertSame(owner, content.findViewTreeOnBackPressedDispatcherOwner())

        val vm = ViewModelProvider(owner)[TrackingViewModel::class.java]

        owner.onShow()
        assertEquals(Lifecycle.State.RESUMED, owner.lifecycle.currentState)

        owner.onHide()
        assertEquals(Lifecycle.State.CREATED, owner.lifecycle.currentState)
        assertEquals(false, vm.cleared)

        // Repeated show on a reused VoiceInteractionSession must resume cleanly.
        owner.onShow()
        assertEquals(Lifecycle.State.RESUMED, owner.lifecycle.currentState)

        // Fallback back press when no custom callback intercepts.
        owner.dispatchBackPressed()
        assertEquals(1, fallbackBacks)

        // Registered BackHandler callback intercepts before fallback.
        var customBacks = 0
        owner.onBackPressedDispatcher.addCallback(
            owner,
            object : OnBackPressedCallback(true) {
                override fun handleOnBackPressed() {
                    customBacks++
                }
            },
        )
        owner.dispatchBackPressed()
        assertEquals(1, customBacks)
        assertEquals(1, fallbackBacks)

        owner.onDestroy()
        assertEquals(Lifecycle.State.DESTROYED, owner.lifecycle.currentState)
        assertTrue(vm.cleared)
    }
}
