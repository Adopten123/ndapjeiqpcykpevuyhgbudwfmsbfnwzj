package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"rzdarctica.local/backend/internal/core/config"
	"rzdarctica.local/backend/internal/core/demoauth"
	"rzdarctica.local/backend/internal/features/approvals"
	"rzdarctica.local/backend/internal/features/calculations"
	"rzdarctica.local/backend/internal/features/calculations/repository"
	"rzdarctica.local/backend/internal/features/calculations/service"
	httptransport "rzdarctica.local/backend/internal/features/calculations/transport/http"
	"rzdarctica.local/backend/internal/features/simulation"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		if errors.Is(err, approvals.ErrSourceUnavailable) {
			logger.Error("Нет сохранённого учебного источника текущей редакции; требуется явный источник или новый изолированный контур.")
		} else {
			logger.Error("Не удалось запустить локальный сервер; проверьте конфигурацию и доступность PostgreSQL.")
		}
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	auth, err := demoauth.Load(os.Getenv("RZD_AUTH_FILE"))
	if err != nil {
		return err
	}
	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	initCtx, cancelInit := context.WithTimeout(signals, 30*time.Second)
	defer cancelInit()
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	if !cfg.IsolatedDatabase(poolCfg.ConnConfig.Host, poolCfg.ConnConfig.Database) {
		return errors.New("требуется отдельная локальная учебная база")
	}
	for _, fallback := range poolCfg.ConnConfig.Fallbacks {
		if !cfg.AllowedDatabaseHost(fallback.Host) {
			return errors.New("внешние резервные адреса БД запрещены в демо")
		}
	}
	poolCfg.MaxConns = 8
	poolCfg.ConnConfig.ConnectTimeout = 3 * time.Second
	pool, err := pgxpool.NewWithConfig(initCtx, poolCfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err = pool.Ping(initCtx); err != nil {
		return err
	}
	driver := riverpgxv5.New(pool)
	inserter, err := river.NewClient(driver, &river.Config{Logger: logger})
	if err != nil {
		return err
	}
	store := repository.New(pool, inserter)
	approvalStore := approvals.New(pool)
	approvalStore.SetQueue(inserter)
	computer := service.NewComputeClient(cfg.ComputeURL)
	simStore := simulation.New(pool, inserter, computer, cfg.SimulatorURL)
	// Общий локальный замок сериализует запуск миграций двух экземпляров демо.
	lock, err := pool.Begin(initCtx)
	if err != nil {
		return err
	}
	defer lock.Rollback(context.Background())
	if _, err = lock.Exec(initCtx, "SELECT pg_advisory_xact_lock($1)", int64(726438221)); err != nil {
		return err
	}
	migrator, err := rivermigrate.New(driver, nil)
	if err != nil {
		return err
	}
	if _, err = migrator.Migrate(initCtx, rivermigrate.DirectionUp, nil); err != nil {
		return err
	}
	if err = store.Migrate(initCtx); err != nil {
		return err
	}
	if err = approvalStore.Migrate(initCtx); err != nil {
		return err
	}
	if err = simStore.Migrate(initCtx); err != nil {
		return err
	}
	if err = approvalStore.Seed(initCtx, auth.Users()); err != nil {
		return err
	}
	if err = lock.Commit(initCtx); err != nil {
		return err
	}
	hasSource, err := approvalStore.HasSource(initCtx)
	if err != nil {
		return err
	}
	if !hasSource {
		source, err := computer.Snapshot(initCtx, "S1")
		if err != nil {
			return err
		}
		if err = approvalStore.EnsureSource(initCtx, source); err != nil {
			return err
		}
	}

	workers := river.NewWorkers()
	river.AddWorker(workers, service.NewWorker(store, computer))
	river.AddWorker(workers, simulation.NewWorker(simStore))
	queue, err := river.NewClient(driver, &river.Config{Logger: logger, Workers: workers, Queues: map[string]river.QueueConfig{calculations.Queue: {MaxWorkers: 1}, simulation.Queue: {MaxWorkers: 1}}})
	if err != nil {
		return err
	}
	workCtx, cancelWork := context.WithCancel(context.Background())
	defer cancelWork()
	if err = queue.Start(workCtx); err != nil {
		return err
	}
	api := service.New(store, computer)
	handler := httptransport.New(api, func(ctx context.Context) (bool, bool) { return pool.Ping(ctx) == nil, computer.Health(ctx) == nil })
	approvalHandler := approvalStore.Handler()
	protected := http.NewServeMux()
	protected.Handle("/api/v1/planning-context", approvalHandler)
	protected.Handle("/api/v1/planning-context/source", approvalHandler)
	protected.Handle("/api/v1/planning-calculations", approvalHandler)
	protected.Handle("/api/v1/approval-packets", approvalHandler)
	protected.Handle("/api/v1/approval-packets/", approvalHandler)
	protected.Handle("/api/v1/approval-context/invalidate", approvalHandler)
	simHandler := simStore.Handler()
	protected.Handle("/api/v1/simulation/runs", simHandler)
	protected.Handle("/api/v1/simulation/runs/", simHandler)
	protected.Handle("/", handler)
	root := http.NewServeMux()
	root.Handle("/api/v1/session", auth.SessionHandler())
	root.Handle("GET /api/health", handler)
	root.Handle("/", auth.Middleware(approvalStore.Guard(protected)))
	server := &http.Server{Addr: cfg.HTTPAddr, Handler: root, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 25 * time.Second, IdleTimeout: 60 * time.Second}
	return serve(signals, server, queue.Stop, logger)
}
