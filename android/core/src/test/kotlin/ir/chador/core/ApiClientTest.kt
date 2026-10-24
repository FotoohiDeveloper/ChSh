package ir.chador.core

import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.flow.toList
import kotlinx.coroutines.runBlocking
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import java.util.concurrent.atomic.AtomicInteger
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertIs
import kotlin.test.assertNull
import kotlin.test.assertTrue

private fun json(body: String, code: Int = 200) = MockResponse().setResponseCode(code).setHeader("Content-Type", "application/json").setBody(body)

class ApiClientTest {
    private val server = MockWebServer()
    @AfterTest fun stop() { runCatching { server.shutdown() } }

    private fun client(
        tokens: TokenHolder = TokenHolder(TokenPair("old", "refresh-1")),
        onTokens: (TokenPair?) -> Unit = {},
        onExpired: () -> Unit = {},
    ): ApiClient {
        server.start()
        return ApiClient(server.url("/").toString(), defaultHttpClient(), tokens, onTokens, onExpired)
    }

    @Test fun decodesAndIgnoresUnknownFields() = runBlocking {
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest) = json("""[{"id":1,"kind":"gate","name":"گیت","doors":[{"id":3,"name":"درب ۱","extra":"x"}],"future_field":true}]""")
        }
        val r = Api(client()).places()
        val places = (r as ApiResult.Ok).value
        assertEquals("گیت", places[0].name)
        assertEquals(3, places[0].doors[0].id)
        val req = server.takeRequest()
        assertEquals("Bearer old", req.getHeader("Authorization"))
    }

    @Test fun businessErrorsAreRejectedNotRetryable() = runBlocking {
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest) = json(
                """{"error":{"code":"tents_unavailable","message":"برخی چادرها آماده نیستند","details":[{"code":"CH-000001","reason":"with_pilgrim"}]},"request_id":"abc"}""", 409)
        }
        val r = Api(client()).assign(AssignRequest("p", listOf("CH-000001")))
        val e = (r as ApiResult.Failure).error
        assertEquals(ApiError.Kind.REJECTED, e.kind)
        assertEquals("tents_unavailable", e.code)
        assertEquals("abc", e.requestId)
        assertTrue(!e.retryable)
        assertEquals(1, e.details.let { (it as kotlinx.serialization.json.JsonArray).size })
    }

    @Test fun serverErrorsAndRateLimitAreRetryable() = runBlocking {
        server.dispatcher = object : Dispatcher() {
            var n = 0
            override fun dispatch(request: RecordedRequest) = if (n++ == 0) MockResponse().setResponseCode(503) else json("""{"error":{"code":"rate_limited","message":"آهسته"}}""", 429)
        }
        val api = Api(client())
        val a = (api.dashboard() as ApiResult.Failure).error
        val b = (api.dashboard() as ApiResult.Failure).error
        assertEquals(ApiError.Kind.SERVER, a.kind); assertTrue(a.retryable)
        assertEquals(ApiError.Kind.SERVER, b.kind); assertEquals("rate_limited", b.code)
    }

    @Test fun unreachableServerIsANetworkError() = runBlocking {
        val c = client()
        server.shutdown()
        val e = (Api(c).places() as ApiResult.Failure).error
        assertEquals(ApiError.Kind.NETWORK, e.kind)
        assertTrue(e.retryable)
    }

    @Test fun silentRefreshOn401AndRetry() = runBlocking {
        val refreshed = mutableListOf<TokenPair?>()
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse = when {
                request.path == "/api/v1/auth/refresh" -> json("""{"tokens":{"access_token":"new","refresh_token":"refresh-2","expires_at":"x"}}""")
                request.getHeader("Authorization") == "Bearer new" -> json("""{"generated_at":"t","totals":{"ready":5}}""")
                else -> json("""{"error":{"code":"unauthorized","message":"ابتدا وارد شوید"}}""", 401)
            }
        }
        val tokens = TokenHolder(TokenPair("old", "refresh-1"))
        val r = Api(client(tokens, onTokens = { refreshed += it })).dashboard()
        assertEquals(5, (r as ApiResult.Ok).value.totals["ready"])
        assertEquals("new", tokens.pair?.accessToken)
        assertEquals("refresh-2", refreshed.single()?.refreshToken)
    }

    @Test fun manyParallelCallsShareOneRefresh() = runBlocking {
        val refreshCalls = AtomicInteger()
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse = when {
                request.path == "/api/v1/auth/refresh" -> { refreshCalls.incrementAndGet(); Thread.sleep(150); json("""{"tokens":{"access_token":"new","refresh_token":"r2"}}""") }
                request.getHeader("Authorization") == "Bearer new" -> json("""[]""")
                else -> json("""{"error":{"code":"unauthorized","message":"x"}}""", 401)
            }
        }
        val api = Api(client())
        val results = (1..8).map { async { api.places() } }.awaitAll()
        assertTrue(results.all { it is ApiResult.Ok })
        assertEquals(1, refreshCalls.get(), "refresh tokens are single-use: concurrent 401s must trigger exactly one refresh")
    }

    @Test fun refreshRefusedEndsTheSession() = runBlocking {
        var expired = 0
        val persisted = mutableListOf<TokenPair?>()
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse =
                if (request.path == "/api/v1/auth/refresh") json("""{"error":{"code":"unauthorized","message":"x"}}""", 401)
                else json("""{"error":{"code":"unauthorized","message":"x"}}""", 401)
        }
        val tokens = TokenHolder(TokenPair("old", "r"))
        val r = Api(client(tokens, onTokens = { persisted += it }, onExpired = { expired++ })).dashboard()
        assertEquals(ApiError.Kind.AUTH, (r as ApiResult.Failure).error.kind)
        assertEquals(1, expired)
        assertNull(tokens.pair)
        assertNull(persisted.single())
    }

    @Test fun refreshWhileOfflineKeepsTheSession() = runBlocking {
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse =
                if (request.path == "/api/v1/auth/refresh") MockResponse().setResponseCode(503) else json("""{"error":{"code":"unauthorized","message":"x"}}""", 401)
        }
        val tokens = TokenHolder(TokenPair("old", "r"))
        val e = (Api(client(tokens)).dashboard() as ApiResult.Failure).error
        assertEquals(ApiError.Kind.NETWORK, e.kind)
        assertEquals("r", tokens.pair?.refreshToken, "a flaky connection must not log the user out")
    }

    @Test fun noContentAndBinaryBodies() = runBlocking {
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse = when {
                request.path!!.startsWith("/api/v1/reports/daily") -> MockResponse().setBody(okio.Buffer().write(byteArrayOf(0x50, 0x4B, 3, 4)))
                else -> MockResponse().setResponseCode(204)
            }
        }
        val api = Api(client())
        assertIs<ApiResult.Ok<Unit>>(api.resolveHandover("h1", true))
        assertEquals("PK", String((api.dailyReportXlsx("2026-10-24") as ApiResult.Ok).value.copyOf(2)))
        val sent = server.takeRequest()
        assertEquals("POST", sent.method)
        assertEquals("""{"accept":true}""", sent.body.readUtf8())
    }

    @Test fun serverSentEvents() = runBlocking {
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest) = MockResponse().setHeader("Content-Type", "text/event-stream")
                .setBody(": connected\n\nevent: assigned\ndata: {}\n\n: ping\n\nevent: returned\ndata: {}\n\n")
        }
        val events = Api(client()).events().toList()
        assertEquals(listOf("assigned", "returned"), events)
    }

    @Test fun eventsStreamRejectedWith401IsAnError() = runBlocking {
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest) = MockResponse().setResponseCode(401)
        }
        val failure = runCatching { Api(client()).events().toList() }.exceptionOrNull()
        assertTrue(failure != null, "an unauthorised stream must fail so the caller can refresh and reconnect")
    }
}
