package ir.chador.core

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.atomic.AtomicInteger
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertIs
import kotlin.test.assertNotNull
import kotlin.test.assertNull
import kotlin.test.assertTrue

private fun ok(body: String, code: Int = 200) = MockResponse().setResponseCode(code).setHeader("Content-Type", "application/json").setBody(body)
private fun err(code: Int, errCode: String, msg: String = "پیام", details: String? = null) =
    ok("""{"error":{"code":"$errCode","message":"$msg"${if (details != null) ""","details":$details""" else ""}}}""", code)

private suspend fun <T> Flow<T>.until(timeoutMs: Long = 8000, p: (T) -> Boolean): T = withTimeout(timeoutMs) { first(p) }

class OpsAndPresentersTest {
    private val server = MockWebServer()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    private val requests = CopyOnWriteArrayList<RecordedRequest>()
    private val ids = AtomicInteger()

    @AfterTest fun stop() { scope.cancel(); runCatching { server.shutdown() } }

    private val loginBody = """{"tokens":{"access_token":"A","refresh_token":"R"},"user":{"id":"u-1","username":"khadem01","full_name":"خادم","role":"server","must_change_password":false,
        "shift":{"id":"shift-9","place_id":1,"place_name":"گیت ۱","started_at":"2026-10-24T08:00:00Z"}}}"""

    /** A core that is already logged in as a server with an open shift. [routes] answers everything except login. */
    private fun loggedInCore(routes: (RecordedRequest) -> MockResponse): ChadorCore {
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse {
                requests += request
                return if (request.path == "/api/v1/auth/login") ok(loginBody) else routes(request)
            }
        }
        server.start()
        val core = ChadorCore(server.url("/").toString(), MemorySessionStorage(), MemoryOpQueue(), scope, newId = { "op-${ids.incrementAndGet()}" })
        core.session.login("khadem01", "pw")
        runBlocking { core.session.state.until { it is SessionState.Ready } }
        requests.clear()
        return core
    }

    private fun bodyOf(r: RecordedRequest): JsonObject = Json.parseToJsonElement(r.body.readUtf8()) as JsonObject
    private fun JsonObject.str(k: String) = (this[k] as? JsonPrimitive)?.content

    @Test fun loginLandsOnReadyWhenAShiftIsOpen() = runBlocking {
        val core = loggedInCore { ok("{}") }
        val st = core.session.state.value as SessionState.Ready
        assertEquals("shift-9", st.shift?.id)
        assertEquals(Role.SERVER, core.session.role)
    }

    @Test fun directSuccessCarriesOpIdAndClientTimestamp() = runBlocking {
        val core = loggedInCore { ok("""{"items":[{"assignment_id":"a1","code":"CH-000001"}],"socks_qty":0,"socks_amount":0}""", 201) }
        val o = core.ops.assign("p-1", listOf("CH-000001"), 0)
        assertIs<Outcome.Done<AssignResponse>>(o)
        val b = bodyOf(requests.single())
        assertEquals("p-1", b.str("pilgrim_id"))
        assertNotNull(b.str("op_id")); assertNotNull(b.str("client_ts"))
        assertNull(b["socks_qty"], "defaults are not sent")
        assertEquals(0, core.queue.pending.value)
    }

    @Test fun offlineOperationsAreQueuedWithTheSameOpId() = runBlocking {
        val core = loggedInCore { ok("{}") }
        server.shutdown() // the gate loses its connection
        val o = core.ops.assign("p-1", listOf("CH-000001", "CH-000002"), 2, opId = "fixed-op")
        assertIs<Outcome.Queued>(o)
        val q = core.queue.pendingFor("u-1").single()
        assertEquals("fixed-op", q.opId)
        assertEquals("assign", q.type)
        assertEquals("shift-9", q.shiftId)
        val payload = q.payload as JsonObject
        assertEquals(setOf("pilgrim_id", "codes", "socks_qty"), payload.keys, "op_id/client_ts travel in the envelope, not the payload")
    }

    @Test fun cancelPayloadIsExactlyTheCode() = runBlocking {
        // The sync endpoint decodes cancel payloads strictly (unknown fields are refused), so nothing else may be in it.
        val core = loggedInCore { ok("{}") }
        server.shutdown()
        core.ops.cancel("CH-000001", opId = "c1")
        val q = core.queue.pendingFor("u-1").single()
        assertEquals("cancel", q.type)
        assertEquals(setOf("code"), (q.payload as JsonObject).keys)
    }

    @Test fun businessRefusalsAreNeverQueued() = runBlocking {
        val core = loggedInCore { err(409, "tents_unavailable", "چادر آماده نیست") }
        val o = core.ops.assign("p-1", listOf("CH-000001"), 0)
        assertIs<Outcome.Rejected>(o)
        assertEquals("tents_unavailable", o.error.code)
        assertEquals(0, core.queue.pending.value)
    }

    @Test fun serverTroubleIsQueuedLikeAnOutage() = runBlocking {
        val core = loggedInCore { MockResponse().setResponseCode(502) }
        assertIs<Outcome.Queued>(core.ops.returns(listOf(ReturnItemRequest("CH-000001", "dirty"))))
        assertEquals(1, core.queue.pending.value)
    }

    @Test fun lostSessionKeepsTheWorkAndAsksForLogin() = runBlocking {
        val core = loggedInCore { err(401, "unauthorized") }
        val o = core.ops.setStatus("CH-000001", TentStatus.DIRTY)
        assertIs<Outcome.Queued>(o)
        val st = core.session.state.until { it is SessionState.LoggedOut }
        assertTrue((st as SessionState.LoggedOut).message!!.contains("وارد شوید"))
        assertEquals(1, core.queue.pending.value, "queued work must survive losing the session")
    }

    // ---------------- hand-out presenter

    private fun validNid() = "0084575948"

    @Test fun handoutValidatesLocallyBeforeAnyRequest() = runBlocking {
        val core = loggedInCore { ok("{}") }
        val p = core.handout(scope)
        p.submitIdentity("1234567890", "1370/05/12", "09123456789")
        assertEquals("کد ملی معتبر نیست.", p.state.value.error)
        p.submitIdentity(validNid(), "1370/13/12", "09123456789")
        assertTrue(p.state.value.error!!.contains("تاریخ تولد"))
        p.submitIdentity(validNid(), "1370/05/12", "0812")
        assertTrue(p.state.value.error!!.contains("موبایل"))
        assertEquals(0, requests.size)
    }

    @Test fun handoutHappyPathAndPersianInput() = runBlocking {
        val core = loggedInCore { r ->
            when (r.path) {
                "/api/v1/pilgrims" -> ok("""{"id":"p-1","verification":"verified","full_name":"علی رضایی","national_id_masked":"008****948","mobile_masked":"0912***789","active_tents":[]}""", 201)
                "/api/v1/assignments" -> ok("""{"items":[{"assignment_id":"a1","code":"CH-000001"},{"assignment_id":"a2","code":"CH-000002"}],"socks_qty":2,"socks_amount":50000}""", 201)
                else -> err(404, "not_found")
            }
        }
        val p = core.handout(scope)
        p.submitIdentity("۰۰۸۴۵۷۵۹۴۸", "۱۳۷۰/۰۵/۱۲", "۰۹۱۲۳۴۵۶۷۸۹")
        p.state.until { it.stage == HandoutStage.Tents }
        val idBody = bodyOf(requests.first { it.path == "/api/v1/pilgrims" })
        assertEquals("0084575948", idBody.str("national_id")); assertEquals("09123456789", idBody.str("mobile")); assertEquals("1370/05/12", idBody.str("birth_date"))

        assertEquals(ScanResult.Added, p.scan("ch-۰۰۰۰۰۲"))
        assertEquals(ScanResult.Added, p.scan("CH-000001"))
        assertEquals(ScanResult.Duplicate, p.scan("CH-000001"))
        assertEquals(ScanResult.Invalid, p.scan("random text from a QR code"))
        p.setSocks(2)
        p.confirm()
        val done = p.state.until { it.stage == HandoutStage.Done }.done!!
        assertEquals(listOf("CH-000001", "CH-000002"), done.codes)
        assertEquals(50000, done.socksAmount)
        assertEquals(false, done.queued)
    }

    @Test fun handoutBlocksUnverifiedPilgrims() = runBlocking {
        val core = loggedInCore { ok("""{"id":"p-1","verification":"pending","national_id_masked":"x","mobile_masked":"y"}""", 201) }
        val p = core.handout(scope)
        p.submitIdentity(validNid(), "1370/05/12", "09123456789")
        val st = p.state.until { !it.busy && it.error != null }
        assertEquals(HandoutStage.Identity, st.stage)
        assertTrue(st.error!!.contains("تأیید نشده"))
    }

    @Test fun handoutMarksUnavailableTentsAndKeepsTheRest() = runBlocking {
        val core = loggedInCore { r ->
            if (r.path == "/api/v1/pilgrims") ok("""{"id":"p-1","verification":"verified","national_id_masked":"x","mobile_masked":"y"}""", 201)
            else err(409, "tents_unavailable", "x", """[{"code":"CH-000002","reason":"with_pilgrim"},{"code":"CH-000003","reason":"not_found"}]""")
        }
        val p = core.handout(scope)
        p.submitIdentity(validNid(), "1370/05/12", "09123456789")
        p.state.until { it.stage == HandoutStage.Tents }
        listOf("CH-000001", "CH-000002", "CH-000003").forEach { p.scan(it) }
        p.confirm()
        val st = p.state.until { !it.busy && it.error != null }
        assertEquals(ItemState.Pending, st.tents.first { it.code == "CH-000001" }.state)
        assertEquals(ItemState.Failed, st.tents.first { it.code == "CH-000002" }.state)
        assertTrue(st.tents.first { it.code == "CH-000002" }.message!!.contains("نزد زائر"))
        assertTrue(st.tents.first { it.code == "CH-000003" }.message!!.contains("ثبت نشده"))
        assertEquals(HandoutStage.Tents, st.stage)
    }

    @Test fun handoutOfflineIsQueuedForLater() = runBlocking {
        val core = loggedInCore { r ->
            if (r.path == "/api/v1/pilgrims") ok("""{"id":"p-1","verification":"verified","national_id_masked":"x","mobile_masked":"y"}""", 201) else MockResponse().setResponseCode(503)
        }
        val p = core.handout(scope)
        p.submitIdentity(validNid(), "1370/05/12", "09123456789")
        p.state.until { it.stage == HandoutStage.Tents }
        p.scan("CH-000001")
        p.confirm()
        val done = p.state.until { it.stage == HandoutStage.Done }.done!!
        assertTrue(done.queued)
        assertEquals(1, core.queue.pending.value)
    }

    // ---------------- return presenter

    @Test fun returnShowsPerTentResultsAndTheCompletionCount() = runBlocking {
        val core = loggedInCore {
            ok("""{"results":[{"code":"CH-000001","ok":true},{"code":"CH-000002","ok":false,"error":"not_assigned","detail":"ready"},{"code":"CH-000003","ok":false,"error":"not_found"}],"returned":1,"completed_pilgrims":1}""")
        }
        val p = core.returns(scope)
        listOf("CH-000001", "CH-000002", "CH-000003").forEach { p.scan(it) }
        p.setStatus("CH-000001", TentStatus.DIRTY)
        p.submit()
        val st = p.state.until { it.summary != null }
        assertEquals(1, st.summary!!.completedPilgrims)
        assertEquals(2, st.summary.failed)
        assertEquals(ItemState.Done, st.items[0].state)
        assertTrue(st.items[1].message!!.contains("تحویل نشده"))
        val sent = bodyOf(requests.first { it.path == "/api/v1/returns" })
        assertEquals("dirty", ((sent["items"] as kotlinx.serialization.json.JsonArray)[0] as JsonObject).str("status"))
    }

    // ---------------- batch status & tent info

    @Test fun workshopBatchExplainsRefusals() = runBlocking {
        val core = loggedInCore { r ->
            if (bodyOf(r).str("code") == "CH-000001") ok("""{"code":"CH-000001","from":"dirty","to":"in_wash"}""") else err(409, "invalid_transition")
        }
        val p = core.statusBatch(scope, TentStatus.IN_WASH)
        p.scan("CH-000001"); p.scan("CH-000002")
        p.submit()
        val st = p.state.until { !it.busy && it.items.none { i -> i.state == ItemState.Pending } }
        assertEquals(1, st.done)
        assertTrue(st.items[1].message!!.contains("نمی‌تواند"))
    }

    @Test fun tentInfoOffersOnlyWhatTheRoleMayDo() = runBlocking {
        val core = loggedInCore { r ->
            when {
                r.path!!.endsWith("/history?limit=30") -> ok("""{"events":[{"at":"2026-10-24T10:00:00Z","type":"assigned","user":"x"}]}""")
                r.path!!.startsWith("/api/v1/tents/") -> ok("""{"code":"CH-000001","status":"with_pilgrim","kind":"standard","assignment":{"id":"a","pilgrim_id":"p","assigned_at":"2026-10-24T10:00:00Z"}}""")
                else -> ok("{}")
            }
        }
        val p = core.tentInfo(scope)
        p.lookup("ch-000001")
        val st = p.state.until { it.tent != null }
        assertTrue(TentAction.CancelAssignment in st.actions, "a server may try to undo their own hand-out")
        assertTrue(st.actions.none { it is TentAction.SetStatus }, "a tent with a pilgrim has no manual status changes")
        assertEquals(1, st.history.size)
        p.lookup("not a code")
        assertEquals("کد چادر معتبر نیست.", p.state.value.error)
    }

    @Test fun sessionRestoresOfflineFromStoredProfile() = runBlocking {
        val storage = MemorySessionStorage().also {
            it.save(StoredSession(TokenPair("A", "R"), UserView("u-1", "khadem01", "خادم", "server"), ShiftView("s", 1, "گیت ۱")))
        }
        server.dispatcher = object : Dispatcher() { override fun dispatch(request: RecordedRequest) = MockResponse().setResponseCode(503) }
        server.start()
        val core = ChadorCore(server.url("/").toString(), storage, MemoryOpQueue(), scope)
        core.session.restore()
        val st = core.session.state.until { it is SessionState.Ready }
        assertEquals("s", (st as SessionState.Ready).shift?.id, "the app must open at the gate even without a connection")
    }
}
