package ir.chador.core

import java.util.Calendar
import java.util.GregorianCalendar
import java.util.Locale
import java.util.TimeZone

/** String.format that never switches to Persian/Arabic digits on devices with a Persian locale. */
internal fun String.fmt(vararg args: Any?): String = String.format(Locale.ROOT, this, *args)

enum class Role(val wire: String) {
    ADMIN("admin"), OFFICE("office"), GATE_LEAD("gate_lead"), SERVER("server"),
    TEAM("team"), WORKSHOP("workshop"), SERVICE("service"), UNKNOWN("?");

    /** Roles that must pick a place (and door) before doing operational work. */
    val needsShift: Boolean get() = this in setOf(GATE_LEAD, SERVER, TEAM, WORKSHOP, SERVICE)

    val canHandOut: Boolean get() = this == SERVER || this == GATE_LEAD
    val canReturn: Boolean get() = this == SERVER || this == GATE_LEAD || this == TEAM
    val canCancelAny: Boolean get() = this == GATE_LEAD || this == OFFICE || this == ADMIN
    val canCancelOwn: Boolean get() = this == SERVER || canCancelAny
    val canRequestRetire: Boolean get() = this == GATE_LEAD || this == TEAM || this == OFFICE || this == ADMIN
    val canDecideRetire: Boolean get() = this == OFFICE || this == ADMIN
    val canMove: Boolean get() = this in setOf(GATE_LEAD, TEAM, WORKSHOP, SERVICE, OFFICE, ADMIN)
    val seesDashboard: Boolean get() = this in setOf(GATE_LEAD, TEAM, WORKSHOP, SERVICE, SERVER, OFFICE, ADMIN)
    val seesAllPlaces: Boolean get() = this == OFFICE || this == ADMIN
    val canHandover: Boolean get() = this == GATE_LEAD
    val canSupplySocks: Boolean get() = this == OFFICE || this == ADMIN
    val canReadReports: Boolean get() = this == OFFICE || this == ADMIN
    val isAdmin: Boolean get() = this == ADMIN

    companion object {
        fun from(s: String): Role = entries.firstOrNull { it.wire == s } ?: UNKNOWN
    }
}

enum class TentStatus(val wire: String) {
    READY("ready"), WITH_PILGRIM("with_pilgrim"), DIRTY("dirty"), IN_WASH("in_wash"),
    PENDING_REVIEW("pending_review"), REPAIR("repair"), RETIRED("retired"), UNKNOWN("?");

    companion object {
        fun from(s: String): TentStatus = entries.firstOrNull { it.wire == s } ?: UNKNOWN
    }
}

/**
 * Mirror of the server's generic status-change table, used only to decide which buttons to show.
 * The server is the authority and re-checks every change.
 */
object Transitions {
    private val table: Map<Pair<TentStatus, TentStatus>, Set<Role>> = mapOf(
        (TentStatus.READY to TentStatus.DIRTY) to setOf(Role.SERVER, Role.GATE_LEAD, Role.TEAM),
        (TentStatus.READY to TentStatus.REPAIR) to setOf(Role.SERVER, Role.GATE_LEAD, Role.TEAM),
        (TentStatus.DIRTY to TentStatus.IN_WASH) to setOf(Role.WORKSHOP),
        (TentStatus.DIRTY to TentStatus.REPAIR) to setOf(Role.TEAM),
        (TentStatus.IN_WASH to TentStatus.PENDING_REVIEW) to setOf(Role.WORKSHOP),
        (TentStatus.PENDING_REVIEW to TentStatus.READY) to setOf(Role.TEAM, Role.GATE_LEAD),
        (TentStatus.PENDING_REVIEW to TentStatus.DIRTY) to setOf(Role.TEAM, Role.GATE_LEAD),
        (TentStatus.PENDING_REVIEW to TentStatus.REPAIR) to setOf(Role.TEAM),
        (TentStatus.REPAIR to TentStatus.PENDING_REVIEW) to setOf(Role.TEAM),
        (TentStatus.RETIRED to TentStatus.READY) to setOf(Role.ADMIN),
    )
    private val officeTargets = setOf(TentStatus.RETIRED, TentStatus.READY, TentStatus.DIRTY, TentStatus.REPAIR)

    fun allowed(role: Role, from: TentStatus, to: TentStatus): Boolean {
        if (from == to || from == TentStatus.WITH_PILGRIM || to == TentStatus.WITH_PILGRIM || from == TentStatus.UNKNOWN) return false
        if ((role == Role.ADMIN || role == Role.OFFICE) && to in officeTargets && !(role == Role.OFFICE && from == TentStatus.RETIRED)) return true
        return table[Pair(from, to)]?.contains(role) == true
    }

    fun targets(role: Role, from: TentStatus): List<TentStatus> =
        TentStatus.entries.filter { it != TentStatus.UNKNOWN && allowed(role, from, it) }
}

object Validators {
    /** Persian/Arabic digits to ASCII; whitespace and zero-width characters removed. */
    fun normalizeDigits(s: String): String {
        val sb = StringBuilder(s.length)
        for (c in s) {
            when {
                c in '۰'..'۹' -> sb.append('0' + (c - '۰'))
                c in '٠'..'٩' -> sb.append('0' + (c - '٠'))
                c.isWhitespace() || c == '‌' || c == '‏' || c == '‎' -> {}
                else -> sb.append(c)
            }
        }
        return sb.toString()
    }

    /** Iranian national code check digit. */
    fun validNationalId(raw: String): Boolean {
        val id = normalizeDigits(raw)
        if (id.length != 10 || id.any { it !in '0'..'9' } || id.all { it == id[0] }) return false
        var sum = 0
        for (i in 0 until 9) sum += (id[i] - '0') * (10 - i)
        val r = sum % 11
        val check = id[9] - '0'
        return if (r < 2) check == r else check == 11 - r
    }

    /** Returns 09xxxxxxxxx or null. */
    fun mobile(raw: String): String? {
        var m = normalizeDigits(raw)
        m = when {
            m.startsWith("+98") -> "0" + m.substring(3)
            m.startsWith("0098") -> "0" + m.substring(4)
            m.startsWith("98") && m.length == 12 -> "0" + m.substring(2)
            m.startsWith("9") && m.length == 10 -> "0$m"
            else -> m
        }
        return if (Regex("^09\\d{9}$").matches(m)) m else null
    }

    /**
     * Accepts 1370/05/12, 1370-5-12 or 1991-08-03 (digits in any script). Returns the date as
     * YYYY/MM/DD, or null. A year in 1200..1499 is read as Jalali, 1900.. as Gregorian.
     */
    fun birthDate(raw: String): String? {
        val parts = normalizeDigits(raw).replace('/', '-').split('-')
        if (parts.size != 3 || parts.any { it.isEmpty() || it.any { c -> c !in '0'..'9' } }) return null
        val y = parts[0].toIntOrNull() ?: return null
        val m = parts[1].toIntOrNull() ?: return null
        val d = parts[2].toIntOrNull() ?: return null
        if (m !in 1..12 || d !in 1..31) return null
        val ok = when (y) {
            in 1200..1499 -> d <= Jalali.monthLength(y, m)
            in 1900..2200 -> d <= gregorianMonthLength(y, m)
            else -> false
        }
        if (!ok) return null
        return "%04d/%02d/%02d".fmt(y, m, d)
    }

    private fun gregorianMonthLength(y: Int, m: Int): Int = when (m) {
        1, 3, 5, 7, 8, 10, 12 -> 31
        4, 6, 9, 11 -> 30
        else -> if ((y % 4 == 0 && y % 100 != 0) || y % 400 == 0) 29 else 28
    }
}

object TentCode {
    private val re = Regex("^CH-\\d{6}$")

    /** Normalised code (CH-000123) or null. Accepts lowercase, Persian digits, stray spaces. */
    fun normalize(raw: String): String? {
        val c = Validators.normalizeDigits(raw).uppercase()
        return if (re.matches(c)) c else null
    }
}

/** Persian (Jalali) calendar helpers for display. */
object Jalali {
    fun monthLength(y: Int, m: Int): Int = when {
        m <= 6 -> 31
        m <= 11 -> 30
        else -> if (isLeap(y)) 30 else 29
    }

    private fun isLeap(y: Int): Boolean = (y % 33) in setOf(1, 5, 9, 13, 17, 22, 26, 30)

    /** Gregorian to Jalali (Pournader's algorithm; correct for 1178..1633 AP). */
    fun fromGregorian(gy: Int, gm: Int, gd: Int): Triple<Int, Int, Int> {
        val gDays = intArrayOf(31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31)
        val jDays = intArrayOf(31, 31, 31, 31, 31, 31, 30, 30, 30, 30, 30, 29)
        val gy2 = gy - 1600
        val gm2 = gm - 1
        val gd2 = gd - 1
        var gDayNo = 365 * gy2 + (gy2 + 3) / 4 - (gy2 + 99) / 100 + (gy2 + 399) / 400
        for (i in 0 until gm2) gDayNo += gDays[i]
        if (gm2 > 1 && ((gy % 4 == 0 && gy % 100 != 0) || gy % 400 == 0)) gDayNo++
        gDayNo += gd2
        var jDayNo = gDayNo - 79
        val jNp = jDayNo / 12053
        jDayNo %= 12053
        var jy = 979 + 33 * jNp + 4 * (jDayNo / 1461)
        jDayNo %= 1461
        if (jDayNo >= 366) {
            jy += (jDayNo - 1) / 365
            jDayNo = (jDayNo - 1) % 365
        }
        var i = 0
        while (i < 11 && jDayNo >= jDays[i]) {
            jDayNo -= jDays[i]
            i++
        }
        return Triple(jy, i + 1, jDayNo + 1)
    }

    private val persianDigits = "۰۱۲۳۴۵۶۷۸۹"
    fun persianDigits(s: String): String = buildString { for (c in s) append(if (c in '0'..'9') persianDigits[c - '0'] else c) }

    private val tehran: TimeZone = TimeZone.getTimeZone("Asia/Tehran")

    /** "1403/01/01" for an instant, in Tehran time. */
    fun date(epochMillis: Long): String {
        val cal = GregorianCalendar(tehran).apply { timeInMillis = epochMillis }
        val (y, m, d) = fromGregorian(cal.get(Calendar.YEAR), cal.get(Calendar.MONTH) + 1, cal.get(Calendar.DAY_OF_MONTH))
        return "%04d/%02d/%02d".fmt(y, m, d)
    }

    fun dateTime(epochMillis: Long): String {
        val cal = GregorianCalendar(tehran).apply { timeInMillis = epochMillis }
        return date(epochMillis) + " " + "%02d:%02d".fmt(cal.get(Calendar.HOUR_OF_DAY), cal.get(Calendar.MINUTE))
    }

    /** Server timestamp (RFC 3339, e.g. 2026-10-24T10:03:12.345678Z) to "۱۴۰۵/۰۸/۰۳ ۱۳:۳۳" in Tehran time. */
    fun display(rfc3339: String?): String {
        val ms = Time.parse(rfc3339) ?: return rfc3339 ?: ""
        return persianDigits(dateTime(ms))
    }
}

object Time {
    /** Parses RFC 3339 with optional fraction and either Z or +hh:mm. Returns epoch millis or null. */
    fun parse(s: String?): Long? {
        if (s.isNullOrBlank()) return null
        val m = Regex("^(\\d{4})-(\\d{2})-(\\d{2})[T ](\\d{2}):(\\d{2}):(\\d{2})(?:\\.(\\d+))?(Z|[+-]\\d{2}:?\\d{2})?$").matchEntire(s.trim()) ?: return null
        val g = m.groupValues
        val cal = GregorianCalendar(TimeZone.getTimeZone("UTC"))
        cal.clear()
        cal.set(g[1].toInt(), g[2].toInt() - 1, g[3].toInt(), g[4].toInt(), g[5].toInt(), g[6].toInt())
        var ms = cal.timeInMillis
        if (g[7].isNotEmpty()) ms += (g[7].padEnd(3, '0').substring(0, 3)).toLong()
        val tz = g[8]
        if (tz.isNotEmpty() && tz != "Z") {
            val sign = if (tz[0] == '-') -1 else 1
            val digits = tz.substring(1).replace(":", "")
            val offset = (digits.substring(0, 2).toLong() * 60 + digits.substring(2, 4).toLong()) * 60_000
            ms -= sign * offset
        }
        return ms
    }

    /** RFC 3339 UTC with millisecond precision. */
    fun format(epochMillis: Long): String {
        val cal = GregorianCalendar(TimeZone.getTimeZone("UTC")).apply { timeInMillis = epochMillis }
        return "%04d-%02d-%02dT%02d:%02d:%02d.%03dZ".fmt(
            cal.get(Calendar.YEAR), cal.get(Calendar.MONTH) + 1, cal.get(Calendar.DAY_OF_MONTH),
            cal.get(Calendar.HOUR_OF_DAY), cal.get(Calendar.MINUTE), cal.get(Calendar.SECOND), cal.get(Calendar.MILLISECOND),
        )
    }

    /** Gregorian YYYY-MM-DD of an instant in Tehran time (what the report endpoint expects). */
    fun tehranDate(epochMillis: Long): String {
        val cal = GregorianCalendar(TimeZone.getTimeZone("Asia/Tehran")).apply { timeInMillis = epochMillis }
        return "%04d-%02d-%02d".fmt(cal.get(Calendar.YEAR), cal.get(Calendar.MONTH) + 1, cal.get(Calendar.DAY_OF_MONTH))
    }
}

/** Persian labels shown to users. Kept in core so presenters can build messages without Android resources. */
object Labels {
    fun status(s: TentStatus): String = when (s) {
        TentStatus.READY -> "آماده"
        TentStatus.WITH_PILGRIM -> "نزد زائر"
        TentStatus.DIRTY -> "کثیف"
        TentStatus.IN_WASH -> "در کارگاه"
        TentStatus.PENDING_REVIEW -> "در انتظار بررسی"
        TentStatus.REPAIR -> "تعمیر"
        TentStatus.RETIRED -> "خارج از چرخه"
        TentStatus.UNKNOWN -> "نامشخص"
    }

    fun status(wire: String): String = status(TentStatus.from(wire))

    fun role(r: Role): String = when (r) {
        Role.ADMIN -> "مدیر سامانه"
        Role.OFFICE -> "دفتر آمرین"
        Role.GATE_LEAD -> "مسئول گیت"
        Role.SERVER -> "خادم گیت"
        Role.TEAM -> "تیم تخصصی چادر"
        Role.WORKSHOP -> "کارگاه شست‌وشو"
        Role.SERVICE -> "نیروی خدمات"
        Role.UNKNOWN -> "نامشخص"
    }

    fun placeKind(kind: String): String = when (kind) {
        "gate" -> "گیت"
        "storage" -> "اتاق امانات"
        "workshop" -> "کارگاه"
        "repair" -> "تعمیرگاه"
        else -> kind
    }

    fun eventType(t: String): String = when (t) {
        "assigned" -> "تحویل به زائر"
        "returned" -> "بازگشت از زائر"
        "assignment_cancelled" -> "لغو تخصیص"
        "status" -> "تغییر وضعیت"
        "moved" -> "انتقال"
        "retired" -> "خروج از چرخه"
        "label_replaced" -> "تعویض برچسب"
        else -> t
    }

    fun retireReason(r: String): String = when (r) {
        "lost" -> "مفقود شده"
        "damaged" -> "آسیب‌دیده"
        else -> "سایر"
    }
}
