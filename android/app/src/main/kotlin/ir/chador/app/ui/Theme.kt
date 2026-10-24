package ir.chador.app.ui

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Typography
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalLayoutDirection
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.Font
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.LayoutDirection
import ir.chador.app.R

val Vazirmatn = FontFamily(
    Font(R.font.vazirmatn_regular, FontWeight.Normal),
    Font(R.font.vazirmatn_medium, FontWeight.Medium),
    Font(R.font.vazirmatn_bold, FontWeight.Bold),
)

private val Light = lightColorScheme(
    primary = Color(0xFF14532D), onPrimary = Color.White,
    primaryContainer = Color(0xFFD8F0DF), onPrimaryContainer = Color(0xFF05260F),
    secondary = Color(0xFF52634F), onSecondary = Color.White,
    secondaryContainer = Color(0xFFD5E8CF), onSecondaryContainer = Color(0xFF111F0F),
    tertiary = Color(0xFF8A5A00), onTertiary = Color.White,
    tertiaryContainer = Color(0xFFFFDDAE), onTertiaryContainer = Color(0xFF2B1800),
    error = Color(0xFFB3261E), onError = Color.White,
    errorContainer = Color(0xFFF9DEDC), onErrorContainer = Color(0xFF410E0B),
    background = Color(0xFFFFFBF7), onBackground = Color(0xFF1B1C18),
    surface = Color(0xFFFFFBF7), onSurface = Color(0xFF1B1C18),
    surfaceVariant = Color(0xFFE0E4D8), onSurfaceVariant = Color(0xFF43483F),
    outline = Color(0xFF73796E),
)

private val Dark = darkColorScheme(
    primary = Color(0xFF8BD6A0), onPrimary = Color(0xFF003918),
    primaryContainer = Color(0xFF0F4A26), onPrimaryContainer = Color(0xFFD8F0DF),
    secondary = Color(0xFFB9CCB4), onSecondary = Color(0xFF253423),
    secondaryContainer = Color(0xFF3B4B38), onSecondaryContainer = Color(0xFFD5E8CF),
    tertiary = Color(0xFFFFB955), onTertiary = Color(0xFF462B00),
    tertiaryContainer = Color(0xFF654000), onTertiaryContainer = Color(0xFFFFDDAE),
    error = Color(0xFFF2B8B5), onError = Color(0xFF601410),
    errorContainer = Color(0xFF8C1D18), onErrorContainer = Color(0xFFF9DEDC),
    background = Color(0xFF121410), onBackground = Color(0xFFE3E3DB),
    surface = Color(0xFF121410), onSurface = Color(0xFFE3E3DB),
    surfaceVariant = Color(0xFF43483F), onSurfaceVariant = Color(0xFFC3C8BB),
    outline = Color(0xFF8D9387),
)

private fun Typography.withFont(f: FontFamily): Typography {
    fun TextStyle.v() = copy(fontFamily = f)
    return copy(
        displayLarge = displayLarge.v(), displayMedium = displayMedium.v(), displaySmall = displaySmall.v(),
        headlineLarge = headlineLarge.v(), headlineMedium = headlineMedium.v(), headlineSmall = headlineSmall.v(),
        titleLarge = titleLarge.v(), titleMedium = titleMedium.v(), titleSmall = titleSmall.v(),
        bodyLarge = bodyLarge.v(), bodyMedium = bodyMedium.v(), bodySmall = bodySmall.v(),
        labelLarge = labelLarge.v(), labelMedium = labelMedium.v(), labelSmall = labelSmall.v(),
    )
}

/** The whole app is Persian: always right-to-left, whatever the phone's language is. */
@Composable
fun ChadorTheme(content: @Composable () -> Unit) {
    CompositionLocalProvider(LocalLayoutDirection provides LayoutDirection.Rtl) {
        MaterialTheme(
            colorScheme = if (isSystemInDarkTheme()) Dark else Light,
            typography = Typography().withFont(Vazirmatn),
            content = content,
        )
    }
}

/** Status colours, readable in light and dark: (background, foreground). */
@Composable
fun statusColors(status: ir.chador.core.TentStatus): Pair<Color, Color> {
    val dark = isSystemInDarkTheme()
    return when (status) {
        ir.chador.core.TentStatus.READY -> if (dark) Color(0xFF0F4A26) to Color(0xFFBFF0CD) else Color(0xFFD8F0DF) to Color(0xFF05260F)
        ir.chador.core.TentStatus.WITH_PILGRIM -> if (dark) Color(0xFF0B3C5C) to Color(0xFFC6E7FF) else Color(0xFFD3EBFF) to Color(0xFF00263D)
        ir.chador.core.TentStatus.DIRTY -> if (dark) Color(0xFF654000) to Color(0xFFFFDDAE) else Color(0xFFFFE5BD) to Color(0xFF2B1800)
        ir.chador.core.TentStatus.IN_WASH -> if (dark) Color(0xFF004F52) to Color(0xFFB0F0F2) else Color(0xFFC6F0F2) to Color(0xFF002021)
        ir.chador.core.TentStatus.PENDING_REVIEW -> if (dark) Color(0xFF4A3C73) to Color(0xFFE6DEFF) else Color(0xFFE6DEFF) to Color(0xFF1F1148)
        ir.chador.core.TentStatus.REPAIR -> if (dark) Color(0xFF8C1D18) to Color(0xFFF9DEDC) else Color(0xFFF9DEDC) to Color(0xFF410E0B)
        else -> if (dark) Color(0xFF3A3A3A) to Color(0xFFE0E0E0) else Color(0xFFE0E0E0) to Color(0xFF1B1B1B)
    }
}
