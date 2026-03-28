package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/anuragxxd/conquest/internal/adapter"
	"github.com/anuragxxd/conquest/internal/api"
	"github.com/anuragxxd/conquest/internal/api/tes"
	"github.com/anuragxxd/conquest/internal/api/wes"
	k8sbackend "github.com/anuragxxd/conquest/internal/backend/k8s"
	"github.com/anuragxxd/conquest/internal/config"
	"github.com/anuragxxd/conquest/internal/controller"
	"github.com/anuragxxd/conquest/internal/filer"
	"github.com/anuragxxd/conquest/internal/reconciler"
	"github.com/anuragxxd/conquest/internal/repository/pg"
	"github.com/anuragxxd/conquest/internal/storage"
	"github.com/anuragxxd/conquest/internal/taskpolicy"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Subcommand dispatch.
	if len(os.Args) >= 3 && os.Args[1] == "filer" && os.Args[2] == "stage-in" {
		if err := runFilerStageIn(log); err != nil {
			log.Error("stage-in failed", "err", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if len(os.Args) >= 3 && os.Args[1] == "filer" && os.Args[2] == "exec" {
		filer.RunExecWrapper(log)
		// RunExecWrapper calls syscall.Exit, so we never reach here.
		os.Exit(0)
	}

	if len(os.Args) >= 3 && os.Args[1] == "runner" && os.Args[2] == "nextflow" {
		if err := runRunnerNextflow(log); err != nil {
			log.Error("runner nextflow failed", "err", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if len(os.Args) >= 3 && os.Args[1] == "runner" && os.Args[2] == "snakemake" {
		if err := runRunnerSnakemake(log); err != nil {
			log.Error("runner snakemake failed", "err", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	// Load config.
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log.Info("starting conquest",
		"addr", cfg.HTTPAddr,
		"bucket", cfg.S3Bucket,
		"namespace", cfg.K8sNamespace,
	)

	// Connect to Postgres.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.PostgresDSN)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping postgres: %w", err)
	}
	log.Info("connected to postgres")

	// Repositories.
	runStore := pg.NewRunStore(pool)
	taskStore := pg.NewTaskStore(pool)
	eventStore := pg.NewEventStore(pool)

	// Storage layout.
	layout := storage.Layout{Bucket: cfg.S3Bucket, Version: cfg.LayoutVersion}

	adapters := []adapter.RunAdapter{
		adapter.NewNextflowRunAdapter(adapter.NextflowRunAdapterConfig{
			DefaultRunnerImage:    cfg.RunnerImage,
			RunnerImagesByVersion: cfg.NextflowRunnerImages,
			InternalBaseURL:       cfg.InternalBaseURL,
			S3Endpoint:            cfg.S3Endpoint,
			S3Bucket:              cfg.S3Bucket,
			S3Region:              cfg.S3Region,
		}),
	}
	if cfg.SnakemakeRunnerImage != "" {
		adapters = append(adapters, adapter.NewSnakemakeRunAdapter(adapter.SnakemakeRunAdapterConfig{
			RunnerImage:     cfg.SnakemakeRunnerImage,
			TaskImage:       cfg.SnakemakeTaskImage,
			DefaultJobs:     cfg.SnakemakeDefaultJobs,
			DefaultDiskMB:   cfg.SnakemakeDefaultDiskMB,
			InternalBaseURL: cfg.InternalBaseURL,
			S3Endpoint:      cfg.S3Endpoint,
			S3Bucket:        cfg.S3Bucket,
			S3Region:        cfg.S3Region,
		}))
	}
	adapterRegistry := adapter.NewRegistry(adapters...)

	// Controller.
	runCtrl := controller.NewRunController(runStore, taskStore, eventStore, layout, adapterRegistry, log)

	// Workspace config is shared between task and run backends.
	wsCfg := k8sbackend.WorkspaceConfig{
		Mode:         cfg.WorkspaceMode,
		AccessMode:   cfg.WorkspaceAccessMode,
		StorageClass: cfg.WorkspaceStorageClass,
		Size:         cfg.WorkspaceSize,
	}

	// Try to create K8s backend (only works inside a K8s cluster).
	var taskBackend *k8sbackend.Backend
	var clientset kubernetes.Interface
	k8sCfg, err := rest.InClusterConfig()
	if err != nil {
		log.Warn("not running in k8s cluster, TES backend disabled", "err", err)
	} else {
		clientset, err = kubernetes.NewForConfig(k8sCfg)
		if err != nil {
			return fmt.Errorf("create k8s client: %w", err)
		}

		backendCfg := k8sbackend.Config{
			Namespace:         cfg.K8sNamespace,
			ConquestImage:     cfg.ConquestImage,
			S3Endpoint:        cfg.S3Endpoint,
			S3Bucket:          cfg.S3Bucket,
			S3Region:          cfg.S3Region,
			ServiceAccount:    cfg.ServiceAccount,
			StorageSecretName: cfg.StorageSecretName,
			ImagePullSecrets:  cfg.ImagePullSecrets,
			Workspace:         wsCfg,
		}
		taskBackend = k8sbackend.NewBackend(clientset, backendCfg, log)
		log.Info("k8s backend enabled",
			"namespace", cfg.K8sNamespace,
			"conquest_image", cfg.ConquestImage,
		)
	}

	// Run-level backend (Nextflow runner Jobs).
	var runBackend *k8sbackend.RunBackend
	if clientset != nil {
		runBackendCfg := k8sbackend.RunConfig{
			Namespace:               cfg.K8sNamespace,
			ServiceAccount:          cfg.ServiceAccount,
			StorageSecretName:       cfg.StorageSecretName,
			ImagePullSecrets:        cfg.ImagePullSecrets,
			EphemeralStorageRequest: cfg.RunnerEphemeralStorageRequest,
			EphemeralStorageLimit:   cfg.RunnerEphemeralStorageLimit,
			Workspace:               wsCfg,
		}
		runBackend = k8sbackend.NewRunBackend(clientset, runBackendCfg, log)
		log.Info("run backend enabled",
			"namespace", cfg.K8sNamespace,
			"internal_base_url", cfg.InternalBaseURL,
		)
	}

	// Wire backends into the controller.
	if runBackend != nil {
		runCtrl.SetRunBackend(runBackend)
	}
	if taskBackend != nil {
		runCtrl.SetTaskBackend(taskBackend)
	}

	// HTTP handlers.
	wesHandler := wes.NewHandler(runCtrl)
	tesHandler := tes.NewHandler(runStore, taskStore, eventStore, taskBackend)
	tesHandler.SetPolicy(&taskpolicy.Policy{
		Resources: taskpolicy.ResourceLimits{
			DefaultCPUCores: int32(cfg.TaskDefaultCPUCores),
			DefaultRAMGb:    cfg.TaskDefaultRAMGb,
			DefaultDiskGb:   cfg.TaskDefaultDiskGb,
			MaxCPUCores:     int32(cfg.TaskMaxCPUCores),
			MaxRAMGb:        cfg.TaskMaxRAMGb,
			MaxDiskGb:       cfg.TaskMaxDiskGb,
			MinCPUCores:     int32(cfg.TaskMinCPUCores),
			MinRAMGb:        cfg.TaskMinRAMGb,
			MinDiskGb:       cfg.TaskMinDiskGb,
		},
		RejectOnMaxViolation: true,
	})

	// Router.
	router := api.NewRouter(wesHandler, tesHandler)

	// Start reconcilers if backends are available.
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	recCfg := reconciler.DefaultConfig()
	recCfg.PollInterval = cfg.ReconcilerInterval

	if taskBackend != nil {
		rec := reconciler.New(taskStore, eventStore, taskBackend, recCfg, log)
		go rec.Run(appCtx)
	}

	if runBackend != nil {
		runRec := reconciler.NewRunReconciler(runStore, eventStore, runBackend, recCfg, log)
		runRec.SetWorkspaceCleaner(runBackend.WorkspaceStrategy())
		go runRec.Run(appCtx)
	}

	// HTTP server with graceful shutdown.
	srv := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in a goroutine.
	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// Wait for interrupt or error.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Info("received signal, shutting down", "signal", sig)
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	}

	// Stop reconciler.
	appCancel()

	// Graceful shutdown.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	log.Info("server stopped")
	return nil
}
