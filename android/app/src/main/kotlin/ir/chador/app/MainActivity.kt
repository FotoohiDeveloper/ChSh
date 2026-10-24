package ir.chador.app

import android.os.Bundle
import android.view.WindowManager
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import ir.chador.app.ui.ChadorTheme

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        // no screenshots, no content in the recent-apps thumbnail: the screens show national IDs and phone numbers
        window.setFlags(WindowManager.LayoutParams.FLAG_SECURE, WindowManager.LayoutParams.FLAG_SECURE)
        val core = (application as ChadorApp).core
        setContent {
            ChadorTheme {
                AppRoot(core)
            }
        }
    }
}
