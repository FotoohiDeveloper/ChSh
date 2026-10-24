// Package jobs runs background work on a Redis-backed queue: SMS delivery,
// identity re-verification, overdue reminders and data retention.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"chador/internal/platform"
	"chador/internal/provider"
	"chador/internal/service"
)

const (
	typeSMS          = "sms:send"
	typeVerify       = "pilgrim:verify"
	typeReminders    = "cron:reminders"
	typeRetention    = "cron:retention"
	typeHousekeeping = "cron:housekeeping"
)

// Client enqueues jobs; it implements service.Queue.
type Client struct{ c *asynq.Client }

func NewClient(redisURL string) (*Client, error) {
	opt, err := asynq.ParseRedisURI(redisURL)
	if err != nil {
		return nil, err
	}
	return &Client{asynq.NewClient(opt)}, nil
}

func (c *Client) Close() error { return c.c.Close() }

func (c *Client) SMS(ctx context.Context, j service.SMSJob) error {
	b, _ := json.Marshal(j)
	_, err := c.c.EnqueueContext(ctx, asynq.NewTask(typeSMS, b),
		asynq.Queue("critical"), asynq.MaxRetry(6), asynq.Timeout(30*time.Second),
		asynq.TaskID("sms:"+j.DedupeKey), asynq.Retention(24*time.Hour))
	if errors.Is(err, asynq.ErrTaskIDConflict) {
		return nil // already queued or done: exactly what dedupe is for
	}
	return err
}

func (c *Client) VerifyPilgrim(ctx context.Context, id uuid.UUID, delay time.Duration) error {
	b, _ := json.Marshal(map[string]string{"id": id.String()})
	_, err := c.c.EnqueueContext(ctx, asynq.NewTask(typeVerify, b),
		asynq.Queue("default"), asynq.MaxRetry(10), asynq.ProcessIn(delay), asynq.Timeout(15*time.Second),
		asynq.TaskID("verify:"+id.String()+":"+fmt.Sprint(time.Now().Unix()/60)))
	if errors.Is(err, asynq.ErrTaskIDConflict) {
		return nil
	}
	return err
}

// Worker consumes the queue and runs the scheduled jobs.
type Worker struct {
	srv   *asynq.Server
	sched *asynq.Scheduler
	mux   *asynq.ServeMux
}

func NewWorker(cfg platform.Config, svc *service.Service, sms provider.SMSSender, log *slog.Logger) (*Worker, error) {
	opt, err := asynq.ParseRedisURI(cfg.RedisURL)
	if err != nil {
		return nil, err
	}
	srv := asynq.NewServer(opt, asynq.Config{
		Concurrency: 10,
		Queues:      map[string]int{"critical": 6, "default": 3, "low": 1},
		RetryDelayFunc: func(n int, _ error, _ *asynq.Task) time.Duration {
			d := time.Duration(1<<uint(min(n, 8))) * 5 * time.Second // 10s, 20s, 40s ... capped ~21m
			return d
		},
		ErrorHandler: asynq.ErrorHandlerFunc(func(_ context.Context, t *asynq.Task, err error) {
			log.Warn("job failed", "type", t.Type(), "err", err)
		}),
	})
	mux := asynq.NewServeMux()
	guard := provider.NewBreaker(5, 30*time.Second)

	mux.HandleFunc(typeSMS, func(ctx context.Context, t *asynq.Task) error {
		var j service.SMSJob
		if err := json.Unmarshal(t.Payload(), &j); err != nil {
			return fmt.Errorf("%w: %v", asynq.SkipRetry, err)
		}
		claimed, err := svc.ClaimSMS(ctx, j)
		if err != nil {
			return err
		}
		if !claimed {
			return nil // already sent or in flight
		}
		mobile, err := svc.PilgrimMobile(ctx, j.PilgrimID)
		if err != nil {
			svc.FinishSMS(ctx, j.DedupeKey, "", errors.New("recipient unavailable"))
			return fmt.Errorf("%w: recipient unavailable", asynq.SkipRetry) // anonymized or deleted
		}
		text, err := svc.SMSText(ctx, j.Template)
		if err != nil {
			svc.FinishSMS(ctx, j.DedupeKey, "", err)
			return fmt.Errorf("%w: %v", asynq.SkipRetry, err)
		}
		var ref string
		err = guard.Do(func() error {
			var e error
			ref, e = sms.Send(ctx, mobile, text)
			return e
		})
		svc.FinishSMS(ctx, j.DedupeKey, ref, err)
		return err
	})

	mux.HandleFunc(typeVerify, func(ctx context.Context, t *asynq.Task) error {
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return fmt.Errorf("%w: %v", asynq.SkipRetry, err)
		}
		id, err := uuid.Parse(p.ID)
		if err != nil {
			return fmt.Errorf("%w: %v", asynq.SkipRetry, err)
		}
		return svc.VerifyPending(ctx, id)
	})

	cl, err := NewClient(cfg.RedisURL)
	if err != nil {
		return nil, err
	}
	mux.HandleFunc(typeReminders, func(ctx context.Context, _ *asynq.Task) error {
		list, err := svc.DueReminders(ctx)
		if err != nil {
			return err
		}
		for _, j := range list {
			if err := cl.SMS(ctx, j); err != nil {
				return err
			}
		}
		if len(list) > 0 {
			log.Info("reminders queued", "count", len(list))
		}
		return nil
	})
	mux.HandleFunc(typeRetention, func(ctx context.Context, _ *asynq.Task) error {
		n, err := svc.AnonymizeInactive(ctx)
		if n > 0 {
			log.Info("pilgrim data anonymized", "count", n)
		}
		return err
	})
	mux.HandleFunc(typeHousekeeping, func(ctx context.Context, _ *asynq.Task) error { return svc.Housekeeping(ctx) })

	sched := asynq.NewScheduler(opt, &asynq.SchedulerOpts{Location: cfg.Location})
	for _, e := range []struct{ spec, typ string }{
		{"@every 10m", typeReminders},
		{"@every 1h", typeHousekeeping},
		{"0 3 * * *", typeRetention},
	} {
		if _, err := sched.Register(e.spec, asynq.NewTask(e.typ, nil), asynq.Queue("low"), asynq.Unique(8*time.Minute), asynq.MaxRetry(2)); err != nil {
			return nil, err
		}
	}
	return &Worker{srv: srv, sched: sched, mux: mux}, nil
}

// Run blocks until ctx is cancelled, then drains in-flight jobs.
func (w *Worker) Run(ctx context.Context) error {
	if err := w.sched.Start(); err != nil {
		return err
	}
	if err := w.srv.Start(w.mux); err != nil {
		return err
	}
	<-ctx.Done()
	w.sched.Shutdown()
	w.srv.Shutdown()
	return nil
}
