package ir.chador.core

import kotlinx.coroutines.channels.awaitClose
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.callbackFlow
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.decodeFromString
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import okhttp3.Call
import okhttp3.Callback
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import java.io.IOException
import java.util.concurrent.TimeUnit
import kotlin.coroutines.resume
import kotlin.coroutines.resumeWithException

sealed interface ApiResult<out T> {
    data class Ok<T>(val value: T) : ApiResult<T>
    data class Failure(val error: ApiError) : ApiResult<Nothing>
}

data class ApiError(
    val kind: Kind,
    val code: String,
    val message: String,
    val status: Int = 0,
    val details: JsonElement? = null,
    val requestId: String? = null,
) {
    enum class Kind {
        /** No usable answer: offline, DNS, timeout, TLS. Safe to queue and retry. */
        NETWORK,
        /** Session is gone (token expired and could not be refreshed). Needs a new login. */
        AUTH,
        /** The server answered and said no (400/403/404/409/422...). Retrying the same thing will not help. */
        REJECTED,
        /** 5xx or 429. Worth retrying later. */
        SERVER,
    }

    /** Offline or temporarily failing: the operation should be queued, not shown as an error. */
    val retryable: Boolean get() = kind == Kind.NETWORK || kind == Kind.SERVER || kind == Kind.AUTH
}

inline fun <T, R> ApiResult<T>.map(f: (T) -> R): ApiResult<R> = when (this) {
    is ApiResult.Ok -> ApiResult.Ok(f(value))
    is ApiResult.Failure -> this
}

fun <T> ApiResult<T>.valueOrNull(): T? = (this as? ApiResult.Ok)?.value
fun ApiResult<*>.errorOrNull(): ApiError? = (this as? ApiResult.Failure)?.error

/** The current token pair, shared by the API client and the session manager. */
class TokenHolder(initial: TokenPair? = null) {
    @Volatile
    var pair: TokenPair? = initial
}

@PublishedApi
internal class RawResponse(val status: Int, val bytes: ByteArray, val requestId: String?) {
    val text: String by lazy { String(bytes, Charsets.UTF_8) }
}

internal suspend fun Call.await(): Response = suspendCancellableCoroutine { cont ->
    cont.invokeOnCancellation { cancel() }
    enqueue(object : Callback {
        override fun onFailure(call: Call, e: IOException) {
            if (cont.isActive) cont.resumeWithException(e)
        }

        override fun onResponse(call: Call, response: Response) {
            if (cont.isActive) cont.resume(response) else response.close()
        }
    })
}

fun defaultHttpClient(): OkHttpClient = OkHttpClient.Builder()
    .connectTimeout(6, TimeUnit.SECONDS)
    .readTimeout(15, TimeUnit.SECONDS)
    .writeTimeout(15, TimeUnit.SECONDS)
    .callTimeout(25, TimeUnit.SECONDS)
    .retryOnConnectionFailure(true)
    .build()

class ApiClient(
    baseUrl: String,
    private val http: OkHttpClient = defaultHttpClient(),
    val tokens: TokenHolder = TokenHolder(),
    private val onTokensChanged: (TokenPair?) -> Unit = {},
    private val onSessionExpired: () -> Unit = {},
) {
    val baseUrl: String = baseUrl.trimEnd('/')
    private val refreshMutex = Mutex()
    private val jsonType = "application/json; charset=utf-8".toMediaType()

    private suspend fun execute(method: String, path: String, body: String?, auth: Boolean): RawResponse {
        val b = Request.Builder().url(baseUrl + path)
        val token = tokens.pair?.accessToken
        if (auth && token != null) b.header("Authorization", "Bearer $token")
        b.header("Accept", "application/json")
        when (method) {
            "GET" -> b.get()
            else -> b.method(method, (body ?: "").toRequestBody(jsonType))
        }
        http.newCall(b.build()).await().use { resp ->
            return RawResponse(resp.code, resp.body?.bytes() ?: ByteArray(0), resp.header("X-Request-Id"))
        }
    }

    private enum class Refresh { DONE, NETWORK, EXPIRED }

    /** Single-flight refresh: concurrent 401s trigger one refresh call; the rest reuse its result. */
    private suspend fun refresh(staleAccess: String?): Refresh = refreshMutex.withLock {
        val current = tokens.pair ?: return Refresh.EXPIRED
        if (current.accessToken != staleAccess) return Refresh.DONE // another caller already refreshed
        val body = ChadorJson.encodeToString(JsonObject.serializer(), JsonObject(mapOf("refresh_token" to JsonPrimitive(current.refreshToken))))
        val raw = try {
            execute("POST", "/api/v1/auth/refresh", body, auth = false)
        } catch (e: IOException) {
            return Refresh.NETWORK
        }
        if (raw.status in 500..599 || raw.status == 429) return Refresh.NETWORK
        if (raw.status != 200) {
            tokens.pair = null
            onTokensChanged(null)
            onSessionExpired()
            return Refresh.EXPIRED
        }
        val fresh = try {
            ChadorJson.decodeFromString(RefreshResponse.serializer(), raw.text).tokens
        } catch (e: Exception) {
            return Refresh.NETWORK
        }
        tokens.pair = fresh
        onTokensChanged(fresh)
        Refresh.DONE
    }

    private fun networkError() = ApiError(ApiError.Kind.NETWORK, "network", "ارتباط با سرور برقرار نشد. اتصال اینترنت را بررسی کنید.")

    private fun toError(raw: RawResponse): ApiError {
        val env = try {
            ChadorJson.decodeFromString(ErrorEnvelope.serializer(), raw.text)
        } catch (e: Exception) {
            null
        }
        val kind = when {
            raw.status == 401 -> ApiError.Kind.AUTH
            raw.status == 429 || raw.status >= 500 -> ApiError.Kind.SERVER
            else -> ApiError.Kind.REJECTED
        }
        val code = env?.error?.code?.ifEmpty { null } ?: "http_${raw.status}"
        val message = env?.error?.message?.ifEmpty { null } ?: when (kind) {
            ApiError.Kind.SERVER -> "سرور موقتاً پاسخ نمی‌دهد."
            ApiError.Kind.AUTH -> "نشست شما تمام شده؛ دوباره وارد شوید."
            else -> "درخواست پذیرفته نشد."
        }
        return ApiError(kind, code, message, raw.status, env?.error?.details, env?.requestId ?: raw.requestId)
    }

    /** Core call: attaches the token, refreshes once on 401, maps every outcome to ApiResult. */
    @PublishedApi
    internal suspend fun <T> call(method: String, path: String, body: String?, auth: Boolean = true, decode: (RawResponse) -> T): ApiResult<T> {
        var raw = try {
            execute(method, path, body, auth)
        } catch (e: IOException) {
            return ApiResult.Failure(networkError())
        }
        if (raw.status == 401 && auth) {
            val stale = tokens.pair?.accessToken
            when (refresh(stale)) {
                Refresh.DONE -> raw = try {
                    execute(method, path, body, auth)
                } catch (e: IOException) {
                    return ApiResult.Failure(networkError())
                }
                Refresh.NETWORK -> return ApiResult.Failure(networkError())
                Refresh.EXPIRED -> return ApiResult.Failure(toError(raw).copy(kind = ApiError.Kind.AUTH))
            }
        }
        if (raw.status !in 200..299) return ApiResult.Failure(toError(raw))
        return try {
            ApiResult.Ok(decode(raw))
        } catch (e: Exception) {
            ApiResult.Failure(ApiError(ApiError.Kind.REJECTED, "bad_response", "پاسخ سرور قابل‌فهم نبود.", raw.status, requestId = raw.requestId))
        }
    }

    suspend inline fun <reified Res> get(path: String): ApiResult<Res> =
        call("GET", path, null) { ChadorJson.decodeFromString<Res>(it.text) }

    suspend inline fun <reified Req, reified Res> send(method: String, path: String, req: Req, auth: Boolean = true): ApiResult<Res> =
        call(method, path, ChadorJson.encodeToString(req), auth) { ChadorJson.decodeFromString<Res>(it.text) }

    /** For endpoints that answer 204 with no body. */
    suspend inline fun <reified Req> sendNoContent(method: String, path: String, req: Req): ApiResult<Unit> =
        call(method, path, ChadorJson.encodeToString(req)) { }

    suspend fun postEmpty(path: String): ApiResult<Unit> = call("POST", path, "{}") { }

    suspend fun download(path: String): ApiResult<ByteArray> = call("GET", path, null) { it.bytes }

    /** Server-sent events: emits the event name ("assigned", "returned", ...). Completes when the server closes the stream. */
    fun events(path: String = "/api/v1/events"): Flow<String> = callbackFlow {
        val stream = http.newBuilder().readTimeout(70, TimeUnit.SECONDS).callTimeout(0, TimeUnit.SECONDS).build()
        val b = Request.Builder().url(baseUrl + path).header("Accept", "text/event-stream")
        tokens.pair?.accessToken?.let { b.header("Authorization", "Bearer $it") }
        val call = stream.newCall(b.build())
        call.enqueue(object : Callback {
            override fun onFailure(call: Call, e: IOException) {
                close(e)
            }

            override fun onResponse(call: Call, response: Response) {
                response.use { r ->
                    if (r.code != 200) {
                        close(IOException("events: HTTP ${r.code}"))
                        return
                    }
                    try {
                        val src = r.body!!.source()
                        while (!src.exhausted()) {
                            val line = src.readUtf8Line() ?: break
                            if (line.startsWith("event: ")) trySend(line.removePrefix("event: ").trim())
                        }
                        close()
                    } catch (e: IOException) {
                        close(e)
                    }
                }
            }
        })
        awaitClose { call.cancel() }
    }
}

// ---- typed endpoints ----

private const val V1 = "/api/v1"

class Api(val client: ApiClient) {
    private fun enc(s: String) = java.net.URLEncoder.encode(s, "UTF-8")

    // auth
    suspend fun login(username: String, password: String): ApiResult<LoginResponse> =
        client.send("POST", "$V1/auth/login", mapOf("username" to username, "password" to password), auth = false)

    suspend fun logout(refreshToken: String?): ApiResult<Unit> =
        client.sendNoContent("POST", "$V1/auth/logout", mapOf("refresh_token" to (refreshToken ?: "")))

    suspend fun changePassword(old: String, new: String): ApiResult<Unit> =
        client.call("POST", "$V1/auth/change-password", ChadorJson.encodeToString(mapOf("old_password" to old, "new_password" to new))) { }

    suspend fun me(): ApiResult<MeResponse> = client.get("$V1/me")

    suspend fun places(): ApiResult<List<Place>> = client.get("$V1/places")

    suspend fun startShift(placeId: Int, doorId: Int?): ApiResult<ShiftView> =
        client.send("POST", "$V1/shifts/start", ShiftStartRequest(placeId, doorId))

    suspend fun endShift(): ApiResult<Unit> = client.postEmpty("$V1/shifts/end")
    suspend fun colleagues(): ApiResult<ColleaguesResponse> = client.get("$V1/shifts/colleagues")

    // pilgrims
    suspend fun registerPilgrim(input: PilgrimInput): ApiResult<PilgrimView> = client.send("POST", "$V1/pilgrims", input)

    // tents
    suspend fun tent(code: String): ApiResult<TentView> = client.get("$V1/tents/${enc(code)}")
    suspend fun tentHistory(code: String, limit: Int = 30): ApiResult<HistoryResponse> = client.get("$V1/tents/${enc(code)}/history?limit=$limit")
    suspend fun assign(req: AssignRequest): ApiResult<AssignResponse> = client.send("POST", "$V1/assignments", req)
    suspend fun cancel(req: CancelRequest): ApiResult<CancelResponse> = client.send("POST", "$V1/assignments/cancel", req)
    suspend fun returns(req: ReturnRequest): ApiResult<ReturnResponse> = client.send("POST", "$V1/returns", req)
    suspend fun setStatus(req: StatusRequest): ApiResult<StatusResponse> = client.send("POST", "$V1/tents/status", req)
    suspend fun move(req: MoveRequest): ApiResult<MoveResponse> = client.send("POST", "$V1/tents/move", req)

    // retire
    suspend fun requestRetire(req: RetireRequest): ApiResult<IdResponse> = client.send("POST", "$V1/retire-requests", req)
    suspend fun retireRequests(): ApiResult<RetireListResponse> = client.get("$V1/retire-requests")
    suspend fun decideRetire(id: String, approve: Boolean): ApiResult<Unit> =
        client.sendNoContent("POST", "$V1/retire-requests/$id/decision", DecisionRequest(approve))

    // socks
    suspend fun socks(req: SocksRequest): ApiResult<SocksResponse> = client.send("POST", "$V1/socks", req)

    // handover
    suspend fun createHandover(req: HandoverRequest): ApiResult<HandoverView> = client.send("POST", "$V1/handovers", req)
    suspend fun handovers(): ApiResult<HandoverListResponse> = client.get("$V1/handovers")
    suspend fun resolveHandover(id: String, accept: Boolean): ApiResult<Unit> =
        client.sendNoContent("POST", "$V1/handovers/$id/resolve", ResolveRequest(accept))

    // sync / dashboard / reports
    suspend fun sync(req: SyncRequest): ApiResult<SyncResponse> = client.send("POST", "$V1/sync", req)
    suspend fun dashboard(): ApiResult<Dashboard> = client.get("$V1/dashboard")
    suspend fun dailyReport(date: String): ApiResult<DailyReport> = client.get("$V1/reports/daily?date=${enc(date)}")
    suspend fun dailyReportXlsx(date: String): ApiResult<ByteArray> = client.download("$V1/reports/daily?format=xlsx&date=${enc(date)}")

    // admin
    suspend fun users(): ApiResult<UsersResponse> = client.get("$V1/admin/users")
    suspend fun createUser(req: CreateUserRequest): ApiResult<CreatedUser> = client.send("POST", "$V1/admin/users", req)
    suspend fun updateUser(id: String, req: UpdateUserRequest): ApiResult<Unit> = client.sendNoContent("PATCH", "$V1/admin/users/$id", req)
    suspend fun resetPassword(id: String): ApiResult<TempPasswordResponse> = client.send("POST", "$V1/admin/users/$id/reset-password", EmptyBody)
    suspend fun settings(): ApiResult<Map<String, JsonElement>> = client.get("$V1/admin/settings")
    suspend fun putSetting(key: String, value: JsonElement): ApiResult<Unit> = client.sendNoContent("PUT", "$V1/admin/settings/${enc(key)}", value)
    suspend fun audit(before: Long = 0, limit: Int = 100): ApiResult<AuditResponse> = client.get("$V1/admin/audit?limit=$limit" + if (before > 0) "&before=$before" else "")
    suspend fun createTents(codes: List<String>): ApiResult<CreatedCount> = client.send("POST", "$V1/admin/tents", CreateTentsRequest(codes))
    suspend fun createPlace(kind: String, name: String): ApiResult<IntId> = client.send("POST", "$V1/admin/places", CreatePlaceRequest(kind, name))
    suspend fun createDoor(placeId: Int, name: String): ApiResult<IntId> = client.send("POST", "$V1/admin/places/$placeId/doors", CreateDoorRequest(name))

    fun events(): Flow<String> = client.events()
}

@kotlinx.serialization.Serializable
data class ShiftStartRequest(
    @kotlinx.serialization.SerialName("place_id") val placeId: Int,
    @kotlinx.serialization.SerialName("door_id") val doorId: Int? = null,
)

@kotlinx.serialization.Serializable
object EmptyBody

@kotlinx.serialization.Serializable
data class CreateTentsRequest(val codes: List<String>)

@kotlinx.serialization.Serializable
data class CreatedCount(val created: Int = 0)

@kotlinx.serialization.Serializable
data class CreatePlaceRequest(val kind: String, val name: String)

@kotlinx.serialization.Serializable
data class CreateDoorRequest(val name: String)

@kotlinx.serialization.Serializable
data class IntId(val id: Int)
