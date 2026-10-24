package ir.chador.app.screens

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.FilterChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
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
import ir.chador.app.ui.ErrorText
import ir.chador.app.ui.Field
import ir.chador.app.ui.InfoText
import ir.chador.app.ui.Page
import ir.chador.app.ui.PrimaryButton
import ir.chador.app.ui.SectionTitle
import ir.chador.core.ChadorCore
import ir.chador.core.Labels
import ir.chador.core.SessionState
import ir.chador.core.UserView

@Composable
fun LoginScreen(core: ChadorCore, message: String?) {
    val ui by core.session.ui.collectAsStateWithLifecycle()
    var user by remember { mutableStateOf("") }
    var pass by remember { mutableStateOf("") }
    Page("ورود به سامانهٔ چادرها") {
        Text("حرم مطهر شاهچراغ (ع)", style = MaterialTheme.typography.titleLarge, fontWeight = FontWeight.Bold)
        Text("با نام کاربری و گذرواژه‌ای که دفتر به شما داده وارد شوید.", color = MaterialTheme.colorScheme.onSurfaceVariant)
        InfoText(message)
        Field(user, { user = it; core.session.clearError() }, "نام کاربری", ltr = true)
        Field(pass, { pass = it; core.session.clearError() }, "گذرواژه", password = true, ltr = true)
        ErrorText(ui.error)
        PrimaryButton("ورود", onClick = { core.session.login(user, pass) }, busy = ui.busy, enabled = user.isNotBlank() && pass.isNotEmpty())
    }
}

@Composable
fun ChangePasswordScreen(core: ChadorCore, user: UserView) {
    val ui by core.session.ui.collectAsStateWithLifecycle()
    var old by remember { mutableStateOf("") }
    var new1 by remember { mutableStateOf("") }
    var new2 by remember { mutableStateOf("") }
    val mismatch = new2.isNotEmpty() && new1 != new2
    Page("تغییر گذرواژه") {
        Text("${user.fullName}، برای ادامه باید گذرواژهٔ موقت را عوض کنید.")
        Field(old, { old = it }, "گذرواژهٔ فعلی", password = true, ltr = true)
        Field(new1, { new1 = it }, "گذرواژهٔ جدید (حداقل ۱۰ نویسه)", password = true, ltr = true)
        Field(new2, { new2 = it }, "تکرار گذرواژهٔ جدید", password = true, ltr = true)
        if (mismatch) ErrorText("تکرار گذرواژه یکسان نیست.")
        ErrorText(ui.error)
        PrimaryButton(
            "تغییر گذرواژه",
            onClick = { core.session.changePassword(old, new1) },
            busy = ui.busy,
            enabled = old.isNotEmpty() && new1.length >= 10 && new1 == new2,
        )
        TextButton(onClick = { core.session.logout() }) { Text("خروج") }
    }
}

@OptIn(androidx.compose.material3.ExperimentalMaterial3Api::class)
@Composable
fun ShiftScreen(core: ChadorCore, state: SessionState.NeedsShift) {
    val places = rememberPresenter("places") { core.places(it) }
    val st by places.state.collectAsStateWithLifecycle()
    val ui by core.session.ui.collectAsStateWithLifecycle()
    var placeId by remember { mutableStateOf<Int?>(null) }
    var doorId by remember { mutableStateOf<Int?>(null) }
    LaunchedEffect(Unit) { places.load() }
    val selected = st.places.firstOrNull { it.id == placeId }
    Page("شروع شیفت") {
        Text("${state.user.fullName} · ${Labels.role(state.user.roleEnum)}", fontWeight = FontWeight.Bold)
        Text("محل کار خود را انتخاب کنید. تمام ثبت‌ها به نام شما و همین محل ذخیره می‌شود.", color = MaterialTheme.colorScheme.onSurfaceVariant)
        ErrorText(st.error)
        if (st.places.isEmpty() && !st.busy && st.error == null) Text("محلی برای نقش شما تعریف نشده است. با دفتر تماس بگیرید.")
        st.places.forEach { p ->
            AppCard(Modifier.clickable { placeId = p.id; doorId = null }) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    RadioButton(selected = placeId == p.id, onClick = { placeId = p.id; doorId = null })
                    Column {
                        Text(p.name, fontWeight = FontWeight.Bold)
                        Text(Labels.placeKind(p.kind), style = MaterialTheme.typography.bodySmall)
                    }
                }
            }
        }
        if (selected != null && selected.doors.isNotEmpty()) {
            SectionTitle("درب / ورودی")
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                selected.doors.forEach { d ->
                    FilterChip(selected = doorId == d.id, onClick = { doorId = d.id }, label = { Text(d.name) })
                }
            }
        }
        ErrorText(ui.error)
        PrimaryButton(
            "شروع شیفت",
            onClick = { placeId?.let { core.session.startShift(it, doorId) } },
            busy = ui.busy,
            enabled = placeId != null && (selected?.doors.isNullOrEmpty() || doorId != null),
        )
        TextButton(onClick = { core.session.logout() }) { Text("خروج از حساب") }
    }
}
