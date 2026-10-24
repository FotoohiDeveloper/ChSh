@file:OptIn(androidx.compose.material3.ExperimentalMaterial3Api::class)

package ir.chador.app.screens

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.FilterChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
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
import ir.chador.app.ui.SecondaryButton
import ir.chador.app.ui.SectionTitle
import ir.chador.app.ui.fa
import ir.chador.core.ChadorCore
import ir.chador.core.Jalali
import ir.chador.core.Labels
import ir.chador.core.Role

// ------------------------------------------------------------------ users

@Composable
fun UsersScreen(core: ChadorCore, onBack: () -> Unit) {
    val p = rememberPresenter("users") { core.users(it) }
    val st by p.state.collectAsStateWithLifecycle()
    var creating by remember { mutableStateOf(false) }
    var resetFor by remember { mutableStateOf<ir.chador.core.UserView?>(null) }
    LaunchedEffect(Unit) { p.load() }

    Page("کاربران", onBack, actions = { TextButton(onClick = { creating = true }) { Text("کاربر جدید") } }) {
        ErrorText(st.error)
        st.users.forEach { u ->
            AppCard {
                Text(u.fullName, fontWeight = FontWeight.Bold)
                Text("${u.username} · ${Labels.role(u.roleEnum)}", style = MaterialTheme.typography.bodySmall)
                if (!u.active) Text("غیرفعال", color = MaterialTheme.colorScheme.error)
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    TextButton(onClick = { p.setActive(u.id, !u.active) }) { Text(if (u.active) "غیرفعال‌سازی" else "فعال‌سازی") }
                    TextButton(onClick = { resetFor = u }) { Text("بازنشانی گذرواژه") }
                }
            }
        }
    }

    if (creating) {
        var username by remember { mutableStateOf("") }
        var name by remember { mutableStateOf("") }
        var role by remember { mutableStateOf(Role.SERVER) }
        AlertDialog(
            onDismissRequest = { creating = false },
            title = { Text("کاربر جدید") },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Field(username, { username = it }, "نام کاربری (انگلیسی)", ltr = true)
                    Field(name, { name = it }, "نام و نام خانوادگی")
                    listOf(Role.SERVER, Role.GATE_LEAD, Role.TEAM, Role.WORKSHOP, Role.SERVICE, Role.OFFICE, Role.ADMIN).chunked(2).forEach { row ->
                        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            row.forEach { r -> FilterChip(selected = role == r, onClick = { role = r }, label = { Text(Labels.role(r)) }) }
                        }
                    }
                }
            },
            confirmButton = {
                TextButton(enabled = username.length >= 3 && name.isNotBlank(), onClick = { creating = false; p.create(username, name, role) }) { Text("ساخت") }
            },
            dismissButton = { TextButton(onClick = { creating = false }) { Text("انصراف") } },
        )
    }

    resetFor?.let { u ->
        ConfirmDialog("بازنشانی گذرواژه", "گذرواژهٔ ${u.fullName} بازنشانی شود؟ همهٔ نشست‌های او بسته می‌شود.", onConfirm = { resetFor = null; p.resetPassword(u) }, onDismiss = { resetFor = null })
    }

    // shown exactly once: the server does not keep the plain password
    st.tempPassword?.let { (user, pass) ->
        AlertDialog(
            onDismissRequest = {},
            title = { Text("گذرواژهٔ موقت") },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text("کاربر: $user")
                    Text(pass, style = MaterialTheme.typography.headlineSmall, fontWeight = FontWeight.Bold)
                    Text("این گذرواژه فقط همین یک بار نمایش داده می‌شود. به کاربر بدهید؛ در اولین ورود باید آن را عوض کند.", style = MaterialTheme.typography.bodySmall)
                }
            },
            confirmButton = { TextButton(onClick = p::consumeTempPassword) { Text("یادداشت کردم") } },
        )
    }
}

// ------------------------------------------------------------------ settings

@Composable
fun SettingsScreen(core: ChadorCore, onBack: () -> Unit) {
    val p = rememberPresenter("settings") { core.settings(it) }
    val st by p.state.collectAsStateWithLifecycle()
    var price by remember { mutableStateOf("") }
    LaunchedEffect(Unit) { p.load() }
    LaunchedEffect(st.socksPrice) { st.socksPrice?.let { price = it.toString() } }

    Page("تنظیمات", onBack) {
        ErrorText(st.error)
        InfoText(st.message)
        Field(price, { price = it.filter { c -> c.isDigit() } }, "قیمت هر جفت جوراب (تومان)", keyboard = KeyboardType.Number, ltr = true)
        st.maxTents?.let { KeyValue("حداکثر چادر در هر نوبت", fa(it)) }
        PrimaryButton("ذخیره", onClick = { p.saveSocksPrice(price.toIntOrNull() ?: -1) }, busy = st.busy, enabled = price.isNotEmpty())
    }
}

// ------------------------------------------------------------------ audit

@Composable
fun AuditScreen(core: ChadorCore, onBack: () -> Unit) {
    val p = rememberPresenter("audit") { core.audit(it) }
    val st by p.state.collectAsStateWithLifecycle()
    LaunchedEffect(Unit) { p.load() }

    Page("گزارش حسابرسی", onBack) {
        ErrorText(st.error)
        st.rows.forEach { r ->
            AppCard {
                Text(r.action, fontWeight = FontWeight.Bold)
                Text("${r.user} · ${Jalali.display(r.at)}", style = MaterialTheme.typography.bodySmall)
                if (r.entity.isNotEmpty()) Text("${r.entity} ${r.entityId}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                if (r.ip.isNotEmpty()) Text(r.ip, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
        if (st.canLoadMore) SecondaryButton("نمایش بیشتر", onClick = { p.load(more = true) })
    }
}

// ------------------------------------------------------------------ offline queue

@Composable
fun QueueScreen(core: ChadorCore, onBack: () -> Unit) {
    val p = rememberPresenter("queue") { core.queueStatus(it) }
    val pending by p.pending.collectAsStateWithLifecycle()
    val rejected by p.rejected.collectAsStateWithLifecycle()
    val ui by p.ui.collectAsStateWithLifecycle()

    Page("عملیات ذخیره‌شده", onBack) {
        Text(
            "وقتی اینترنت قطع است، عملیات روی همین دستگاه ذخیره و پس از اتصال خودکار ارسال می‌شود. ارسال دوباره هیچ ثبت تکراری نمی‌سازد.",
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
        AppCard {
            KeyValue("منتظر ارسال", fa(pending), valueBold = true)
            KeyValue("ردشده توسط سرور", fa(rejected.size), valueBold = rejected.isNotEmpty())
        }
        InfoText(ui.lastMessage)
        PrimaryButton("ارسال همین حالا", onClick = p::syncNow, busy = ui.syncing, enabled = pending > 0)

        if (rejected.isNotEmpty()) {
            SectionTitle("ردشده‌ها")
            Text("این موارد را سرور نپذیرفت (مثلاً چادر دیگر آماده نبود). لازم است دستی بررسی شوند.", style = MaterialTheme.typography.bodySmall)
            rejected.forEach { r ->
                AppCard {
                    Text(r.op.label, fontWeight = FontWeight.Bold)
                    Text(r.message, color = MaterialTheme.colorScheme.error)
                    Text(Jalali.display(ir.chador.core.Time.format(r.at)), style = MaterialTheme.typography.bodySmall)
                }
            }
            SecondaryButton("پاک‌کردن فهرست ردشده‌ها", onClick = p::clearRejected)
        }
    }
}
