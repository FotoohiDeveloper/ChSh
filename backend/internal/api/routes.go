package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"chador/internal/domain"
	"chador/internal/service"
)

// handler is a typed endpoint: it returns the response body, the status, or an error.
type handler func(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error)

func (s *Server) h(fn handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, status, err := fn(w, r, actorOf(r))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		writeJSON(w, status, body)
	}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(s.requestID, s.recoverer, secureHeaders, s.accessLog, limitBody(1<<20))

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := s.svc.Ready(ctx); err != nil {
			s.log.Warn("not ready", "err", err)
			writeJSON(w, 503, map[string]string{"status": "unavailable"})
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(timeout(10 * time.Second))

		// public
		r.Post("/auth/login", s.h(s.login))
		r.Post("/auth/refresh", s.h(s.refresh))

		// authenticated, allowed even while a temporary password is still active
		r.Group(func(r chi.Router) {
			r.Use(s.authenticate(true))
			r.Get("/me", s.h(s.me))
			r.Post("/auth/logout", s.h(s.logout))
			r.Post("/auth/change-password", s.h(s.changePassword))
		})

		// authenticated, normal use
		r.Group(func(r chi.Router) {
			r.Use(s.authenticate(false))
			r.Get("/places", s.h(s.places))
			r.Post("/shifts/start", s.h(s.shiftStart))
			r.Post("/shifts/end", s.h(s.shiftEnd))

			r.Post("/pilgrims", s.h(s.pilgrimUpsert))
			r.Post("/pilgrims/lookup", s.h(s.pilgrimLookup)) // POST so national IDs never appear in URLs/logs
			r.Get("/pilgrims/{id}", s.h(s.pilgrimGet))

			r.Get("/tents/{code}", s.h(s.tentGet))
			r.Get("/tents/{code}/history", s.h(s.tentHistory))
			r.Post("/assignments", s.h(s.assign))
			r.Post("/assignments/cancel", s.h(s.cancel))
			r.Post("/returns", s.h(s.returns))
			r.Post("/tents/status", s.h(s.setStatus))
			r.Post("/tents/move", s.h(s.move))

			r.Post("/retire-requests", s.h(s.retireRequest))
			r.Get("/retire-requests", s.h(s.retireList))
			r.Post("/retire-requests/{id}/decision", s.h(s.retireDecide))

			r.Post("/socks", s.h(s.socks))
			r.Post("/handovers", s.h(s.handoverCreate))
			r.Get("/handovers", s.h(s.handoverList))
			r.Post("/handovers/{id}/resolve", s.h(s.handoverResolve))

			r.With(limitBody(2<<20)).Post("/sync", s.h(s.sync))
			r.Get("/dashboard", s.h(s.dashboard))
			r.Get("/reports/daily", s.dailyReport)
			r.Get("/events", s.events) // server-sent events; no request timeout

			r.Route("/admin", func(r chi.Router) {
				r.Get("/users", s.h(s.usersList))
				r.Post("/users", s.h(s.userCreate))
				r.Patch("/users/{id}", s.h(s.userUpdate))
				r.Post("/users/{id}/reset-password", s.h(s.userResetPassword))
				r.Post("/tents", s.h(s.tentsCreate))
				r.Post("/tents/replace-label", s.h(s.tentReplaceLabel))
				r.Post("/places", s.h(s.placeCreate))
				r.Patch("/places/{id}", s.h(s.placeUpdate))
				r.Post("/places/{id}/doors", s.h(s.doorCreate))
				r.Get("/settings", s.h(s.settingsGet))
				r.Put("/settings/{key}", s.h(s.settingsPut))
				r.Get("/audit", s.h(s.auditList))
			})
		})
	})
	return r
}

// ---- auth ----

func (s *Server) login(w http.ResponseWriter, r *http.Request, _ service.Actor) (any, int, error) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	ip := clientIP(r, s.cfg.TrustProxy)
	if !s.svc.RateAllow(r.Context(), "login:"+ip, 120, time.Minute) {
		return nil, 0, &service.Error{Status: 429, Code: "rate_limited", Message: "درخواست‌ها خیلی زیاد است؛ کمی بعد دوباره تلاش کنید"}
	}
	if len(in.Username) > 64 || len(in.Password) > 128 {
		return nil, 0, service.BadRequest("bad_credentials", "شناسه یا رمز عبور نامعتبر است")
	}
	pair, user, err := s.svc.Login(r.Context(), in.Username, in.Password, ip)
	if err != nil {
		return nil, 0, err
	}
	return map[string]any{"tokens": pair, "user": user}, 200, nil
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request, _ service.Actor) (any, int, error) {
	var in struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	ip := clientIP(r, s.cfg.TrustProxy)
	if !s.svc.RateAllow(r.Context(), "refresh:"+ip, 60, time.Minute) {
		return nil, 0, &service.Error{Status: 429, Code: "rate_limited", Message: "درخواست‌ها خیلی زیاد است"}
	}
	pair, err := s.svc.Refresh(r.Context(), in.RefreshToken, ip)
	if err != nil {
		return nil, 0, err
	}
	return map[string]any{"tokens": pair}, 200, nil
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	out := map[string]any{"id": a.UserID, "username": a.Username, "full_name": a.FullName, "role": a.Role,
		"needs_shift": a.Role.NeedsShift(), "shift_active": a.ShiftID != nil}
	if a.ShiftID != nil {
		out["shift_id"], out["place_id"], out["door_id"] = a.ShiftID, a.PlaceID, a.DoorID
	}
	return out, 200, nil
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	var in struct {
		RefreshToken string `json:"refresh_token"`
	}
	_ = decode(r, &in) // body is optional
	s.svc.Logout(r.Context(), a, in.RefreshToken)
	return nil, http.StatusNoContent, nil
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	var in struct {
		Old string `json:"old_password"`
		New string `json:"new_password"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	if err := s.svc.ChangePassword(r.Context(), a, in.Old, in.New); err != nil {
		return nil, 0, err
	}
	return map[string]string{"status": "changed", "next": "login_again"}, 200, nil
}

// ---- places & shifts ----

func (s *Server) places(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	p, err := s.svc.ListPlaces(r.Context())
	return p, 200, err
}

func (s *Server) shiftStart(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	var in struct {
		PlaceID int16  `json:"place_id"`
		DoorID  *int16 `json:"door_id"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	v, err := s.svc.StartShift(r.Context(), a, in.PlaceID, in.DoorID)
	return v, 200, err
}

func (s *Server) shiftEnd(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	return nil, http.StatusNoContent, s.svc.EndShift(r.Context(), a)
}

// ---- pilgrims ----

func (s *Server) pilgrimUpsert(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	var in service.PilgrimInput
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	v, err := s.svc.UpsertPilgrim(r.Context(), a, in)
	if err != nil {
		return nil, 0, err
	}
	if v.Existing {
		return v, 200, nil
	}
	return v, 201, nil
}

func (s *Server) pilgrimLookup(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	var in struct {
		NationalID string `json:"national_id"`
		Mobile     string `json:"mobile"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	v, err := s.svc.FindPilgrims(r.Context(), a, in.NationalID, in.Mobile)
	return map[string]any{"pilgrims": v}, 200, err
}

func (s *Server) pilgrimGet(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return nil, 0, service.BadRequest("bad_id", "شناسه نامعتبر است")
	}
	v, err := s.svc.GetPilgrim(r.Context(), a, id)
	return v, 200, err
}

// ---- tents ----

func (s *Server) tentGet(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	v, err := s.svc.GetTent(r.Context(), a, chi.URLParam(r, "code"))
	return v, 200, err
}

func (s *Server) tentHistory(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	v, err := s.svc.TentHistory(r.Context(), a, chi.URLParam(r, "code"), limit)
	return map[string]any{"events": v}, 200, err
}

func (s *Server) assign(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	var in service.AssignReq
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	res, replayed, err := s.svc.Assign(r.Context(), a, in)
	if replayed {
		w.Header().Set("Idempotent-Replay", "true")
	}
	return res, 201, err
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	var in struct {
		Code string     `json:"code"`
		OpID *uuid.UUID `json:"op_id"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	res, replayed, err := s.svc.CancelAssignment(r.Context(), a, in.Code, in.OpID)
	if replayed {
		w.Header().Set("Idempotent-Replay", "true")
	}
	return res, 200, err
}

func (s *Server) returns(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	var in service.ReturnReq
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	res, replayed, err := s.svc.Return(r.Context(), a, in)
	if replayed {
		w.Header().Set("Idempotent-Replay", "true")
	}
	return res, 200, err
}

func (s *Server) setStatus(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	var in service.StatusReq
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	res, replayed, err := s.svc.SetStatus(r.Context(), a, in)
	if replayed {
		w.Header().Set("Idempotent-Replay", "true")
	}
	return res, 200, err
}

func (s *Server) move(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	var in service.MoveReq
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	res, replayed, err := s.svc.Move(r.Context(), a, in)
	if replayed {
		w.Header().Set("Idempotent-Replay", "true")
	}
	return res, 200, err
}

func (s *Server) retireRequest(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	var in service.RetireReq
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	id, err := s.svc.RequestRetire(r.Context(), a, in)
	return map[string]any{"id": id}, 201, err
}

func (s *Server) retireList(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	v, err := s.svc.ListRetireRequests(r.Context(), a)
	return map[string]any{"requests": v}, 200, err
}

func (s *Server) retireDecide(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return nil, 0, service.BadRequest("bad_id", "شناسه نامعتبر است")
	}
	var in struct {
		Approve bool `json:"approve"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	return nil, http.StatusNoContent, s.svc.DecideRetire(r.Context(), a, id, in.Approve)
}

// ---- socks, handover, sync ----

func (s *Server) socks(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	var in service.SocksReq
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	res, replayed, err := s.svc.Socks(r.Context(), a, in)
	if replayed {
		w.Header().Set("Idempotent-Replay", "true")
	}
	return res, 200, err
}

func (s *Server) handoverCreate(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	var in service.HandoverReq
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	v, err := s.svc.CreateHandover(r.Context(), a, in)
	return v, 201, err
}

func (s *Server) handoverList(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	v, err := s.svc.PendingHandovers(r.Context(), a)
	return map[string]any{"handovers": v}, 200, err
}

func (s *Server) handoverResolve(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return nil, 0, service.BadRequest("bad_id", "شناسه نامعتبر است")
	}
	var in struct {
		Accept bool `json:"accept"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	return nil, http.StatusNoContent, s.svc.ResolveHandover(r.Context(), a, id, in.Accept)
}

func (s *Server) sync(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	var in struct {
		Ops []service.SyncOp `json:"ops"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	res, err := s.svc.Sync(r.Context(), a, in.Ops)
	return map[string]any{"results": res}, 200, err
}

// ---- dashboard, reports, events ----

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	d, err := s.svc.Dashboard(r.Context(), a)
	return d, 200, err
}

func (s *Server) dailyReport(w http.ResponseWriter, r *http.Request) {
	a := actorOf(r)
	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().In(s.cfg.Location).Format("2006-01-02")
	}
	rep, err := s.svc.DailyReport(r.Context(), a, date)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if r.URL.Query().Get("format") != "xlsx" {
		writeJSON(w, 200, rep)
		return
	}
	f, err := s.svc.DailyReportXLSX(rep)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="report-%s.xlsx"`, date))
	if err := f.Write(w); err != nil {
		s.log.Error("write xlsx", "err", err)
	}
}

// events streams realtime changes (server-sent events). Browsers cannot set an
// Authorization header on EventSource, so the PWA reads this with fetch().
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	a := actorOf(r)
	if !a.Can(domain.PermDashboardOwn) && !a.Can(domain.PermDashboardAll) {
		s.fail(w, r, service.Forbidden("دسترسی لازم برای این کار را ندارید"))
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		s.fail(w, r, fmt.Errorf("streaming unsupported"))
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // long-lived response
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	ctx := r.Context()
	sub := s.svc.Subscribe(ctx)
	defer sub.Close()
	ch := sub.Channel()
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	sessionEnd := time.NewTimer(s.cfg.AccessTTL) // client reconnects with a fresh token
	defer sessionEnd.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sessionEnd.C:
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case m, ok := <-ch:
			if !ok {
				return
			}
			var ev service.Event
			if json.Unmarshal([]byte(m.Payload), &ev) != nil {
				continue
			}
			if !a.Can(domain.PermDashboardAll) && ev.PlaceID != nil && (a.PlaceID == nil || *a.PlaceID != *ev.PlaceID) {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, m.Payload)
			fl.Flush()
		}
	}
}

// ---- admin ----

func (s *Server) usersList(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	v, err := s.svc.ListUsers(r.Context(), a)
	return map[string]any{"users": v}, 200, err
}

func (s *Server) userCreate(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	var in service.CreateUserReq
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	v, err := s.svc.CreateUser(r.Context(), a, in)
	return v, 201, err
}

func (s *Server) userUpdate(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return nil, 0, service.BadRequest("bad_id", "شناسه نامعتبر است")
	}
	var in service.UpdateUserReq
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	return nil, http.StatusNoContent, s.svc.UpdateUser(r.Context(), a, id, in)
}

func (s *Server) userResetPassword(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return nil, 0, service.BadRequest("bad_id", "شناسه نامعتبر است")
	}
	pw, err := s.svc.ResetPassword(r.Context(), a, id)
	return map[string]string{"temp_password": pw}, 200, err
}

func (s *Server) tentsCreate(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	var in struct {
		Codes   []string `json:"codes"`
		From    int      `json:"from"`
		To      int      `json:"to"`
		Kind    string   `json:"kind"`
		PlaceID *int16   `json:"place_id"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	codes := in.Codes
	if len(codes) == 0 && in.From > 0 {
		if in.To < in.From || in.To-in.From >= 5000 || in.To > 999999 {
			return nil, 0, service.BadRequest("bad_range", "بازه‌ی نامعتبر")
		}
		for i := in.From; i <= in.To; i++ {
			codes = append(codes, fmt.Sprintf("CH-%06d", i))
		}
	}
	n, err := s.svc.CreateTents(r.Context(), a, codes, in.Kind, in.PlaceID)
	return map[string]int64{"created": n}, 201, err
}

func (s *Server) tentReplaceLabel(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	var in struct {
		Old string `json:"old_code"`
		New string `json:"new_code"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	return nil, http.StatusNoContent, s.svc.ReplaceLabel(r.Context(), a, in.Old, in.New)
}

func (s *Server) placeCreate(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	var in struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	id, err := s.svc.CreatePlace(r.Context(), a, in.Kind, in.Name)
	return map[string]int16{"id": id}, 201, err
}

func (s *Server) placeUpdate(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 16)
	if err != nil {
		return nil, 0, service.BadRequest("bad_id", "شناسه نامعتبر است")
	}
	var in struct {
		Active bool `json:"active"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	return nil, http.StatusNoContent, s.svc.SetPlaceActive(r.Context(), a, int16(id), in.Active)
}

func (s *Server) doorCreate(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	pid, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 16)
	if err != nil {
		return nil, 0, service.BadRequest("bad_id", "شناسه نامعتبر است")
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	id, err := s.svc.CreateDoor(r.Context(), a, int16(pid), in.Name)
	return map[string]int16{"id": id}, 201, err
}

func (s *Server) settingsGet(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	if !a.Can(domain.PermSettings) {
		return nil, 0, service.Forbidden("دسترسی لازم برای این کار را ندارید")
	}
	v, err := s.svc.AllSettings(r.Context())
	return v, 200, err
}

func (s *Server) settingsPut(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	var raw json.RawMessage
	if err := decode(r, &raw); err != nil {
		return nil, 0, err
	}
	return nil, http.StatusNoContent, s.svc.PutSetting(r.Context(), a, chi.URLParam(r, "key"), raw)
}

func (s *Server) auditList(w http.ResponseWriter, r *http.Request, a service.Actor) (any, int, error) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	v, err := s.svc.ListAudit(r.Context(), a, before, limit)
	return map[string]any{"records": v}, 200, err
}
