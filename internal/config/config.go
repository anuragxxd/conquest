package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/anuragxxd/conquest/internal/storage"
)

type Config struct {
	HTTPAddr                      string
	PostgresDSN                   string
	S3Bucket                      string
	S3Endpoint                    string
	S3Region                      string
	K8sNamespace                  string
	LayoutVersion                 string
	PageSizeMax                   int
	ServiceAccount                string
	ConquestImage                 string
	RunnerImage                   string
	InternalBaseURL               string
	NextflowRunnerImages          map[string]string
	SnakemakeRunnerImage          string
	SnakemakeTaskImage            string
	SnakemakeDefaultJobs          int
	SnakemakeDefaultDiskMB        int
	RunnerEphemeralStorageRequest string
	RunnerEphemeralStorageLimit   string
	StorageSecretName             string
	ImagePullSecrets              []string
	ReconcilerInterval            time.Duration

	// Task-level resource policy (applies to both Nextflow and Snakemake).
	TaskDefaultCPUCores int
	TaskDefaultRAMGb    float64
	TaskDefaultDiskGb   float64
	TaskMaxCPUCores     int
	TaskMaxRAMGb        float64
	TaskMaxDiskGb       float64
	TaskMinCPUCores     int
	TaskMinRAMGb        float64
	TaskMinDiskGb       float64
}

func Load() (*Config, error) {
	nextflowRunnerImages := map[string]string{}
	if raw := strings.TrimSpace(os.Getenv("CONQUEST_NEXTFLOW_RUNNER_IMAGES")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &nextflowRunnerImages); err != nil {
			return nil, fmt.Errorf("parse CONQUEST_NEXTFLOW_RUNNER_IMAGES: %w", err)
		}
	}

	c := &Config{
		HTTPAddr:                      envOr("CONQUEST_HTTP_ADDR", ":8080"),
		PostgresDSN:                   envOr("CONQUEST_POSTGRES_DSN", "postgres://conquest:conquest@localhost:5432/conquest?sslmode=disable"),
		S3Bucket:                      envOr("CONQUEST_S3_BUCKET", "conquest-workflows"),
		S3Endpoint:                    envOr("CONQUEST_S3_ENDPOINT", ""),
		S3Region:                      envOr("CONQUEST_S3_REGION", "us-east-1"),
		K8sNamespace:                  envOr("CONQUEST_K8S_NAMESPACE", "conquest"),
		LayoutVersion:                 envOr("CONQUEST_LAYOUT_VERSION", storage.LayoutVersion),
		PageSizeMax:                   envIntOr("CONQUEST_PAGE_SIZE_MAX", 256),
		ServiceAccount:                envOr("CONQUEST_SERVICE_ACCOUNT", "conquest"),
		ConquestImage:                 envOr("CONQUEST_IMAGE", "harbor.pacificanalytics.com/pacificanalytics/conquest:latest"),
		RunnerImage:                   envOr("CONQUEST_RUNNER_IMAGE", "harbor.pacificanalytics.com/pacificanalytics/conquest-runner:latest"),
		InternalBaseURL:               envOr("CONQUEST_INTERNAL_BASE_URL", "http://conquest.services.svc"),
		NextflowRunnerImages:          nextflowRunnerImages,
		SnakemakeRunnerImage:          strings.TrimSpace(os.Getenv("CONQUEST_SNAKEMAKE_RUNNER_IMAGE")),
		SnakemakeTaskImage:            strings.TrimSpace(os.Getenv("CONQUEST_SNAKEMAKE_TASK_IMAGE")),
		SnakemakeDefaultJobs:          envIntOr("CONQUEST_SNAKEMAKE_DEFAULT_JOBS", 32),
		SnakemakeDefaultDiskMB:        envIntOr("CONQUEST_SNAKEMAKE_DEFAULT_DISK_MB", 8192),
		RunnerEphemeralStorageRequest: envOr("CONQUEST_RUNNER_EPHEMERAL_STORAGE_REQUEST", "20Gi"),
		RunnerEphemeralStorageLimit:   envOr("CONQUEST_RUNNER_EPHEMERAL_STORAGE_LIMIT", "40Gi"),
		StorageSecretName:             envOr("CONQUEST_STORAGE_SECRET_NAME", "storage-secrets"),
		ImagePullSecrets:              envCSV("CONQUEST_IMAGE_PULL_SECRETS"),
		ReconcilerInterval:            envDurationOr("CONQUEST_RECONCILER_INTERVAL", 5*time.Second),

		TaskDefaultCPUCores: envIntOr("CONQUEST_TASK_DEFAULT_CPU_CORES", 1),
		TaskDefaultRAMGb:    envFloat64Or("CONQUEST_TASK_DEFAULT_RAM_GB", 2),
		TaskDefaultDiskGb:   envFloat64Or("CONQUEST_TASK_DEFAULT_DISK_GB", 8),
		TaskMaxCPUCores:     envIntOr("CONQUEST_TASK_MAX_CPU_CORES", 0),
		TaskMaxRAMGb:        envFloat64Or("CONQUEST_TASK_MAX_RAM_GB", 0),
		TaskMaxDiskGb:       envFloat64Or("CONQUEST_TASK_MAX_DISK_GB", 0),
		TaskMinCPUCores:     envIntOr("CONQUEST_TASK_MIN_CPU_CORES", 0),
		TaskMinRAMGb:        envFloat64Or("CONQUEST_TASK_MIN_RAM_GB", 0),
		TaskMinDiskGb:       envFloat64Or("CONQUEST_TASK_MIN_DISK_GB", 0),
	}

	if c.PostgresDSN == "" {
		return nil, fmt.Errorf("CONQUEST_POSTGRES_DSN is required")
	}
	if c.S3Bucket == "" {
		return nil, fmt.Errorf("CONQUEST_S3_BUCKET is required")
	}
	if c.LayoutVersion == "" {
		return nil, fmt.Errorf("CONQUEST_LAYOUT_VERSION is required")
	}
	if c.SnakemakeRunnerImage == "" && c.SnakemakeTaskImage != "" {
		return nil, fmt.Errorf("CONQUEST_SNAKEMAKE_RUNNER_IMAGE is required when CONQUEST_SNAKEMAKE_TASK_IMAGE is set")
	}
	if c.SnakemakeRunnerImage != "" && c.SnakemakeTaskImage == "" {
		c.SnakemakeTaskImage = c.SnakemakeRunnerImage
	}
	if c.SnakemakeDefaultJobs <= 0 {
		return nil, fmt.Errorf("CONQUEST_SNAKEMAKE_DEFAULT_JOBS must be greater than zero")
	}
	if c.SnakemakeDefaultDiskMB <= 0 {
		return nil, fmt.Errorf("CONQUEST_SNAKEMAKE_DEFAULT_DISK_MB must be greater than zero")
	}
	if c.RunnerEphemeralStorageRequest == "" {
		return nil, fmt.Errorf("CONQUEST_RUNNER_EPHEMERAL_STORAGE_REQUEST is required")
	}
	if c.RunnerEphemeralStorageLimit == "" {
		return nil, fmt.Errorf("CONQUEST_RUNNER_EPHEMERAL_STORAGE_LIMIT is required")
	}

	return c, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func envFloat64Or(key string, fallback float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return f
}

func envDurationOr(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

func envCSV(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}
