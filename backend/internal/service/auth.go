package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"chador/internal/domain"
	"chador/internal/platform"
)

// Session is what lives in Redis for an access token. Keeping role and shift
// here means authenticating a request needs no database query.
type Session struct {
	UserID     uuid.UUID   `json:"u"`
	Username   string      `json:"n"`
	FullName   string      `json:"f"`
	Role       domain.Role `json:"r"`
	MustChange bool        `json:"mc,omitempty"`
	ShiftID    *uuid.UUID  `json:"s,omitempty"`
	PlaceID    *int16      `json:"p,omitempty"`
	DoorID     *int16      `json:"d,omitempty"`
}

func (s Session) Actor(tokenID, ip string) Actor {
	return Actor{UserID: s.UserID, Username: s.Username, FullName: s.FullName, Role: s.Role,
		ShiftID: s.ShiftID, PlaceID: s.PlaceID, DoorID: s.DoorID, IP: ip, TokenID: tokenID}
}

type TokenPair struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type UserView struct {
	ID         uuid.UUID   `json:"id"`
	Username   string      `json:"username"`
	FullName   string      `json:"full_name"`
	Role       domain.Role `json:"role"`
	Active     bool        `json:"active"`
	MustChange bool        `json:"must_change_password"`
	Shift      *ShiftView  `json:"shift,omitempty"`
}

type ShiftView struct {
	ID        uuid.UUID `json:"id"`
	PlaceID   int16     `json:"place_id"`
	PlaceName string    `json:"place_name"`
	DoorID    *int16    `json:"door_id,omitempty"`
	DoorName  string    `json:"door_name,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

const (
	maxLoginFailures = 5
	lockDuration     = 15 * time.Minute
	failWindow       = 15 * time.Minute
)

func sessKey(tid string) string      { return "sess:" + tid }
func refreshKey(tid string) string   { return "refresh:" + tid }
func userSetKey(id uuid.UUID) string { return "usess:" + id.String() }

// ---- login ----

func (s *Service) Login(ctx context.Context, username, password, ip string) (*TokenPair, *UserView, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	bad := newErr(401, "invalid_credentials", "شناسه یا رمز عبور درست نیست", nil)

	if locked, err := s.loginLocked(ctx, username, ip); err != nil {
		return nil, nil, err
	} else if locked {
		return nil, nil, newErr(429, "too_many_attempts", "تلاش‌های ناموفق زیاد بود؛ چند دقیقه بعد دوباره تلاش کنید", nil)
	}

	var (
		id     uuid.UUID
		hash   string
		active bool
		u      UserView
	)
	err := s.db.QueryRow(ctx, `SELECT id, password_hash, active, full_name, role, must_change_password FROM users WHERE username=$1`,
		username).Scan(&id, &hash, &active, &u.FullName, &u.Role, &u.MustChange)
	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, err
	}
	if !found {
		hash = platform.DummyHash // equalise timing for unknown usernames
	}
	ok := platform.CheckPassword(password, hash)
	if !found || !ok || !active {
		s.recordLoginFailure(ctx, username, ip)
		var uid *uuid.UUID
		if found {
			uid = &id
		}
		s.AuditNow(ctx, uid, ip, "auth.login_failed", "user", username, nil)
		return nil, nil, bad
	}
	s.clearLoginFailures(ctx, username)
	u.ID, u.Username, u.Active = id, username, true

	sess := Session{UserID: id, Username: username, FullName: u.FullName, Role: u.Role, MustChange: u.MustChange}
	shift, err := s.openShift(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if shift != nil {
		sess.ShiftID, sess.PlaceID, sess.DoorID = &shift.ID, &shift.PlaceID, shift.DoorID
		u.Shift = shift
	}
	pair, err := s.issueTokens(ctx, sess)
	if err != nil {
		return nil, nil, err
	}
	_, _ = s.db.Exec(ctx, `UPDATE users SET last_login_at=now() WHERE id=$1`, id)
	s.AuditNow(ctx, &id, ip, "auth.login", "user", id.String(), nil)
	return pair, &u, nil
}

func (s *Service) issueTokens(ctx context.Context, sess Session) (*TokenPair, error) {
	access, refresh := platform.RandomToken(32), platform.RandomToken(32)
	at, rt := platform.TokenID(access), platform.TokenID(refresh)
	b, _ := json.Marshal(sess)
	exp := time.Now().Add(s.cfg.AccessTTL)
	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, sessKey(at), b, s.cfg.AccessTTL)
	pipe.Set(ctx, refreshKey(rt), sess.UserID.String(), s.cfg.RefreshTTL)
	pipe.SAdd(ctx, userSetKey(sess.UserID), at, rt)
	pipe.Expire(ctx, userSetKey(sess.UserID), s.cfg.RefreshTTL+time.Hour)
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}
	return &TokenPair{AccessToken: access, RefreshToken: refresh, ExpiresAt: exp}, nil
}

// Authenticate resolves a bearer token and counts the request against the
// per-session rate limit, all in a single Redis round trip.
func (s *Service) Authenticate(ctx context.Context, token string, perSecond int64) (sess Session, tid string, limited bool, err error) {
	tid = platform.TokenID(token)
	pipe := s.rdb.Pipeline()
	get := pipe.Get(ctx, sessKey(tid))
	incr := pipe.Incr(ctx, "rl:t:"+tid)
	pipe.ExpireNX(ctx, "rl:t:"+tid, time.Second)
	_, _ = pipe.Exec(ctx) // per-command errors are read below
	raw, err := get.Bytes()
	if errors.Is(err, redis.Nil) {
		return sess, "", false, ErrUnauthorized
	}
	if err != nil {
		return sess, "", false, err
	}
	if err := json.Unmarshal(raw, &sess); err != nil {
		return sess, "", false, ErrUnauthorized
	}
	return sess, tid, incr.Err() == nil && incr.Val() > perSecond, nil
}

// Subscribe returns a pub/sub subscription to realtime events.
func (s *Service) Subscribe(ctx context.Context) *redis.PubSub {
	return s.rdb.Subscribe(ctx, EventsChannel)
}

// Ready reports whether the database and Redis answer.
func (s *Service) Ready(ctx context.Context) error {
	if err := s.db.Ping(ctx); err != nil {
		return fmt.Errorf("database: %w", err)
	}
	if err := s.rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	return nil
}

func (s *Service) PoolStats() (acquired, idle, max int32) {
	st := s.db.Stat()
	return st.AcquiredConns(), st.IdleConns(), st.MaxConns()
}

// Refresh rotates the refresh token (single use) and rebuilds the session from
// the database, so role changes, deactivation and shift changes take effect.
func (s *Service) Refresh(ctx context.Context, refresh, ip string) (*TokenPair, error) {
	rt := platform.TokenID(refresh)
	val, err := s.rdb.GetDel(ctx, refreshKey(rt)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	id, err := uuid.Parse(val)
	if err != nil {
		return nil, ErrUnauthorized
	}
	_ = s.rdb.SRem(ctx, userSetKey(id), rt).Err()
	var sess Session
	var active bool
	err = s.db.QueryRow(ctx, `SELECT username, full_name, role, must_change_password, active FROM users WHERE id=$1`, id).
		Scan(&sess.Username, &sess.FullName, &sess.Role, &sess.MustChange, &active)
	if err != nil || !active {
		return nil, ErrUnauthorized
	}
	sess.UserID = id
	shift, err := s.openShift(ctx, id)
	if err != nil {
		return nil, err
	}
	if shift != nil {
		sess.ShiftID, sess.PlaceID, sess.DoorID = &shift.ID, &shift.PlaceID, shift.DoorID
	}
	return s.issueTokens(ctx, sess)
}

func (s *Service) Logout(ctx context.Context, a Actor, refresh string) {
	keys := []string{sessKey(a.TokenID)}
	if refresh != "" {
		keys = append(keys, refreshKey(platform.TokenID(refresh)))
	}
	_ = s.rdb.Del(ctx, keys...).Err()
	_ = s.rdb.SRem(ctx, userSetKey(a.UserID), a.TokenID).Err()
	s.AuditNow(ctx, &a.UserID, a.IP, "auth.logout", "user", a.UserID.String(), nil)
}

// RevokeUser kills every session and refresh token of a user immediately.
func (s *Service) RevokeUser(ctx context.Context, id uuid.UUID) error {
	ids, err := s.rdb.SMembers(ctx, userSetKey(id)).Result()
	if err != nil {
		return err
	}
	keys := []string{userSetKey(id)}
	for _, t := range ids {
		keys = append(keys, sessKey(t), refreshKey(t))
	}
	return s.rdb.Del(ctx, keys...).Err()
}

// ---- brute-force protection ----

func (s *Service) loginLocked(ctx context.Context, username, ip string) (bool, error) {
	n, err := s.rdb.Exists(ctx, "lock:u:"+username, "lock:ip:"+ip).Result()
	return n > 0, err
}

func (s *Service) recordLoginFailure(ctx context.Context, username, ip string) {
	for _, k := range []struct {
		counter, lock string
		limit         int64
	}{
		{"lf:u:" + username, "lock:u:" + username, maxLoginFailures},
		{"lf:ip:" + ip, "lock:ip:" + ip, maxLoginFailures * 6}, // many users behind one gate router
	} {
		n, err := s.rdb.Incr(ctx, k.counter).Result()
		if err != nil {
			continue
		}
		if n == 1 {
			s.rdb.Expire(ctx, k.counter, failWindow)
		}
		if n >= k.limit {
			s.rdb.Set(ctx, k.lock, 1, lockDuration)
			s.rdb.Del(ctx, k.counter)
		}
	}
}

func (s *Service) clearLoginFailures(ctx context.Context, username string) {
	s.rdb.Del(ctx, "lf:u:"+username)
}

// RateAllow is a per-key fixed-window limiter (one Redis round trip).
func (s *Service) RateAllow(ctx context.Context, key string, limit int64, window time.Duration) bool {
	pipe := s.rdb.Pipeline()
	incr := pipe.Incr(ctx, "rl:"+key)
	pipe.ExpireNX(ctx, "rl:"+key, window)
	if _, err := pipe.Exec(ctx); err != nil {
		return true // fail open: Redis hiccups must not stop the gates
	}
	return incr.Val() <= limit
}

// ---- password ----

func validPassword(p string) error {
	if len([]rune(p)) < 10 {
		return BadRequest("weak_password", "رمز عبور باید حداقل ۱۰ نویسه باشد")
	}
	if len(p) > 128 {
		return BadRequest("weak_password", "رمز عبور خیلی طولانی است")
	}
	return nil
}

func (s *Service) ChangePassword(ctx context.Context, a Actor, oldPw, newPw string) error {
	if err := validPassword(newPw); err != nil {
		return err
	}
	if oldPw == newPw {
		return BadRequest("same_password", "رمز جدید باید با رمز قبلی فرق داشته باشد")
	}
	var hash string
	if err := s.db.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1`, a.UserID).Scan(&hash); err != nil {
		return err
	}
	if !platform.CheckPassword(oldPw, hash) {
		return newErr(401, "invalid_credentials", "رمز فعلی درست نیست", nil)
	}
	nh, err := platform.HashPassword(newPw)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `UPDATE users SET password_hash=$2, must_change_password=false WHERE id=$1`, a.UserID, nh); err != nil {
		return err
	}
	// every other session must log in again; the current one is re-issued by the caller
	_ = s.RevokeUser(ctx, a.UserID)
	s.AuditNow(ctx, &a.UserID, a.IP, "auth.password_changed", "user", a.UserID.String(), nil)
	return nil
}

// ---- shifts ----

func (s *Service) openShift(ctx context.Context, userID uuid.UUID) (*ShiftView, error) {
	var v ShiftView
	var door *string
	err := s.db.QueryRow(ctx, `SELECT sh.id, sh.place_id, p.name, sh.door_id, d.name, sh.started_at
		FROM shifts sh JOIN places p ON p.id=sh.place_id LEFT JOIN doors d ON d.id=sh.door_id
		WHERE sh.user_id=$1 AND sh.ended_at IS NULL`, userID).Scan(&v.ID, &v.PlaceID, &v.PlaceName, &v.DoorID, &door, &v.StartedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if door != nil {
		v.DoorName = *door
	}
	return &v, nil
}

func (s *Service) updateSession(ctx context.Context, a Actor, mutate func(*Session)) error {
	raw, err := s.rdb.Get(ctx, sessKey(a.TokenID)).Bytes()
	if err != nil {
		return ErrUnauthorized
	}
	var sess Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return err
	}
	mutate(&sess)
	b, _ := json.Marshal(sess)
	return s.rdb.SetArgs(ctx, sessKey(a.TokenID), b, redis.SetArgs{KeepTTL: true}).Err()
}

func (s *Service) StartShift(ctx context.Context, a Actor, placeID int16, doorID *int16) (*ShiftView, error) {
	if !a.Role.NeedsShift() {
		return nil, BadRequest("no_shift_needed", "این نقش به شیفت نیاز ندارد")
	}
	var placeName string
	var kind domain.PlaceKind
	if err := s.db.QueryRow(ctx, `SELECT name, kind::text FROM places WHERE id=$1 AND active`, placeID).Scan(&placeName, &kind); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, BadRequest("bad_place", "مکان نامعتبر است")
		}
		return nil, err
	}
	if err := domain.RoleMayWorkAt(a.Role, kind); err != nil {
		return nil, Forbidden("این نقش اجازه‌ی کار در این مکان را ندارد")
	}
	var doorName string
	if doorID != nil {
		err := s.db.QueryRow(ctx, `SELECT name FROM doors WHERE id=$1 AND place_id=$2 AND active`, *doorID, placeID).Scan(&doorName)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, BadRequest("bad_door", "درب نامعتبر است")
		}
		if err != nil {
			return nil, err
		}
	}
	// ending a still-open shift and starting a new one is one atomic step
	var v ShiftView
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE shifts SET ended_at=now() WHERE user_id=$1 AND ended_at IS NULL`, a.UserID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO shifts(user_id, place_id, door_id) VALUES ($1,$2,$3) RETURNING id, started_at`,
			a.UserID, placeID, doorID).Scan(&v.ID, &v.StartedAt)
	})
	if err != nil {
		return nil, err
	}
	v.PlaceID, v.PlaceName, v.DoorID, v.DoorName = placeID, placeName, doorID, doorName
	err = s.updateSession(ctx, a, func(se *Session) { se.ShiftID, se.PlaceID, se.DoorID = &v.ID, &placeID, doorID })
	s.Audit(a, "shift.start", "shift", v.ID.String(), map[string]any{"place": placeID, "door": doorID})
	return &v, err
}

func (s *Service) EndShift(ctx context.Context, a Actor) error {
	if a.ShiftID == nil {
		return nil
	}
	if _, err := s.db.Exec(ctx, `UPDATE shifts SET ended_at=now() WHERE id=$1 AND user_id=$2 AND ended_at IS NULL`, *a.ShiftID, a.UserID); err != nil {
		return err
	}
	s.Audit(a, "shift.end", "shift", a.ShiftID.String(), nil)
	return s.updateSession(ctx, a, func(se *Session) { se.ShiftID, se.PlaceID, se.DoorID = nil, nil, nil })
}

// ActorForShift rebuilds the context of an earlier shift of the same user. It
// is how offline operations are attributed to the place they were done at.
func (s *Service) ActorForShift(ctx context.Context, base Actor, shiftID uuid.UUID) (Actor, error) {
	var placeID int16
	var doorID *int16
	var endedAt *time.Time
	err := s.db.QueryRow(ctx, `SELECT place_id, door_id, ended_at FROM shifts WHERE id=$1 AND user_id=$2`, shiftID, base.UserID).
		Scan(&placeID, &doorID, &endedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return base, Forbidden("شیفت متعلق به شما نیست")
	}
	if err != nil {
		return base, err
	}
	if endedAt != nil && time.Since(*endedAt) > 48*time.Hour {
		return base, newErr(410, "shift_expired", "این شیفت مدت‌هاست بسته شده و عملیات آن پذیرفته نمی‌شود", nil)
	}
	base.ShiftID, base.PlaceID, base.DoorID = &shiftID, &placeID, doorID
	return base, nil
}

// ---- users ----

var usernameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{2,31}$`)

type CreateUserReq struct {
	Username string      `json:"username"`
	FullName string      `json:"full_name"`
	Role     domain.Role `json:"role"`
}

type CreatedUser struct {
	UserView
	TempPassword string `json:"temp_password"`
}

func (s *Service) CreateUser(ctx context.Context, a Actor, req CreateUserReq) (*CreatedUser, error) {
	if err := a.require(domain.PermUsers); err != nil {
		return nil, err
	}
	req.Username = strings.ToLower(strings.TrimSpace(req.Username))
	req.FullName = strings.TrimSpace(req.FullName)
	if !usernameRe.MatchString(req.Username) {
		return nil, BadRequest("bad_username", "شناسه باید ۳ تا ۳۲ نویسه‌ی انگلیسی کوچک، عدد یا . _ - باشد")
	}
	if req.FullName == "" || len([]rune(req.FullName)) > 100 {
		return nil, BadRequest("bad_name", "نام نامعتبر است")
	}
	if !req.Role.Valid() {
		return nil, BadRequest("bad_role", "نقش نامعتبر است")
	}
	temp := platform.TempPassword(12)
	hash, err := platform.HashPassword(temp)
	if err != nil {
		return nil, err
	}
	out := &CreatedUser{TempPassword: temp}
	err = s.db.QueryRow(ctx, `INSERT INTO users(username, full_name, role, password_hash, created_by)
		VALUES ($1,$2,$3,$4,$5) RETURNING id, active, must_change_password`,
		req.Username, req.FullName, req.Role, hash, a.UserID).Scan(&out.ID, &out.Active, &out.MustChange)
	if pgCode(err) == "23505" {
		return nil, Conflict("username_taken", "این شناسه قبلاً ثبت شده است", nil)
	}
	if err != nil {
		return nil, err
	}
	out.Username, out.FullName, out.Role = req.Username, req.FullName, req.Role
	s.AuditNow(ctx, &a.UserID, a.IP, "user.create", "user", out.ID.String(), map[string]any{"role": req.Role})
	return out, nil
}

type UpdateUserReq struct {
	FullName *string      `json:"full_name"`
	Role     *domain.Role `json:"role"`
	Active   *bool        `json:"active"`
}

func (s *Service) UpdateUser(ctx context.Context, a Actor, id uuid.UUID, req UpdateUserReq) error {
	if err := a.require(domain.PermUsers); err != nil {
		return err
	}
	if id == a.UserID && ((req.Active != nil && !*req.Active) || req.Role != nil) {
		return BadRequest("self_change", "نمی‌توانید نقش یا فعال‌بودن خودتان را تغییر دهید")
	}
	if req.Role != nil && !req.Role.Valid() {
		return BadRequest("bad_role", "نقش نامعتبر است")
	}
	tag, err := s.db.Exec(ctx, `UPDATE users SET full_name=COALESCE($2, full_name), role=COALESCE($3, role), active=COALESCE($4, active) WHERE id=$1`,
		id, req.FullName, req.Role, req.Active)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return NotFound("کاربر پیدا نشد")
	}
	if req.Active != nil && !*req.Active {
		_, _ = s.db.Exec(ctx, `UPDATE shifts SET ended_at=now() WHERE user_id=$1 AND ended_at IS NULL`, id)
	}
	if req.Active != nil || req.Role != nil {
		_ = s.RevokeUser(ctx, id) // role/active changes apply immediately
	}
	s.AuditNow(ctx, &a.UserID, a.IP, "user.update", "user", id.String(), req)
	return nil
}

func (s *Service) ResetPassword(ctx context.Context, a Actor, id uuid.UUID) (string, error) {
	if err := a.require(domain.PermUsers); err != nil {
		return "", err
	}
	temp := platform.TempPassword(12)
	hash, err := platform.HashPassword(temp)
	if err != nil {
		return "", err
	}
	tag, err := s.db.Exec(ctx, `UPDATE users SET password_hash=$2, must_change_password=true WHERE id=$1`, id, hash)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", NotFound("کاربر پیدا نشد")
	}
	_ = s.RevokeUser(ctx, id)
	s.AuditNow(ctx, &a.UserID, a.IP, "user.reset_password", "user", id.String(), nil)
	return temp, nil
}

func (s *Service) ListUsers(ctx context.Context, a Actor) ([]UserView, error) {
	if err := a.require(domain.PermUsers); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT id, username, full_name, role, active, must_change_password FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserView{}
	for rows.Next() {
		var u UserView
		if err := rows.Scan(&u.ID, &u.Username, &u.FullName, &u.Role, &u.Active, &u.MustChange); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// BootstrapAdmin creates the first admin (CLI only). It refuses if any admin exists.
func (s *Service) BootstrapAdmin(ctx context.Context, username, fullName string) (string, error) {
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM users WHERE role='admin'`).Scan(&n); err != nil {
		return "", err
	}
	if n > 0 {
		return "", fmt.Errorf("an admin already exists")
	}
	temp := platform.TempPassword(14)
	hash, err := platform.HashPassword(temp)
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO users(username, full_name, role, password_hash) VALUES ($1,$2,'admin',$3)`,
		strings.ToLower(username), fullName, hash)
	return temp, err
}
