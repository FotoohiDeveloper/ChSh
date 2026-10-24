package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"chador/internal/domain"
)

// ---- places & doors ----

type PlaceView struct {
	ID    int16      `json:"id"`
	Kind  string     `json:"kind"`
	Name  string     `json:"name"`
	Doors []DoorView `json:"doors"`
}

type DoorView struct {
	ID   int16  `json:"id"`
	Name string `json:"name"`
}

func (s *Service) ListPlaces(ctx context.Context) ([]PlaceView, error) {
	rows, err := s.db.Query(ctx, `SELECT p.id, p.kind::text, p.name, d.id, d.name FROM places p
		LEFT JOIN doors d ON d.place_id=p.id AND d.active WHERE p.active ORDER BY p.id, d.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlaceView{}
	for rows.Next() {
		var p PlaceView
		var did *int16
		var dname *string
		if err := rows.Scan(&p.ID, &p.Kind, &p.Name, &did, &dname); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].ID != p.ID {
			p.Doors = []DoorView{}
			out = append(out, p)
		}
		if did != nil {
			last := &out[len(out)-1]
			last.Doors = append(last.Doors, DoorView{*did, *dname})
		}
	}
	return out, rows.Err()
}

func (s *Service) CreatePlace(ctx context.Context, a Actor, kind, name string) (int16, error) {
	if err := a.require(domain.PermPlaces); err != nil {
		return 0, err
	}
	if !domain.PlaceKind(kind).Valid() || name == "" || len([]rune(name)) > 80 {
		return 0, BadRequest("bad_place", "مشخصات مکان نامعتبر است")
	}
	var id int16
	err := s.db.QueryRow(ctx, `INSERT INTO places(kind, name) VALUES ($1::place_kind,$2) RETURNING id`, kind, name).Scan(&id)
	if pgCode(err) == "23505" {
		return 0, Conflict("name_taken", "این نام قبلاً ثبت شده است", nil)
	}
	if err == nil {
		s.AuditNow(ctx, &a.UserID, a.IP, "place.create", "place", name, nil)
	}
	return id, err
}

func (s *Service) CreateDoor(ctx context.Context, a Actor, placeID int16, name string) (int16, error) {
	if err := a.require(domain.PermPlaces); err != nil {
		return 0, err
	}
	if name == "" || len([]rune(name)) > 80 {
		return 0, BadRequest("bad_door", "نام درب نامعتبر است")
	}
	var id int16
	err := s.db.QueryRow(ctx, `INSERT INTO doors(place_id, name) VALUES ($1,$2) RETURNING id`, placeID, name).Scan(&id)
	if pgCode(err) == "23505" {
		return 0, Conflict("name_taken", "این درب قبلاً ثبت شده است", nil)
	}
	if pgCode(err) == "23503" {
		return 0, NotFound("مکان پیدا نشد")
	}
	if err == nil {
		s.AuditNow(ctx, &a.UserID, a.IP, "door.create", "door", name, nil)
	}
	return id, err
}

func (s *Service) SetPlaceActive(ctx context.Context, a Actor, placeID int16, active bool) error {
	if err := a.require(domain.PermPlaces); err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE places SET active=$2 WHERE id=$1`, placeID, active)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return NotFound("مکان پیدا نشد")
	}
	s.AuditNow(ctx, &a.UserID, a.IP, "place.active", "place", "", map[string]any{"id": placeID, "active": active})
	return nil
}

// ---- socks ledger ----

type SocksReq struct {
	Kind      string     `json:"kind"` // supply | issue | return
	Qty       int        `json:"qty"`
	PlaceID   *int16     `json:"place_id"`   // supply only: the gate that receives them
	PilgrimID *uuid.UUID `json:"pilgrim_id"` // issue only
	OpID      *uuid.UUID `json:"op_id"`
	ClientTS  *time.Time `json:"client_ts"`
}

type SocksRes struct {
	Kind    string `json:"kind"`
	Qty     int    `json:"qty"`
	Amount  int64  `json:"amount"`
	Balance int    `json:"balance"`
}

func (s *Service) Socks(ctx context.Context, a Actor, req SocksReq) (SocksRes, bool, error) {
	var zero SocksRes
	if req.Qty <= 0 || req.Qty > 5000 {
		return zero, false, BadRequest("bad_qty", "تعداد نامعتبر است")
	}
	place := a.PlaceID
	switch req.Kind {
	case "supply":
		if err := a.require(domain.PermSocksSupply); err != nil {
			return zero, false, err
		}
		if req.PlaceID == nil {
			return zero, false, BadRequest("place_required", "گیت دریافت‌کننده را مشخص کنید")
		}
		place = req.PlaceID
	case "issue", "return":
		if err := a.require(domain.PermSocks); err != nil {
			return zero, false, err
		}
		if err := a.requireShift(); err != nil {
			return zero, false, err
		}
		if req.Kind == "issue" && req.PilgrimID == nil {
			return zero, false, BadRequest("pilgrim_required", "زائر را مشخص کنید")
		}
	default:
		return zero, false, BadRequest("bad_kind", "نوع نامعتبر است")
	}
	unit := int64(0)
	if req.Kind == "issue" {
		unit = int64(s.SettingInt(ctx, "socks_unit_price"))
	}
	res, replayed, err := runOp(ctx, s, a, "socks", req.OpID, func(tx pgx.Tx) (SocksRes, error) {
		if req.Kind != "supply" {
			// leftover can never go negative
			var bal int
			if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(CASE kind WHEN 'supply' THEN qty ELSE -qty END),0)::int FROM socks_ledger WHERE place_id=$1`, place).Scan(&bal); err != nil {
				return zero, err
			}
			if bal < req.Qty {
				return zero, Conflict("insufficient_socks", "موجودی جوراب این گیت کافی نیست", map[string]int{"balance": bal})
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO socks_ledger(kind, place_id, shift_id, user_id, pilgrim_id, qty, unit_price, amount)
			VALUES ($1::socks_kind,$2,$3,$4,$5,$6,$7,$8)`, req.Kind, place, a.ShiftID, a.UserID, req.PilgrimID, req.Qty, unit, unit*int64(req.Qty))
		if err != nil {
			if pgCode(err) == "23503" {
				return zero, NotFound("زائر یا گیت پیدا نشد")
			}
			return zero, err
		}
		var bal int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(CASE kind WHEN 'supply' THEN qty ELSE -qty END),0)::int FROM socks_ledger WHERE place_id=$1`, place).Scan(&bal); err != nil {
			return zero, err
		}
		return SocksRes{Kind: req.Kind, Qty: req.Qty, Amount: unit * int64(req.Qty), Balance: bal}, nil
	})
	return res, replayed, err
}

// ---- shift handover ----

type HandoverReq struct {
	ToUser uuid.UUID `json:"to_user"`
	Notes  string    `json:"notes"`
}

type HandoverView struct {
	ID        uuid.UUID      `json:"id"`
	Place     string         `json:"place"`
	From      string         `json:"from"`
	To        string         `json:"to"`
	Snapshot  map[string]any `json:"snapshot"`
	Notes     string         `json:"notes,omitempty"`
	State     string         `json:"state"`
	CreatedAt time.Time      `json:"created_at"`
}

func (s *Service) placeSnapshot(ctx context.Context, tx pgx.Tx, placeID int16) (map[string]any, error) {
	snap := map[string]any{}
	rows, err := tx.Query(ctx, `SELECT status::text, count(*)::int FROM tents WHERE place_id=$1 GROUP BY 1`, placeID)
	if err != nil {
		return nil, err
	}
	tents := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return nil, err
		}
		tents[st] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var socks, out int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(CASE kind WHEN 'supply' THEN qty ELSE -qty END),0)::int FROM socks_ledger WHERE place_id=$1`, placeID).Scan(&socks); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM assignments WHERE assigned_place_id=$1 AND returned_at IS NULL AND cancelled_at IS NULL AND lost_at IS NULL`, placeID).Scan(&out); err != nil {
		return nil, err
	}
	snap["tents_by_status"], snap["socks_balance"], snap["tents_out_from_here"] = tents, socks, out
	return snap, nil
}

func (s *Service) CreateHandover(ctx context.Context, a Actor, req HandoverReq) (*HandoverView, error) {
	if err := a.require(domain.PermHandover); err != nil {
		return nil, err
	}
	if err := a.requireShift(); err != nil {
		return nil, err
	}
	if req.ToUser == a.UserID || len(req.Notes) > 1000 {
		return nil, BadRequest("bad_handover", "تحویل‌گیرنده یا توضیحات نامعتبر است")
	}
	var id uuid.UUID
	var snap map[string]any
	var created time.Time
	var toName string
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var role domain.Role
		var active bool
		if err := tx.QueryRow(ctx, `SELECT full_name, role, active FROM users WHERE id=$1`, req.ToUser).Scan(&toName, &role, &active); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return NotFound("تحویل‌گیرنده پیدا نشد")
			}
			return err
		}
		if !active || (role != domain.GateLead && role != domain.Server) {
			return BadRequest("bad_handover", "تحویل‌گیرنده باید مسئول گیت یا خادم فعال باشد")
		}
		var err error
		if snap, err = s.placeSnapshot(ctx, tx, *a.PlaceID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO handovers(place_id, from_user, to_user, snapshot, notes) VALUES ($1,$2,$3,$4,NULLIF($5,''))
			RETURNING id, created_at`, *a.PlaceID, a.UserID, req.ToUser, snap, req.Notes).Scan(&id, &created)
	})
	if err != nil {
		return nil, err
	}
	s.Audit(a, "handover.create", "handover", id.String(), nil)
	return &HandoverView{ID: id, From: a.FullName, To: toName, Snapshot: snap, Notes: req.Notes, State: "pending", CreatedAt: created}, nil
}

func (s *Service) ResolveHandover(ctx context.Context, a Actor, id uuid.UUID, accept bool) error {
	state := "disputed"
	if accept {
		state = "accepted"
	}
	tag, err := s.db.Exec(ctx, `UPDATE handovers SET state=$3, resolved_at=now() WHERE id=$1 AND to_user=$2 AND state='pending'`, id, a.UserID, state)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return NotFound("تحویلی در انتظار تأیید شما پیدا نشد")
	}
	s.Audit(a, "handover."+state, "handover", id.String(), nil)
	return nil
}

func (s *Service) PendingHandovers(ctx context.Context, a Actor) ([]HandoverView, error) {
	rows, err := s.db.Query(ctx, `SELECT h.id, p.name, f.full_name, t.full_name, h.snapshot, COALESCE(h.notes,''), h.state, h.created_at
		FROM handovers h JOIN places p ON p.id=h.place_id JOIN users f ON f.id=h.from_user JOIN users t ON t.id=h.to_user
		WHERE (h.to_user=$1 OR h.from_user=$1) AND h.created_at > now() - interval '3 days' ORDER BY h.created_at DESC LIMIT 20`, a.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HandoverView{}
	for rows.Next() {
		var v HandoverView
		if err := rows.Scan(&v.ID, &v.Place, &v.From, &v.To, &v.Snapshot, &v.Notes, &v.State, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---- offline sync ----

type SyncOp struct {
	OpID     uuid.UUID       `json:"op_id"`
	Type     string          `json:"type"` // assign | return | cancel | status | move | socks
	ShiftID  *uuid.UUID      `json:"shift_id"`
	ClientTS *time.Time      `json:"client_ts"`
	Payload  json.RawMessage `json:"payload"`
}

type SyncResult struct {
	OpID   uuid.UUID `json:"op_id"`
	Status string    `json:"status"` // ok | replayed | rejected | retry
	Result any       `json:"result,omitempty"`
	Error  *Error    `json:"error,omitempty"`
}

const maxSyncOps = 200

// Sync applies a queue of operations recorded while the device was offline,
// in order. Each op is idempotent on op_id, so re-sending the whole queue
// after a dropped connection is always safe. "rejected" means the business
// rule said no (do not resend); "retry" means a transient failure.
func (s *Service) Sync(ctx context.Context, a Actor, ops []SyncOp) ([]SyncResult, error) {
	if err := a.require(domain.PermSync); err != nil {
		return nil, err
	}
	if len(ops) == 0 || len(ops) > maxSyncOps {
		return nil, BadRequest("bad_sync", "تعداد عملیات نامعتبر است")
	}
	out := make([]SyncResult, 0, len(ops))
	for _, op := range ops {
		r := SyncResult{OpID: op.OpID}
		actor := a
		if op.ShiftID != nil {
			var err error
			if actor, err = s.ActorForShift(ctx, a, *op.ShiftID); err != nil {
				r.Status, r.Error = classify(err)
				out = append(out, r)
				continue
			}
		}
		res, replayed, err := s.applyOp(ctx, actor, op)
		switch {
		case err != nil:
			r.Status, r.Error = classify(err)
		case replayed:
			r.Status, r.Result = "replayed", res
		default:
			r.Status, r.Result = "ok", res
		}
		out = append(out, r)
	}
	return out, nil
}

func classify(err error) (string, *Error) {
	var e *Error
	if errors.As(err, &e) {
		if e.Status >= 500 {
			return "retry", e
		}
		return "rejected", e
	}
	return "retry", newErr(503, "temporary", "خطای موقت؛ دوباره تلاش می‌شود", nil)
}

func (s *Service) applyOp(ctx context.Context, a Actor, op SyncOp) (any, bool, error) {
	id := op.OpID
	dec := func(v any) error {
		d := json.NewDecoder(bytes.NewReader(op.Payload))
		d.DisallowUnknownFields()
		if err := d.Decode(v); err != nil {
			return BadRequest("bad_payload", "داده‌ی عملیات نامعتبر است")
		}
		return nil
	}
	switch op.Type {
	case "assign":
		var r AssignReq
		if err := dec(&r); err != nil {
			return nil, false, err
		}
		r.OpID, r.ClientTS = &id, op.ClientTS
		return s.Assign(ctx, a, r)
	case "return":
		var r ReturnReq
		if err := dec(&r); err != nil {
			return nil, false, err
		}
		r.OpID, r.ClientTS = &id, op.ClientTS
		return s.Return(ctx, a, r)
	case "cancel":
		var r struct {
			Code string `json:"code"`
		}
		if err := dec(&r); err != nil {
			return nil, false, err
		}
		return s.CancelAssignment(ctx, a, r.Code, &id)
	case "status":
		var r StatusReq
		if err := dec(&r); err != nil {
			return nil, false, err
		}
		r.OpID, r.ClientTS = &id, op.ClientTS
		return s.SetStatus(ctx, a, r)
	case "move":
		var r MoveReq
		if err := dec(&r); err != nil {
			return nil, false, err
		}
		r.OpID, r.ClientTS = &id, op.ClientTS
		return s.Move(ctx, a, r)
	case "socks":
		var r SocksReq
		if err := dec(&r); err != nil {
			return nil, false, err
		}
		r.OpID, r.ClientTS = &id, op.ClientTS
		return s.Socks(ctx, a, r)
	}
	return nil, false, BadRequest("bad_op", "نوع عملیات ناشناخته: "+op.Type)
}
