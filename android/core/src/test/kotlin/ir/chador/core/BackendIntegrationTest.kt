package ir.chador.core

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import okhttp3.Interceptor
import java.io.IOException
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertIs
import kotlin.test.assertNotEquals
import kotlin.test.assertNotNull
import kotlin.test.assertTrue
import kotlin.test.fail

/**
 * Drives the real client code (session, API, offline queue, presenters) against a running backend.
 * Skipped unless CHADOR_BASE_URL is set; android/scripts/run-integration.sh starts a backend and sets it.
 */
class BackendIntegrationTest {
    private val base = System.getenv("CHADOR_BASE_URL")
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    private val sfx = (1..5).map { ('a'..'z').random() }.joinToString("")
    private val offline = AtomicBoolean(false)

    @AfterTest fun stop() { scope.cancel() }

    private suspend fun <T> Flow<T>.until(timeoutMs: Long = 15000, p: (T) -> Boolean): T = withTimeout(timeoutMs) { first(p) }

    private fun newCore(withOfflineSwitch: Boolean = false): ChadorCore {
        val http = defaultHttpClient().newBuilder().apply {
            if (withOfflineSwitch) addInterceptor(Interceptor { chain ->
                if (offline.get()) throw IOException("simulated outage") else chain.proceed(chain.request())
            })
        }.build()
        return ChadorCore(base!!, MemorySessionStorage(), MemoryOpQueue(), scope, http)
    }

    /** Logs in, handles the temporary-password step, and starts a shift when the role needs one. */
    private suspend fun signIn(core: ChadorCore, user: String, tempPw: String, place: Place? = null, door: Int? = null): String {
        core.session.login(user, tempPw)
        var st = core.session.state.until { it is SessionState.MustChangePassword || it is SessionState.NeedsShift || it is SessionState.Ready || (it is SessionState.LoggedOut && core.session.ui.value.error != null) }
        if (st is SessionState.LoggedOut) fail("login failed for $user: ${core.session.ui.value.error}")
        val newPw = "Pw-$user-12345!"
        if (st is SessionState.MustChangePassword) {
            core.session.changePassword(tempPw, newPw)
            st = core.session.state.until { it is SessionState.NeedsShift || it is SessionState.Ready }
        }
        if (st is SessionState.NeedsShift) {
            core.session.startShift(place!!.id, door)
            core.session.state.until { it is SessionState.Ready }
        }
        return newPw
    }

    private fun nid(i: Int): String {
        val b = "%09d".format(java.util.Locale.ROOT, 200000000 + i * 7919)
        val sum = (0 until 9).sumOf { (b[it] - '0') * (10 - it) }
        val r = sum % 11
        return b + (if (r < 2) r else 11 - r)
    }

    @Test fun everyRoleEndToEnd() = runBlocking {
        if (base == null) { println("skipped: CHADOR_BASE_URL not set"); return@runBlocking }
        val adminUser = System.getenv("CHADOR_ADMIN_USER") ?: "admin"
        val adminPw = System.getenv("CHADOR_ADMIN_PASSWORD") ?: fail("CHADOR_ADMIN_PASSWORD is required")

        // ---------------- admin sets the world up
        val admin = newCore()
        signIn(admin, adminUser, adminPw)
        val gate = (admin.api.createPlace("gate", "گیت $sfx") as ApiResult.Ok).value.id
        val store = (admin.api.createPlace("storage", "اتاق امانات $sfx") as ApiResult.Ok).value.id
        val wash = (admin.api.createPlace("workshop", "کارگاه $sfx") as ApiResult.Ok).value.id
        val door = (admin.api.createDoor(gate, "درب ۱") as ApiResult.Ok).value.id
        val places = (admin.api.places() as ApiResult.Ok).value
        fun place(id: Int) = places.first { it.id == id }
        val base0 = sfx.fold(0) { a, c -> a * 31 + c.code } % 20000 // distinct tent range per run
        val tents = (1..12).map { "CH-%06d".format(java.util.Locale.ROOT, 100000 + base0 * 10 + it) }
        assertEquals(12, (admin.api.createTents(tents) as ApiResult.Ok).value.created)

        // admin screens: users, settings
        val users = admin.users(scope)
        fun mk(role: Role, tag: String): Pair<String, String> {
            val name = "$tag-$sfx"
            users.create(name, "کاربر $tag", role)
            val temp = runBlocking { users.state.until { it.tempPassword?.first == name } }.tempPassword!!.second
            users.consumeTempPassword()
            return name to temp
        }
        val (srvU, srvT) = mk(Role.SERVER, "srv"); val (leadU, leadT) = mk(Role.GATE_LEAD, "lead")
        val (teamU, teamT) = mk(Role.TEAM, "team"); val (washU, washT) = mk(Role.WORKSHOP, "wash"); val (offU, offT) = mk(Role.OFFICE, "off")
        assertTrue(users.state.until { s -> s.users.any { it.username == srvU } }.users.any { it.username == leadU })
        val settings = admin.settings(scope)
        settings.saveSocksPrice(25000)
        assertEquals("ذخیره شد.", settings.state.until { it.message != null }.message)
        settings.load()
        assertEquals(25000, settings.state.until { it.socksPrice == 25000 }.socksPrice)

        // ---------------- everyone signs in (temporary password -> new password -> shift)
        val server = newCore(withOfflineSwitch = true); val lead = newCore(); val team = newCore(); val washer = newCore(); val office = newCore()
        signIn(server, srvU, srvT, place(gate), door)
        signIn(lead, leadU, leadT, place(gate), door)
        signIn(team, teamU, teamT, place(store))
        signIn(washer, washU, washT, place(wash))
        signIn(office, offU, offT)
        assertEquals(Role.SERVER, server.session.role)
        // a washer may only start a shift at a workshop: the place list offered to them is already filtered
        val washPlaces = washer.places(scope).also { it.load() }
        assertEquals(listOf(wash), washPlaces.state.until { it.places.isNotEmpty() }.places.map { it.id })

        // ---------------- server: hand out tents with socks
        val h1 = server.handout(scope)
        h1.submitIdentity(nid(1), "1370/05/12", "09121110001")
        val pilgrim1 = h1.state.until { it.stage == HandoutStage.Tents }.pilgrim!!
        assertTrue(pilgrim1.verified)
        assertTrue(pilgrim1.nationalIdMasked.contains("*"))
        listOf(tents[0], tents[1], tents[2]).forEach { assertEquals(ScanResult.Added, h1.scan(it)) }
        h1.setSocks(2)
        h1.confirm()
        val done1 = h1.state.until { it.stage == HandoutStage.Done }.done!!
        assertEquals(50000, done1.socksAmount, "price comes from the admin setting")

        val info = server.tentInfo(scope)
        info.lookup(tents[0])
        val t0 = info.state.until { it.tent != null }.tent!!
        assertEquals(TentStatus.WITH_PILGRIM, t0.statusEnum)
        assertNotNull(t0.assignment)

        // handing the same tent to someone else is refused with a per-tent explanation
        val h2 = server.handout(scope)
        h2.submitIdentity(nid(2), "1371/01/01", "09121110002")
        h2.state.until { it.stage == HandoutStage.Tents }
        h2.scan(tents[0]); h2.scan(tents[3])
        h2.confirm()
        val refused = h2.state.until { !it.busy && it.error != null }
        assertEquals(ItemState.Failed, refused.tents.first { it.code == tents[0] }.state)
        assertEquals(ItemState.Pending, refused.tents.first { it.code == tents[3] }.state)
        h2.remove(tents[0])
        h2.confirm()
        h2.state.until { it.stage == HandoutStage.Done }

        // ---------------- offline: the connection drops at the gate, work continues, then syncs exactly once
        val h3 = server.handout(scope)
        h3.submitIdentity(nid(3), "1372/02/02", "09121110003")
        h3.state.until { it.stage == HandoutStage.Tents }
        offline.set(true)
        h3.scan(tents[4])
        h3.confirm()
        assertTrue(h3.state.until { it.stage == HandoutStage.Done }.done!!.queued, "no connection: saved on the device")
        assertEquals(1, server.queue.pending.value)
        val ret = server.returns(scope); ret.scan(tents[3]); ret.setStatus(tents[3], TentStatus.DIRTY); ret.submit()
        ret.state.until { it.summary != null }
        assertEquals(2, server.queue.pending.value, "returns work offline too")
        val uid = server.session.userId!!
        assertTrue(server.sync.syncOnce(uid).networkDown, "still offline: nothing lost")
        offline.set(false)
        val report = server.sync.syncOnce(uid)
        assertEquals(2, report.ok, "both queued operations reach the server: $report")
        assertEquals(0, server.queue.pending.value)
        // replaying the identical operation (as after a dropped response) changes nothing
        val replay = QueuedOp(java.util.UUID.randomUUID().toString(), uid, "assign", server.session.shiftId, Time.format(System.currentTimeMillis()),
            ChadorJson.encodeToJsonElement(AssignRequest.serializer(), AssignRequest(h3.state.value.pilgrim!!.id, listOf(tents[5]))), 1, "x")
        server.queue.add(replay)
        val first = server.sync.syncOnce(uid)
        assertEquals(1, first.ok, "$first rejected=${server.queue.rejected.value.map { it.code + ':' + it.message }}")
        server.queue.add(replay)
        val second = server.sync.syncOnce(uid)
        assertEquals(1, second.ok, "replayed op id is accepted as already done: $second")
        val hist5 = (server.api.tentHistory(tents[5]) as ApiResult.Ok).value.events
        assertEquals(1, hist5.count { it.type == "assigned" }, "the tent was handed out exactly once")
        assertEquals(TentStatus.DIRTY, (server.api.tent(tents[3]) as ApiResult.Ok).value.statusEnum)

        // ---------------- server: return pilgrim 1's tents (one dirty)
        val r1 = server.returns(scope)
        tents.take(3).forEach { r1.scan(it) }
        r1.setStatus(tents[1], TentStatus.DIRTY)
        r1.submit()
        val rs = r1.state.until { it.summary != null }.summary!!
        assertEquals(1, rs.completedPilgrims, "pilgrim 1 now holds nothing (SMS confirmation is queued server-side)")
        assertEquals(0, rs.failed)

        // ---------------- washer and team: dirty -> in wash -> pending review -> ready
        val w1 = washer.statusBatch(scope, TentStatus.IN_WASH); w1.scan(tents[1]); w1.scan(tents[3]); w1.submit()
        assertEquals(2, w1.state.until { !it.busy && it.done > 0 }.done)
        val w2 = washer.statusBatch(scope, TentStatus.PENDING_REVIEW); w2.scan(tents[1]); w2.scan(tents[3]); w2.submit()
        assertEquals(2, w2.state.until { !it.busy && it.done > 0 }.done)
        val wrong = washer.statusBatch(scope, TentStatus.READY); wrong.scan(tents[1]); wrong.submit()
        assertTrue(wrong.state.until { !it.busy && it.items.first().state == ItemState.Failed }.items.first().message!!.isNotEmpty(), "a washer cannot approve their own work")
        val review = team.statusBatch(scope, TentStatus.READY); review.scan(tents[1]); review.scan(tents[3]); review.submit()
        assertEquals(2, review.state.until { !it.busy && it.done > 0 }.done)
        assertEquals(TentStatus.READY, (server.api.tent(tents[1]) as ApiResult.Ok).value.statusEnum)

        // ---------------- undo
        val h4 = server.handout(scope)
        h4.submitIdentity(nid(4), "1373/03/03", "09121110004")
        h4.state.until { it.stage == HandoutStage.Tents }
        h4.scan(tents[6]); h4.confirm(); h4.state.until { it.stage == HandoutStage.Done }
        val undo = server.tentInfo(scope); undo.lookup(tents[6]); undo.state.until { it.tent != null }
        undo.perform(TentAction.CancelAssignment)
        assertEquals(TentStatus.READY, undo.state.until { it.message != null }.tent!!.statusEnum)

        // ---------------- live dashboard: office sees a change made at the gate without refreshing
        val dash = office.dashboard(scope)
        dash.start()
        val before = dash.state.until { it.data != null }.data!!.today.assigned
        val h5 = server.handout(scope)
        h5.submitIdentity(nid(5), "1374/04/04", "09121110005")
        h5.state.until { it.stage == HandoutStage.Tents }
        h5.scan(tents[7]); h5.confirm(); h5.state.until { it.stage == HandoutStage.Done }
        val after = dash.state.until(20000) { (it.data?.today?.assigned ?: 0) > before }
        assertTrue(after.data!!.today.assigned > before)
        assertTrue(after.data!!.totals.values.sum() >= 12)
        dash.stop()
        // a gate-level user only gets their own place
        val leadDash = lead.dashboard(scope); leadDash.refresh(); leadDash.start()
        assertEquals(listOf(gate), leadDash.state.until { it.data != null }.data!!.places.map { it.placeId })
        leadDash.stop()

        // ---------------- retire request (lead) and decision (office)
        val ri = lead.tentInfo(scope); ri.lookup(tents[8]); ri.state.until { it.tent != null }
        ri.perform(TentAction.RequestRetire, reason = "damaged", note = "پاره شده")
        ri.state.until { it.message != null }
        val retire = office.retire(scope); retire.load()
        val req = retire.state.until { s -> s.requests.any { it.code == tents[8] } }.requests.first { it.code == tents[8] }
        retire.decide(req.id, approve = true)
        retire.state.until { s -> s.requests.none { it.code == tents[8] } }
        assertEquals(TentStatus.RETIRED, (server.api.tent(tents[8]) as ApiResult.Ok).value.statusEnum)

        // ---------------- move (lead), socks (office supplies, lead returns leftovers)
        val mv = lead.move(scope); mv.load(); mv.state.until { it.places.isNotEmpty() }
        mv.selectPlace(gate); mv.scan(tents[9]); mv.scan(tents[10]); mv.submit()
        assertEquals(2, mv.state.until { !it.busy && it.message != null }.items.count { it.state == ItemState.Done })
        val sockOffice = office.socks(scope); sockOffice.load(); sockOffice.state.until { it.places.isNotEmpty() }
        sockOffice.selectPlace(gate); sockOffice.submit(40)
        assertTrue(sockOffice.state.until { it.message != null }.message!!.contains("موجودی"))
        val sockLead = lead.socks(scope); sockLead.submit(5)
        assertTrue(sockLead.state.until { it.message != null }.message!!.contains("موجودی"))

        // ---------------- shift handover: lead -> server
        val hoLead = lead.handover(scope); hoLead.load()
        val colleague = hoLead.state.until { s -> s.colleagues.any { it.id == server.session.userId } }.colleagues.first { it.id == server.session.userId }
        hoLead.create(colleague.id, "کارت‌خوان شارژ کم دارد")
        hoLead.state.until { s -> s.handovers.any { it.state == "pending" } }
        val hoServer = server.handover(scope); hoServer.load()
        val pending = hoServer.state.until { s -> s.handovers.any { it.state == "pending" } }.handovers.first()
        hoServer.resolve(pending.id, accept = true)
        hoServer.state.until { s -> s.handovers.any { it.state == "accepted" } }

        // ---------------- daily report and Excel export
        val rep = office.report(scope); rep.load(0)
        assertTrue(rep.state.until { it.report != null }.report!!.assigned >= 6)
        rep.export()
        val xlsx = rep.state.until { it.export != null }.export!!
        assertEquals("PK", String(xlsx.copyOf(2)))

        // ---------------- silent token refresh (the harness sets a very short access-token lifetime)
        val before2 = server.tokens.pair!!.accessToken
        delay(5500)
        assertIs<ApiResult.Ok<*>>(server.api.places(), "an expired access token is refreshed transparently")
        assertNotEquals(before2, server.tokens.pair!!.accessToken)

        // ---------------- deactivating a user ends their session immediately
        val victim = users.state.value.users.first { it.username == teamU }
        users.setActive(victim.id, false)
        users.state.until { s -> s.users.any { it.username == teamU && !it.active } }
        assertIs<ApiResult.Failure>(team.api.dashboard())
        val again = newCore(); again.session.login(teamU, "Pw-$teamU-12345!")
        again.session.ui.until { it.error != null }

        // the audit trail saw it all
        val audit = admin.audit(scope); audit.load()
        assertTrue(audit.state.until { it.rows.isNotEmpty() }.rows.any { it.action == "user.update" })
    }
}
