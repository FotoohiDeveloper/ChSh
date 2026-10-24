package ir.chador.core

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotNull
import kotlin.test.assertNull
import kotlin.test.assertTrue

class DomainTest {
    @Test fun nationalId() {
        listOf("0084575948", "0499370899", "0013542419", "۰۰۸۴۵۷۵۹۴۸").forEach { assertTrue(Validators.validNationalId(it), it) }
        listOf("0084575949", "1111111111", "123", "00845759480", "00845a5948", "").forEach { assertFalse(Validators.validNationalId(it), it) }
    }

    @Test fun mobile() {
        listOf("09123456789", "9123456789", "+989123456789", "00989123456789", "۰۹۱۲۳۴۵۶۷۸۹", "989123456789")
            .forEach { assertEquals("09123456789", Validators.mobile(it), it) }
        listOf("0812345678", "0912345", "abc", "", "091234567890").forEach { assertNull(Validators.mobile(it), it) }
    }

    @Test fun birthDate() {
        assertEquals("1370/05/12", Validators.birthDate("۱۳۷۰/۵/۱۲"))
        assertEquals("1370/05/12", Validators.birthDate("1370-05-12"))
        assertEquals("1991/08/03", Validators.birthDate("1991-08-03"))
        listOf("1370/13/01", "1370/07/31", "1371/12/30", "2999-01-01", "abc", "1991-02-30", "13/70/05").forEach { assertNull(Validators.birthDate(it), it) }
        assertNotNull(Validators.birthDate("1370/12/30")) // 1370 is a leap year
    }

    @Test fun tentCode() {
        assertEquals("CH-000123", TentCode.normalize(" ch-۰۰۰۱۲۳ "))
        listOf("CH-12", "XX-000123", "CH-0001234", "'; drop table tents;--").forEach { assertNull(TentCode.normalize(it), it) }
    }

    @Test fun jalaliConversion() {
        assertEquals(Triple(1403, 1, 1), Jalali.fromGregorian(2024, 3, 20))
        assertEquals(Triple(1370, 5, 12), Jalali.fromGregorian(1991, 8, 3))
        assertEquals(Triple(1399, 12, 30), Jalali.fromGregorian(2021, 3, 20))
        assertEquals(Triple(1404, 1, 1), Jalali.fromGregorian(2025, 3, 21))
        assertEquals(Triple(1405, 8, 2), Jalali.fromGregorian(2026, 10, 24))
    }

    @Test fun timestamps() {
        val t = Time.parse("2026-10-24T10:03:12.345678Z")!!
        assertEquals("2026-10-24T10:03:12.345Z", Time.format(t))
        assertEquals(t, Time.parse("2026-10-24T13:33:12.345+03:30"))
        assertEquals(t, Time.parse("2026-10-24T08:03:12.345-02:00"))
        assertNull(Time.parse("yesterday"))
        // Tehran is UTC+3:30 (no daylight saving since 2022); shown with Persian digits
        assertEquals("۱۴۰۵/۰۸/۰۲ ۱۳:۳۳", Jalali.display("2026-10-24T10:03:12Z"))
        assertEquals("2026-10-24", Time.tehranDate(t))
        assertEquals("2026-10-25", Time.tehranDate(Time.parse("2026-10-24T21:00:00Z")!!)) // 00:30 next day in Tehran
    }

    @Test fun formattingIgnoresDeviceLocale() {
        val old = java.util.Locale.getDefault()
        try {
            java.util.Locale.setDefault(java.util.Locale.forLanguageTag("fa-IR"))
            assertEquals("1370/05/12", Validators.birthDate("1370/5/12"))
            assertTrue(Time.format(0).startsWith("1970-01-01T00:00:00"))
        } finally {
            java.util.Locale.setDefault(old)
        }
    }

    @Test fun transitionsMatchTheServerTable() {
        assertTrue(Transitions.allowed(Role.WORKSHOP, TentStatus.DIRTY, TentStatus.IN_WASH))
        assertFalse(Transitions.allowed(Role.SERVER, TentStatus.DIRTY, TentStatus.IN_WASH))
        assertFalse(Transitions.allowed(Role.WORKSHOP, TentStatus.PENDING_REVIEW, TentStatus.READY)) // a washer cannot approve own work
        assertTrue(Transitions.allowed(Role.TEAM, TentStatus.PENDING_REVIEW, TentStatus.READY))
        assertFalse(Transitions.allowed(Role.SERVER, TentStatus.IN_WASH, TentStatus.READY))
        assertTrue(Transitions.allowed(Role.OFFICE, TentStatus.READY, TentStatus.RETIRED))
        assertFalse(Transitions.allowed(Role.OFFICE, TentStatus.RETIRED, TentStatus.READY))
        assertTrue(Transitions.allowed(Role.ADMIN, TentStatus.RETIRED, TentStatus.READY))
        assertFalse(Transitions.allowed(Role.ADMIN, TentStatus.WITH_PILGRIM, TentStatus.READY))
        assertEquals(listOf(TentStatus.READY, TentStatus.DIRTY, TentStatus.REPAIR), Transitions.targets(Role.TEAM, TentStatus.PENDING_REVIEW))
    }

    @Test fun roles() {
        assertEquals(Role.GATE_LEAD, Role.from("gate_lead"))
        assertEquals(Role.UNKNOWN, Role.from("hacker"))
        assertTrue(Role.SERVER.needsShift && !Role.ADMIN.needsShift && !Role.OFFICE.needsShift)
        assertFalse(Role.SERVER.canDecideRetire)
    }
}
