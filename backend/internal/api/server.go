// Package api is the HTTP layer: routing, authentication, request/response
// handling and security headers. Business rules live in package service.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"chador/internal/platform"
	"chador/internal/service"
)

type Server struct {
	svc *service.Service
	cfg platform.Config
	log *slog.Logger
	met *metrics
}

func New(svc *service.Service, cfg platform.Config, log *slog.Logger) *Server {
	return &Server{svc: svc, cfg: cfg, log: log, met: newMetrics()}
}

type ctxKey int

const (
	actorKey ctxKey = iota
	reqIDKey
	sessKey
)

// ---- JSON helpers ----

type errBody struct {
	Error *service.Error `json:"error"`
	ReqID string         `json:"request_id,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var e *service.Error
	rid, _ := r.Context().Value(reqIDKey).(string)
	switch {
	case errors.As(err, &e):
	case errors.Is(err, context.DeadlineExceeded):
		e = &service.Error{Status: http.StatusServiceUnavailable, Code: "timeout", Message: "پاسخ سرور طول کشید؛ دوباره تلاش کنید"}
	case errors.Is(err, context.Canceled):
		return // client went away
	default:
		s.log.Error("request failed", "err", err, "request_id", rid, "path", r.URL.Path)
		e = &service.Error{Status: http.StatusInternalServerError, Code: "internal_error", Message: "خطای داخلی؛ اگر تکرار شد کد پیگیری را اعلام کنید"}
	}
	writeJSON(w, e.Status, errBody{Error: e, ReqID: rid})
}

// decode reads exactly one JSON object, rejects unknown fields and wrong content types.
func decode(r *http.Request, v any) error {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return service.BadRequest("content_type", "Content-Type باید application/json باشد")
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return &service.Error{Status: http.StatusRequestEntityTooLarge, Code: "too_large", Message: "درخواست خیلی بزرگ است"}
		}
		return service.BadRequest("bad_json", "ساختار درخواست نامعتبر است")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return service.BadRequest("bad_json", "ساختار درخواست نامعتبر است")
	}
	return nil
}

func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first := strings.TrimSpace(strings.Split(xff, ",")[0])
			if net.ParseIP(first) != nil {
				return first
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---- middleware ----

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := platform.RandomToken(9)
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), reqIDKey, id)))
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				rid, _ := r.Context().Value(reqIDKey).(string)
				s.log.Error("panic in handler", "panic", fmt.Sprint(rec), "request_id", rid, "path", r.URL.Path)
				writeJSON(w, 500, errBody{Error: &service.Error{Status: 500, Code: "internal_error", Message: "خطای داخلی"}, ReqID: rid})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Cache-Control", "no-store") // responses contain personal data
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(c int) {
	if !w.wrote {
		w.status, w.wrote = c, true
	}
	w.ResponseWriter.WriteHeader(c)
}
func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status, w.wrote = 200, true
	}
	return w.ResponseWriter.Write(b)
}
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// accessLog logs method, route pattern, status and latency. It never logs
// query strings, bodies or headers, so personal data cannot leak into logs.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}
		d := time.Since(start)
		s.met.observe(r.Method, route, sw.status, d)
		rid, _ := r.Context().Value(reqIDKey).(string)
		attrs := []any{"method", r.Method, "route", route, "status", sw.status, "ms", d.Milliseconds(), "request_id", rid}
		if a, ok := r.Context().Value(actorKey).(service.Actor); ok {
			attrs = append(attrs, "user", a.Username)
		}
		if sw.status >= 500 {
			s.log.Error("http", attrs...)
		} else if route != "/healthz" && route != "/readyz" {
			s.log.Info("http", attrs...)
		}
	})
}

func limitBody(max int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, max)
			next.ServeHTTP(w, r)
		})
	}
}

func timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/events") { // long-lived stream
				next.ServeHTTP(w, r)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

const perSessionRPS = 60

// authenticate resolves the bearer token. Sessions that must change their
// password can only reach the endpoints that let them do so.
func (s *Server) authenticate(allowMustChange bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			tok, ok := strings.CutPrefix(h, "Bearer ")
			if !ok || tok == "" || len(tok) > 200 {
				s.fail(w, r, service.ErrUnauthorized)
				return
			}
			sess, tid, limited, err := s.svc.Authenticate(r.Context(), tok, perSessionRPS)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			if limited {
				w.Header().Set("Retry-After", "1")
				s.fail(w, r, &service.Error{Status: 429, Code: "rate_limited", Message: "درخواست‌ها خیلی سریع است؛ یک لحظه صبر کنید"})
				return
			}
			if sess.MustChange && !allowMustChange {
				s.fail(w, r, &service.Error{Status: 403, Code: "password_change_required", Message: "ابتدا رمز عبور موقت را تغییر دهید"})
				return
			}
			a := sess.Actor(tid, clientIP(r, s.cfg.TrustProxy))
			ctx := context.WithValue(r.Context(), actorKey, a)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func actorOf(r *http.Request) service.Actor {
	a, _ := r.Context().Value(actorKey).(service.Actor)
	return a
}

// ---- metrics (Prometheus text format, served on a separate internal port) ----

type metrics struct {
	mu       sync.Mutex
	requests map[string]*atomic.Int64
	secSum   map[string]float64
	secCnt   map[string]int64
}

func newMetrics() *metrics {
	return &metrics{requests: map[string]*atomic.Int64{}, secSum: map[string]float64{}, secCnt: map[string]int64{}}
}

func (m *metrics) observe(method, route string, status int, d time.Duration) {
	k := fmt.Sprintf(`method=%q,route=%q`, method, route)
	m.mu.Lock()
	defer m.mu.Unlock()
	c := fmt.Sprintf(`{%s,code="%dxx"}`, k, status/100)
	if m.requests[c] == nil {
		m.requests[c] = &atomic.Int64{}
	}
	m.requests[c].Add(1)
	m.secSum[k] += d.Seconds()
	m.secCnt[k]++
}

func (s *Server) MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		m := s.met
		m.mu.Lock()
		for k, v := range m.requests {
			fmt.Fprintf(w, "chador_http_requests_total%s %d\n", k, v.Load())
		}
		for k, v := range m.secSum {
			fmt.Fprintf(w, "chador_http_request_seconds_sum{%s} %f\nchador_http_request_seconds_count{%s} %d\n", k, v, k, m.secCnt[k])
		}
		m.mu.Unlock()
		acq, idle, max := s.svc.PoolStats()
		fmt.Fprintf(w, "chador_db_pool_acquired %d\nchador_db_pool_idle %d\nchador_db_pool_max %d\n", acq, idle, max)
	})
}
