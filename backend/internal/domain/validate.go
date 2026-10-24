package domain

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// NormalizeDigits maps Persian (۰-۹) and Arabic-Indic (٠-٩) digits to ASCII and
// strips whitespace and zero-width characters that keyboards insert.
func NormalizeDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '۰' && r <= '۹':
			b.WriteRune('0' + (r - '۰'))
		case r >= '٠' && r <= '٩':
			b.WriteRune('0' + (r - '٠'))
		case unicode.IsSpace(r), r == '‌', r == '‏', r == '‎':
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ValidNationalID implements the Iranian national code check digit.
func ValidNationalID(id string) bool {
	if len(id) != 10 {
		return false
	}
	allSame := true
	sum := 0
	for i := 0; i < 10; i++ {
		if id[i] < '0' || id[i] > '9' {
			return false
		}
		if id[i] != id[0] {
			allSame = false
		}
	}
	if allSame {
		return false
	}
	for i := 0; i < 9; i++ {
		sum += int(id[i]-'0') * (10 - i)
	}
	r := sum % 11
	check := int(id[9] - '0')
	if r < 2 {
		return check == r
	}
	return check == 11-r
}

var mobileRe = regexp.MustCompile(`^09\d{9}$`)

// NormalizeMobile accepts 09xxxxxxxxx, 9xxxxxxxxx, +989xxxxxxxxx, 00989xxxxxxxxx.
func NormalizeMobile(m string) (string, error) {
	m = NormalizeDigits(m)
	switch {
	case strings.HasPrefix(m, "+98"):
		m = "0" + m[3:]
	case strings.HasPrefix(m, "0098"):
		m = "0" + m[4:]
	case strings.HasPrefix(m, "98") && len(m) == 12:
		m = "0" + m[2:]
	case strings.HasPrefix(m, "9") && len(m) == 10:
		m = "0" + m
	}
	if !mobileRe.MatchString(m) {
		return "", errors.New("invalid mobile number")
	}
	return m, nil
}

// ParseBirthDate accepts YYYY-MM-DD or YYYY/MM/DD; a year in 1200..1499 is
// read as a Jalali date and converted.
func ParseBirthDate(s string) (time.Time, error) {
	s = strings.ReplaceAll(NormalizeDigits(s), "/", "-")
	parts := strings.Split(s, "-")
	if len(parts) != 3 {
		return time.Time{}, errors.New("invalid birth date")
	}
	y, e1 := strconv.Atoi(parts[0])
	m, e2 := strconv.Atoi(parts[1])
	d, e3 := strconv.Atoi(parts[2])
	if e1 != nil || e2 != nil || e3 != nil || m < 1 || m > 12 || d < 1 || d > 31 {
		return time.Time{}, errors.New("invalid birth date")
	}
	if y >= 1200 && y < 1500 {
		if d > JalaliMonthLength(y, m) {
			return time.Time{}, errors.New("invalid birth date")
		}
		y, m, d = JalaliToGregorian(y, m, d)
	}
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	if t.Year() != y || int(t.Month()) != m || t.Day() != d {
		return time.Time{}, errors.New("invalid birth date")
	}
	if t.After(time.Now()) || t.Year() < 1900 {
		return time.Time{}, errors.New("birth date out of range")
	}
	return t, nil
}

// MaskNationalID shows only the first three and last three digits.
func MaskNationalID(id string) string {
	if len(id) != 10 {
		return "***"
	}
	return id[:3] + "****" + id[7:]
}

// MaskMobile shows 0912***4567.
func MaskMobile(m string) string {
	if len(m) != 11 {
		return "***"
	}
	return m[:4] + "***" + m[7:]
}

var tentCodeRe = regexp.MustCompile(`^CH-\d{6}$`)

// NormalizeTentCode uppercases and fixes digits so hand-typed codes work.
func NormalizeTentCode(c string) (string, bool) {
	c = strings.ToUpper(NormalizeDigits(c))
	return c, tentCodeRe.MatchString(c)
}

// ---- Jalali (Persian) calendar ----

func JalaliMonthLength(y, m int) int {
	switch {
	case m <= 6:
		return 31
	case m <= 11:
		return 30
	}
	if isJalaliLeap(y) {
		return 30
	}
	return 29
}

func isJalaliLeap(y int) bool {
	// 33-year arithmetic approximation; exact for the range used by this system.
	r := y % 33
	switch r {
	case 1, 5, 9, 13, 17, 22, 26, 30:
		return true
	}
	return false
}

// JalaliToGregorian converts using the well-known jalaali algorithm.
func JalaliToGregorian(jy, jm, jd int) (int, int, int) {
	jy += 1595
	days := -355668 + 365*jy + (jy/33)*8 + ((jy%33)+3)/4 + jd
	if jm < 7 {
		days += (jm - 1) * 31
	} else {
		days += (jm-7)*30 + 186
	}
	gy := 400 * (days / 146097)
	days %= 146097
	if days > 36524 {
		days--
		gy += 100 * (days / 36524)
		days %= 36524
		if days >= 365 {
			days++
		}
	}
	gy += 4 * (days / 1461)
	days %= 1461
	if days > 365 {
		gy += (days - 1) / 365
		days = (days - 1) % 365
	}
	gd := days + 1
	monthDays := []int{0, 31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	if (gy%4 == 0 && gy%100 != 0) || gy%400 == 0 {
		monthDays[2] = 29
	}
	gm := 1
	for gm <= 12 && gd > monthDays[gm] {
		gd -= monthDays[gm]
		gm++
	}
	return gy, gm, gd
}
