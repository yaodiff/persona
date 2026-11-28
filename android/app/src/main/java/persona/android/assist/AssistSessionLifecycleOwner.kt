package persona.android.assist

import android.view.View
import androidx.activity.OnBackPressedDispatcher
import androidx.activity.OnBackPressedDispatcherOwner
import androidx.activity.setViewTreeOnBackPressedDispatcherOwner
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleOwner
import androidx.lifecycle.LifecycleRegistry
import androidx.lifecycle.ViewModelStore
import androidx.lifecycle.ViewModelStoreOwner
import androidx.lifecycle.setViewTreeLifecycleOwner
import androidx.lifecycle.setViewTreeViewModelStoreOwner
import androidx.savedstate.SavedStateRegistry
import androidx.savedstate.SavedStateRegistryController
import androidx.savedstate.SavedStateRegistryOwner
import androidx.savedstate.setViewTreeSavedStateRegistryOwner

/**
 * Provides AndroidX lifecycle, saved-state, ViewModelStore, and back-dispatch owners for a
 * [android.service.voice.VoiceInteractionSession] window hosting Compose UI. Supports multiple
 * show/hide cycles across a single session instance before [onDestroy].
 */
internal class AssistSessionLifecycleOwner(
    fallbackOnBackPressed: () -> Unit = {},
) : LifecycleOwner, SavedStateRegistryOwner, ViewModelStoreOwner, OnBackPressedDispatcherOwner {

    private val lifecycleRegistry = LifecycleRegistry(this)
    private val savedStateController = SavedStateRegistryController.create(this)
    private val store = ViewModelStore()
    private val backDispatcher = OnBackPressedDispatcher(fallbackOnBackPressed)
    private var created = false
    private var destroyed = false

    override val lifecycle: Lifecycle get() = lifecycleRegistry
    override val savedStateRegistry: SavedStateRegistry get() = savedStateController.savedStateRegistry
    override val viewModelStore: ViewModelStore get() = store
    override val onBackPressedDispatcher: OnBackPressedDispatcher get() = backDispatcher

    fun onCreate() {
        if (created || destroyed) return
        created = true
        savedStateController.performAttach()
        savedStateController.performRestore(null)
        lifecycleRegistry.currentState = Lifecycle.State.CREATED
    }

    /**
     * Attaches all ViewTree owners to both the session window's decorView (where Compose's
     * WindowRecomposer searches) and the content root returned from `onCreateContentView`.
     */
    fun attachTo(decorView: View?, contentView: View) {
        onCreate()
        listOfNotNull(decorView, contentView).distinct().forEach { target ->
            target.setViewTreeLifecycleOwner(this)
            target.setViewTreeSavedStateRegistryOwner(this)
            target.setViewTreeViewModelStoreOwner(this)
            target.setViewTreeOnBackPressedDispatcherOwner(this)
        }
    }

    fun onShow() {
        if (destroyed) return
        if (!created) onCreate()
        lifecycleRegistry.currentState = Lifecycle.State.RESUMED
    }

    fun onHide() {
        if (!created || destroyed) return
        if (lifecycleRegistry.currentState.isAtLeast(Lifecycle.State.STARTED)) {
            lifecycleRegistry.currentState = Lifecycle.State.CREATED
        }
    }

    /** Dispatches back to any enabled Compose [androidx.activity.compose.BackHandler] or fallback. */
    fun dispatchBackPressed() {
        if (destroyed) return
        backDispatcher.onBackPressed()
    }

    fun onDestroy() {
        if (destroyed) return
        destroyed = true
        if (created) {
            lifecycleRegistry.currentState = Lifecycle.State.DESTROYED
        }
        store.clear()
    }
}
