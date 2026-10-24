// Package domain holds the rules that do not depend on any infrastructure:
// roles and permissions, the tent state machine, and input validation.
package domain

type Role string

const (
	Admin    Role = "admin"
	Office   Role = "office"    // دفتر آمرین
	GateLead Role = "gate_lead" // مسئول ارشد گیت
	Server   Role = "server"    // خادم گیت
	Team     Role = "team"      // تیم تخصصی چادر
	Workshop Role = "workshop"  // کارگاه شست‌وشو
	Service  Role = "service"   // نیروی رسمی خدمات
)

func (r Role) Valid() bool {
	switch r {
	case Admin, Office, GateLead, Server, Team, Workshop, Service:
		return true
	}
	return false
}

// NeedsShift reports whether the role must pick a place (and door) before
// doing operational work. Admin and office work without a shift.
func (r Role) NeedsShift() bool {
	switch r {
	case GateLead, Server, Team, Workshop, Service:
		return true
	}
	return false
}

type Perm string

const (
	PermPilgrimWrite  Perm = "pilgrim.write"
	PermPilgrimRead   Perm = "pilgrim.read"
	PermAssign        Perm = "assignment.create"
	PermReturn        Perm = "assignment.return"
	PermCancelOwn     Perm = "assignment.cancel_own"
	PermCancelAny     Perm = "assignment.cancel_any"
	PermTentRead      Perm = "tent.read"
	PermTentStatus    Perm = "tent.status"
	PermTentMove      Perm = "tent.move"
	PermTentManage    Perm = "tent.manage"
	PermSocks         Perm = "socks.write"
	PermSocksSupply   Perm = "socks.supply"
	PermHandover      Perm = "handover.write"
	PermDashboardOwn  Perm = "dashboard.own"
	PermDashboardAll  Perm = "dashboard.all"
	PermReports       Perm = "reports.read"
	PermUsers         Perm = "users.manage"
	PermPlaces        Perm = "places.manage"
	PermSettings      Perm = "settings.manage"
	PermAudit         Perm = "audit.read"
	PermRetireRequest Perm = "retire.request"
	PermRetireDecide  Perm = "retire.decide"
	PermSync          Perm = "sync"
)

var rolePerms = map[Role]map[Perm]bool{
	Server: set(PermPilgrimWrite, PermPilgrimRead, PermAssign, PermReturn, PermCancelOwn, PermTentRead,
		PermTentStatus, PermSocks, PermDashboardOwn, PermSync),
	GateLead: set(PermPilgrimWrite, PermPilgrimRead, PermAssign, PermReturn, PermCancelOwn, PermCancelAny,
		PermTentRead, PermTentStatus, PermTentMove, PermSocks, PermHandover, PermDashboardOwn, PermRetireRequest, PermSync),
	Team:     set(PermReturn, PermTentRead, PermTentStatus, PermTentMove, PermDashboardOwn, PermRetireRequest, PermSync),
	Workshop: set(PermTentRead, PermTentStatus, PermTentMove, PermDashboardOwn, PermSync),
	Service:  set(PermTentRead, PermTentMove, PermDashboardOwn, PermSync),
	Office: set(PermPilgrimRead, PermTentRead, PermTentStatus, PermTentMove, PermSocksSupply, PermDashboardAll,
		PermReports, PermRetireRequest, PermRetireDecide, PermCancelAny),
	Admin: set(PermPilgrimRead, PermTentRead, PermTentStatus, PermTentMove, PermTentManage, PermSocksSupply,
		PermDashboardAll, PermReports, PermUsers, PermPlaces, PermSettings, PermAudit, PermRetireRequest, PermRetireDecide, PermCancelAny),
}

func set(p ...Perm) map[Perm]bool {
	m := make(map[Perm]bool, len(p))
	for _, x := range p {
		m[x] = true
	}
	return m
}

func Can(r Role, p Perm) bool { return rolePerms[r][p] }

type PlaceKind string

const (
	PlaceGate     PlaceKind = "gate"
	PlaceStorage  PlaceKind = "storage" // اتاق امانات
	PlaceWorkshop PlaceKind = "workshop"
	PlaceRepair   PlaceKind = "repair"
)

func (k PlaceKind) Valid() bool {
	switch k {
	case PlaceGate, PlaceStorage, PlaceWorkshop, PlaceRepair:
		return true
	}
	return false
}

// PlaceError says why a role cannot start a shift at a kind of place.
type PlaceError struct {
	Role Role
	Kind PlaceKind
}

func (e *PlaceError) Error() string {
	return "role " + string(e.Role) + " cannot work at " + string(e.Kind)
}

// RoleMayWorkAt limits where each role can start a shift, so a workshop worker
// cannot record gate operations by accident (or on purpose).
func RoleMayWorkAt(r Role, k PlaceKind) error {
	ok := false
	switch r {
	case Server, GateLead:
		ok = k == PlaceGate || k == PlaceStorage
	case Team:
		ok = k == PlaceGate || k == PlaceStorage || k == PlaceRepair
	case Workshop:
		ok = k == PlaceWorkshop
	case Service:
		ok = true
	}
	if !ok {
		return &PlaceError{r, k}
	}
	return nil
}
