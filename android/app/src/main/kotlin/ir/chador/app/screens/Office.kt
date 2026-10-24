@file:OptIn(androidx.compose.material3.ExperimentalMaterial3Api::class)

package ir.chador.app.screens

import android.content.Intent
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.material3.FilterChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.core.content.FileProvider
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import ir.chador.app.rememberPresenter
import ir.chador.app.ui.AppCard
import ir.chador.app.ui.ErrorText
import ir.chador.app.ui.Field
import ir.chador.app.ui.InfoText
import ir.chador.app.ui.KeyValue
import ir.chador.app.ui.Page
import ir.chador.app.ui.PrimaryButton
import ir.chador.app.ui.SecondaryButton
import ir.chador.app.ui.SectionTitle
import ir.chador.app.ui.StatusBadge
import ir.chador.app.ui.fa
import ir.chador.core.ChadorCore
import ir.chador.core.Jalali
import ir.chador.core.Labels
import ir.chador.core.Role
import ir.chador.core.TentStatus
import java.io.File

private val statusOrder = listOf(
    TentStatus.READY, TentStatus.WITH_PILGRIM, TentStatus.DIRTY, TentStatus.IN_WASH,
    TentStatus.PENDING_REVIEW, TentStatus.REPAIR, TentStatus.RETIRED,
)

// ------------------------------------------------------------------ dashboard

@Composable
fun DashboardScreen(core: ChadorCore, onBack: () -> Unit) {
    val p = rememberPresenter("dashboard") { core.dashboard(it) }
    val st by p.state.collectAsStateWithLifecycle()
    // the live connection only runs while this screen is visible
    DisposableEffect(Unit) { p.start(); onDispose { p.stop() } }

    Page("داشبورد", onBack, actions = { TextButton(onClick = p::refresh) { Text("به‌روزرسانی") } }) {
        Text(
            if (st.live) "● زنده" else "○ به‌روزرسانی دوره‌ای",
            color = if (st.live) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurfaceVariant,
            style = MaterialTheme.typography.labelLarge,
        )
        ErrorText(st.error)
        val d = st.data
        if (d != null) {
            AppCard {
                Text("کل چادرها", fontWeight = FontWeight.Bold)
                statusOrder.forEach { s ->
                    val n = d.totals[s.wire] ?: 0
                    if (n > 0 || s == TentStatus.READY || s == TentStatus.WITH_PILGRIM) {
                        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                            StatusBadge(s)
                            Text(fa(n), fontWeight = FontWeight.Bold, style = MaterialTheme.typography.titleMedium)
                        }
                    }
                }
            }
            AppCard {
                Text("امروز", fontWeight = FontWeight.Bold)
                KeyValue("تحویل داده‌شده", fa(d.today.assigned))
                KeyValue("بازگشته", fa(d.today.returned))
                if (d.overdue > 0) KeyValue("دیرکرد", fa(d.overdue), valueBold = true)
            }
            SectionTitle("به تفکیک محل")
            d.places.forEach { pl ->
                AppCard {
                    Text("${pl.name} · ${Labels.placeKind(pl.kind)}", fontWeight = FontWeight.Bold)
                    statusOrder.forEach { s ->
                        val n = pl.byStatus[s.wire] ?: 0
                        if (n > 0) KeyValue(Labels.status(s), fa(n))
                    }
                    if (pl.socksBalance != 0) KeyValue("موجودی جوراب", fa(pl.socksBalance))
                    if (pl.outFromHere > 0) KeyValue("تحویل‌شده از این محل و هنوز بیرون", fa(pl.outFromHere))
                }
            }
        } else if (st.loading) Text("در حال بارگذاری…")
    }
}

// ------------------------------------------------------------------ daily report

@Composable
fun ReportScreen(core: ChadorCore, onBack: () -> Unit) {
    val p = rememberPresenter("report") { core.report(it) }
    val st by p.state.collectAsStateWithLifecycle()
    val ctx = LocalContext.current
    LaunchedEffect(Unit) { p.load() }

    // a finished xlsx download is written to the app cache and handed to the system share sheet
    LaunchedEffect(st.export) {
        val bytes = st.export ?: return@LaunchedEffect
        val name = st.exportName ?: "report.xlsx"
        val dir = File(ctx.cacheDir, "exports").apply { mkdirs() }
        dir.listFiles()?.forEach { it.delete() }
        val f = File(dir, name).apply { writeBytes(bytes) }
        val uri = FileProvider.getUriForFile(ctx, "${ctx.packageName}.files", f)
        val send = Intent(Intent.ACTION_SEND).apply {
            type = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
            putExtra(Intent.EXTRA_STREAM, uri)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
        ctx.startActivity(Intent.createChooser(send, "ارسال گزارش").addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
        p.consumeExport()
    }

    Page("گزارش روزانه", onBack) {
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
            TextButton(onClick = p::previousDay) { Text("روز قبل") }
            Text(Jalali.persianDigits(st.date), fontWeight = FontWeight.Bold, style = MaterialTheme.typography.titleMedium)
            TextButton(onClick = p::nextDay, enabled = st.dayOffset < 0) { Text("روز بعد") }
        }
        ErrorText(st.error)
        val r = st.report
        if (r != null) {
            AppCard {
                KeyValue("تحویل", fa(r.assigned))
                KeyValue("بازگشت", fa(r.returned))
                KeyValue("لغوشده", fa(r.cancelled))
                KeyValue("مفقود", fa(r.lost))
                KeyValue("جوراب (عدد)", fa(r.socksQty))
                KeyValue("مبلغ جوراب (تومان)", fa(r.socksAmount))
            }
            SectionTitle("وضعیت فعلی")
            AppCard {
                statusOrder.forEach { s -> r.statusNow[s.wire]?.let { KeyValue(Labels.status(s), fa(it)) } }
            }
            if (r.places.isNotEmpty()) {
                SectionTitle("به تفکیک محل")
                r.places.forEach { pl ->
                    AppCard {
                        Text(pl.name, fontWeight = FontWeight.Bold)
                        KeyValue("تحویل / بازگشت", "${fa(pl.assigned)} / ${fa(pl.returned)}")
                        if (pl.socksQty > 0) KeyValue("جوراب", "${fa(pl.socksQty)} (${fa(pl.socksAmount)} تومان)")
                    }
                }
            }
            if (r.overdue.isNotEmpty()) {
                SectionTitle("دیرکردها (${fa(r.overdue.size)})")
                r.overdue.forEach { o ->
                    AppCard {
                        Text(Jalali.persianDigits(o.code), fontWeight = FontWeight.Bold)
                        Text("${o.place} · ${Jalali.display(o.assignedAt)}", style = MaterialTheme.typography.bodySmall)
                        if (o.reminders > 0) Text("پیامک یادآوری: ${fa(o.reminders)}", style = MaterialTheme.typography.bodySmall)
                    }
                }
            }
        }
        PrimaryButton("خروجی اکسل", onClick = p::export, busy = st.busy, enabled = st.report != null)
    }
}

// ------------------------------------------------------------------ socks

@Composable
fun SocksScreen(core: ChadorCore, onBack: () -> Unit) {
    val p = rememberPresenter("socks") { core.socks(it) }
    val st by p.state.collectAsStateWithLifecycle()
    var qty by remember { mutableStateOf("") }
    val supply = core.session.role.canSupplySocks
    LaunchedEffect(Unit) { if (supply) p.load() }

    Page("جوراب", onBack) {
        Text(
            if (supply) "تعداد جورابی را که به گیت تحویل می‌دهید ثبت کنید." else "جورابِ باقی‌ماندهٔ گیت را در پایان شیفت برگشت بزنید.",
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
        if (supply) {
            SectionTitle("گیت")
            st.places.forEach { pl -> FilterChip(selected = st.placeId == pl.id, onClick = { p.selectPlace(pl.id) }, label = { Text(pl.name) }) }
        }
        Field(qty, { qty = it.filter { c -> c.isDigit() } }, "تعداد", keyboard = KeyboardType.Number, ltr = true)
        ErrorText(st.error)
        InfoText(st.message)
        PrimaryButton("ثبت", onClick = { p.submit(qty.toIntOrNull() ?: 0); qty = "" }, busy = st.busy, enabled = qty.isNotEmpty())
    }
}

// ------------------------------------------------------------------ handover

@Composable
fun HandoverScreen(core: ChadorCore, onBack: () -> Unit) {
    val p = rememberPresenter("handover") { core.handover(it) }
    val st by p.state.collectAsStateWithLifecycle()
    var to by remember { mutableStateOf<String?>(null) }
    var notes by remember { mutableStateOf("") }
    val me = core.session.userId
    LaunchedEffect(Unit) { p.load() }

    Page("تحویل شیفت", onBack) {
        ErrorText(st.error)
        InfoText(st.message)
        SectionTitle("تحویل به")
        if (st.colleagues.isEmpty()) Text("همکار دیگری در شیفت فعال نیست.", color = MaterialTheme.colorScheme.onSurfaceVariant)
        st.colleagues.forEach { c ->
            FilterChip(selected = to == c.id, onClick = { to = c.id }, label = { Text("${c.fullName} · ${Labels.role(Role.from(c.role))}") })
        }
        Field(notes, { notes = it }, "توضیحات (اختیاری)")
        PrimaryButton("ثبت تحویل", onClick = { to?.let { p.create(it, notes); notes = "" } }, busy = st.busy, enabled = to != null)

        if (st.handovers.isNotEmpty()) {
            SectionTitle("سوابق")
            st.handovers.forEach { h ->
                AppCard {
                    Text("${h.from} ← ${h.to}", fontWeight = FontWeight.Bold)
                    Text("${h.place} · ${Jalali.display(h.createdAt)}", style = MaterialTheme.typography.bodySmall)
                    h.notes?.let { Text(it) }
                    Text(
                        when (h.state) { "pending" -> "منتظر تأیید"; "accepted" -> "پذیرفته شد"; "disputed" -> "اختلاف ثبت شد"; else -> h.state },
                        color = MaterialTheme.colorScheme.primary,
                    )
                    // only the receiver may answer, and only while it is pending
                    if (h.state == "pending" && h.to.isNotEmpty() && core.session.currentUser?.fullName == h.to && me != null) {
                        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            TextButton(onClick = { p.resolve(h.id, true) }) { Text("پذیرش") }
                            TextButton(onClick = { p.resolve(h.id, false) }) { Text("ثبت اختلاف") }
                        }
                    }
                }
            }
        }
    }
}

// ------------------------------------------------------------------ retire requests

@Composable
fun RetireScreen(core: ChadorCore, onBack: () -> Unit) {
    val p = rememberPresenter("retire") { core.retire(it) }
    val st by p.state.collectAsStateWithLifecycle()
    LaunchedEffect(Unit) { p.load() }

    Page("درخواست‌های خروج از چرخه", onBack) {
        ErrorText(st.error)
        val open = st.requests.filter { it.state == "pending" }
        if (open.isEmpty() && !st.busy) Text("درخواست بازی وجود ندارد.", color = MaterialTheme.colorScheme.onSurfaceVariant)
        open.forEach { r ->
            AppCard {
                Text(Jalali.persianDigits(r.code), fontWeight = FontWeight.Bold, style = MaterialTheme.typography.titleMedium)
                KeyValue("دلیل", Labels.retireReason(r.reason))
                r.note?.let { KeyValue("توضیح", it) }
                KeyValue("درخواست‌دهنده", r.requestedBy)
                KeyValue("زمان", Jalali.display(r.requestedAt))
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    TextButton(onClick = { p.decide(r.id, true) }) { Text("تأیید خروج") }
                    TextButton(onClick = { p.decide(r.id, false) }) { Text("رد") }
                }
            }
        }
    }
}
