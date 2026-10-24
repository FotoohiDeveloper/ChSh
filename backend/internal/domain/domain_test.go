package domain

import (
	"testing"
	"time"
)

func TestValidNationalID(t *testing.T) {
	good := []string{"0084575948", "0499370899", "0013542419"}
	bad := []string{"0084575949", "1111111111", "123", "00845759480", "00845a5948", ""}
	for _, g := range good {
		if !ValidNationalID(g) {
			t.Errorf("%s should be valid", g)
		}
	}
	for _, b := range bad {
		if ValidNationalID(b) {
			t.Errorf("%q should be invalid", b)
		}
	}
}

func TestNormalizeDigitsAndMobile(t *testing.T) {
	if got := NormalizeDigits("۰۹۱۲ ۳۴۵۶۷۸۹"); got != "09123456789" {
		t.Fatalf("got %q", got)
	}
	for _, in := range []string{"09123456789", "9123456789", "+989123456789", "00989123456789", "۰۹۱۲۳۴۵۶۷۸۹", "989123456789"} {
		got, err := NormalizeMobile(in)
		if err != nil || got != "09123456789" {
			t.Errorf("%q -> %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"0812345678", "0912345", "abc", "", "091234567890"} {
		if _, err := NormalizeMobile(in); err == nil {
			t.Errorf("%q should be rejected", in)
		}
	}
}

func TestJalali(t *testing.T) {
	cases := []struct {
		j [3]int
		g [3]int
	}{
		{[3]int{1370, 5, 12}, [3]int{1991, 8, 3}},
		{[3]int{1403, 1, 1}, [3]int{2024, 3, 20}},
		{[3]int{1399, 12, 30}, [3]int{2021, 3, 20}}, // leap year last day
		{[3]int{1404, 1, 1}, [3]int{2025, 3, 21}},
		{[3]int{1300, 1, 1}, [3]int{1921, 3, 21}},
		{[3]int{1385, 7, 1}, [3]int{2006, 9, 23}},
	}
	for _, c := range cases {
		y, m, d := JalaliToGregorian(c.j[0], c.j[1], c.j[2])
		if [3]int{y, m, d} != c.g {
			t.Errorf("%v -> %d-%d-%d, want %v", c.j, y, m, d, c.g)
		}
	}
}

func TestParseBirthDate(t *testing.T) {
	for _, in := range []string{"1370/05/12", "۱۳۷۰-۰۵-۱۲", "1991-08-03"} {
		got, err := ParseBirthDate(in)
		if err != nil || got != time.Date(1991, 8, 3, 0, 0, 0, 0, time.UTC) {
			t.Errorf("%q -> %v, %v", in, got, err)
		}
	}
	for _, in := range []string{"1370/13/01", "1370/07/31", "2999-01-01", "abcd", "1991-02-30", "1371/12/30"} {
		if _, err := ParseBirthDate(in); err == nil {
			t.Errorf("%q should be rejected", in)
		}
	}
}

func TestTentCode(t *testing.T) {
	if c, ok := NormalizeTentCode(" ch-۰۰۰۱۲۳ "); !ok || c != "CH-000123" {
		t.Fatalf("got %q %v", c, ok)
	}
	for _, bad := range []string{"CH-12", "XX-000123", "CH-0001234", "'; drop table tents;--"} {
		if _, ok := NormalizeTentCode(bad); ok {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestTransitions(t *testing.T) {
	type tc struct {
		role     Role
		from, to Status
		ok       bool
	}
	cases := []tc{
		{Workshop, Dirty, InWash, true},
		{Server, Dirty, InWash, false},
		{Workshop, InWash, PendingReview, true},
		{Team, PendingReview, Ready, true},
		{Workshop, PendingReview, Ready, false}, // washer cannot approve their own work
		{Server, InWash, Ready, false},          // no shortcut around review
		{Server, Ready, Dirty, true},
		{Team, Repair, PendingReview, true},
		{Office, Ready, Retired, true},
		{Server, Ready, Retired, false},
		{Admin, Retired, Ready, true},
		{Office, Retired, Ready, false},
		{Admin, WithPilgrim, Ready, false}, // only through assignment operations
		{Admin, Ready, WithPilgrim, false},
		{Team, Ready, Ready, false},
	}
	for _, c := range cases {
		err := CheckTransition(c.role, c.from, c.to)
		if (err == nil) != c.ok {
			t.Errorf("%s %s->%s: err=%v want ok=%v", c.role, c.from, c.to, err, c.ok)
		}
	}
}

func TestPermissions(t *testing.T) {
	if Can(Server, PermUsers) || Can(Server, PermCancelAny) || Can(Workshop, PermAssign) {
		t.Fatal("least privilege violated")
	}
	if !Can(GateLead, PermCancelAny) || !Can(Admin, PermUsers) {
		t.Fatal("expected permissions missing")
	}
	// only the admin manages users, tents and settings
	for _, r := range []Role{Office, GateLead, Server, Team, Workshop, Service} {
		for _, p := range []Perm{PermUsers, PermTentManage, PermSettings, PermAudit, PermPlaces} {
			if Can(r, p) {
				t.Errorf("%s must not have %s", r, p)
			}
		}
	}
}

func TestRoleMayWorkAt(t *testing.T) {
	if RoleMayWorkAt(Workshop, PlaceGate) == nil || RoleMayWorkAt(Server, PlaceWorkshop) == nil {
		t.Fatal("role/place restriction missing")
	}
	if RoleMayWorkAt(Service, PlaceWorkshop) != nil || RoleMayWorkAt(Server, PlaceGate) != nil {
		t.Fatal("legit assignment rejected")
	}
}
