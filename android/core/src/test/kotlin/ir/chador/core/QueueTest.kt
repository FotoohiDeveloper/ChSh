package ir.chador.core

import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import java.io.File
import kotlin.io.path.createTempDirectory
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

private fun op(id: String, user: String = "u1", at: Long = 1, type: String = "assign") =
    QueuedOp(id, user, type, "shift-1", "2026-10-24T10:00:00.000Z", buildJsonObject { put("code", "CH-000001") }, at, "تحویل ۱ چادر")

class QueueTest {
    private val dirs = mutableListOf<File>()
    private val server = MockWebServer()
    private fun tmp() = createTempDirectory("queue").toFile().also { dirs += it }
    @AfterTest fun cleanup() { dirs.forEach { it.deleteRecursively() }; runCatching { server.shutdown() } }

    @Test fun fileQueueSurvivesRestartAndHasNoTempFilesLeft() = runBlocking {
        val dir = tmp()
        val q1 = FileOpQueue(dir)
        q1.add(op("a", at = 2)); q1.add(op("b", at = 1)); q1.add(op("a", at = 3)) // duplicate op id ignored
        assertEquals(2, q1.pending.value)

        val q2 = FileOpQueue(dir) // "app restarted"
        assertEquals(2, q2.pending.value)
        assertEquals(listOf("b", "a"), q2.pendingFor("u1").map { it.opId }, "oldest first")
        assertTrue(q2.pendingFor("someone-else").isEmpty(), "a queue entry only belongs to the user who created it")
        assertFalse(dir.listFiles()!!.any { it.name.endsWith(".tmp") })
    }

    @Test fun corruptFileDoesNotBrickTheApp() = runBlocking {
        val dir = tmp()
        File(dir, "queue.json").writeText("{ this is not json")
        val q = FileOpQueue(dir)
        assertEquals(0, q.pending.value)
        assertTrue(dir.listFiles()!!.any { it.name.startsWith("queue.json.corrupt-") }, "the broken file is kept for inspection")
        q.add(op("x"))
        assertEquals(1, FileOpQueue(dir).pending.value)
    }

    @Test fun applyRemovesBumpsAndCapsRejected() = runBlocking {
        val q = MemoryOpQueue()
        listOf("a", "b", "c").forEachIndexed { i, id -> q.add(op(id, at = i.toLong())) }
        q.apply(done = setOf("a"), newlyRejected = listOf(RejectedOp(op("a"), "tents_unavailable", "x", 1)), retried = setOf("b"))
        assertEquals(listOf("b", "c"), q.pendingFor("u1").map { it.opId })
        assertEquals(1, q.pendingFor("u1").first { it.opId == "b" }.attempts)
        assertEquals(0, q.pendingFor("u1").first { it.opId == "c" }.attempts)
        repeat(150) { q.apply(emptySet(), listOf(RejectedOp(op("r$it"), "x", "y", it.toLong())), emptySet()) }
        assertEquals(100, q.rejected.value.size)
    }

    private fun mockSync(results: (List<String>) -> String): Dispatcher = object : Dispatcher() {
        override fun dispatch(request: RecordedRequest): MockResponse {
            val body = Json.parseToJsonElement(request.body.readUtf8()) as JsonObject
            val ids = (body["ops"] as kotlinx.serialization.json.JsonArray).map { ((it as JsonObject)["op_id"] as JsonPrimitive).content }
            return MockResponse().setHeader("Content-Type", "application/json").setBody(results(ids))
        }
    }

    private fun engine(q: OpQueue): SyncEngine {
        server.start()
        val client = ApiClient(server.url("/").toString(), defaultHttpClient(), TokenHolder(TokenPair("t", "r")))
        return SyncEngine(Api(client), q) { 42L }
    }

    @Test fun syncInterpretsEveryResultKind() = runBlocking {
        val q = MemoryOpQueue()
        listOf("ok", "replayed", "bad", "busy", "later").forEachIndexed { i, id -> q.add(op(id, at = i.toLong())) }
        server.dispatcher = mockSync { """{"results":[
            {"op_id":"ok","status":"ok"},
            {"op_id":"replayed","status":"replayed"},
            {"op_id":"bad","status":"rejected","error":{"code":"tents_unavailable","message":"چادر آماده نیست"}},
            {"op_id":"busy","status":"rejected","error":{"code":"op_in_progress","message":"در حال انجام"}},
            {"op_id":"later","status":"retry"}]}""" }
        val report = engine(q).syncOnce("u1")
        assertEquals(SyncReport(sent = 5, ok = 2, rejected = 1, retry = 2), report)
        assertEquals(setOf("busy", "later"), q.pendingFor("u1").map { it.opId }.toSet(), "transient results stay queued")
        assertEquals("tents_unavailable", q.rejected.value.single().code)
        assertEquals("چادر آماده نیست", q.rejected.value.single().message)
    }

    @Test fun syncSendsOldestFirstWithShiftAndTimestamp() = runBlocking {
        val q = MemoryOpQueue()
        q.add(op("second", at = 2)); q.add(op("first", at = 1)); q.add(op("other-user", user = "u2", at = 0))
        var sent = ""
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse {
                sent = request.body.readUtf8()
                return MockResponse().setBody("""{"results":[]}""")
            }
        }
        engine(q).syncOnce("u1")
        val ops = (Json.parseToJsonElement(sent) as JsonObject)["ops"] as kotlinx.serialization.json.JsonArray
        assertEquals(listOf("first", "second"), ops.map { ((it as JsonObject)["op_id"] as JsonPrimitive).content })
        val first = ops[0] as JsonObject
        assertEquals("shift-1", (first["shift_id"] as JsonPrimitive).content)
        assertEquals("2026-10-24T10:00:00.000Z", (first["client_ts"] as JsonPrimitive).content)
        assertEquals(1, q.pendingFor("u2").size, "another user's operations are never sent")
    }

    @Test fun offlineSyncKeepsEverythingAndSaysSo() = runBlocking {
        val q = MemoryOpQueue(); q.add(op("a"))
        val e = engine(q)
        server.shutdown()
        val report = e.syncOnce("u1")
        assertTrue(report.networkDown)
        assertEquals(1, q.pending.value)
    }

    @Test fun expiredSessionKeepsTheQueueForTheNextLogin() = runBlocking {
        val q = MemoryOpQueue(); q.add(op("a"))
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest) = MockResponse().setResponseCode(401).setBody("""{"error":{"code":"unauthorized","message":"x"}}""")
        }
        val report = engine(q).syncOnce("u1")
        assertTrue(report.authLost)
        assertEquals(1, q.pending.value)
    }

    @Test fun wholeRequestRefusedKeepsOpsAndAsksForRetry() = runBlocking {
        val q = MemoryOpQueue(); q.add(op("a"))
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest) = MockResponse().setResponseCode(403).setBody("""{"error":{"code":"password_change_required","message":"x"}}""")
        }
        val report = engine(q).syncOnce("u1")
        assertEquals(1, report.retry, "without this the coordinator would spin in a tight loop")
        assertEquals(1, q.pending.value)
    }

    @Test fun aBatchTheServerCallsMalformedIsParkedAfterFiveAttempts() = runBlocking {
        val q = MemoryOpQueue(); q.add(op("poison"))
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest) = MockResponse().setResponseCode(400).setBody("""{"error":{"code":"bad_json","message":"ساختار نامعتبر"}}""")
        }
        val e = engine(q)
        repeat(MAX_BAD_REQUEST_ATTEMPTS - 1) { assertEquals(1, e.syncOnce("u1").retry); assertEquals(1, q.pending.value) }
        val last = e.syncOnce("u1")
        assertEquals(1, last.rejected)
        assertEquals(0, q.pending.value, "the queue is free again")
        assertEquals("bad_json", q.rejected.value.single().code)
    }
}
