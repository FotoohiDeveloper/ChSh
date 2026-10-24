package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"chador/internal/domain"
	"chador/internal/service"
	"chador/internal/testenv"
)

var ctx = context.Background()

func code(i int) string { return "CH-" + strings.Repeat("0", 6-len(itoa(i))) + itoa(i) }
func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func asErr(t *testing.T, err error, wantCode string) {
	t.Helper()
	var e *service.Error
	if !errors.As(err, &e) {
		t.Fatalf("want service error %q, got %v", wantCode, err)
	}
	if e.Code != wantCode {
		t.Fatalf("want code %q, got %q (%s)", wantCode, e.Code, e.Message)
	}
}

func TestAssignReturnFullCycle(t *testing.T) {
	e := testenv.New(t)
	e.Tents(t, 1, 10)
	server := e.User(t, "s1", domain.Server, e.Gate, &e.Door)
	team := e.User(t, "team1", domain.Team, e.Store, nil)
	wash := e.User(t, "wash1", domain.Workshop, e.Wash, nil)
	p := e.Pilgrim(t, server, 1)
	if p.Verification != "verified" || p.NationalID == testenv.NID(1) {
		t.Fatalf("view must be verified and masked: %+v", p)
	}

	res, _, err := e.Svc.Assign(ctx, server, service.AssignReq{PilgrimID: p.ID, Codes: []string{code(1), "ch-000002"}, SocksQty: 2})
	if err != nil || len(res.Items) != 2 {
		t.Fatalf("assign: %v %+v", err, res)
	}
	tent, err := e.Svc.GetTent(ctx, server, code(1))
	if err != nil || tent.Status != domain.WithPilgrim || tent.Assignment == nil || tent.PlaceID != nil {
		t.Fatalf("tent after assign: %+v %v", tent, err)
	}
	// cannot hand out a tent that is already out
	_, _, err = e.Svc.Assign(ctx, server, service.AssignReq{PilgrimID: p.ID, Codes: []string{code(1), code(3)}})
	asErr(t, err, "tents_unavailable")
	if t3, _ := e.Svc.GetTent(ctx, server, code(3)); t3.Status != domain.Ready {
		t.Fatal("failed assign must not take any tent (all or nothing)")
	}

	// return one clean, one dirty
	rr, _, err := e.Svc.Return(ctx, server, service.ReturnReq{Items: []service.ReturnItem{
		{Code: code(1), Status: domain.Ready}, {Code: code(2), Status: domain.Dirty}, {Code: code(5)}}})
	if err != nil || rr.Returned != 2 || rr.Completed != 1 {
		t.Fatalf("return: %v %+v", err, rr)
	}
	for _, r := range rr.Results {
		if r.Code == code(5) && (r.OK || r.Error != "not_assigned") {
			t.Fatalf("unassigned scan must be reported, not fail the batch: %+v", r)
		}
	}
	if got := e.Queue.Templates(); len(got) != 2 || got[0] != "delivered" || got[1] != "returned" {
		t.Fatalf("sms jobs: %v", got)
	}

	// dirty -> washer -> review -> ready
	if _, _, err := e.Svc.SetStatus(ctx, server, service.StatusReq{Code: code(2), To: domain.InWash}); err == nil {
		t.Fatal("server must not send a tent to wash")
	}
	if _, _, err := e.Svc.SetStatus(ctx, wash, service.StatusReq{Code: code(2), To: domain.InWash}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Svc.SetStatus(ctx, wash, service.StatusReq{Code: code(2), To: domain.PendingReview}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Svc.SetStatus(ctx, wash, service.StatusReq{Code: code(2), To: domain.Ready}); err == nil {
		t.Fatal("washer must not approve own work")
	}
	if _, _, err := e.Svc.SetStatus(ctx, team, service.StatusReq{Code: code(2), To: domain.Ready}); err != nil {
		t.Fatal(err)
	}

	hist, err := e.Svc.TentHistory(ctx, server, code(2), 20)
	if err != nil || len(hist) != 5 { // assigned, returned, in_wash, pending_review, ready
		t.Fatalf("history: %d events, %v", len(hist), err)
	}
}

func TestOnlyOneWinnerWhenManyGatesGrabTheSameTent(t *testing.T) {
	e := testenv.New(t)
	e.Tents(t, 1, 1)
	server := e.User(t, "s1", domain.Server, e.Gate, &e.Door)
	var pilgrims []uuid.UUID
	for i := 0; i < 8; i++ {
		pilgrims = append(pilgrims, e.Pilgrim(t, server, i).ID)
	}
	var wins, conflicts int32
	var wg sync.WaitGroup
	for r := 0; r < 4; r++ {
		for _, pid := range pilgrims {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _, err := e.Svc.Assign(ctx, server, service.AssignReq{PilgrimID: pid, Codes: []string{code(1)}})
				var se *service.Error
				switch {
				case err == nil:
					atomic.AddInt32(&wins, 1)
				case errors.As(err, &se) && se.Code == "tents_unavailable":
					atomic.AddInt32(&conflicts, 1)
				default:
					t.Errorf("unexpected: %v", err)
				}
			}()
		}
	}
	wg.Wait()
	if wins != 1 || conflicts != 31 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
	var n int
	_ = e.Pool.QueryRow(ctx, `SELECT count(*) FROM assignments WHERE tent_id=1 AND returned_at IS NULL AND cancelled_at IS NULL`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d active assignments for one tent", n)
	}
}

func TestIdempotentReplay(t *testing.T) {
	e := testenv.New(t)
	e.Tents(t, 1, 5)
	s := e.User(t, "s1", domain.Server, e.Gate, &e.Door)
	p := e.Pilgrim(t, s, 1)
	op := uuid.New()
	req := service.AssignReq{PilgrimID: p.ID, Codes: []string{code(1), code(2)}, SocksQty: 1, OpID: &op}
	first, replayed, err := e.Svc.Assign(ctx, s, req)
	if err != nil || replayed {
		t.Fatal(err, replayed)
	}
	for i := 0; i < 3; i++ {
		again, replayed, err := e.Svc.Assign(ctx, s, req)
		if err != nil || !replayed {
			t.Fatalf("retry %d: replayed=%v err=%v", i, replayed, err)
		}
		if again.Items[0].AssignmentID != first.Items[0].AssignmentID {
			t.Fatal("replay must return the original result")
		}
	}
	var assignments, socks int
	_ = e.Pool.QueryRow(ctx, `SELECT count(*) FROM assignments`).Scan(&assignments)
	_ = e.Pool.QueryRow(ctx, `SELECT count(*) FROM socks_ledger`).Scan(&socks)
	if assignments != 2 || socks != 1 {
		t.Fatalf("replays created duplicates: %d assignments, %d socks rows", assignments, socks)
	}
	if got := len(e.Queue.SMSs); got != 1 {
		t.Fatalf("replay must not send another SMS, got %d", got)
	}
	// the same op id cannot be borrowed by another user or another kind of operation
	other := e.User(t, "s2", domain.Server, e.Gate, &e.Door)
	_, _, err = e.Svc.Assign(ctx, other, service.AssignReq{PilgrimID: p.ID, Codes: []string{code(3)}, OpID: &op})
	asErr(t, err, "op_id_reused")
}

func TestCancelWindowAndRoles(t *testing.T) {
	e := testenv.New(t)
	e.Tents(t, 1, 5)
	s := e.User(t, "s1", domain.Server, e.Gate, &e.Door)
	s2 := e.User(t, "s2", domain.Server, e.Gate, &e.Door)
	lead := e.User(t, "lead", domain.GateLead, e.Gate, &e.Door)
	p := e.Pilgrim(t, s, 1)
	_, _, _ = e.Svc.Assign(ctx, s, service.AssignReq{PilgrimID: p.ID, Codes: []string{code(1), code(2)}})

	if _, _, err := e.Svc.CancelAssignment(ctx, s2, code(1), nil); err == nil {
		t.Fatal("another server must not cancel")
	}
	if _, _, err := e.Svc.CancelAssignment(ctx, s, code(1), nil); err != nil {
		t.Fatalf("own undo inside window: %v", err)
	}
	if tt, _ := e.Svc.GetTent(ctx, s, code(1)); tt.Status != domain.Ready {
		t.Fatal("tent must be ready again")
	}
	// age the second assignment past the undo window
	_, _ = e.Pool.Exec(ctx, `UPDATE assignments SET assigned_at = now() - interval '10 minutes' WHERE cancelled_at IS NULL`)
	if _, _, err := e.Svc.CancelAssignment(ctx, s, code(2), nil); err == nil {
		t.Fatal("undo after the window must be refused for a plain server")
	}
	if _, _, err := e.Svc.CancelAssignment(ctx, lead, code(2), nil); err != nil {
		t.Fatalf("gate lead can cancel any time: %v", err)
	}
	// a cancelled hand-out leaves no active assignment, and the tent can go out again
	if _, _, err := e.Svc.Assign(ctx, s, service.AssignReq{PilgrimID: p.ID, Codes: []string{code(1)}}); err != nil {
		t.Fatal(err)
	}
}

func TestShiftAndPermissionGuards(t *testing.T) {
	e := testenv.New(t)
	e.Tents(t, 1, 3)
	wash := e.User(t, "w", domain.Workshop, e.Wash, nil)
	s := e.User(t, "s", domain.Server, e.Gate, &e.Door)
	p := e.Pilgrim(t, s, 1)

	if _, _, err := e.Svc.Assign(ctx, wash, service.AssignReq{PilgrimID: p.ID, Codes: []string{code(1)}}); err == nil {
		t.Fatal("workshop must not assign")
	}
	noShift := s
	noShift.ShiftID, noShift.PlaceID = nil, nil
	_, _, err := e.Svc.Assign(ctx, noShift, service.AssignReq{PilgrimID: p.ID, Codes: []string{code(1)}})
	asErr(t, err, "shift_required")
	if _, err := e.Svc.CreateUser(ctx, s, service.CreateUserReq{Username: "evil", FullName: "x", Role: domain.Admin}); err == nil {
		t.Fatal("server must not create users")
	}
	if _, err := e.Svc.StartShift(ctx, wash, e.Gate, nil); err == nil {
		t.Fatal("workshop must not start a shift at a gate")
	}
	// bad input never reaches SQL
	for _, bad := range []string{"CH-1", "'; DROP TABLE tents;--", "CH-00000X"} {
		if _, _, err := e.Svc.Assign(ctx, s, service.AssignReq{PilgrimID: p.ID, Codes: []string{bad}}); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestPersonalDataIsEncryptedAtRest(t *testing.T) {
	e := testenv.New(t)
	s := e.User(t, "s", domain.Server, e.Gate, &e.Door)
	p := e.Pilgrim(t, s, 2)
	nid, mobile := testenv.NID(2), "09120000002"
	var enc1, enc2, enc3 []byte
	_ = e.Pool.QueryRow(ctx, `SELECT nid_enc, birth_enc, mobile_enc FROM pilgrims WHERE id=$1`, p.ID).Scan(&enc1, &enc2, &enc3)
	for _, b := range [][]byte{enc1, enc2, enc3} {
		if len(b) == 0 || bytes.Contains(b, []byte(nid)) || bytes.Contains(b, []byte(mobile)) || bytes.Contains(b, []byte("1991")) {
			t.Fatal("personal data stored in clear")
		}
	}
	// the dump of the whole row must not contain the plaintext either
	var dump string
	_ = e.Pool.QueryRow(ctx, `SELECT p::text FROM pilgrims p WHERE id=$1`, p.ID).Scan(&dump)
	if strings.Contains(dump, nid) || strings.Contains(dump, mobile) {
		t.Fatal("plaintext in row dump")
	}
	// blind index lookups work
	found, err := e.Svc.FindPilgrims(ctx, s, nid, "")
	if err != nil || len(found) != 1 || found[0].ID != p.ID {
		t.Fatalf("lookup by national id: %v %v", found, err)
	}
	found, err = e.Svc.FindPilgrims(ctx, s, "", "+989120000002")
	if err != nil || len(found) != 1 {
		t.Fatalf("lookup by mobile: %v %v", found, err)
	}
	// the same person registered again is the same record
	again := e.Pilgrim(t, s, 2)
	if again.ID != p.ID || !again.Existing {
		t.Fatal("duplicate pilgrim record")
	}
}

func TestIdentityInquiryOutage(t *testing.T) {
	e := testenv.New(t)
	e.Tents(t, 1, 2)
	s := e.User(t, "s", domain.Server, e.Gate, &e.Door)
	e.Inq.SetDown(true)
	p := e.Pilgrim(t, s, 3)
	if p.Verification != "pending" {
		t.Fatalf("want pending, got %s", p.Verification)
	}
	_, _, err := e.Svc.Assign(ctx, s, service.AssignReq{PilgrimID: p.ID, Codes: []string{code(1)}})
	asErr(t, err, "pilgrim_unverified")
	e.Inq.SetDown(false)
	if err := e.Svc.VerifyPending(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Svc.Assign(ctx, s, service.AssignReq{PilgrimID: p.ID, Codes: []string{code(1)}}); err != nil {
		t.Fatalf("after retry job verified the pilgrim: %v", err)
	}
	// invalid ids are refused before any lookup
	_, err = e.Svc.UpsertPilgrim(ctx, s, service.PilgrimInput{NationalID: "1111111111", BirthDate: "1370/01/01", Mobile: "09120000000"})
	asErr(t, err, "bad_national_id")
}

func TestAppendOnlyTables(t *testing.T) {
	e := testenv.New(t)
	e.Tents(t, 1, 1)
	s := e.User(t, "s", domain.Server, e.Gate, &e.Door)
	p := e.Pilgrim(t, s, 1)
	_, _, _ = e.Svc.Assign(ctx, s, service.AssignReq{PilgrimID: p.ID, Codes: []string{code(1)}})
	e.Svc.AuditNow(ctx, nil, "", "test", "x", "1", nil)
	for _, q := range []string{
		`UPDATE tent_events SET type='x'`, `DELETE FROM tent_events`, `TRUNCATE tent_events`,
		`UPDATE audit_log SET action='x'`, `DELETE FROM audit_log`, `TRUNCATE audit_log`,
	} {
		if _, err := e.Pool.Exec(ctx, q); err == nil {
			t.Errorf("%q must be rejected", q)
		}
	}
	// state that contradicts itself is rejected by the schema too
	if _, err := e.Pool.Exec(ctx, `UPDATE tents SET status='with_pilgrim', place_id=1`); err == nil {
		t.Error("a tent with a pilgrim cannot also be at a place")
	}
	if _, err := e.Pool.Exec(ctx, `INSERT INTO assignments(pilgrim_id,tent_id,assigned_by) SELECT $1, 1, $2`, p.ID, s.UserID); err == nil {
		t.Error("second active assignment for one tent must be rejected by the unique index")
	}
}

func TestLoginLockoutAndTokens(t *testing.T) {
	e := testenv.New(t)
	u, err := e.Svc.CreateUser(ctx, e.Admin, service.CreateUserReq{Username: "kh1", FullName: "خادم", Role: domain.Server})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		_, _, err := e.Svc.Login(ctx, "kh1", "wrong-password", "1.2.3.4")
		asErr(t, err, "invalid_credentials")
	}
	_, _, err = e.Svc.Login(ctx, "kh1", u.TempPassword, "1.2.3.4")
	asErr(t, err, "too_many_attempts") // even the right password is refused while locked
	e.Redis.FlushDB(ctx)

	pair, user, err := e.Svc.Login(ctx, "KH1", u.TempPassword, "1.2.3.4") // case-insensitive id
	if err != nil || !user.MustChange {
		t.Fatalf("login: %v %+v", err, user)
	}
	sess, tid, limited, err := e.Svc.Authenticate(ctx, pair.AccessToken, 60)
	if err != nil || limited || sess.Username != "kh1" || !sess.MustChange {
		t.Fatalf("authenticate: %v %+v", err, sess)
	}
	// unknown tokens
	if _, _, _, err := e.Svc.Authenticate(ctx, "nope", 60); err == nil {
		t.Fatal("garbage token accepted")
	}
	// refresh tokens are single use
	pair2, err := e.Svc.Refresh(ctx, pair.RefreshToken, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Svc.Refresh(ctx, pair.RefreshToken, ""); err == nil {
		t.Fatal("refresh token reused")
	}
	// changing the password kills every session
	actor := sess.Actor(tid, "")
	if err := e.Svc.ChangePassword(ctx, actor, u.TempPassword, "a-much-better-passphrase"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := e.Svc.Authenticate(ctx, pair2.AccessToken, 60); err == nil {
		t.Fatal("old session survived password change")
	}
	if _, _, err := e.Svc.Login(ctx, "kh1", "a-much-better-passphrase", ""); err != nil {
		t.Fatal(err)
	}
	// deactivating a user revokes access immediately
	pair3, _, _ := e.Svc.Login(ctx, "kh1", "a-much-better-passphrase", "")
	active := false
	if err := e.Svc.UpdateUser(ctx, e.Admin, u.ID, service.UpdateUserReq{Active: &active}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := e.Svc.Authenticate(ctx, pair3.AccessToken, 60); err == nil {
		t.Fatal("deactivated user still authenticated")
	}
	if _, _, err := e.Svc.Login(ctx, "kh1", "a-much-better-passphrase", ""); err == nil {
		t.Fatal("deactivated user can log in")
	}
	// weak passwords
	if err := e.Svc.ChangePassword(ctx, actor, "x", "short"); err == nil {
		t.Fatal("weak password accepted")
	}
}

func TestSyncOfflineQueue(t *testing.T) {
	e := testenv.New(t)
	e.Tents(t, 1, 6)
	s := e.User(t, "s", domain.Server, e.Gate, &e.Door)
	p := e.Pilgrim(t, s, 1)
	mk := func(typ string, payload any) service.SyncOp {
		b, _ := json.Marshal(payload)
		return service.SyncOp{OpID: uuid.New(), Type: typ, ShiftID: s.ShiftID, Payload: b}
	}
	assign := mk("assign", map[string]any{"pilgrim_id": p.ID, "codes": []string{code(1), code(2)}})
	ret := mk("return", map[string]any{"items": []map[string]any{{"code": code(1), "status": "dirty"}}})
	bad := mk("assign", map[string]any{"pilgrim_id": p.ID, "codes": []string{code(99)}})
	bogus := mk("explode", map[string]any{})
	unknownField := mk("assign", map[string]any{"pilgrim_id": p.ID, "codes": []string{code(3)}, "admin": true})
	ops := []service.SyncOp{assign, ret, bad, bogus, unknownField}

	res, err := e.Svc.Sync(ctx, s, ops)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ok", "ok", "rejected", "rejected", "rejected"}
	for i, r := range res {
		if r.Status != want[i] {
			t.Fatalf("op %d: %s (%v)", i, r.Status, r.Error)
		}
	}
	// the device resends the whole queue after a dropped connection: nothing is applied twice
	res, err = e.Svc.Sync(ctx, s, ops)
	if err != nil || res[0].Status != "replayed" || res[1].Status != "replayed" {
		t.Fatalf("replay: %v %+v", err, res)
	}
	var n int
	_ = e.Pool.QueryRow(ctx, `SELECT count(*) FROM assignments`).Scan(&n)
	if n != 2 {
		t.Fatalf("%d assignments", n)
	}
	// operations are attributed to the shift they were done in, not the caller's current one
	other := e.User(t, "s2", domain.Server, e.Gate2, nil)
	stolen := mk("assign", map[string]any{"pilgrim_id": p.ID, "codes": []string{code(4)}})
	stolen.ShiftID = s.ShiftID
	r, _ := e.Svc.Sync(ctx, other, []service.SyncOp{stolen})
	if r[0].Status != "rejected" {
		t.Fatalf("another user's shift accepted: %+v", r[0])
	}
}

func TestRetireWorkflow(t *testing.T) {
	e := testenv.New(t)
	e.Tents(t, 1, 3)
	s := e.User(t, "s", domain.Server, e.Gate, &e.Door)
	lead := e.User(t, "lead", domain.GateLead, e.Gate, &e.Door)
	office := e.User(t, "off", domain.Office, 0, nil)
	p := e.Pilgrim(t, s, 1)
	_, _, _ = e.Svc.Assign(ctx, s, service.AssignReq{PilgrimID: p.ID, Codes: []string{code(1)}})

	if _, err := e.Svc.RequestRetire(ctx, s, service.RetireReq{Code: code(1), Reason: "lost"}); err == nil {
		t.Fatal("server must not request retirement")
	}
	id, err := e.Svc.RequestRetire(ctx, lead, service.RetireReq{Code: code(1), Reason: "lost"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Svc.RequestRetire(ctx, lead, service.RetireReq{Code: code(1), Reason: "lost"}); err == nil {
		t.Fatal("duplicate pending request")
	}
	if err := e.Svc.DecideRetire(ctx, lead, id, true); err == nil {
		t.Fatal("gate lead must not decide")
	}
	if err := e.Svc.DecideRetire(ctx, office, id, true); err != nil {
		t.Fatal(err)
	}
	tent, _ := e.Svc.GetTent(ctx, office, code(1))
	if tent.Status != domain.Retired {
		t.Fatalf("status %s", tent.Status)
	}
	var lost int
	_ = e.Pool.QueryRow(ctx, `SELECT count(*) FROM assignments WHERE lost_at IS NOT NULL`).Scan(&lost)
	if lost != 1 {
		t.Fatal("assignment must be closed as lost")
	}
	if _, _, err := e.Svc.Assign(ctx, s, service.AssignReq{PilgrimID: p.ID, Codes: []string{code(1)}}); err == nil {
		t.Fatal("retired tent was handed out")
	}
	if err := e.Svc.DecideRetire(ctx, office, id, true); err == nil {
		t.Fatal("request decided twice")
	}
}

func TestMoveDashboardAndSocks(t *testing.T) {
	e := testenv.New(t)
	e.Tents(t, 1, 10)
	svc := e.User(t, "svc", domain.Service, e.Wash, nil)
	s := e.User(t, "s", domain.Server, e.Gate, &e.Door)
	office := e.User(t, "off", domain.Office, 0, nil)
	lead := e.User(t, "lead", domain.GateLead, e.Gate, &e.Door)

	mv, _, err := e.Svc.Move(ctx, svc, service.MoveReq{Codes: []string{code(1), code(2), code(3), code(99)}, ToPlaceID: e.Gate})
	if err != nil || len(mv.Moved) != 3 || len(mv.Skipped) != 1 {
		t.Fatalf("move: %v %+v", err, mv)
	}
	if _, _, err := e.Svc.Socks(ctx, office, service.SocksReq{Kind: "supply", Qty: 30, PlaceID: &e.Gate}); err != nil {
		t.Fatal(err)
	}
	p := e.Pilgrim(t, s, 1)
	if _, _, err := e.Svc.Assign(ctx, s, service.AssignReq{PilgrimID: p.ID, Codes: []string{code(1)}, SocksQty: 4}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Svc.Socks(ctx, s, service.SocksReq{Kind: "issue", Qty: 100, PilgrimID: &p.ID}); err == nil {
		t.Fatal("negative stock allowed")
	}
	d, err := e.Svc.Dashboard(ctx, office)
	if err != nil {
		t.Fatal(err)
	}
	if d.Totals["with_pilgrim"] != 1 || d.Totals["ready"] != 9 || d.Today.Assigned != 1 {
		t.Fatalf("dashboard: %+v", d)
	}
	var gate service.PlaceStat
	for _, ps := range d.Places {
		if ps.PlaceID == e.Gate {
			gate = ps
		}
	}
	if gate.SocksBalance != 26 || gate.OutFromHere != 1 || gate.ByStatus["ready"] != 2 {
		t.Fatalf("gate stats: %+v", gate)
	}
	own, err := e.Svc.Dashboard(ctx, s)
	if err != nil || len(own.Places) != 1 || own.Places[0].PlaceID != e.Gate {
		t.Fatalf("gate-scoped dashboard leaks other places: %+v %v", own, err)
	}

	// handover snapshot
	lead2 := e.User(t, "lead2", domain.GateLead, e.Gate, &e.Door)
	h, err := e.Svc.CreateHandover(ctx, lead, service.HandoverReq{ToUser: lead2.UserID, Notes: "card reader low battery"})
	if err != nil || h.Snapshot["socks_balance"].(int) != 26 {
		t.Fatalf("handover: %v %+v", err, h)
	}
	if err := e.Svc.ResolveHandover(ctx, lead, h.ID, true); err == nil {
		t.Fatal("only the receiver can accept")
	}
	if err := e.Svc.ResolveHandover(ctx, lead2, h.ID, true); err != nil {
		t.Fatal(err)
	}

	rep, err := e.Svc.DailyReport(ctx, office, time.Now().UTC().Format("2006-01-02"))
	if err != nil || rep.Assigned != 1 || rep.SocksQty != 4 {
		t.Fatalf("report: %v %+v", err, rep)
	}
	f, err := e.Svc.DailyReportXLSX(rep)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil || buf.Len() < 1000 {
		t.Fatalf("xlsx: %v (%d bytes)", err, buf.Len())
	}
}

func TestRetentionAndReminders(t *testing.T) {
	e := testenv.New(t)
	e.Tents(t, 1, 3)
	s := e.User(t, "s", domain.Server, e.Gate, &e.Door)
	p1, p2 := e.Pilgrim(t, s, 1), e.Pilgrim(t, s, 2)
	_, _, _ = e.Svc.Assign(ctx, s, service.AssignReq{PilgrimID: p1.ID, Codes: []string{code(1)}})
	_, _, _ = e.Svc.Assign(ctx, s, service.AssignReq{PilgrimID: p2.ID, Codes: []string{code(2)}})
	_, _, _ = e.Svc.Return(ctx, s, service.ReturnReq{Items: []service.ReturnItem{{Code: code(2)}}})

	// nothing is overdue yet
	if jobs, err := e.Svc.DueReminders(ctx); err != nil || len(jobs) != 0 {
		t.Fatalf("early reminders: %v %v", jobs, err)
	}
	_, _ = e.Pool.Exec(ctx, `UPDATE assignments SET assigned_at = now() - interval '7 hours' WHERE returned_at IS NULL`)
	jobs, err := e.Svc.DueReminders(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].PilgrimID != p1.ID {
		t.Fatalf("reminders: %v %v", jobs, err)
	}
	if again, _ := e.Svc.DueReminders(ctx); len(again) != 0 {
		t.Fatal("same assignment reminded twice in a row")
	}

	// retention: only pilgrims without tents and not seen for a long time are erased
	_, _ = e.Pool.Exec(ctx, `UPDATE pilgrims SET last_seen_at = now() - interval '200 days'`)
	n, err := e.Svc.AnonymizeInactive(ctx)
	if err != nil || n != 1 {
		t.Fatalf("anonymize: %d %v", n, err)
	}
	var enc []byte
	var anon *time.Time
	_ = e.Pool.QueryRow(ctx, `SELECT nid_enc, anonymized_at FROM pilgrims WHERE id=$1`, p2.ID).Scan(&enc, &anon)
	if enc != nil || anon == nil {
		t.Fatal("personal data not erased")
	}
	if _, err := e.Svc.PilgrimMobile(ctx, p1.ID); err != nil {
		t.Fatal("pilgrim with a tent out must keep their data")
	}
}

func TestSettingsValidation(t *testing.T) {
	e := testenv.New(t)
	if err := e.Svc.PutSetting(ctx, e.Admin, "socks_unit_price", json.RawMessage(`25000`)); err != nil {
		t.Fatal(err)
	}
	if got := e.Svc.SettingInt(ctx, "socks_unit_price"); got != 25000 {
		t.Fatalf("got %d", got)
	}
	for _, bad := range []struct{ k, v string }{{"socks_unit_price", `"x"`}, {"socks_unit_price", `-5`}, {"nope", `1`}, {"sms_templates", `{"evil":"x"}`}} {
		if err := e.Svc.PutSetting(ctx, e.Admin, bad.k, json.RawMessage(bad.v)); err == nil {
			t.Errorf("%s=%s accepted", bad.k, bad.v)
		}
	}
	s := e.User(t, "s", domain.Server, e.Gate, &e.Door)
	if err := e.Svc.PutSetting(ctx, s, "socks_unit_price", json.RawMessage(`1`)); err == nil {
		t.Fatal("server changed settings")
	}
}

// With TEST_APP_DATABASE_URL set, the whole suite runs as the unprivileged
// runtime role. This test proves what that role cannot do even if the
// application were compromised.
func TestRuntimeRoleCannotTamper(t *testing.T) {
	if os.Getenv("TEST_APP_DATABASE_URL") == "" {
		t.Skip("set TEST_APP_DATABASE_URL (and TEST_APP_ROLE) to run as the unprivileged role")
	}
	e := testenv.New(t)
	e.Tents(t, 1, 1)
	s := e.User(t, "s", domain.Server, e.Gate, &e.Door)
	p := e.Pilgrim(t, s, 1)
	_, _, _ = e.Svc.Assign(ctx, s, service.AssignReq{PilgrimID: p.ID, Codes: []string{code(1)}})
	for _, q := range []string{
		`UPDATE audit_log SET action='x'`, `DELETE FROM audit_log`, `TRUNCATE audit_log`,
		`UPDATE tent_events SET type='x'`, `DELETE FROM tent_events`, `TRUNCATE tent_events`,
		`ALTER TABLE audit_log DISABLE TRIGGER audit_log_append_only`,
		`DROP TRIGGER tent_events_append_only ON tent_events`,
		`DROP TABLE audit_log`, `CREATE TABLE evil(x int)`, `SELECT * FROM schema_migrations`,
		`CREATE ROLE evil SUPERUSER`,
	} {
		_, err := e.AppPool.Exec(ctx, q)
		if err == nil {
			t.Errorf("runtime role was able to run: %s", q)
			continue
		}
		if !strings.Contains(err.Error(), "42501") && !strings.Contains(err.Error(), "must be owner") && !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: unexpected error %v", q, err)
		}
	}
	// ...but normal operation works
	if _, err := e.AppPool.Exec(ctx, `INSERT INTO audit_log(action) VALUES ('ok')`); err != nil {
		t.Fatalf("runtime role must be able to append to the audit log: %v", err)
	}
}

func TestColleaguesForHandover(t *testing.T) {
	e := testenv.New(t)
	lead := e.User(t, "lead", domain.GateLead, e.Gate, &e.Door)
	srv := e.User(t, "srv", domain.Server, e.Gate, &e.Door)
	e.User(t, "elsewhere", domain.Server, e.Gate2, nil)
	e.User(t, "washer", domain.Workshop, e.Wash, nil)
	got, err := e.Svc.Colleagues(ctx, lead)
	if err != nil || len(got) != 1 || got[0].ID != srv.UserID {
		t.Fatalf("lead should see only the server on the same gate: %v %v", got, err)
	}
	if _, err := e.Svc.Colleagues(ctx, srv); err == nil {
		t.Fatal("a plain server has no use for the colleague list")
	}
}
