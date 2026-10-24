// Package provider contains adapters for the two external services the system
// depends on. Both sit behind small interfaces so the real vendor can be
// plugged in without touching business code, and both are wrapped with a
// timeout and a circuit breaker so an outage cannot stall the gates.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"chador/internal/domain"
)

// ---- identity inquiry ----

type InquiryResult struct {
	Verified bool
	FullName string // optional, returned by some services
}

type Inquirer interface {
	Verify(ctx context.Context, nationalID string, birth time.Time, mobile string) (InquiryResult, error)
}

// MockInquirer accepts any structurally valid national ID. Dev/test only;
// the config loader refuses to start in prod with it.
type MockInquirer struct{}

func (MockInquirer) Verify(_ context.Context, nid string, _ time.Time, _ string) (InquiryResult, error) {
	return InquiryResult{Verified: domain.ValidNationalID(nid)}, nil
}

// OffInquirer is used when no inquiry vendor is contracted yet (INQUIRY_MODE=off). Every lookup reports
// "service unavailable", so pilgrims are stored as unverified and can be handed tents only when
// ALLOW_UNVERIFIED=true. Identity is NOT checked in this mode.
type OffInquirer struct{}

func (OffInquirer) Verify(context.Context, string, time.Time, string) (InquiryResult, error) {
	return InquiryResult{}, errors.New("identity inquiry is switched off (INQUIRY_MODE=off)")
}

// HTTPInquirer is a generic JSON client. The exact request/response contract
// depends on the vendor that is contracted; adjust the two structs below.
type HTTPInquirer struct {
	URL    string
	Token  string
	Client *http.Client
}

type inquiryReq struct {
	NationalID string `json:"national_id"`
	BirthDate  string `json:"birth_date"`
	Mobile     string `json:"mobile"`
}
type inquiryResp struct {
	Match    bool   `json:"match"`
	FullName string `json:"full_name"`
}

func (h *HTTPInquirer) Verify(ctx context.Context, nid string, birth time.Time, mobile string) (InquiryResult, error) {
	body, _ := json.Marshal(inquiryReq{nid, birth.Format("2006-01-02"), mobile})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		return InquiryResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.Token)
	resp, err := h.Client.Do(req)
	if err != nil {
		return InquiryResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return InquiryResult{}, fmt.Errorf("inquiry service status %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return InquiryResult{}, nil // 4xx: the service answered, identity not confirmed
	}
	var out inquiryResp
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return InquiryResult{}, err
	}
	return InquiryResult{Verified: out.Match, FullName: out.FullName}, nil
}

// ---- SMS ----

type SMSSender interface {
	Send(ctx context.Context, mobile, text string) (providerRef string, err error)
}

type MockSMS struct{ Log *slog.Logger }

func (m MockSMS) Send(_ context.Context, mobile, text string) (string, error) {
	m.Log.Info("sms (mock)", "to", domain.MaskMobile(mobile), "chars", len([]rune(text)))
	return "mock", nil
}

// NoopSMS is used when no SMS vendor is contracted yet (SMS_MODE=off): nothing is sent.
type NoopSMS struct{ Log *slog.Logger }

func (n NoopSMS) Send(_ context.Context, mobile, _ string) (string, error) {
	n.Log.Info("sms skipped (SMS_MODE=off)", "to", domain.MaskMobile(mobile))
	return "off", nil
}

type HTTPSMS struct {
	URL    string
	Token  string
	Client *http.Client
}

func (h *HTTPSMS) Send(ctx context.Context, mobile, text string) (string, error) {
	body, _ := json.Marshal(map[string]string{"to": mobile, "text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.Token)
	resp, err := h.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("sms service status %d", resp.StatusCode)
	}
	var out struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out)
	return out.ID, nil
}

// ---- circuit breaker ----

var ErrCircuitOpen = errors.New("circuit open")

// Breaker opens after `threshold` consecutive failures and lets one probe
// through after `cooldown`. While open, calls fail immediately instead of
// waiting for a timeout, which keeps gate operations fast during an outage.
type Breaker struct {
	mu        sync.Mutex
	failures  int
	openUntil time.Time
	threshold int
	cooldown  time.Duration
}

func NewBreaker(threshold int, cooldown time.Duration) *Breaker {
	return &Breaker{threshold: threshold, cooldown: cooldown}
}

func (b *Breaker) Do(fn func() error) error {
	b.mu.Lock()
	if time.Now().Before(b.openUntil) {
		b.mu.Unlock()
		return ErrCircuitOpen
	}
	b.mu.Unlock()
	err := fn()
	b.mu.Lock()
	defer b.mu.Unlock()
	if err != nil {
		b.failures++
		if b.failures >= b.threshold {
			b.openUntil = time.Now().Add(b.cooldown)
			b.failures = 0
		}
		return err
	}
	b.failures = 0
	return nil
}

type guardedInquirer struct {
	inner   Inquirer
	breaker *Breaker
	timeout time.Duration
}

// GuardInquirer adds a per-call timeout and a circuit breaker.
func GuardInquirer(in Inquirer, timeout time.Duration) Inquirer {
	return &guardedInquirer{in, NewBreaker(5, 30*time.Second), timeout}
}

func (g *guardedInquirer) Verify(ctx context.Context, nid string, birth time.Time, mobile string) (res InquiryResult, err error) {
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	err = g.breaker.Do(func() error {
		var e error
		res, e = g.inner.Verify(ctx, nid, birth, mobile)
		return e
	})
	return res, err
}
