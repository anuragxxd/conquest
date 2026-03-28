package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/anuragxxd/conquest/internal/filer"
	"github.com/anuragxxd/conquest/internal/objstore"
)

// runFilerStageIn implements the "filer stage-in" subcommand.
// This runs inside the init container of a task Job.
func runFilerStageIn(log *slog.Logger) error {
	// The manifest is passed via the STAGE_MANIFEST env var.
	manifestStr := os.Getenv("STAGE_MANIFEST")
	if manifestStr == "" {
		return fmt.Errorf("STAGE_MANIFEST env var is required")
	}

	var manifest filer.StageManifest
	if err := json.Unmarshal([]byte(manifestStr), &manifest); err != nil {
		return fmt.Errorf("parse STAGE_MANIFEST: %w", err)
	}

	// Build S3 client from env.
	endpoint := os.Getenv("CONQUEST_S3_ENDPOINT")
	bucket := os.Getenv("CONQUEST_S3_BUCKET")
	region := os.Getenv("CONQUEST_S3_REGION")
	accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")

	if endpoint == "" || accessKey == "" || secretKey == "" {
		return fmt.Errorf("S3 config incomplete: CONQUEST_S3_ENDPOINT, AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY required")
	}

	// Strip http:// or https:// from endpoint for minio client.
	useSSL := strings.HasPrefix(endpoint, "https://")
	cleanEndpoint := strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")

	client, err := objstore.NewS3Client(objstore.S3Config{
		Endpoint:  cleanEndpoint,
		AccessKey: accessKey,
		SecretKey: secretKey,
		Region:    region,
		UseSSL:    useSSL,
	})
	if err != nil {
		return fmt.Errorf("create S3 client: %w", err)
	}

	_ = bucket // bucket is embedded in the S3 URLs

	ctx := context.Background()
	return filer.StageIn(ctx, client, manifest, log)
}
