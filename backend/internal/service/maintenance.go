package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"chador/internal/domain"
)

// DueReminders marks overdue assignments as reminded and returns one SMS job
// per pilgrim. The UPDATE ... RETURNING is the claim, so two workers can never
// remind the same assignment twice.
func (s *Service) DueReminders(ctx context.Context) ([]SMSJob, error) {
	rows, err := s.db.Query(ctx, `UPDATE assignments SET reminders = reminders + 1, last_reminded_at = now()
		WHERE returned_at IS NULL AND cancelled_at IS NULL AND lost_at IS NULL
		AND reminders < $1 AND assigned_at < now() - make_interval(secs => $2)
		AND (last_reminded_at IS NULL OR last_reminded_at < now() - make_interval(secs => $2))
		RETURNING pilgrim_id, reminders`, s.cfg.MaxReminders, s.cfg.ReminderAfter.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[uuid.UUID]bool{}
	var jobs []SMSJob
	for rows.Next() {
		var p uuid.UUID
		var n int
		if err := rows.Scan(&p, &n); err != nil {
			return nil, err
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		jobs = append(jobs, SMSJob{PilgrimID: p, Template: "reminder", DedupeKey: fmt.Sprintf("rem:%s:%d", p, n)})
	}
	return jobs, rows.Err()
}

// Housekeeping closes forgotten shifts and prunes old idempotency records.
func (s *Service) Housekeeping(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, `UPDATE shifts SET ended_at=now() WHERE ended_at IS NULL AND started_at < now() - interval '16 hours'`); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM client_ops WHERE created_at < now() - interval '7 days'`)
	return err
}

// ---- SMS bookkeeping (used by the worker) ----

// SMSText renders a template; unknown names fail loudly.
func (s *Service) SMSText(ctx context.Context, template string) (string, error) {
	var m map[string]string
	if err := s.Setting(ctx, "sms_templates", &m); err != nil {
		return "", err
	}
	t, ok := m[template]
	if !ok {
		return "", fmt.Errorf("unknown sms template %q", template)
	}
	return t, nil
}

// ClaimSMS records the dedupe key; false means this message was already handled.
func (s *Service) ClaimSMS(ctx context.Context, j SMSJob) (bool, error) {
	tag, err := s.db.Exec(ctx, `INSERT INTO sms_log(dedupe_key, pilgrim_id, template) VALUES ($1,$2,$3) ON CONFLICT (dedupe_key) DO NOTHING`,
		j.DedupeKey, j.PilgrimID, j.Template)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 1 {
		return true, nil
	}
	// existing row: retry only if a previous attempt failed
	tag, err = s.db.Exec(ctx, `UPDATE sms_log SET state='queued', error=NULL, created_at=now() WHERE dedupe_key=$1
		AND (state='failed' OR (state='queued' AND created_at < now() - interval '2 minutes'))`, j.DedupeKey)
	return err == nil && tag.RowsAffected() == 1, err
}

func (s *Service) FinishSMS(ctx context.Context, key, ref string, sendErr error) {
	if sendErr != nil {
		msg := sendErr.Error()
		if len(msg) > 300 {
			msg = msg[:300]
		}
		_, _ = s.db.Exec(ctx, `UPDATE sms_log SET state='failed', error=$2 WHERE dedupe_key=$1`, key, msg)
		return
	}
	_, _ = s.db.Exec(ctx, `UPDATE sms_log SET state='sent', provider_ref=$2, sent_at=now() WHERE dedupe_key=$1`, key, ref)
}

// ---- audit viewer ----

type AuditRow struct {
	ID       int64     `json:"id"`
	At       time.Time `json:"at"`
	User     string    `json:"user,omitempty"`
	Action   string    `json:"action"`
	Entity   string    `json:"entity,omitempty"`
	EntityID string    `json:"entity_id,omitempty"`
	IP       string    `json:"ip,omitempty"`
}

func (s *Service) ListAudit(ctx context.Context, a Actor, before int64, limit int) ([]AuditRow, error) {
	if err := a.require(domain.PermAudit); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if before <= 0 {
		before = 1 << 62
	}
	rows, err := s.db.Query(ctx, `SELECT l.id, l.at, COALESCE(u.username,''), l.action, COALESCE(l.entity,''), COALESCE(l.entity_id,''), COALESCE(l.ip,'')
		FROM audit_log l LEFT JOIN users u ON u.id=l.user_id WHERE l.id < $1 ORDER BY l.id DESC LIMIT $2`, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditRow{}
	for rows.Next() {
		var r AuditRow
		if err := rows.Scan(&r.ID, &r.At, &r.User, &r.Action, &r.Entity, &r.EntityID, &r.IP); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
