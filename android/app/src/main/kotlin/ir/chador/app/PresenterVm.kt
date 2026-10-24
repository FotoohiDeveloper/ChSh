package ir.chador.app

import androidx.compose.runtime.Composable
import androidx.lifecycle.ViewModel
import androidx.lifecycle.ViewModelProvider
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.compose.viewModel
import kotlinx.coroutines.CoroutineScope

/** Holds a core presenter for the lifetime of a navigation entry; its coroutines stop when the screen is left. */
class PresenterVm<P : Any>(make: (CoroutineScope) -> P) : ViewModel() {
    val presenter: P = make(viewModelScope)
}

@Composable
fun <P : Any> rememberPresenter(key: String, make: (CoroutineScope) -> P): P {
    val vm: PresenterVm<P> = viewModel(
        key = key,
        factory = object : ViewModelProvider.Factory {
            @Suppress("UNCHECKED_CAST")
            override fun <T : ViewModel> create(modelClass: Class<T>): T = PresenterVm(make) as T
        },
    )
    return vm.presenter
}
