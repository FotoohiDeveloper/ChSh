// Package service contains the business logic. Every state-changing operation
// is one database transaction; invariants are enforced by SQL constraints and
// conditional updates, not by read-then-write races in Go.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"chador/internal/domain"
	"chador/internal/platform"
	"chador/internal/provider"
)

// ---- errors ----

// Error is what handlers turn into an HTTP response. Message is user-facing
// Persian text; Code is stable and meant for client logic.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func newErr(status int, code, msg string, details any) *Error {
	return &Error{Status: status, Code: code, Message: msg, Details: details}
}
func BadRequest(code, msg string) *Error { return newErr(http.StatusBadRequest, code, msg, nil) }
func Forbidden(msg string) *Error        { return newErr(http.StatusForbidden, "forbidden", msg, nil) }
func NotFound(msg string) *Error         { return newErr(http.StatusNotFound, "not_found", msg, nil) }
func Conflict(code, msg string, details any) *Error {
	return newErr(http.StatusConflict, code, msg, details)
}
func Unprocessable(code, msg string) *Error {
	return newErr(http.StatusUnprocessableEntity, code, msg, nil)
}

var (
	ErrUnauthorized = newErr(http.StatusUnauthorized, "unauthorized", "ابتدا وارد شوید", nil)
	ErrNeedShift    = newErr(http.StatusConflict, "shift_required", "ابتدا شیفت را شروع کنید (گیت و درب را انتخاب کنید)", nil)
)

// ---- actor ----

// Actor is the authenticated caller plus the shift context the operation is
// recorded under.
type Actor struct {
	UserID   uuid.UUID
	Username string
	FullName string
	Role     domain.Role
	ShiftID  *uuid.UUID
	PlaceID  *int16
	DoorID   *int16
	IP       string
	TokenID  string
}

func (a Actor) Can(p domain.Perm) bool { return domain.Can(a.Role, p) }

func (a Actor) require(p domain.Perm) error {
	if !a.Can(p) {
		return Forbidden("دسترسی لازم برای این کار را ندارید")
	}
	return nil
}

// requireShift is for operations recorded against a gate/place.
func (a Actor) requireShift() error {
	if a.ShiftID == nil || a.PlaceID == nil {
		return ErrNeedShift
	}
	return nil
}

// ---- queue ----

type SMSJob struct {
	PilgrimID uuid.UUID         `json:"pilgrim_id"`
	Template  string            `json:"template"`
	DedupeKey string            `json:"dedupe_key"`
	Params    map[string]string `json:"params,omitempty"`
}

// Queue is implemented by the jobs package (Redis-backed).
type Queue interface {
	SMS(ctx context.Context, j SMSJob) error
	VerifyPilgrim(ctx context.Context, id uuid.UUID, delay time.Duration) error
}

type noQueue struct{}

func (noQueue) SMS(context.Context, SMSJob) error                             { return nil }
func (noQueue) VerifyPilgrim(context.Context, uuid.UUID, time.Duration) error { return nil }

// ---- service ----

type Service struct {
	db     *pgxpool.Pool
	rdb    *redis.Client
	cfg    platform.Config
	crypto *platform.Crypto
	inq    provider.Inquirer
	q      Queue
	log    *slog.Logger

	auditCh  chan auditRec
	auditWG  sync.WaitGroup
	settings *settingsCache
	dash     *ttlCache[*Dashboard]
}

type Deps struct {
	DB      *pgxpool.Pool
	Redis   *redis.Client
	Config  platform.Config
	Crypto  *platform.Crypto
	Inquiry provider.Inquirer
	Queue   Queue
	Log     *slog.Logger
}

func New(d Deps) *Service {
	if d.Queue == nil {
		d.Queue = noQueue{}
	}
	s := &Service{
		db: d.DB, rdb: d.Redis, cfg: d.Config, crypto: d.Crypto, inq: d.Inquiry, q: d.Queue, log: d.Log,
		auditCh:  make(chan auditRec, 4096),
		settings: newSettingsCache(),
		dash:     newTTLCache[*Dashboard](2 * time.Second),
	}
	s.auditWG.Add(1)
	go s.auditWriter()
	return s
}

// Close flushes buffered audit records.
func (s *Service) Close() {
	close(s.auditCh)
	s.auditWG.Wait()
}

func (s *Service) DB() *pgxpool.Pool { return s.db }

// ---- transactions & idempotency ----

// inTx runs fn in a transaction (read committed, which is enough because every
// invariant is a constraint or a conditional update).
func (s *Service) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// runOp executes fn exactly once per opID. A repeated opID (client retry or
// offline replay) returns the stored result without touching anything. opID
// is optional; without it fn simply runs.
func runOp[T any](ctx context.Context, s *Service, a Actor, kind string, opID *uuid.UUID, fn func(tx pgx.Tx) (T, error)) (res T, replayed bool, err error) {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return res, false, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	if opID != nil {
		tag, e := tx.Exec(ctx, `INSERT INTO client_ops(op_id, user_id, kind) VALUES ($1,$2,$3) ON CONFLICT (op_id) DO NOTHING`,
			*opID, a.UserID, kind)
		if e != nil {
			return res, false, e
		}
		if tag.RowsAffected() == 0 {
			var uid uuid.UUID
			var k string
			var raw []byte
			if e := tx.QueryRow(ctx, `SELECT user_id, kind, result FROM client_ops WHERE op_id=$1`, *opID).Scan(&uid, &k, &raw); e != nil {
				return res, false, e
			}
			if uid != a.UserID || k != kind {
				return res, false, Conflict("op_id_reused", "شناسه‌ی عملیات قبلاً برای کار دیگری استفاده شده", nil)
			}
			if raw == nil {
				return res, false, Conflict("op_in_progress", "این عملیات در حال انجام است، چند لحظه بعد دوباره تلاش کنید", nil)
			}
			_ = tx.Rollback(ctx)
			if e := json.Unmarshal(raw, &res); e != nil {
				return res, false, e
			}
			return res, true, nil
		}
	}
	res, err = fn(tx)
	if err != nil {
		return res, false, err
	}
	if opID != nil {
		raw, e := json.Marshal(res)
		if e != nil {
			err = e
			return res, false, e
		}
		if _, e := tx.Exec(ctx, `UPDATE client_ops SET result=$2 WHERE op_id=$1`, *opID, raw); e != nil {
			err = e
			return res, false, e
		}
	}
	if e := tx.Commit(ctx); e != nil {
		err = e
		return res, false, e
	}
	return res, false, nil
}

// ---- realtime events (SSE fan-out via Redis pub/sub) ----

type Event struct {
	Type    string `json:"type"`
	PlaceID *int16 `json:"place_id,omitempty"`
	Count   int    `json:"count,omitempty"`
}

const EventsChannel = "chador:events"

func (s *Service) publish(ctx context.Context, e Event) {
	b, _ := json.Marshal(e)
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 300*time.Millisecond)
	defer cancel()
	if err := s.rdb.Publish(pctx, EventsChannel, b).Err(); err != nil {
		s.log.Warn("publish event failed", "err", err)
	}
	s.dash.Invalidate()
}

// ---- audit ----

type auditRec struct {
	userID   *uuid.UUID
	action   string
	entity   string
	entityID string
	ip       string
	meta     []byte
}

// Audit queues a record; a background writer batches them with COPY. Used for
// high-volume, low-criticality events (viewing personal data).
func (s *Service) Audit(a Actor, action, entity, entityID string, meta any) {
	rec := s.newRec(a.UserID, a.IP, action, entity, entityID, meta)
	select {
	case s.auditCh <- rec:
	default:
		s.log.Error("audit buffer full, writing synchronously", "action", action)
		s.writeAudit(context.Background(), []auditRec{rec})
	}
}

// AuditNow writes immediately (security-relevant events: login, user changes).
func (s *Service) AuditNow(ctx context.Context, userID *uuid.UUID, ip, action, entity, entityID string, meta any) {
	rec := s.newRec(uuid.Nil, ip, action, entity, entityID, meta)
	rec.userID = userID
	s.writeAudit(context.WithoutCancel(ctx), []auditRec{rec})
}

func (s *Service) newRec(uid uuid.UUID, ip, action, entity, entityID string, meta any) auditRec {
	rec := auditRec{action: action, entity: entity, entityID: entityID, ip: ip}
	if uid != uuid.Nil {
		u := uid
		rec.userID = &u
	}
	if meta != nil {
		rec.meta, _ = json.Marshal(meta)
	}
	return rec
}

func (s *Service) auditWriter() {
	defer s.auditWG.Done()
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	var buf []auditRec
	flush := func() {
		if len(buf) > 0 {
			s.writeAudit(context.Background(), buf)
			buf = buf[:0]
		}
	}
	for {
		select {
		case r, ok := <-s.auditCh:
			if !ok {
				flush()
				return
			}
			buf = append(buf, r)
			if len(buf) >= 200 {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
}

func (s *Service) writeAudit(ctx context.Context, recs []auditRec) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows := make([][]any, len(recs))
	for i, r := range recs {
		var uid any
		if r.userID != nil {
			uid = *r.userID
		}
		var meta any
		if r.meta != nil {
			meta = r.meta
		}
		rows[i] = []any{uid, r.action, r.entity, r.entityID, r.ip, meta}
	}
	_, err := s.db.CopyFrom(ctx, pgx.Identifier{"audit_log"},
		[]string{"user_id", "action", "entity", "entity_id", "ip", "meta"}, pgx.CopyFromRows(rows))
	if err != nil {
		s.log.Error("audit write failed", "err", err, "records", len(recs))
	}
}

// ---- settings ----

type settingsCache struct {
	mu      sync.RWMutex
	vals    map[string]json.RawMessage
	fetched time.Time
}

func newSettingsCache() *settingsCache { return &settingsCache{} }

var defaultSettings = map[string]any{
	"socks_unit_price":      0,
	"max_tents_per_request": 20,
	"sms_templates": map[string]string{
		"delivered": "زائر گرامی، چادر شما با موفقیت تحویل گردید. لطفاً در حفظ و نگهداری آن تا زمان عودت همکاری فرمایید.",
		"returned":  "زائر گرامی، عودت چادر شما با موفقیت ثبت و تأیید شد. از همکاری و همراهی شما سپاسگزاریم.",
		"reminder":  "زائر گرامی، طبق اطلاعات ثبت‌شده، عودت چادر شما هنوز در سامانه ثبت نشده است. لطفاً جهت تعیین تکلیف، در اولین فرصت به گیت مربوطه مراجعه فرمایید.",
	},
}

func (s *Service) loadSettings(ctx context.Context) (map[string]json.RawMessage, error) {
	s.settings.mu.RLock()
	if s.settings.vals != nil && time.Since(s.settings.fetched) < 30*time.Second {
		v := s.settings.vals
		s.settings.mu.RUnlock()
		return v, nil
	}
	s.settings.mu.RUnlock()
	rows, err := s.db.Query(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	vals := map[string]json.RawMessage{}
	for rows.Next() {
		var k string
		var v []byte
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		vals[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.settings.mu.Lock()
	s.settings.vals, s.settings.fetched = vals, time.Now()
	s.settings.mu.Unlock()
	return vals, nil
}

// Setting decodes a setting into out, falling back to its built-in default.
func (s *Service) Setting(ctx context.Context, key string, out any) error {
	vals, err := s.loadSettings(ctx)
	if err != nil {
		return err
	}
	if raw, ok := vals[key]; ok {
		return json.Unmarshal(raw, out)
	}
	def, ok := defaultSettings[key]
	if !ok {
		return fmt.Errorf("unknown setting %q", key)
	}
	b, _ := json.Marshal(def)
	return json.Unmarshal(b, out)
}

func (s *Service) SettingInt(ctx context.Context, key string) int {
	var n int
	if err := s.Setting(ctx, key, &n); err != nil {
		s.log.Error("read setting", "key", key, "err", err)
	}
	return n
}

func (s *Service) AllSettings(ctx context.Context) (map[string]any, error) {
	out := map[string]any{}
	for k := range defaultSettings {
		var v any
		if err := s.Setting(ctx, k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

func (s *Service) PutSetting(ctx context.Context, a Actor, key string, value json.RawMessage) error {
	if err := a.require(domain.PermSettings); err != nil {
		return err
	}
	if _, ok := defaultSettings[key]; !ok {
		return BadRequest("unknown_setting", "تنظیم ناشناخته")
	}
	switch key {
	case "socks_unit_price", "max_tents_per_request":
		var n int
		if err := json.Unmarshal(value, &n); err != nil || n < 0 || n > 1_000_000_000 {
			return BadRequest("bad_value", "مقدار نامعتبر")
		}
	case "sms_templates":
		var m map[string]string
		if err := json.Unmarshal(value, &m); err != nil {
			return BadRequest("bad_value", "مقدار نامعتبر")
		}
		for k, v := range m {
			if _, ok := defaultSettings["sms_templates"].(map[string]string)[k]; !ok || len([]rune(v)) > 500 || v == "" {
				return BadRequest("bad_value", "قالب پیامک نامعتبر")
			}
		}
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO settings(key,value,updated_by) VALUES ($1,$2,$3)
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now(), updated_by=EXCLUDED.updated_by`,
		key, []byte(value), a.UserID); err != nil {
		return err
	}
	s.settings.mu.Lock()
	s.settings.vals = nil
	s.settings.mu.Unlock()
	s.AuditNow(ctx, &a.UserID, a.IP, "settings.update", "settings", key, nil)
	return nil
}

// ---- small TTL cache with request coalescing ----

type ttlCache[T any] struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]cacheEntry[T]
	flights map[string]*flight[T]
}
type cacheEntry[T any] struct {
	v   T
	exp time.Time
}
type flight[T any] struct {
	wg  sync.WaitGroup
	v   T
	err error
}

func newTTLCache[T any](ttl time.Duration) *ttlCache[T] {
	return &ttlCache[T]{ttl: ttl, entries: map[string]cacheEntry[T]{}, flights: map[string]*flight[T]{}}
}

func (c *ttlCache[T]) Invalidate() {
	c.mu.Lock()
	c.entries = map[string]cacheEntry[T]{}
	c.mu.Unlock()
}

// Get returns a fresh cached value or computes it once for all concurrent callers.
func (c *ttlCache[T]) Get(key string, load func() (T, error)) (T, error) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && time.Now().Before(e.exp) {
		c.mu.Unlock()
		return e.v, nil
	}
	if f, ok := c.flights[key]; ok {
		c.mu.Unlock()
		f.wg.Wait()
		return f.v, f.err
	}
	f := &flight[T]{}
	f.wg.Add(1)
	c.flights[key] = f
	c.mu.Unlock()

	f.v, f.err = load()

	c.mu.Lock()
	delete(c.flights, key)
	if f.err == nil {
		c.entries[key] = cacheEntry[T]{f.v, time.Now().Add(c.ttl)}
	}
	c.mu.Unlock()
	f.wg.Done()
	return f.v, f.err
}

// pgCode extracts a Postgres SQLSTATE if err carries one.
func pgCode(err error) string {
	var pe interface{ SQLState() string }
	if errors.As(err, &pe) {
		return pe.SQLState()
	}
	return ""
}
