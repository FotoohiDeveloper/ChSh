package ir.chador.app

import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.rememberNavController
import ir.chador.app.screens.AuditScreen
import ir.chador.app.screens.BatchStatusScreen
import ir.chador.app.screens.ChangePasswordScreen
import ir.chador.app.screens.DashboardScreen
import ir.chador.app.screens.HandoutScreen
import ir.chador.app.screens.HandoverScreen
import ir.chador.app.screens.HomeScreen
import ir.chador.app.screens.LoginScreen
import ir.chador.app.screens.MoveScreen
import ir.chador.app.screens.QueueScreen
import ir.chador.app.screens.ReportScreen
import ir.chador.app.screens.RetireScreen
import ir.chador.app.screens.ReturnScreen
import ir.chador.app.screens.Routes
import ir.chador.app.screens.SettingsScreen
import ir.chador.app.screens.ShiftScreen
import ir.chador.app.screens.SocksScreen
import ir.chador.app.screens.TentScreen
import ir.chador.app.screens.UsersScreen
import ir.chador.app.ui.Centered
import ir.chador.core.ChadorCore
import ir.chador.core.SessionState
import ir.chador.core.TentStatus

/** The session decides which part of the app is shown; there is no way to reach a work screen without a ready session. */
@Composable
fun AppRoot(core: ChadorCore) {
    val state by core.session.state.collectAsStateWithLifecycle()
    when (val s = state) {
        SessionState.Loading -> Centered { CircularProgressIndicator() }
        is SessionState.LoggedOut -> LoginScreen(core, s.message)
        is SessionState.MustChangePassword -> ChangePasswordScreen(core, s.user)
        is SessionState.NeedsShift -> ShiftScreen(core, s)
        is SessionState.Ready -> MainNav(core, s)
    }
}

@Composable
private fun MainNav(core: ChadorCore, ready: SessionState.Ready) {
    val nav = rememberNavController()
    val back: () -> Unit = { nav.popBackStack() }
    NavHost(nav, startDestination = Routes.HOME) {
        composable(Routes.HOME) { HomeScreen(core, ready) { nav.navigate(it) } }
        composable(Routes.HANDOUT) { HandoutScreen(core, back) }
        composable(Routes.RETURN) { ReturnScreen(core, back) }
        composable(Routes.TENT) { TentScreen(core, back) }
        composable(Routes.WASH_IN) { BatchStatusScreen(core, TentStatus.IN_WASH, "ورود به شست‌وشو", "چادرهای کثیف را اسکن کنید تا در کارگاه ثبت شوند.", back) }
        composable(Routes.WASH_OUT) { BatchStatusScreen(core, TentStatus.PENDING_REVIEW, "خروج از شست‌وشو", "چادرهای شسته‌شده را اسکن کنید تا برای بررسی ثبت شوند.", back) }
        composable(Routes.REVIEW) { BatchStatusScreen(core, TentStatus.READY, "بررسی و تأیید", "چادرهای سالم و تمیز را اسکن کنید تا «آماده» شوند. برای کثیف یا خراب از «استعلام چادر» استفاده کنید.", back) }
        composable(Routes.MOVE) { MoveScreen(core, back) }
        composable(Routes.DASHBOARD) { DashboardScreen(core, back) }
        composable(Routes.REPORT) { ReportScreen(core, back) }
        composable(Routes.SOCKS) { SocksScreen(core, back) }
        composable(Routes.HANDOVER) { HandoverScreen(core, back) }
        composable(Routes.RETIRE) { RetireScreen(core, back) }
        composable(Routes.USERS) { UsersScreen(core, back) }
        composable(Routes.SETTINGS) { SettingsScreen(core, back) }
        composable(Routes.AUDIT) { AuditScreen(core, back) }
        composable(Routes.QUEUE) { QueueScreen(core, back) }
    }
}
