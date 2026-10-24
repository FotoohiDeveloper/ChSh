package api_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chador/internal/api"
	"chador/internal/domain"
	"chador/internal/service"
	"chador/internal/testenv"
)

type client struct {
	t     *testing.T
	base  string
	token string
}

func (c *client) do(method, path string, body any) (int, map[string]any, http.Header) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out, resp.Header
}

func errCode(m map[string]any) string {
	if e, ok := m["error"].(map[string]any); ok {
		s, _ := e["code"].(string)
		return s
	}
	return ""
}

func setup(t *testing.T) (*testenv.Env, *httptest.Server) {
	e := testenv.New(t)
	cfg := e.Cfg
	cfg.TrustProxy = false
	srv := api.New(e.Svc, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return e, ts
}

func TestEndToEndOverHTTP(t *testing.T) {
	e, ts := setup(t)
	e.Tents(t, 1, 5)
	u, err := e.Svc.CreateUser(t.Context(), e.Admin, service.CreateUserReq{Username: "khadem1", FullName: "خادم", Role: domain.Server})
	if err != nil {
		t.Fatal(err)
	}
	c := &client{t: t, base: ts.URL}

	// --- not logged in
	if st, body, _ := c.do("GET", "/api/v1/me", nil); st != 401 || errCode(body) != "unauthorized" {
		t.Fatalf("anonymous /me: %d %v", st, body)
	}
	// --- wrong password gives the same answer as an unknown user
	st1, b1, _ := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "khadem1", "password": "nope-nope-nope"})
	st2, b2, _ := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "ghost", "password": "nope-nope-nope"})
	if st1 != 401 || st2 != 401 || b1["error"].(map[string]any)["message"] != b2["error"].(map[string]any)["message"] {
		t.Fatal("login must not reveal which usernames exist")
	}
	// --- temporary password: can only change it
	st, body, hdr := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "khadem1", "password": u.TempPassword})
	if st != 200 {
		t.Fatalf("login: %d %v", st, body)
	}
	if hdr.Get("Cache-Control") != "no-store" || hdr.Get("X-Content-Type-Options") != "nosniff" || hdr.Get("X-Request-Id") == "" {
		t.Fatalf("security headers missing: %v", hdr)
	}
	c.token = body["tokens"].(map[string]any)["access_token"].(string)
	if st, body, _ := c.do("GET", "/api/v1/places", nil); st != 403 || errCode(body) != "password_change_required" {
		t.Fatalf("temp password must gate everything: %d %v", st, body)
	}
	if st, _, _ := c.do("GET", "/api/v1/me", nil); st != 200 {
		t.Fatal("/me must work with a temporary password")
	}
	if st, body, _ := c.do("POST", "/api/v1/auth/change-password", map[string]string{"old_password": u.TempPassword, "new_password": "short"}); st != 400 || errCode(body) != "weak_password" {
		t.Fatalf("weak password: %d %v", st, body)
	}
	st, _, _ = c.do("POST", "/api/v1/auth/change-password", map[string]string{"old_password": u.TempPassword, "new_password": "a-long-enough-passphrase"})
	if st != 200 {
		t.Fatal("change password failed")
	}
	st, body, _ = c.do("POST", "/api/v1/auth/login", map[string]string{"username": "khadem1", "password": "a-long-enough-passphrase"})
	if st != 200 {
		t.Fatalf("login after change: %d %v", st, body)
	}
	c.token = body["tokens"].(map[string]any)["access_token"].(string)

	// --- needs a shift first
	_, pbody, _ := c.do("POST", "/api/v1/pilgrims", map[string]string{"national_id": "۰۰۸۴۵۷۵۹۴۸", "birth_date": "1370/05/12", "mobile": "۰۹۱۲۳۴۵۶۷۸۹"})
	pid, _ := pbody["id"].(string)
	if pid == "" || pbody["national_id_masked"] == "0084575948" {
		t.Fatalf("pilgrim: %v", pbody)
	}
	st, body, _ = c.do("POST", "/api/v1/assignments", map[string]any{"pilgrim_id": pid, "codes": []string{"CH-000001"}})
	if st != 409 || errCode(body) != "shift_required" {
		t.Fatalf("assign without shift: %d %v", st, body)
	}
	if st, body, _ := c.do("POST", "/api/v1/shifts/start", map[string]any{"place_id": e.Wash}); st != 403 {
		t.Fatalf("server in workshop: %d %v", st, body)
	}
	if st, _, _ := c.do("POST", "/api/v1/shifts/start", map[string]any{"place_id": e.Gate, "door_id": e.Door}); st != 200 {
		t.Fatal("start shift")
	}

	// --- realtime stream: subscribe, then act, then expect the event
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("sse: %v %v", err, resp)
	}
	defer resp.Body.Close()
	got := make(chan string, 4)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "event: ") {
				got <- strings.TrimPrefix(sc.Text(), "event: ")
			}
		}
	}()
	time.Sleep(200 * time.Millisecond) // let the subscription register

	st, body, hdr = c.do("POST", "/api/v1/assignments", map[string]any{"pilgrim_id": pid, "codes": []string{"CH-000001", "ch-000002"}, "socks_qty": 1, "op_id": "11111111-1111-4111-8111-111111111111"})
	if st != 201 || len(body["items"].([]any)) != 2 {
		t.Fatalf("assign: %d %v", st, body)
	}
	select {
	case ev := <-got:
		if ev != "assigned" {
			t.Fatalf("event %q", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no realtime event")
	}
	// the same request again is a replay
	st, _, hdr = c.do("POST", "/api/v1/assignments", map[string]any{"pilgrim_id": pid, "codes": []string{"CH-000001", "ch-000002"}, "socks_qty": 1, "op_id": "11111111-1111-4111-8111-111111111111"})
	if st != 201 || hdr.Get("Idempotent-Replay") != "true" {
		t.Fatalf("replay: %d %v", st, hdr)
	}
	if st, body, _ := c.do("POST", "/api/v1/assignments", map[string]any{"pilgrim_id": pid, "codes": []string{"CH-000001"}}); st != 409 || errCode(body) != "tents_unavailable" {
		t.Fatalf("conflict: %d %v", st, body)
	} else if len(body["error"].(map[string]any)["details"].([]any)) != 1 {
		t.Fatal("conflict must say which tent")
	}
	st, body, _ = c.do("POST", "/api/v1/returns", map[string]any{"items": []map[string]string{{"code": "CH-000001", "status": "dirty"}, {"code": "CH-000002"}}})
	if st != 200 || body["completed_pilgrims"].(float64) != 1 {
		t.Fatalf("return: %d %v", st, body)
	}

	// --- strict input handling
	if st, body, _ := c.do("POST", "/api/v1/returns", map[string]any{"items": []map[string]string{{"code": "CH-000003"}}, "is_admin": true}); st != 400 || errCode(body) != "bad_json" {
		t.Fatalf("unknown field: %d %v", st, body)
	}
	req, _ = http.NewRequest("POST", ts.URL+"/api/v1/returns", strings.NewReader(`{"items":[]}`))
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "text/plain")
	r2, _ := http.DefaultClient.Do(req)
	if r2.StatusCode != 400 {
		t.Fatalf("wrong content type accepted: %d", r2.StatusCode)
	}
	big := bytes.Repeat([]byte("a"), 2<<20)
	req, _ = http.NewRequest("POST", ts.URL+"/api/v1/returns", bytes.NewReader(append([]byte(`{"items":[],"x":"`), big...)))
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	r3, _ := http.DefaultClient.Do(req)
	if r3.StatusCode != 413 {
		t.Fatalf("oversized body: %d", r3.StatusCode)
	}

	// --- least privilege over HTTP
	for _, path := range []string{"/api/v1/admin/users", "/api/v1/admin/settings", "/api/v1/admin/audit", "/api/v1/reports/daily", "/api/v1/retire-requests"} {
		if st, _, _ := c.do("GET", path, nil); st != 403 {
			t.Errorf("%s: server got %d, want 403", path, st)
		}
	}
	if st, _, _ := c.do("POST", "/api/v1/admin/users", map[string]string{"username": "evil1", "full_name": "x", "role": "admin"}); st != 403 {
		t.Fatalf("server created a user: %d", st)
	}
	if st, body, _ := c.do("GET", "/api/v1/dashboard", nil); st != 200 || body["scope"] != "place" {
		t.Fatalf("dashboard: %d %v", st, body)
	}
	if st, _, _ := c.do("GET", "/api/v1/tents/CH-000001/history", nil); st != 200 {
		t.Fatal("history")
	}
	if st, body, _ := c.do("GET", "/api/v1/tents/'%20OR%201=1", nil); st != 400 || errCode(body) != "bad_tent_code" {
		t.Fatalf("injection attempt: %d %v", st, body)
	}

	// --- logout kills the token
	if st, _, _ := c.do("POST", "/api/v1/auth/logout", map[string]string{}); st != 204 {
		t.Fatal("logout")
	}
	if st, _, _ := c.do("GET", "/api/v1/me", nil); st != 401 {
		t.Fatal("token still valid after logout")
	}
}

func TestAdminSurfaceAndReportDownload(t *testing.T) {
	e, ts := setup(t)
	_ = e
	// create an admin with a known password via the service, then drive the admin API over HTTP
	adm, err := e.Svc.CreateUser(t.Context(), e.Admin, service.CreateUserReq{Username: "admin2", FullName: "مدیر", Role: domain.Admin})
	if err != nil {
		t.Fatal(err)
	}
	c := &client{t: t, base: ts.URL}
	_, body, _ := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin2", "password": adm.TempPassword})
	c.token = body["tokens"].(map[string]any)["access_token"].(string)
	c.do("POST", "/api/v1/auth/change-password", map[string]string{"old_password": adm.TempPassword, "new_password": "admin-passphrase-123"})
	_, body, _ = c.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin2", "password": "admin-passphrase-123"})
	c.token = body["tokens"].(map[string]any)["access_token"].(string)

	if st, body, _ := c.do("POST", "/api/v1/admin/tents", map[string]any{"from": 1, "to": 50}); st != 201 || body["created"].(float64) != 50 {
		t.Fatalf("bulk tents: %d %v", st, body)
	}
	if st, body, _ := c.do("POST", "/api/v1/admin/tents", map[string]any{"from": 1, "to": 50}); st != 201 || body["created"].(float64) != 0 {
		t.Fatalf("re-import must be a no-op: %d %v", st, body)
	}
	if st, _, _ := c.do("POST", "/api/v1/admin/tents", map[string]any{"from": 1, "to": 900000}); st != 400 {
		t.Fatal("absurd range accepted")
	}
	st, body, _ := c.do("POST", "/api/v1/admin/users", map[string]string{"username": "lead01", "full_name": "مسئول", "role": "gate_lead"})
	if st != 201 || body["temp_password"] == "" {
		t.Fatalf("create user: %d %v", st, body)
	}
	if st, body, _ := c.do("POST", "/api/v1/admin/users", map[string]string{"username": "lead01", "full_name": "x", "role": "gate_lead"}); st != 409 || errCode(body) != "username_taken" {
		t.Fatalf("dup: %d %v", st, body)
	}
	if st, body, _ := c.do("PUT", "/api/v1/admin/settings/socks_unit_price", 30000); st != 204 {
		t.Fatalf("settings: %d %v", st, body)
	}
	if st, body, _ := c.do("GET", "/api/v1/admin/audit?limit=5", nil); st != 200 || len(body["records"].([]any)) == 0 {
		t.Fatalf("audit: %d %v", st, body)
	}
	if st, _, _ := c.do("GET", "/api/v1/admin/users", nil); st != 200 {
		t.Fatal("users")
	}

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/reports/daily?format=xlsx", nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "spreadsheetml") || len(raw) < 1000 || !bytes.HasPrefix(raw, []byte("PK")) {
		t.Fatalf("xlsx: %d %s (%d bytes)", resp.StatusCode, resp.Header.Get("Content-Type"), len(raw))
	}
}

func TestHealthAndRateLimit(t *testing.T) {
	e, ts := setup(t)
	c := &client{t: t, base: ts.URL}
	if st, _, _ := c.do("GET", "/healthz", nil); st != 200 {
		t.Fatal("healthz")
	}
	if st, _, _ := c.do("GET", "/readyz", nil); st != 200 {
		t.Fatal("readyz")
	}
	u, _ := e.Svc.CreateUser(t.Context(), e.Admin, service.CreateUserReq{Username: "rate01", FullName: "x", Role: domain.Server})
	_, body, _ := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "rate01", "password": u.TempPassword})
	c.token = body["tokens"].(map[string]any)["access_token"].(string)
	limited := 0
	for i := 0; i < 200; i++ {
		if st, _, h := c.do("GET", "/api/v1/me", nil); st == 429 {
			limited++
			if h.Get("Retry-After") == "" {
				t.Fatal("Retry-After missing")
			}
		}
	}
	if limited == 0 {
		t.Fatal("no rate limiting under a burst of 200 requests")
	}
}
