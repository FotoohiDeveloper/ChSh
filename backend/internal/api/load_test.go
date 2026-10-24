package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"chador/internal/domain"
	"chador/internal/service"
	"chador/internal/testenv"
)

// TestLoad drives the real HTTP stack with many concurrent "servers" doing the
// hand-out / scan / return cycle. Run with LOADTEST=1; it is skipped otherwise.
//
//	LOADTEST=1 go test ./internal/api -run TestLoad -v -count=1
func TestLoad(t *testing.T) {
	if os.Getenv("LOADTEST") != "1" {
		t.Skip("set LOADTEST=1 to run the load test")
	}
	e, ts := setup(t)
	const workers = 40
	e.Tents(t, 1, 1000)

	type lat struct {
		mu sync.Mutex
		d  map[string][]time.Duration
	}
	l := &lat{d: map[string][]time.Duration{}}
	rec := func(name string, d time.Duration) { l.mu.Lock(); l.d[name] = append(l.d[name], d); l.mu.Unlock() }

	hc := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: workers * 2}}
	call := func(token, method, path string, body any) (int, map[string]any) {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, ts.URL+path, rd)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := hc.Do(req)
		if err != nil {
			t.Error(err)
			return 0, nil
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	type worker struct {
		token string
		pid   string
		lo    int
	}
	ws := make([]worker, workers)
	for i := range ws {
		name := fmt.Sprintf("load%02d", i)
		u, err := e.Svc.CreateUser(t.Context(), e.Admin, service.CreateUserReq{Username: name, FullName: name, Role: domain.Server})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Pool.Exec(t.Context(), `UPDATE users SET must_change_password=false WHERE id=$1`, u.ID); err != nil {
			t.Fatal(err)
		}
		c := &client{t: t, base: ts.URL}
		_, body, _ := c.do("POST", "/api/v1/auth/login", map[string]string{"username": name, "password": u.TempPassword})
		tok := body["tokens"].(map[string]any)["access_token"].(string)
		gate := e.Gate
		door := e.Door
		if st, b := call(tok, "POST", "/api/v1/shifts/start", map[string]any{"place_id": gate, "door_id": door}); st != 200 {
			t.Fatalf("shift: %d %v", st, b)
		}
		ws[i] = worker{token: tok, lo: i*25 + 1}
	}
	// one pilgrim per worker (national IDs are unique)
	admin := e.User(t, "loadlead", domain.GateLead, e.Gate, &e.Door)
	for i := range ws {
		v, err := e.Svc.UpsertPilgrim(t.Context(), admin, service.PilgrimInput{NationalID: testenv.NID(100 + i), BirthDate: "1370/05/12", Mobile: fmt.Sprintf("0913%07d", i)})
		if err != nil {
			t.Fatal(err)
		}
		ws[i].pid = v.ID.String()
	}

	dur := 8 * time.Second
	deadline := time.Now().Add(dur)
	var wg sync.WaitGroup
	var errs, total int64
	var emu sync.Mutex
	for _, w := range ws {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c1, c2 := fmt.Sprintf("CH-%06d", w.lo), fmt.Sprintf("CH-%06d", w.lo+1)
			for time.Now().Before(deadline) {
				step := func(name, method, path string, body any, want int) {
					start := time.Now()
					st, _ := call(w.token, method, path, body)
					rec(name, time.Since(start))
					emu.Lock()
					total++
					if st != want {
						errs++
					}
					emu.Unlock()
					time.Sleep(15 * time.Millisecond) // a human scanning, not a tight loop
				}
				step("POST /assignments (2 tents)", "POST", "/api/v1/assignments", map[string]any{"pilgrim_id": w.pid, "codes": []string{c1, c2}}, 201)
				step("GET /tents/{code} (scan)", "GET", "/api/v1/tents/"+c1, nil, 200)
				step("GET /dashboard", "GET", "/api/v1/dashboard", nil, 200)
				step("POST /returns (2 tents)", "POST", "/api/v1/returns", map[string]any{"items": []map[string]string{{"code": c1}, {"code": c2}}}, 200)
			}
		}()
	}
	wg.Wait()

	t.Logf("%d workers, %s, %d requests, %.0f req/s, %d unexpected responses", workers, dur, total, float64(total)/dur.Seconds(), errs)
	var names []string
	for n := range l.d {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		ds := l.d[n]
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		p := func(q float64) time.Duration { return ds[int(float64(len(ds)-1)*q)] }
		t.Logf("%-32s n=%-6d p50=%-8s p95=%-8s p99=%-8s max=%s", n, len(ds), p(.5).Round(10*time.Microsecond), p(.95).Round(10*time.Microsecond), p(.99).Round(10*time.Microsecond), ds[len(ds)-1].Round(10*time.Microsecond))
	}
	if errs > 0 {
		t.Fatalf("%d requests returned an unexpected status", errs)
	}
}
