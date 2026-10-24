package ir.chador.app.screens

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import ir.chador.app.rememberPresenter
import ir.chador.app.ui.AppCard
import ir.chador.app.ui.ConfirmDialog
import ir.chador.app.ui.Page
import ir.chador.app.ui.SectionTitle
import ir.chador.app.ui.fa
import ir.chador.core.ChadorCore
import ir.chador.core.Labels
import ir.chador.core.Role
import ir.chador.core.SessionState

private data class MenuItem(val route: String, val title: String, val hint: String)

private fun menuFor(role: Role): List<Pair<String, List<MenuItem>>> {
    val work = buildList {
        if (role.canHandOut) add(MenuItem(Routes.HANDOUT, "تحویل چادر به زائر", "ثبت هویت زائر و اسکن چادرها"))
        if (role.canReturn) add(MenuItem(Routes.RETURN, "دریافت چادر از زائر", "اسکن چادرهای برگشتی"))
        if (role == Role.WORKSHOP) {
            add(MenuItem(Routes.WASH_IN, "ورود به شست‌وشو", "چادرهای کثیف ← در کارگاه"))
            add(MenuItem(Routes.WASH_OUT, "خروج از شست‌وشو", "در کارگاه ← در انتظار بررسی"))
        }
        if (role == Role.TEAM || role == Role.GATE_LEAD) add(MenuItem(Routes.REVIEW, "بررسی و تأیید چادر", "تأیید آماده‌بودن چادرهای شسته‌شده"))
        if (role.canMove) add(MenuItem(Routes.MOVE, "انتقال بین محل‌ها", "حرکت چادر بین انبار، گیت و کارگاه"))
        add(MenuItem(Routes.TENT, "استعلام چادر", "وضعیت، سوابق و عملیات روی یک چادر"))
    }
    val office = buildList {
        if (role.seesDashboard) add(MenuItem(Routes.DASHBOARD, "داشبورد زنده", "موجودی و وضعیت چادرها"))
        if (role.canReadReports) add(MenuItem(Routes.REPORT, "گزارش روزانه", "آمار، دیرکردها و خروجی اکسل"))
        if (role.canDecideRetire) add(MenuItem(Routes.RETIRE, "درخواست‌های خروج از چرخه", "تأیید یا رد"))
        if (role.canSupplySocks || role == Role.GATE_LEAD) add(MenuItem(Routes.SOCKS, "جوراب", if (role.canSupplySocks) "تحویل جوراب به گیت" else "برگشت جوراب باقی‌مانده"))
        if (role.canHandover) add(MenuItem(Routes.HANDOVER, "تحویل شیفت", "تحویل به مسئول بعدی"))
    }
    val admin = buildList {
        if (role.isAdmin) {
            add(MenuItem(Routes.USERS, "کاربران", "ساخت حساب، غیرفعال‌سازی، بازنشانی گذرواژه"))
            add(MenuItem(Routes.SETTINGS, "تنظیمات", "قیمت جوراب و ..."))
            add(MenuItem(Routes.AUDIT, "گزارش حسابرسی", "همهٔ اقدامات حساس"))
        }
    }
    return listOf("کار روزانه" to work, "نظارت و مدیریت" to office, "مدیر سامانه" to admin).filter { it.second.isNotEmpty() }
}

@Composable
fun HomeScreen(core: ChadorCore, ready: SessionState.Ready, go: (String) -> Unit) {
    val queue = rememberPresenter("queue-home") { core.queueStatus(it) }
    val pending by queue.pending.collectAsStateWithLifecycle()
    val rejected by queue.rejected.collectAsStateWithLifecycle()
    var confirmEnd by remember { mutableStateOf(false) }
    var confirmLogout by remember { mutableStateOf(false) }
    val user = ready.user
    val role = user.roleEnum

    Page("چادرها") {
        AppCard {
            Text(user.fullName, style = MaterialTheme.typography.titleLarge, fontWeight = FontWeight.Bold)
            Text(Labels.role(role), color = MaterialTheme.colorScheme.onSurfaceVariant)
            ready.shift?.let {
                Text("شیفت: ${it.placeName}" + (it.doorName?.let { d -> " · $d" } ?: ""), color = MaterialTheme.colorScheme.primary, fontWeight = FontWeight.Medium)
            }
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                if (ready.shift != null) TextButton(onClick = { confirmEnd = true }) { Text("پایان شیفت") }
                TextButton(onClick = { confirmLogout = true }) { Text("خروج از حساب") }
            }
        }

        if (pending > 0 || rejected.isNotEmpty()) {
            AppCard(Modifier.clickable { go(Routes.QUEUE) }) {
                if (pending > 0) Text("${fa(pending)} عملیات هنوز به سرور نرسیده است", fontWeight = FontWeight.Bold, color = MaterialTheme.colorScheme.tertiary)
                if (rejected.isNotEmpty()) Text("${fa(rejected.size)} عملیات رد شده است؛ برای مشاهده بزنید", color = MaterialTheme.colorScheme.error)
                Text("برای جزئیات و ارسال دستی بزنید", style = MaterialTheme.typography.bodySmall)
            }
        }

        menuFor(role).forEach { (section, items) ->
            SectionTitle(section)
            items.forEach { item ->
                AppCard(Modifier.clickable { go(item.route) }) {
                    Text(item.title, style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.Bold)
                    Text(item.hint, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
        }
    }

    if (confirmEnd) ConfirmDialog(
        "پایان شیفت",
        if (pending > 0) "${fa(pending)} عملیات هنوز ارسال نشده و پس از اتصال به‌صورت خودکار ارسال می‌شود. شیفت پایان یابد؟" else "شیفت پایان یابد؟",
        onConfirm = { confirmEnd = false; core.session.endShift() },
        onDismiss = { confirmEnd = false },
    )
    if (confirmLogout) ConfirmDialog(
        "خروج از حساب",
        if (pending > 0) "${fa(pending)} عملیات ارسال‌نشده روی این دستگاه می‌ماند و پس از ورود دوباره ارسال می‌شود." else "از حساب خارج شوید؟",
        onConfirm = { confirmLogout = false; core.session.logout() },
        onDismiss = { confirmLogout = false },
    )
}
