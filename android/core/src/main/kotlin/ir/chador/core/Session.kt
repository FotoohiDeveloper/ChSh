package ir.chador.core

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

@Serializable
data class MeResponse(
    val id: String,
    val username: String,
    @SerialName("full_name") val fullName: String,
    val role: String,
    @SerialName("needs_shift") val needsShift: Boolean = false,
    @SerialName("shift_active") val shiftActive: Boolean = false,
    @SerialName("shift_id") val shiftId: String? = null,
    @SerialName("place_id") val placeId: Int? = null,
    @SerialName("door_id") val doorId: Int? = null,
)

/** What is persisted between app launches (encrypted by the Android implementation of SessionStorage). */
@Serializable
data class StoredSession(val tokens: TokenPair, val user: UserView, val shift: ShiftView? = null)

interface SessionStorage {
    fun load(): StoredSession?
    fun save(s: StoredSession)
    fun clear()
}

class MemorySessionStorage : SessionStorage {
    @Volatile
    var value: StoredSession? = null
    override fun load() = value
    override fun save(s: StoredSession) { value = s }
    override fun clear() { value = null }
}

sealed interface SessionState {
    data object Loading : SessionState
    data class LoggedOut(val message: String? = null) : SessionState
    data class MustChangePassword(val user: UserView) : SessionState
    data class NeedsShift(val user: UserView) : SessionState
    data class Ready(val user: UserView, val shift: ShiftView?) : SessionState
}

data class LoginUi(val busy: Boolean = false, val error: String? = null)

fun ApiError.userMessage(): String = message

class SessionManager(
    private val api: Api,
    private val tokens: TokenHolder,
    private val storage: SessionStorage,
    private val scope: CoroutineScope,
) {
    private val _state = MutableStateFlow<SessionState>(SessionState.Loading)
    val state: StateFlow<SessionState> = _state.asStateFlow()
    private val _ui = MutableStateFlow(LoginUi())
    val ui: StateFlow<LoginUi> = _ui.asStateFlow()

    @Volatile private var user: UserView? = null
    @Volatile private var shift: ShiftView? = null

    val userId: String? get() = user?.id
    val role: Role get() = user?.roleEnum ?: Role.UNKNOWN
    val shiftId: String? get() = shift?.id
    val currentUser: UserView? get() = user
    val currentShift: ShiftView? get() = shift

    private fun persist(tp: TokenPair? = tokens.pair) {
        val u = user
        if (tp != null && u != null) storage.save(StoredSession(tp, u, shift))
    }

    /** Called by ApiClient after a silent token refresh. */
    fun onTokensChanged(tp: TokenPair?) {
        if (tp != null) persist(tp)
    }

    /** Called by ApiClient when the refresh token was refused: the session is over. */
    fun onExpired() {
        user = null; shift = null
        storage.clear()
        _state.value = SessionState.LoggedOut("نشست شما تمام شد؛ دوباره وارد شوید. عملیات ذخیره‌شده از بین نمی‌رود.")
    }

    private fun settle(u: UserView, s: ShiftView?) {
        user = u; shift = s
        _state.value = when {
            u.mustChangePassword -> SessionState.MustChangePassword(u)
            u.roleEnum.needsShift && s == null -> SessionState.NeedsShift(u)
            else -> SessionState.Ready(u, s)
        }
    }

    /** App start: reuse the stored session. Works offline (the stored profile is trusted until the server says otherwise). */
    fun restore() {
        val stored = storage.load()
        if (stored == null) {
            _state.value = SessionState.LoggedOut()
            return
        }
        tokens.pair = stored.tokens
        user = stored.user; shift = stored.shift
        settle(stored.user, stored.shift)
        scope.launch {
            when (val r = api.me()) {
                is ApiResult.Ok -> {
                    val me = r.value
                    val keep = if (me.shiftActive && stored.shift?.id == me.shiftId) stored.shift else null
                    val refreshed = stored.user.copy(role = me.role, fullName = me.fullName)
                    user = refreshed; shift = keep
                    persist()
                    settle(refreshed, keep)
                }
                is ApiResult.Failure -> if (r.error.kind == ApiError.Kind.AUTH) onExpired()
            }
        }
    }

    fun login(username: String, password: String) {
        if (_ui.value.busy) return
        _ui.value = LoginUi(busy = true)
        scope.launch {
            when (val r = api.login(username.trim(), password)) {
                is ApiResult.Ok -> {
                    tokens.pair = r.value.tokens
                    user = r.value.user; shift = r.value.user.shift
                    persist(r.value.tokens)
                    _ui.value = LoginUi()
                    settle(r.value.user, r.value.user.shift)
                }
                is ApiResult.Failure -> _ui.value = LoginUi(error = r.error.userMessage())
            }
        }
    }

    /** Changes the password, then signs in again (the server revokes every session on a password change). */
    fun changePassword(old: String, new: String) {
        val name = user?.username ?: return
        if (_ui.value.busy) return
        _ui.value = LoginUi(busy = true)
        scope.launch {
            when (val r = api.changePassword(old, new)) {
                is ApiResult.Failure -> _ui.value = LoginUi(error = r.error.userMessage())
                is ApiResult.Ok -> {
                    tokens.pair = null
                    _ui.value = LoginUi()
                    login(name, new)
                }
            }
        }
    }

    fun startShift(placeId: Int, doorId: Int?) {
        if (_ui.value.busy) return
        val u = user ?: return
        _ui.value = LoginUi(busy = true)
        scope.launch {
            when (val r = api.startShift(placeId, doorId)) {
                is ApiResult.Ok -> {
                    _ui.value = LoginUi()
                    shift = r.value
                    persist()
                    settle(u, r.value)
                }
                is ApiResult.Failure -> _ui.value = LoginUi(error = r.error.userMessage())
            }
        }
    }

    fun endShift() {
        val u = user ?: return
        scope.launch {
            api.endShift()
            shift = null
            persist()
            settle(u, null)
        }
    }

    fun logout() {
        val refresh = tokens.pair?.refreshToken
        scope.launch {
            api.logout(refresh)
            tokens.pair = null; user = null; shift = null
            storage.clear()
            _state.value = SessionState.LoggedOut()
        }
    }

    fun clearError() { _ui.value = _ui.value.copy(error = null) }
}
