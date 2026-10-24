@file:OptIn(androidx.compose.material3.ExperimentalMaterial3Api::class)

package ir.chador.app.screens

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.material3.FilterChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import ir.chador.app.rememberPresenter
import ir.chador.app.ui.AppCard
import ir.chador.app.ui.ConfirmDialog
import ir.chador.app.ui.ErrorText
import ir.chador.app.ui.Field
import ir.chador.app.ui.InfoText
import ir.chador.app.ui.KeyValue
import ir.chador.app.ui.Page
import ir.chador.app.ui.PrimaryButton
import ir.chador.app.ui.ScanRow
import ir.chador.app.ui.ScannerPanel
import ir.chador.app.ui.SecondaryButton
import ir.chador.app.ui.SectionTitle
import ir.chador.app.ui.StatusBadge
import ir.chador.app.ui.fa
import ir.chador.app.ui.rememberScanFeedback
import ir.chador.core.ChadorCore
import ir.chador.core.HandoutStage
import ir.chador.core.ItemState
import ir.chador.core.Jalali
import ir.chador.core.Labels
import ir.chador.core.ScanResult
import ir.chador.core.TentAction
import ir.chador.core.TentStatus

private fun scanHint(r: ScanResult): String? = when (r) {
    ScanResult.Added -> null
    ScanResult.Duplicate -> "این چادر قبلاً در فهرست است."
    ScanResult.Invalid -> "کد خوانده‌شده شناسهٔ چادر نیست."
    ScanResult.Full -> "حداکثر تعداد مجاز در یک نوبت پر شده است."
}

/** Small helper so every scanning screen shows the reason a scan was ignored. */
@Composable
private fun rememberScanHint() = remember { mutableStateOf<String?>(null) }

// ------------------------------------------------------------------ hand out

@Composable
fun HandoutScreen(core: ChadorCore, onBack: () -> Unit) {
    val p = rememberPresenter("handout") { core.handout(it) }
    val st by p.state.collectAsStateWithLifecycle()
    val fb = rememberScanFeedback()
    val hint = rememberScanHint()
    var nid by remember { mutableStateOf("") }
    var birth by remember { mutableStateOf("") }
    var mobile by remember { mutableStateOf("") }

    Page("تحویل چادر", onBack) {
        when (st.stage) {
            HandoutStage.Identity -> {
                Text("هویت زائر را وارد کنید. کد ملی، تاریخ تولد و شمارهٔ موبایل او استعلام می‌شود.", color = MaterialTheme.colorScheme.onSurfaceVariant)
                Field(nid, { nid = it }, "کد ملی", keyboard = KeyboardType.Number, ltr = true)
                Field(birth, { birth = it }, "تاریخ تولد", keyboard = KeyboardType.Number, placeholder = "۱۳۷۰/۰۵/۱۲", ltr = true)
                Field(mobile, { mobile = it }, "شمارهٔ موبایل", keyboard = KeyboardType.Phone, placeholder = "۰۹۱۲۳۴۵۶۷۸۹", ltr = true)
                ErrorText(st.error)
                PrimaryButton("استعلام و ادامه", onClick = { p.submitIdentity(nid, birth, mobile) }, busy = st.busy)
            }

            HandoutStage.Tents -> {
                val pilgrim = st.pilgrim
                if (pilgrim != null) AppCard {
                    Text(pilgrim.fullName ?: "زائر", style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.Bold)
                    KeyValue("کد ملی", Jalali.persianDigits(pilgrim.nationalIdMasked))
                    KeyValue("موبایل", Jalali.persianDigits(pilgrim.mobileMasked))
                    if (pilgrim.activeTents.isNotEmpty()) {
                        Text("چادرهای در دست این زائر: " + pilgrim.activeTents.joinToString("، ") { Jalali.persianDigits(it) }, color = MaterialTheme.colorScheme.tertiary)
                    }
                }
                ScannerPanel(onCode = { raw ->
                    val r = p.scan(raw)
                    fb.play(r)
                    hint.value = scanHint(r)
                })
                InfoText(hint.value)
                SectionTitle("چادرهای اسکن‌شده (${fa(st.tents.size)})")
                st.tents.forEach { ScanRow(it, onRemove = { p.remove(it.code) }) }
                SectionTitle("جوراب")
                ir.chador.app.ui.Stepper(st.socks, p::setSocks)
                ErrorText(st.error)
                PrimaryButton("تأیید تحویل (${fa(st.tents.size)} چادر)", onClick = p::confirm, busy = st.busy, enabled = st.tents.isNotEmpty())
                SecondaryButton("انصراف", onClick = { p.reset(); nid = ""; birth = ""; mobile = "" })
            }

            HandoutStage.Done -> {
                val d = st.done
                InfoText(
                    if (d?.queued == true) "اتصال برقرار نیست؛ تحویل روی دستگاه ذخیره شد و خودکار ارسال می‌شود."
                    else "تحویل ثبت شد.",
                )
                d?.codes?.forEach { Text(Jalali.persianDigits(it), fontWeight = FontWeight.Bold) }
                if (d != null && d.socksAmount > 0) Text("مبلغ جوراب: ${fa(d.socksAmount)} تومان")
                PrimaryButton("زائر بعدی", onClick = { p.reset(); nid = ""; birth = ""; mobile = "" })
                SecondaryButton("بازگشت", onClick = onBack)
            }
        }
    }
}

// ------------------------------------------------------------------ return

@Composable
fun ReturnScreen(core: ChadorCore, onBack: () -> Unit) {
    val p = rememberPresenter("return") { core.returns(it) }
    val st by p.state.collectAsStateWithLifecycle()
    val fb = rememberScanFeedback()
    val hint = rememberScanHint()

    Page("دریافت چادر", onBack) {
        ScannerPanel(onCode = { raw ->
            val r = p.scan(raw)
            fb.play(r)
            hint.value = scanHint(r)
        })
        InfoText(hint.value)
        Text("وضعیت هر چادر را هنگام دریافت مشخص کنید.", style = MaterialTheme.typography.bodySmall)
        st.items.forEach { item ->
            ScanRow(item, onRemove = { p.remove(item.code) })
            if (item.state != ItemState.Done) {
                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    listOf(TentStatus.READY, TentStatus.DIRTY, TentStatus.REPAIR).forEach { s ->
                        FilterChip(selected = item.status == s, onClick = { p.setStatus(item.code, s) }, label = { Text(Labels.status(s)) })
                    }
                }
            }
        }
        ErrorText(st.error)
        st.summary?.let { s ->
            InfoText(
                if (s.queued) "اتصال برقرار نیست؛ ذخیره شد و خودکار ارسال می‌شود."
                else "${fa(s.returned)} چادر دریافت شد" +
                    (if (s.completedPilgrims > 0) " · ${fa(s.completedPilgrims)} زائر تسویه شد" else "") +
                    (if (s.failed > 0) " · ${fa(s.failed)} مورد ناموفق" else ""),
            )
        }
        PrimaryButton("ثبت دریافت", onClick = p::submit, busy = st.busy, enabled = st.items.any { it.state == ItemState.Pending || it.state == ItemState.Failed })
        if (st.items.isNotEmpty()) SecondaryButton("شروع دوباره", onClick = p::reset)
    }
}

// ------------------------------------------------------------------ one tent

@Composable
fun TentScreen(core: ChadorCore, onBack: () -> Unit) {
    val p = rememberPresenter("tent") { core.tentInfo(it) }
    val st by p.state.collectAsStateWithLifecycle()
    var pending by remember { mutableStateOf<TentAction?>(null) }
    var retireReason by remember { mutableStateOf("lost") }
    var note by remember { mutableStateOf("") }

    Page("استعلام چادر", onBack) {
        ScannerPanel(onCode = { raw -> p.lookup(raw) }, cameraHeight = 180)
        ErrorText(st.error)
        InfoText(st.message)
        val tent = st.tent
        if (tent != null) {
            AppCard {
                Text(Jalali.persianDigits(tent.code), style = MaterialTheme.typography.headlineSmall, fontWeight = FontWeight.Bold)
                StatusBadge(tent.statusEnum)
                tent.placeName?.let { KeyValue("محل", it) }
                tent.note?.let { KeyValue("یادداشت", it) }
            }
            tent.assignment?.let { a ->
                AppCard {
                    Text("نزد زائر", fontWeight = FontWeight.Bold)
                    a.pilgrimName?.let { KeyValue("نام", it) }
                    a.mobileMasked?.let { KeyValue("موبایل", Jalali.persianDigits(it)) }
                    KeyValue("زمان تحویل", Jalali.display(a.assignedAt))
                    a.assignedPlace?.let { KeyValue("محل تحویل", it) }
                }
            }
            if (st.actions.isNotEmpty()) {
                SectionTitle("عملیات")
                st.actions.forEach { a ->
                    when (a) {
                        TentAction.CancelAssignment -> SecondaryButton("لغو تحویل", onClick = { pending = a })
                        is TentAction.SetStatus -> SecondaryButton("تغییر به «${Labels.status(a.to)}»", onClick = { pending = a })
                        TentAction.RequestRetire -> SecondaryButton("درخواست خروج از چرخه", onClick = { pending = a })
                    }
                }
            }
            if (st.history.isNotEmpty()) {
                SectionTitle("سوابق")
                st.history.forEach { e ->
                    AppCard {
                        Text(Labels.eventType(e.type), fontWeight = FontWeight.Medium)
                        if (e.from != null || e.to != null) Text("${e.from?.let { Labels.status(it) } ?: "—"} ← ${e.to?.let { Labels.status(it) } ?: "—"}", style = MaterialTheme.typography.bodySmall)
                        Text("${Jalali.display(e.at)} · ${e.user}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                }
            }
        }
    }

    when (val a = pending) {
        TentAction.CancelAssignment -> ConfirmDialog("لغو تحویل", "تحویل این چادر به زائر لغو شود؟ (فقط برای اشتباه ثبت‌شده در چند دقیقهٔ اول)", onConfirm = { pending = null; p.perform(a) }, onDismiss = { pending = null })
        is TentAction.SetStatus -> ConfirmDialog("تغییر وضعیت", "وضعیت چادر به «${Labels.status(a.to)}» تغییر کند؟", onConfirm = { pending = null; p.perform(a) }, onDismiss = { pending = null })
        TentAction.RequestRetire -> androidx.compose.material3.AlertDialog(
            onDismissRequest = { pending = null },
            title = { Text("درخواست خروج از چرخه") },
            text = {
                androidx.compose.foundation.layout.Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        listOf("lost", "damaged", "other").forEach { r ->
                            FilterChip(selected = retireReason == r, onClick = { retireReason = r }, label = { Text(Labels.retireReason(r)) })
                        }
                    }
                    Field(note, { note = it }, "توضیح (اختیاری)")
                }
            },
            confirmButton = {
                androidx.compose.material3.TextButton(onClick = { pending = null; p.perform(TentAction.RequestRetire, retireReason, note.ifBlank { null }); note = "" }) { Text("ثبت درخواست") }
            },
            dismissButton = { androidx.compose.material3.TextButton(onClick = { pending = null }) { Text("انصراف") } },
        )
        null -> {}
    }
}

// ------------------------------------------------------------------ batch status change

@Composable
fun BatchStatusScreen(core: ChadorCore, target: TentStatus, title: String, help: String, onBack: () -> Unit) {
    val p = rememberPresenter("batch-${target.wire}") { core.statusBatch(it, target) }
    val st by p.state.collectAsStateWithLifecycle()
    val fb = rememberScanFeedback()
    val hint = rememberScanHint()

    Page(title, onBack) {
        Text(help, color = MaterialTheme.colorScheme.onSurfaceVariant)
        ScannerPanel(onCode = { raw ->
            val r = p.scan(raw)
            fb.play(r)
            hint.value = scanHint(r)
        })
        InfoText(hint.value)
        st.items.forEach { ScanRow(it, onRemove = { p.remove(it.code) }) }
        ErrorText(st.error)
        if (!st.busy && (st.done > 0 || st.queued > 0)) {
            InfoText("${fa(st.done)} چادر ثبت شد" + if (st.queued > 0) " · ${fa(st.queued)} چادر در صف ارسال" else "")
        }
        PrimaryButton(
            "ثبت (${fa(st.items.count { it.state == ItemState.Pending || it.state == ItemState.Failed })} چادر)",
            onClick = p::submit,
            busy = st.busy,
            enabled = st.items.any { it.state == ItemState.Pending || it.state == ItemState.Failed },
        )
        if (st.items.isNotEmpty()) SecondaryButton("شروع دوباره", onClick = p::reset)
    }
}

// ------------------------------------------------------------------ move

@Composable
fun MoveScreen(core: ChadorCore, onBack: () -> Unit) {
    val p = rememberPresenter("move") { core.move(it) }
    val st by p.state.collectAsStateWithLifecycle()
    val fb = rememberScanFeedback()
    val hint = rememberScanHint()
    LaunchedEffect(Unit) { p.load() }

    Page("انتقال چادر", onBack) {
        SectionTitle("مقصد")
        st.places.forEach { pl ->
            FilterChip(selected = st.toPlaceId == pl.id, onClick = { p.selectPlace(pl.id) }, label = { Text("${pl.name} (${Labels.placeKind(pl.kind)})") })
        }
        ScannerPanel(onCode = { raw ->
            val r = p.scan(raw)
            fb.play(r)
            hint.value = scanHint(r)
        })
        InfoText(hint.value)
        st.items.forEach { ScanRow(it, onRemove = { p.remove(it.code) }) }
        ErrorText(st.error)
        InfoText(st.message)
        PrimaryButton("انتقال (${fa(st.items.size)} چادر)", onClick = p::submit, busy = st.busy, enabled = st.items.isNotEmpty() && st.toPlaceId != null)
        if (st.items.isNotEmpty()) SecondaryButton("شروع دوباره", onClick = p::reset)
    }
}
