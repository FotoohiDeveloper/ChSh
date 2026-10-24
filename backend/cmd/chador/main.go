// Command chador is the single binary behind every container:
//
//	chador serve         HTTP API (+ internal metrics port)
//	chador worker        background jobs (SMS, reminders, retention)
//	chador migrate       apply database migrations and exit
//	chador create-admin  create the first admin user (prints a one-time password)
//	chador seed-places   create the initial places (gates, storage room, workshop)
//	chador import-tents  load tent codes from the label CSV
//	chador healthcheck   exit 0 when the local API answers /readyz (container HEALTHCHECK)
package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // Asia/Tehran must resolve even in a minimal container image

	"github.com/redis/go-redis/v9"

	"chador/internal/api"
	"chador/internal/jobs"
	"chador/internal/platform"
	"chador/internal/provider"
	"chador/internal/service"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: chador <serve|worker|migrate|create-admin|seed-places|import-tents> [flags]")
		os.Exit(2)
	}
	if os.Args[1] == "healthcheck" { // used by the container HEALTHCHECK; no shell or curl in the image
		os.Exit(healthcheck())
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, log, os.Args[1], os.Args[2:]); err != nil {
		log.Error("fatal", "cmd", os.Args[1], "err", err)
		os.Exit(1)
	}
}

func healthcheck() int {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + addr + "/readyz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "unhealthy:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "unhealthy: status", resp.StatusCode)
		return 1
	}
	return 0
}

type app struct {
	cfg  platform.Config
	svc  *service.Service
	jobs *jobs.Client
	sms  provider.SMSSender
	rdb  *redis.Client
	stop func()
}

func bootstrap(ctx context.Context, log *slog.Logger, needVendors bool) (*app, error) {
	cfg, err := platform.LoadConfig()
	if err != nil {
		return nil, err
	}
	pool, err := platform.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	ropt, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return nil, err
	}
	ropt.PoolSize, ropt.MinIdleConns = 50, 5
	ropt.DialTimeout, ropt.ReadTimeout, ropt.WriteTimeout = 2*time.Second, time.Second, time.Second
	rdb := redis.NewClient(ropt)
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis: %w", err)
	}
	crypto, err := platform.NewCrypto(cfg.EncKey, cfg.IdxKey)
	if err != nil {
		return nil, err
	}

	var inq provider.Inquirer = provider.MockInquirer{}
	if cfg.InquiryMode == "http" && needVendors {
		if cfg.InquiryURL == "" || cfg.InquiryToken == "" {
			return nil, errors.New("INQUIRY_URL and INQUIRY_TOKEN are required when INQUIRY_MODE=http")
		}
		inq = &provider.HTTPInquirer{URL: cfg.InquiryURL, Token: cfg.InquiryToken, Client: &http.Client{Timeout: 5 * time.Second}}
	}
	if cfg.InquiryMode == "off" {
		inq = provider.OffInquirer{}
	}
	inq = provider.GuardInquirer(inq, 4*time.Second)

	var sms provider.SMSSender = provider.MockSMS{Log: log}
	if cfg.SMSMode == "http" && needVendors {
		if cfg.SMSURL == "" || cfg.SMSToken == "" {
			return nil, errors.New("SMS_URL and SMS_TOKEN are required when SMS_MODE=http")
		}
		sms = &provider.HTTPSMS{URL: cfg.SMSURL, Token: cfg.SMSToken, Client: &http.Client{Timeout: 10 * time.Second}}
	}
	if cfg.SMSMode == "off" {
		sms = provider.NoopSMS{Log: log}
	}
	jc, err := jobs.NewClient(cfg.RedisURL)
	if err != nil {
		return nil, err
	}
	svc := service.New(service.Deps{DB: pool, Redis: rdb, Config: cfg, Crypto: crypto, Inquiry: inq, Queue: jc, Log: log})
	return &app{cfg: cfg, svc: svc, jobs: jc, sms: sms, rdb: rdb, stop: func() {
		svc.Close()
		_ = jc.Close()
		_ = rdb.Close()
		pool.Close()
	}}, nil
}

func run(ctx context.Context, log *slog.Logger, cmd string, args []string) error {
	// Vendor credentials are only mandatory for the processes that call the vendors;
	// migrating or creating the first admin must work before the tokens exist.
	a, err := bootstrap(ctx, log, cmd == "serve" || cmd == "worker")
	if err != nil {
		return err
	}
	defer a.stop()

	switch cmd {
	case "migrate":
		// Schema changes use the owner role when one is configured; the API itself runs unprivileged.
		pool := a.svc.DB()
		if a.cfg.MigrateURL != "" {
			owner, err := platform.NewPool(ctx, a.cfg.MigrateURL)
			if err != nil {
				return fmt.Errorf("migrate database: %w", err)
			}
			defer owner.Close()
			pool = owner
		}
		applied, err := platform.Migrate(ctx, pool, a.cfg.AppDBRole)
		log.Info("migrations applied", "files", applied)
		return err

	case "serve":
		if a.cfg.AutoMigrate { // dev convenience; production runs the separate migrate step
			if _, err := platform.Migrate(ctx, a.svc.DB(), a.cfg.AppDBRole); err != nil {
				return err
			}
		}
		return serve(ctx, log, a)

	case "worker":
		w, err := jobs.NewWorker(a.cfg, a.svc, a.sms, log)
		if err != nil {
			return err
		}
		log.Info("worker started")
		return w.Run(ctx)

	case "create-admin":
		fs := flag.NewFlagSet("create-admin", flag.ExitOnError)
		user := fs.String("username", "admin", "login name")
		name := fs.String("name", "مدیر سامانه", "display name")
		_ = fs.Parse(args)
		pw, err := a.svc.BootstrapAdmin(ctx, *user, *name)
		if err != nil {
			return err
		}
		fmt.Printf("username: %s\none-time password: %s\n(change it at first login)\n", strings.ToLower(*user), pw)
		return nil

	case "seed-places":
		fs := flag.NewFlagSet("seed-places", flag.ExitOnError)
		gates := fs.String("gates", "احمدبن‌موسی,باب‌الرضا,باب‌السجاد", "comma-separated gate names")
		storage := fs.String("storage", "اتاق امانات", "storage room name")
		workshop := fs.String("workshop", "کارگاه شست‌وشو", "workshop name")
		_ = fs.Parse(args)
		return seedPlaces(ctx, a, *gates, *storage, *workshop)

	case "import-tents":
		fs := flag.NewFlagSet("import-tents", flag.ExitOnError)
		file := fs.String("file", "", "tent_ids.csv produced by the label generator")
		place := fs.String("place", "اتاق امانات", "place the tents start in")
		_ = fs.Parse(args)
		return importTents(ctx, a, *file, *place)
	}
	return fmt.Errorf("unknown command %q", cmd)
}

func serve(ctx context.Context, log *slog.Logger, a *app) error {
	srv := api.New(a.svc, a.cfg, log)
	httpSrv := &http.Server{
		Addr:              a.cfg.HTTPAddr,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	metricsAddr := os.Getenv("METRICS_ADDR")
	var metricsSrv *http.Server
	if metricsAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", srv.MetricsHandler())
		metricsSrv = &http.Server{Addr: metricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("metrics server", "err", err)
			}
		}()
	}
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.ListenAndServe() }()
	log.Info("api listening", "addr", a.cfg.HTTPAddr, "env", a.cfg.Env)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if metricsSrv != nil {
		_ = metricsSrv.Shutdown(sctx)
	}
	return httpSrv.Shutdown(sctx)
}

func seedPlaces(ctx context.Context, a *app, gates, storage, workshop string) error {
	db := a.svc.DB()
	add := func(kind, name string) error {
		_, err := db.Exec(ctx, `INSERT INTO places(kind, name) VALUES ($1::place_kind, $2) ON CONFLICT (name) DO NOTHING`, kind, name)
		return err
	}
	for _, g := range strings.Split(gates, ",") {
		if g = strings.TrimSpace(g); g != "" {
			if err := add("gate", g); err != nil {
				return err
			}
		}
	}
	if err := add("storage", storage); err != nil {
		return err
	}
	if err := add("workshop", workshop); err != nil {
		return err
	}
	fmt.Println("places ready")
	return nil
}

func importTents(ctx context.Context, a *app, file, place string) error {
	if file == "" {
		return errors.New("--file is required")
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return err
	}
	col := -1
	for i, h := range header {
		if h == "tent_id" {
			col = i
		}
	}
	if col < 0 {
		return errors.New("no tent_id column in csv")
	}
	var codes []string
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		codes = append(codes, rec[col])
	}
	var placeID *int16
	var id int16
	if err := a.svc.DB().QueryRow(ctx, `SELECT id FROM places WHERE name=$1`, place).Scan(&id); err != nil {
		return fmt.Errorf("place %q not found (run seed-places first): %w", place, err)
	}
	placeID = &id
	tag, err := a.svc.DB().Exec(ctx, `INSERT INTO tents(code, place_id) SELECT unnest($1::text[]), $2 ON CONFLICT (code) DO NOTHING`, codes, placeID)
	if err != nil {
		return err
	}
	fmt.Printf("%d tents in file, %d newly registered\n", len(codes), tag.RowsAffected())
	return nil
}
