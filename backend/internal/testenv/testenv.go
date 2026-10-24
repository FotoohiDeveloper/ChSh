// Package testenv builds a real service against a real Postgres and Redis for
// integration tests. It is skipped unless TEST_DATABASE_URL and TEST_REDIS_URL
// are set, and it refuses to wipe a database unless TEST_ALLOW_RESET=1.
package testenv

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"chador/internal/domain"
	"chador/internal/platform"
	"chador/internal/provider"
	"chador/internal/service"
)

type FakeQueue struct {
	mu   sync.Mutex
	SMSs []service.SMSJob
}

func (q *FakeQueue) SMS(_ context.Context, j service.SMSJob) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.SMSs = append(q.SMSs, j)
	return nil
}
func (q *FakeQueue) VerifyPilgrim(context.Context, uuid.UUID, time.Duration) error { return nil }
func (q *FakeQueue) Templates() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]string, len(q.SMSs))
	for i, j := range q.SMSs {
		out[i] = j.Template
	}
	return out
}

type Env struct {
	Svc     *service.Service
	Cfg     platform.Config
	Pool    *pgxpool.Pool // owner connection: tests use it to inspect and age data
	AppPool *pgxpool.Pool // the connection the service really uses (unprivileged when TEST_APP_DATABASE_URL is set)
	Redis   *redis.Client
	Queue   *FakeQueue
	Inq     *FlakyInquirer
	Admin   service.Actor
	Gate    int16 // gate A place id
	Door    int16
	Gate2   int16
	Store   int16
	Wash    int16
}

// FlakyInquirer lets a test simulate the identity service being down.
type FlakyInquirer struct {
	mu   sync.Mutex
	Down bool
}

func (f *FlakyInquirer) Verify(ctx context.Context, nid string, b time.Time, m string) (provider.InquiryResult, error) {
	f.mu.Lock()
	down := f.Down
	f.mu.Unlock()
	if down {
		return provider.InquiryResult{}, fmt.Errorf("inquiry service down")
	}
	return provider.MockInquirer{}.Verify(ctx, nid, b, m)
}
func (f *FlakyInquirer) SetDown(v bool) { f.mu.Lock(); f.Down = v; f.mu.Unlock() }

func New(t testing.TB) *Env {
	t.Helper()
	dbURL, redisURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_REDIS_URL")
	if dbURL == "" || redisURL == "" || os.Getenv("TEST_ALLOW_RESET") != "1" {
		t.Skip("integration tests need TEST_DATABASE_URL, TEST_REDIS_URL and TEST_ALLOW_RESET=1")
	}
	ctx := context.Background()
	// Test packages run as separate processes against one database; serialise them.
	lock, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock.Exec(ctx, `SELECT pg_advisory_lock(990011)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close(context.Background()) }) // closing the session releases the lock
	pool, err := platform.NewPool(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if _, err := platform.Migrate(ctx, pool, os.Getenv("TEST_APP_ROLE")); err != nil {
		t.Fatal(err)
	}
	ropt, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(ropt)
	t.Cleanup(func() { _ = rdb.Close() })
	if err := rdb.FlushDB(ctx).Err(); err != nil {
		t.Fatal(err)
	}

	enc, idx := make([]byte, 32), make([]byte, 32)
	_, _ = rand.Read(enc)
	_, _ = rand.Read(idx)
	cfg := platform.Config{
		Env: "dev", EncKey: enc, IdxKey: idx, AccessTTL: 15 * time.Minute, RefreshTTL: 12 * time.Hour,
		UndoWindow: 3 * time.Minute, ReminderAfter: 6 * time.Hour, MaxReminders: 2, RetentionDays: 90,
		Location: time.UTC, InquiryMode: "mock", SMSMode: "mock",
	}
	crypto, err := platform.NewCrypto(enc, idx)
	if err != nil {
		t.Fatal(err)
	}
	q := &FakeQueue{}
	inq := &FlakyInquirer{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svcPool := pool
	if appURL := os.Getenv("TEST_APP_DATABASE_URL"); appURL != "" {
		if svcPool, err = platform.NewPool(ctx, appURL); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(svcPool.Close)
	}
	svc := service.New(service.Deps{DB: svcPool, Redis: rdb, Config: cfg, Crypto: crypto, Inquiry: inq, Queue: q, Log: log})
	t.Cleanup(svc.Close)

	e := &Env{Svc: svc, Cfg: cfg, Pool: pool, AppPool: svcPool, Redis: rdb, Queue: q, Inq: inq}
	e.Admin = service.Actor{UserID: uuid.New(), Role: domain.Admin, Username: "root"}
	if _, err := pool.Exec(ctx, `INSERT INTO users(id, username, full_name, role, password_hash, must_change_password) VALUES ($1,'root','Root','admin','x',false)`, e.Admin.UserID); err != nil {
		t.Fatal(err)
	}
	mustPlace := func(kind, name string) int16 {
		id, err := svc.CreatePlace(ctx, e.Admin, kind, name)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	e.Gate, e.Gate2 = mustPlace("gate", "Gate A"), mustPlace("gate", "Gate B")
	e.Store, e.Wash = mustPlace("storage", "Store"), mustPlace("workshop", "Workshop")
	if e.Door, err = svc.CreateDoor(ctx, e.Admin, e.Gate, "Door 1"); err != nil {
		t.Fatal(err)
	}
	return e
}

// User creates a user and returns an actor already inside a shift at the place
// (when the role needs one) plus the temporary password.
func (e *Env) User(t testing.TB, username string, role domain.Role, place int16, door *int16) service.Actor {
	t.Helper()
	ctx := context.Background()
	username = "usr-" + username
	u, err := e.Svc.CreateUser(ctx, e.Admin, service.CreateUserReq{Username: username, FullName: "User " + username, Role: role})
	if err != nil {
		t.Fatal(err)
	}
	a := service.Actor{UserID: u.ID, Username: username, FullName: u.FullName, Role: role, TokenID: "t-" + username}
	if role.NeedsShift() && place != 0 {
		// StartShift updates the Redis session, so give the actor one first
		if err := e.Redis.Set(ctx, "sess:"+a.TokenID, fmt.Sprintf(`{"u":%q,"n":%q,"f":"x","r":%q}`, u.ID, username, role), time.Hour).Err(); err != nil {
			t.Fatal(err)
		}
		sh, err := e.Svc.StartShift(ctx, a, place, door)
		if err != nil {
			t.Fatal(err)
		}
		a.ShiftID, a.PlaceID, a.DoorID = &sh.ID, &place, door
	}
	return a
}

func (e *Env) Tents(t testing.TB, from, to int) []string {
	t.Helper()
	var codes []string
	for i := from; i <= to; i++ {
		codes = append(codes, fmt.Sprintf("CH-%06d", i))
	}
	if _, err := e.Svc.CreateTents(context.Background(), e.Admin, codes, "", &e.Store); err != nil {
		t.Fatal(err)
	}
	return codes
}

// NID returns the i-th valid synthetic national ID.
func NID(i int) string {
	base := fmt.Sprintf("%09d", 100000000+i*7919)
	sum := 0
	for k := 0; k < 9; k++ {
		sum += int(base[k]-'0') * (10 - k)
	}
	r := sum % 11
	check := r
	if r >= 2 {
		check = 11 - r
	}
	return fmt.Sprintf("%s%d", base, check)
}

// Pilgrim registers pilgrim number i (cycling valid sample national IDs; i < 8).
func (e *Env) Pilgrim(t testing.TB, a service.Actor, i int) *service.PilgrimView {
	t.Helper()
	v, err := e.Svc.UpsertPilgrim(context.Background(), a, service.PilgrimInput{
		NationalID: NID(i), BirthDate: "1370/05/12", Mobile: fmt.Sprintf("0912000%04d", i)})
	if err != nil {
		t.Fatalf("pilgrim %d: %v", i, err)
	}
	return v
}
