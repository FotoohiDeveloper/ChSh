package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"chador/internal/domain"
)

// ---- helpers ----

func uniqueCodes(raw []string, max int) ([]string, error) {
	if len(raw) == 0 {
		return nil, BadRequest("no_tents", "حداقل یک چادر را اسکن کنید")
	}
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		c, ok := domain.NormalizeTentCode(r)
		if !ok {
			return nil, BadRequest("bad_tent_code", "کد چادر نامعتبر است: "+r)
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	if len(out) > max {
		return nil, BadRequest("too_many_tents", fmt.Sprintf("در هر درخواست حداکثر %d چادر مجاز است", max))
	}
	return out, nil
}

type evRow struct {
	TentID       int32
	Type         string
	From, To     *string
	PilgrimID    *string
	AssignmentID *string
	Meta         *string
}

func sp(s string) *string { return &s }

// insertEvents appends rows to tent_events in one statement.
func (s *Service) insertEvents(ctx context.Context, tx pgx.Tx, a Actor, clientTS *time.Time, rows []evRow) error {
	if len(rows) == 0 {
		return nil
	}
	n := len(rows)
	ids, types := make([]int32, n), make([]string, n)
	from, to, pil, asg, meta := make([]*string, n), make([]*string, n), make([]*string, n), make([]*string, n), make([]*string, n)
	for i, r := range rows {
		ids[i], types[i], from[i], to[i], pil[i], asg[i], meta[i] = r.TentID, r.Type, r.From, r.To, r.PilgrimID, r.AssignmentID, r.Meta
	}
	_, err := tx.Exec(ctx, `INSERT INTO tent_events (tent_id, type, from_status, to_status, pilgrim_id, assignment_id, meta, place_id, door_id, user_id, shift_id, client_ts)
		SELECT x.tent_id, x.type, x.f::tent_status, x.t::tent_status, x.p::uuid, x.a::uuid, x.m::jsonb, $7::smallint, $8::smallint, $9::uuid, $10::uuid, $11::timestamptz
		FROM unnest($1::int[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $12::text[]) AS x(tent_id, type, f, t, p, a, m)`,
		ids, types, from, to, pil, asg, a.PlaceID, a.DoorID, a.UserID, a.ShiftID, clientTS, meta)
	return err
}

func metaJSON(v any) *string {
	b, _ := json.Marshal(v)
	return sp(string(b))
}

// ---- views ----

type TentView struct {
	Code       string            `json:"code"`
	Status     domain.Status     `json:"status"`
	PlaceID    *int16            `json:"place_id,omitempty"`
	PlaceName  string            `json:"place_name,omitempty"`
	Kind       string            `json:"kind"`
	Note       string            `json:"note,omitempty"`
	Assignment *ActiveAssignment `json:"assignment,omitempty"`
}

type ActiveAssignment struct {
	ID            uuid.UUID `json:"id"`
	PilgrimID     uuid.UUID `json:"pilgrim_id"`
	PilgrimName   string    `json:"pilgrim_name,omitempty"`
	MobileMasked  string    `json:"mobile_masked,omitempty"`
	AssignedAt    time.Time `json:"assigned_at"`
	AssignedPlace string    `json:"assigned_place,omitempty"`
}

// GetTent is the scan result shown to the person holding the phone.
func (s *Service) GetTent(ctx context.Context, a Actor, rawCode string) (*TentView, error) {
	if err := a.require(domain.PermTentRead); err != nil {
		return nil, err
	}
	code, ok := domain.NormalizeTentCode(rawCode)
	if !ok {
		return nil, BadRequest("bad_tent_code", "کد چادر نامعتبر است")
	}
	var v TentView
	var note, placeName *string
	var tentID int32
	err := s.db.QueryRow(ctx, `SELECT t.id, t.code, t.status::text, t.place_id, p.name, t.kind, t.note
		FROM tents t LEFT JOIN places p ON p.id=t.place_id WHERE t.code=$1`, code).
		Scan(&tentID, &v.Code, &v.Status, &v.PlaceID, &placeName, &v.Kind, &note)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, NotFound("چادری با این کد ثبت نشده است")
	}
	if err != nil {
		return nil, err
	}
	if placeName != nil {
		v.PlaceName = *placeName
	}
	if note != nil {
		v.Note = *note
	}
	if v.Status == domain.WithPilgrim {
		var aa ActiveAssignment
		var mobEnc, nameEnc []byte
		var place *string
		err := s.db.QueryRow(ctx, `SELECT a.id, a.pilgrim_id, a.assigned_at, p.mobile_enc, p.name_enc, pl.name
			FROM assignments a JOIN pilgrims p ON p.id=a.pilgrim_id LEFT JOIN places pl ON pl.id=a.assigned_place_id
			WHERE a.tent_id=$1 AND a.returned_at IS NULL AND a.cancelled_at IS NULL AND a.lost_at IS NULL`, tentID).
			Scan(&aa.ID, &aa.PilgrimID, &aa.AssignedAt, &mobEnc, &nameEnc, &place)
		if err == nil {
			if mobEnc != nil {
				m, _ := s.crypto.Open("pilgrim.mobile", mobEnc)
				aa.MobileMasked = domain.MaskMobile(m)
			}
			if nameEnc != nil {
				aa.PilgrimName, _ = s.crypto.Open("pilgrim.name", nameEnc)
			}
			if place != nil {
				aa.AssignedPlace = *place
			}
			v.Assignment = &aa
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	return &v, nil
}

type TentEvent struct {
	At       time.Time       `json:"at"`
	Type     string          `json:"type"`
	From     *string         `json:"from,omitempty"`
	To       *string         `json:"to,omitempty"`
	PlaceID  *int16          `json:"place_id,omitempty"`
	DoorID   *int16          `json:"door_id,omitempty"`
	UserName string          `json:"user"`
	Meta     json.RawMessage `json:"meta,omitempty"`
}

func (s *Service) TentHistory(ctx context.Context, a Actor, rawCode string, limit int) ([]TentEvent, error) {
	if err := a.require(domain.PermTentRead); err != nil {
		return nil, err
	}
	code, ok := domain.NormalizeTentCode(rawCode)
	if !ok {
		return nil, BadRequest("bad_tent_code", "کد چادر نامعتبر است")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `SELECT e.created_at, e.type, e.from_status::text, e.to_status::text, e.place_id, e.door_id, COALESCE(u.full_name,''), e.meta
		FROM tent_events e JOIN tents t ON t.id=e.tent_id LEFT JOIN users u ON u.id=e.user_id
		WHERE t.code=$1 ORDER BY e.id DESC LIMIT $2`, code, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TentEvent{}
	for rows.Next() {
		var e TentEvent
		var meta []byte
		if err := rows.Scan(&e.At, &e.Type, &e.From, &e.To, &e.PlaceID, &e.DoorID, &e.UserName, &meta); err != nil {
			return nil, err
		}
		e.Meta = meta
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- assign ----

type AssignReq struct {
	PilgrimID uuid.UUID  `json:"pilgrim_id"`
	Codes     []string   `json:"codes"`
	SocksQty  int        `json:"socks_qty"`
	OpID      *uuid.UUID `json:"op_id"`
	ClientTS  *time.Time `json:"client_ts"`
}

type AssignItem struct {
	AssignmentID uuid.UUID `json:"assignment_id"`
	Code         string    `json:"code"`
}

type AssignRes struct {
	Items       []AssignItem `json:"items"`
	SocksQty    int          `json:"socks_qty"`
	SocksAmount int64        `json:"socks_amount"`
}

type unavailable struct {
	Code   string `json:"code"`
	Reason string `json:"reason"` // not_found | <current status>
}

// Assign hands one or more tents to a pilgrim. It is all-or-nothing: if any
// tent is unavailable nothing is recorded and the response lists which ones.
func (s *Service) Assign(ctx context.Context, a Actor, req AssignReq) (AssignRes, bool, error) {
	var zero AssignRes
	if err := a.require(domain.PermAssign); err != nil {
		return zero, false, err
	}
	if err := a.requireShift(); err != nil {
		return zero, false, err
	}
	codes, err := uniqueCodes(req.Codes, s.SettingInt(ctx, "max_tents_per_request"))
	if err != nil {
		return zero, false, err
	}
	if req.SocksQty < 0 || req.SocksQty > 50 {
		return zero, false, BadRequest("bad_socks", "تعداد جوراب نامعتبر است")
	}
	unitPrice := int64(s.SettingInt(ctx, "socks_unit_price"))

	res, replayed, err := runOp(ctx, s, a, "assign", req.OpID, func(tx pgx.Tx) (AssignRes, error) {
		var state string
		err := tx.QueryRow(ctx, `SELECT verification::text FROM pilgrims WHERE id=$1 AND anonymized_at IS NULL`, req.PilgrimID).Scan(&state)
		if errors.Is(err, pgx.ErrNoRows) {
			return zero, NotFound("زائر پیدا نشد")
		}
		if err != nil {
			return zero, err
		}
		if state == "failed" || (state != "verified" && !s.cfg.AllowUnverified) {
			return zero, Unprocessable("pilgrim_unverified", "هویت زائر هنوز تأیید نشده است")
		}

		// Lock candidate tents in id order (no deadlocks), keep the ones that are ready.
		rows, err := tx.Query(ctx, `WITH old AS (
				SELECT id, code, place_id FROM tents WHERE code = ANY($1) AND status = 'ready' ORDER BY id FOR UPDATE)
			UPDATE tents t SET status='with_pilgrim', place_id=NULL, updated_at=now()
			FROM old WHERE t.id = old.id RETURNING t.id, t.code, old.place_id`, codes)
		if err != nil {
			return zero, err
		}
		type got struct {
			id   int32
			code string
			from *int16
		}
		var taken []got
		for rows.Next() {
			var g got
			if err := rows.Scan(&g.id, &g.code, &g.from); err != nil {
				rows.Close()
				return zero, err
			}
			taken = append(taken, g)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return zero, err
		}
		if len(taken) != len(codes) {
			return zero, s.explainUnavailable(ctx, tx, codes, taken2codes(len(taken), func(i int) string { return taken[i].code }))
		}

		ids := make([]int32, len(taken))
		for i, g := range taken {
			ids[i] = g.id
		}
		arows, err := tx.Query(ctx, `INSERT INTO assignments (pilgrim_id, tent_id, assigned_by, assigned_shift_id, assigned_place_id, assigned_door_id)
			SELECT $1, unnest($2::int[]), $3, $4, $5, $6 RETURNING id, tent_id`,
			req.PilgrimID, ids, a.UserID, a.ShiftID, a.PlaceID, a.DoorID)
		if err != nil {
			if pgCode(err) == "23505" { // lost a race that the lock above should already prevent
				return zero, Conflict("tent_taken", "یکی از چادرها همین الان به دیگری داده شد", nil)
			}
			return zero, err
		}
		asgByTent := map[int32]uuid.UUID{}
		for arows.Next() {
			var id uuid.UUID
			var tid int32
			if err := arows.Scan(&id, &tid); err != nil {
				arows.Close()
				return zero, err
			}
			asgByTent[tid] = id
		}
		arows.Close()
		if err := arows.Err(); err != nil {
			if pgCode(err) == "23505" { // INSERT ... RETURNING reports constraint errors while rows are read
				return zero, Conflict("tent_taken", "یکی از چادرها همین الان به دیگری داده شد", nil)
			}
			return zero, err
		}

		res := AssignRes{Items: make([]AssignItem, 0, len(taken))}
		evs := make([]evRow, 0, len(taken))
		pid := req.PilgrimID.String()
		for _, g := range taken {
			aid := asgByTent[g.id]
			res.Items = append(res.Items, AssignItem{AssignmentID: aid, Code: g.code})
			var fp any
			if g.from != nil {
				fp = *g.from
			}
			evs = append(evs, evRow{TentID: g.id, Type: "assigned", From: sp("ready"), To: sp("with_pilgrim"),
				PilgrimID: &pid, AssignmentID: sp(aid.String()), Meta: metaJSON(map[string]any{"from_place": fp})})
		}
		sort.Slice(res.Items, func(i, j int) bool { return res.Items[i].Code < res.Items[j].Code })
		if err := s.insertEvents(ctx, tx, a, req.ClientTS, evs); err != nil {
			return zero, err
		}
		if req.SocksQty > 0 {
			res.SocksQty, res.SocksAmount = req.SocksQty, unitPrice*int64(req.SocksQty)
			if _, err := tx.Exec(ctx, `INSERT INTO socks_ledger(kind, place_id, shift_id, user_id, pilgrim_id, qty, unit_price, amount)
				VALUES ('issue',$1,$2,$3,$4,$5,$6,$7)`, a.PlaceID, a.ShiftID, a.UserID, req.PilgrimID, req.SocksQty, unitPrice, res.SocksAmount); err != nil {
				return zero, err
			}
		}
		return res, nil
	})
	if err != nil || replayed {
		return res, replayed, err
	}
	s.publish(ctx, Event{Type: "assigned", PlaceID: a.PlaceID, Count: len(res.Items)})
	_ = s.q.SMS(ctx, SMSJob{PilgrimID: req.PilgrimID, Template: "delivered", DedupeKey: "del:" + res.Items[0].AssignmentID.String()})
	return res, false, nil
}

func taken2codes(n int, at func(int) string) map[string]bool {
	m := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		m[at(i)] = true
	}
	return m
}

func (s *Service) explainUnavailable(ctx context.Context, tx pgx.Tx, codes []string, ok map[string]bool) error {
	rows, err := tx.Query(ctx, `SELECT code, status::text FROM tents WHERE code = ANY($1)`, codes)
	if err != nil {
		return err
	}
	defer rows.Close()
	status := map[string]string{}
	for rows.Next() {
		var c, st string
		if err := rows.Scan(&c, &st); err != nil {
			return err
		}
		status[c] = st
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var bad []unavailable
	for _, c := range codes {
		if ok[c] {
			continue
		}
		if st, found := status[c]; found {
			bad = append(bad, unavailable{c, st})
		} else {
			bad = append(bad, unavailable{c, "not_found"})
		}
	}
	return Conflict("tents_unavailable", "برخی چادرها برای تحویل آماده نیستند", bad)
}

// ---- return ----

type ReturnItem struct {
	Code   string        `json:"code"`
	Status domain.Status `json:"status"` // what the tent is now: ready | dirty | repair (default ready)
}

type ReturnReq struct {
	Items    []ReturnItem `json:"items"`
	OpID     *uuid.UUID   `json:"op_id"`
	ClientTS *time.Time   `json:"client_ts"`
}

type ReturnResult struct {
	Code   string `json:"code"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type ReturnRes struct {
	Results   []ReturnResult `json:"results"`
	Returned  int            `json:"returned"`
	Completed int            `json:"completed_pilgrims"`
}

// Return closes assignments and sets each tent's new status. Unlike Assign it
// is per item: scanning a stack of returned tents must not fail because one
// of them was never handed out.
func (s *Service) Return(ctx context.Context, a Actor, req ReturnReq) (ReturnRes, bool, error) {
	var zero ReturnRes
	if err := a.require(domain.PermReturn); err != nil {
		return zero, false, err
	}
	if err := a.requireShift(); err != nil {
		return zero, false, err
	}
	max := s.SettingInt(ctx, "max_tents_per_request")
	if len(req.Items) == 0 || len(req.Items) > max*5 {
		return zero, false, BadRequest("bad_items", "تعداد چادرها نامعتبر است")
	}
	statusOf := map[string]domain.Status{}
	var codes []string
	for _, it := range req.Items {
		c, ok := domain.NormalizeTentCode(it.Code)
		if !ok {
			return zero, false, BadRequest("bad_tent_code", "کد چادر نامعتبر است: "+it.Code)
		}
		st := it.Status
		if st == "" {
			st = domain.Ready
		}
		if !domain.ValidReturnStatus(st) {
			return zero, false, BadRequest("bad_status", "وضعیت بازگشت باید سالم، کثیف یا تعمیری باشد")
		}
		if _, dup := statusOf[c]; !dup {
			codes = append(codes, c)
		}
		statusOf[c] = st
	}

	var completed map[uuid.UUID]uuid.UUID // pilgrim -> one assignment id (for the SMS dedupe key)

	res, replayed, err := runOp(ctx, s, a, "return", req.OpID, func(tx pgx.Tx) (ReturnRes, error) {
		completed = map[uuid.UUID]uuid.UUID{}
		rows, err := tx.Query(ctx, `SELECT id, code, status::text FROM tents WHERE code = ANY($1) ORDER BY id FOR UPDATE`, codes)
		if err != nil {
			return zero, err
		}
		type tentRow struct {
			id     int32
			status string
		}
		tents := map[string]tentRow{}
		var ids []int32
		for rows.Next() {
			var id int32
			var code, st string
			if err := rows.Scan(&id, &code, &st); err != nil {
				rows.Close()
				return zero, err
			}
			tents[code] = tentRow{id, st}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return zero, err
		}
		arows, err := tx.Query(ctx, `SELECT id, tent_id, pilgrim_id FROM assignments WHERE tent_id = ANY($1)
			AND returned_at IS NULL AND cancelled_at IS NULL AND lost_at IS NULL`, ids)
		if err != nil {
			return zero, err
		}
		type asg struct{ id, pilgrim uuid.UUID }
		active := map[int32]asg{}
		for arows.Next() {
			var x asg
			var tid int32
			if err := arows.Scan(&x.id, &tid, &x.pilgrim); err != nil {
				arows.Close()
				return zero, err
			}
			active[tid] = x
		}
		arows.Close()
		if err := arows.Err(); err != nil {
			return zero, err
		}

		out := ReturnRes{Results: make([]ReturnResult, 0, len(codes))}
		var asgIDs, asgStatus, tentStatus, pilgrimIDs []string
		var tentIDs []int32
		var evs []evRow
		for _, c := range codes {
			t, found := tents[c]
			switch {
			case !found:
				out.Results = append(out.Results, ReturnResult{Code: c, Error: "not_found"})
			case active[t.id].id == uuid.Nil:
				out.Results = append(out.Results, ReturnResult{Code: c, Error: "not_assigned", Detail: t.status})
			default:
				x, st := active[t.id], string(statusOf[c])
				asgIDs, asgStatus = append(asgIDs, x.id.String()), append(asgStatus, st)
				tentIDs, tentStatus = append(tentIDs, t.id), append(tentStatus, st)
				pilgrimIDs = append(pilgrimIDs, x.pilgrim.String())
				pid, aid := x.pilgrim.String(), x.id.String()
				evs = append(evs, evRow{TentID: t.id, Type: "returned", From: sp("with_pilgrim"), To: sp(st), PilgrimID: &pid, AssignmentID: &aid})
				out.Results = append(out.Results, ReturnResult{Code: c, OK: true})
				completed[x.pilgrim] = x.id
			}
		}
		out.Returned = len(asgIDs)
		if out.Returned == 0 {
			return out, nil
		}
		tag, err := tx.Exec(ctx, `UPDATE assignments a SET returned_at=now(), returned_by=$1, returned_shift_id=$2, returned_place_id=$3, returned_door_id=$4,
			return_status=x.st::tent_status
			FROM unnest($5::text[], $6::text[]) AS x(id, st)
			WHERE a.id = x.id::uuid AND a.returned_at IS NULL AND a.cancelled_at IS NULL AND a.lost_at IS NULL`,
			a.UserID, a.ShiftID, a.PlaceID, a.DoorID, asgIDs, asgStatus)
		if err != nil {
			return zero, err
		}
		if int(tag.RowsAffected()) != out.Returned {
			return zero, Conflict("concurrent_change", "وضعیت چادرها همزمان تغییر کرد؛ دوباره اسکن کنید", nil)
		}
		if _, err := tx.Exec(ctx, `UPDATE tents t SET status=x.st::tent_status, place_id=$1, updated_at=now()
			FROM unnest($2::int[], $3::text[]) AS x(id, st) WHERE t.id=x.id`, a.PlaceID, tentIDs, tentStatus); err != nil {
			return zero, err
		}
		if err := s.insertEvents(ctx, tx, a, req.ClientTS, evs); err != nil {
			return zero, err
		}
		// which of these pilgrims now hold nothing?
		var plist []string
		for p := range completed {
			plist = append(plist, p.String())
		}
		drows, err := tx.Query(ctx, `SELECT p FROM unnest($1::text[]) AS p WHERE NOT EXISTS (
			SELECT 1 FROM assignments WHERE pilgrim_id = p::uuid AND returned_at IS NULL AND cancelled_at IS NULL AND lost_at IS NULL)`, plist)
		if err != nil {
			return zero, err
		}
		done := map[uuid.UUID]bool{}
		for drows.Next() {
			var p string
			if err := drows.Scan(&p); err != nil {
				drows.Close()
				return zero, err
			}
			if id, err := uuid.Parse(p); err == nil {
				done[id] = true
			}
		}
		drows.Close()
		if err := drows.Err(); err != nil {
			return zero, err
		}
		for p := range completed {
			if !done[p] {
				delete(completed, p)
			}
		}
		out.Completed = len(completed)
		return out, nil
	})
	if err != nil || replayed {
		return res, replayed, err
	}
	if res.Returned > 0 {
		s.publish(ctx, Event{Type: "returned", PlaceID: a.PlaceID, Count: res.Returned})
	}
	for p, asgID := range completed {
		_ = s.q.SMS(ctx, SMSJob{PilgrimID: p, Template: "returned", DedupeKey: "ret:" + asgID.String()})
	}
	return res, false, nil
}

// ---- cancel (undo) ----

type CancelRes struct {
	Code string `json:"code"`
}

// CancelAssignment undoes a hand-out. A server can undo their own mistake
// within the undo window; a gate lead, office or admin can do it any time.
func (s *Service) CancelAssignment(ctx context.Context, a Actor, rawCode string, opID *uuid.UUID) (CancelRes, bool, error) {
	var zero CancelRes
	if !a.Can(domain.PermCancelOwn) && !a.Can(domain.PermCancelAny) {
		return zero, false, Forbidden("دسترسی لازم برای این کار را ندارید")
	}
	code, ok := domain.NormalizeTentCode(rawCode)
	if !ok {
		return zero, false, BadRequest("bad_tent_code", "کد چادر نامعتبر است")
	}
	window := s.cfg.UndoWindow
	var placeID *int16
	res, replayed, err := runOp(ctx, s, a, "cancel", opID, func(tx pgx.Tx) (CancelRes, error) {
		var tid int32
		if err := tx.QueryRow(ctx, `SELECT id FROM tents WHERE code=$1 FOR UPDATE`, code).Scan(&tid); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return zero, NotFound("چادری با این کد ثبت نشده است")
			}
			return zero, err
		}
		var asgID, pilgrim uuid.UUID
		var by uuid.UUID
		var ageSec float64
		var atPlace *int16
		err := tx.QueryRow(ctx, `SELECT id, pilgrim_id, assigned_by, extract(epoch from now()-assigned_at)::float8, assigned_place_id FROM assignments
			WHERE tent_id=$1 AND returned_at IS NULL AND cancelled_at IS NULL AND lost_at IS NULL FOR UPDATE`, tid).Scan(&asgID, &pilgrim, &by, &ageSec, &atPlace)
		if errors.Is(err, pgx.ErrNoRows) {
			return zero, NotFound("برای این چادر تخصیص فعالی وجود ندارد")
		}
		if err != nil {
			return zero, err
		}
		if !a.Can(domain.PermCancelAny) {
			if by != a.UserID {
				return zero, Forbidden("فقط ثبت‌کننده یا مسئول گیت می‌تواند این تخصیص را لغو کند")
			}
			if time.Duration(ageSec*float64(time.Second)) > window {
				return zero, Forbidden(fmt.Sprintf("مهلت لغو (%d دقیقه) تمام شده؛ از مسئول گیت بخواهید", int(window.Minutes())))
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE assignments SET cancelled_at=now(), cancelled_by=$2 WHERE id=$1`, asgID, a.UserID); err != nil {
			return zero, err
		}
		place := atPlace
		if a.PlaceID != nil {
			place = a.PlaceID
		}
		placeID = place
		if _, err := tx.Exec(ctx, `UPDATE tents SET status='ready', place_id=$2, updated_at=now() WHERE id=$1`, tid, place); err != nil {
			return zero, err
		}
		pid, aid := pilgrim.String(), asgID.String()
		if err := s.insertEvents(ctx, tx, a, nil, []evRow{{TentID: tid, Type: "assignment_cancelled", From: sp("with_pilgrim"), To: sp("ready"), PilgrimID: &pid, AssignmentID: &aid}}); err != nil {
			return zero, err
		}
		return CancelRes{Code: code}, nil
	})
	if err == nil && !replayed {
		s.publish(ctx, Event{Type: "cancelled", PlaceID: placeID, Count: 1})
	}
	return res, replayed, err
}

// ---- generic status change ----

type StatusReq struct {
	Code     string        `json:"code"`
	To       domain.Status `json:"to"`
	Note     string        `json:"note"`
	OpID     *uuid.UUID    `json:"op_id"`
	ClientTS *time.Time    `json:"client_ts"`
}

type StatusRes struct {
	Code string        `json:"code"`
	From domain.Status `json:"from"`
	To   domain.Status `json:"to"`
}

func (s *Service) SetStatus(ctx context.Context, a Actor, req StatusReq) (StatusRes, bool, error) {
	var zero StatusRes
	if err := a.require(domain.PermTentStatus); err != nil {
		return zero, false, err
	}
	if a.Role.NeedsShift() {
		if err := a.requireShift(); err != nil {
			return zero, false, err
		}
	}
	code, ok := domain.NormalizeTentCode(req.Code)
	if !ok {
		return zero, false, BadRequest("bad_tent_code", "کد چادر نامعتبر است")
	}
	if !req.To.Valid() || len(req.Note) > 300 {
		return zero, false, BadRequest("bad_status", "وضعیت نامعتبر است")
	}
	var placeID *int16
	res, replayed, err := runOp(ctx, s, a, "status", req.OpID, func(tx pgx.Tx) (StatusRes, error) {
		var id int32
		var from domain.Status
		var place *int16
		if err := tx.QueryRow(ctx, `SELECT id, status::text, place_id FROM tents WHERE code=$1 FOR UPDATE`, code).Scan(&id, &from, &place); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return zero, NotFound("چادری با این کد ثبت نشده است")
			}
			return zero, err
		}
		if err := domain.CheckTransition(a.Role, from, req.To); err != nil {
			return zero, Conflict("invalid_transition", "این تغییر وضعیت برای نقش شما یا برای وضعیت فعلی چادر مجاز نیست", map[string]any{"from": from, "to": req.To})
		}
		newPlace := place
		if a.PlaceID != nil {
			newPlace = a.PlaceID
		}
		if req.To == domain.Retired {
			newPlace = nil
		}
		if req.To == domain.InWash { // washing happens at a workshop
			var kind string
			if newPlace == nil || tx.QueryRow(ctx, `SELECT kind::text FROM places WHERE id=$1`, *newPlace).Scan(&kind) != nil || kind != "workshop" {
				return zero, Conflict("not_at_workshop", "ورود به کارگاه باید از داخل شیفت کارگاه ثبت شود", nil)
			}
		}
		placeID = newPlace
		if _, err := tx.Exec(ctx, `UPDATE tents SET status=$2, place_id=$3, updated_at=now() WHERE id=$1`, id, req.To, newPlace); err != nil {
			return zero, err
		}
		var meta *string
		if req.Note != "" {
			meta = metaJSON(map[string]string{"note": req.Note})
		}
		f, t := string(from), string(req.To)
		if err := s.insertEvents(ctx, tx, a, req.ClientTS, []evRow{{TentID: id, Type: "status", From: &f, To: &t, Meta: meta}}); err != nil {
			return zero, err
		}
		return StatusRes{Code: code, From: from, To: req.To}, nil
	})
	if err == nil && !replayed {
		s.publish(ctx, Event{Type: "status", PlaceID: placeID, Count: 1})
	}
	return res, replayed, err
}

// ---- move ----

type MoveReq struct {
	Codes     []string   `json:"codes"`
	ToPlaceID int16      `json:"to_place_id"`
	OpID      *uuid.UUID `json:"op_id"`
	ClientTS  *time.Time `json:"client_ts"`
}

type MoveRes struct {
	Moved   []string `json:"moved"`
	Skipped []struct {
		Code   string `json:"code"`
		Reason string `json:"reason"`
	} `json:"skipped,omitempty"`
}

// Move records that tents physically went from one place to another.
func (s *Service) Move(ctx context.Context, a Actor, req MoveReq) (MoveRes, bool, error) {
	var zero MoveRes
	if err := a.require(domain.PermTentMove); err != nil {
		return zero, false, err
	}
	if a.Role.NeedsShift() {
		if err := a.requireShift(); err != nil {
			return zero, false, err
		}
	}
	codes, err := uniqueCodes(req.Codes, s.SettingInt(ctx, "max_tents_per_request")*5)
	if err != nil {
		return zero, false, err
	}
	res, replayed, err := runOp(ctx, s, a, "move", req.OpID, func(tx pgx.Tx) (MoveRes, error) {
		var active bool
		if err := tx.QueryRow(ctx, `SELECT active FROM places WHERE id=$1`, req.ToPlaceID).Scan(&active); err != nil || !active {
			return zero, BadRequest("bad_place", "مقصد نامعتبر است")
		}
		rows, err := tx.Query(ctx, `WITH old AS (SELECT id, code, place_id FROM tents WHERE code = ANY($1)
				AND status NOT IN ('with_pilgrim','retired') ORDER BY id FOR UPDATE)
			UPDATE tents t SET place_id=$2, updated_at=now() FROM old WHERE t.id=old.id RETURNING t.id, t.code, old.place_id, t.status::text`, codes, req.ToPlaceID)
		if err != nil {
			return zero, err
		}
		out := MoveRes{Moved: []string{}}
		moved := map[string]bool{}
		var evs []evRow
		for rows.Next() {
			var id int32
			var code, st string
			var from *int16
			if err := rows.Scan(&id, &code, &from, &st); err != nil {
				rows.Close()
				return zero, err
			}
			moved[code] = true
			out.Moved = append(out.Moved, code)
			var fp any
			if from != nil {
				fp = *from
			}
			evs = append(evs, evRow{TentID: id, Type: "moved", To: &st, From: &st, Meta: metaJSON(map[string]any{"from_place": fp, "to_place": req.ToPlaceID})})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return zero, err
		}
		for _, c := range codes {
			if !moved[c] {
				out.Skipped = append(out.Skipped, struct {
					Code   string `json:"code"`
					Reason string `json:"reason"`
				}{c, "not_movable"})
			}
		}
		sort.Strings(out.Moved)
		if err := s.insertEvents(ctx, tx, a, req.ClientTS, evs); err != nil {
			return zero, err
		}
		return out, nil
	})
	if err == nil && !replayed && len(res.Moved) > 0 {
		s.publish(ctx, Event{Type: "moved", PlaceID: &req.ToPlaceID, Count: len(res.Moved)})
	}
	return res, replayed, err
}

// ---- retire requests ----

type RetireReq struct {
	Code   string `json:"code"`
	Reason string `json:"reason"` // lost | damaged | other
	Note   string `json:"note"`
}

type RetireRequestView struct {
	ID          uuid.UUID `json:"id"`
	Code        string    `json:"code"`
	Reason      string    `json:"reason"`
	Note        string    `json:"note,omitempty"`
	State       string    `json:"state"`
	RequestedBy string    `json:"requested_by"`
	RequestedAt time.Time `json:"requested_at"`
}

func (s *Service) RequestRetire(ctx context.Context, a Actor, req RetireReq) (uuid.UUID, error) {
	if err := a.require(domain.PermRetireRequest); err != nil {
		return uuid.Nil, err
	}
	code, ok := domain.NormalizeTentCode(req.Code)
	if !ok {
		return uuid.Nil, BadRequest("bad_tent_code", "کد چادر نامعتبر است")
	}
	if req.Reason != "lost" && req.Reason != "damaged" && req.Reason != "other" || len(req.Note) > 300 {
		return uuid.Nil, BadRequest("bad_reason", "دلیل نامعتبر است")
	}
	var id uuid.UUID
	err := s.db.QueryRow(ctx, `INSERT INTO retire_requests(tent_id, reason, note, requested_by)
		SELECT id, $2, NULLIF($3,''), $4 FROM tents WHERE code=$1 AND status <> 'retired' RETURNING id`,
		code, req.Reason, req.Note, a.UserID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, NotFound("چادر پیدا نشد یا قبلاً از چرخه خارج شده")
	}
	if pgCode(err) == "23505" {
		return uuid.Nil, Conflict("already_requested", "برای این چادر درخواست باز وجود دارد", nil)
	}
	if err != nil {
		return uuid.Nil, err
	}
	s.AuditNow(ctx, &a.UserID, a.IP, "retire.request", "tent", code, map[string]string{"reason": req.Reason})
	return id, nil
}

func (s *Service) ListRetireRequests(ctx context.Context, a Actor) ([]RetireRequestView, error) {
	if err := a.require(domain.PermRetireDecide); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT r.id, t.code, r.reason, COALESCE(r.note,''), r.state::text, u.full_name, r.requested_at
		FROM retire_requests r JOIN tents t ON t.id=r.tent_id JOIN users u ON u.id=r.requested_by
		WHERE r.state='pending' ORDER BY r.requested_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RetireRequestView{}
	for rows.Next() {
		var v RetireRequestView
		if err := rows.Scan(&v.ID, &v.Code, &v.Reason, &v.Note, &v.State, &v.RequestedBy, &v.RequestedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// DecideRetire approves or rejects. Approval takes the tent out of the cycle;
// if it was with a pilgrim the assignment is closed as lost.
func (s *Service) DecideRetire(ctx context.Context, a Actor, id uuid.UUID, approve bool) error {
	if err := a.require(domain.PermRetireDecide); err != nil {
		return err
	}
	var code string
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var tid int32
		var by uuid.UUID
		var reason, state string
		if err := tx.QueryRow(ctx, `SELECT r.tent_id, r.requested_by, r.reason, r.state::text, t.code FROM retire_requests r
			JOIN tents t ON t.id=r.tent_id WHERE r.id=$1 FOR UPDATE OF r`, id).Scan(&tid, &by, &reason, &state, &code); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return NotFound("درخواست پیدا نشد")
			}
			return err
		}
		if state != "pending" {
			return Conflict("already_decided", "این درخواست قبلاً بررسی شده است", nil)
		}
		if by == a.UserID && a.Role != domain.Admin {
			return Forbidden("درخواست خودتان را نمی‌توانید تأیید کنید")
		}
		newState := "rejected"
		if approve {
			newState = "approved"
			var from domain.Status
			if err := tx.QueryRow(ctx, `SELECT status::text FROM tents WHERE id=$1 FOR UPDATE`, tid).Scan(&from); err != nil {
				return err
			}
			var pil *string
			var asg *string
			if from == domain.WithPilgrim {
				var p, ai uuid.UUID
				err := tx.QueryRow(ctx, `UPDATE assignments SET lost_at=now() WHERE tent_id=$1 AND returned_at IS NULL AND cancelled_at IS NULL AND lost_at IS NULL
					RETURNING id, pilgrim_id`, tid).Scan(&ai, &p)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				if err == nil {
					ps, as := p.String(), ai.String()
					pil, asg = &ps, &as
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE tents SET status='retired', place_id=NULL, updated_at=now() WHERE id=$1`, tid); err != nil {
				return err
			}
			f := string(from)
			if err := s.insertEvents(ctx, tx, a, nil, []evRow{{TentID: tid, Type: "retired", From: &f, To: sp("retired"),
				PilgrimID: pil, AssignmentID: asg, Meta: metaJSON(map[string]string{"reason": reason})}}); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE retire_requests SET state=$2::request_state, decided_by=$3, decided_at=now() WHERE id=$1`, id, newState, a.UserID)
		return err
	})
	if err == nil {
		s.AuditNow(ctx, &a.UserID, a.IP, "retire.decide", "tent", code, map[string]bool{"approved": approve})
		s.publish(ctx, Event{Type: "retired", Count: 1})
	}
	return err
}

// ---- tent registry (admin) ----

// CreateTents registers tent codes (e.g. the CSV produced with the labels).
func (s *Service) CreateTents(ctx context.Context, a Actor, codes []string, kind string, placeID *int16) (int64, error) {
	if err := a.require(domain.PermTentManage); err != nil {
		return 0, err
	}
	list, err := uniqueCodes(codes, 5000)
	if err != nil {
		return 0, err
	}
	if kind == "" {
		kind = "standard"
	}
	if len(kind) > 30 || strings.ContainsAny(kind, "\n\r") {
		return 0, BadRequest("bad_kind", "نوع نامعتبر است")
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO tents(code, kind, place_id) SELECT unnest($1::text[]), $2, $3 ON CONFLICT (code) DO NOTHING`, list, kind, placeID)
	if err != nil {
		return 0, err
	}
	s.AuditNow(ctx, &a.UserID, a.IP, "tent.create_bulk", "tent", "", map[string]any{"requested": len(list), "created": tag.RowsAffected()})
	return tag.RowsAffected(), nil
}

// ReplaceLabel gives a tent a new code (damaged label) and keeps its history.
func (s *Service) ReplaceLabel(ctx context.Context, a Actor, oldCode, newCode string) error {
	if err := a.require(domain.PermTentManage); err != nil {
		return err
	}
	o, ok1 := domain.NormalizeTentCode(oldCode)
	n, ok2 := domain.NormalizeTentCode(newCode)
	if !ok1 || !ok2 {
		return BadRequest("bad_tent_code", "کد چادر نامعتبر است")
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var id int32
		var st string
		err := tx.QueryRow(ctx, `UPDATE tents SET code=$2, updated_at=now() WHERE code=$1 RETURNING id, status::text`, o, n).Scan(&id, &st)
		if errors.Is(err, pgx.ErrNoRows) {
			return NotFound("چادر پیدا نشد")
		}
		if pgCode(err) == "23505" {
			return Conflict("code_taken", "کد جدید قبلاً استفاده شده است", nil)
		}
		if err != nil {
			return err
		}
		return s.insertEvents(ctx, tx, a, nil, []evRow{{TentID: id, Type: "label_replaced", From: &st, To: &st, Meta: metaJSON(map[string]string{"old": o, "new": n})}})
	})
}
