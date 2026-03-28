package objstore

import (
	"context"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Client implements Client using the MinIO Go SDK (S3-compatible).
type S3Client struct {
	mc *minio.Client
}

// S3Config holds the config for connecting to an S3-compatible store.
type S3Config struct {
	Endpoint  string // e.g. "minio.platform.svc.cluster.local:9000"
	AccessKey string
	SecretKey string
	Region    string
	UseSSL    bool
}

// NewS3Client creates a new S3-compatible object store client.
func NewS3Client(cfg S3Config) (*S3Client, error) {
	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, err
	}
	return &S3Client{mc: mc}, nil
}

func (c *S3Client) Get(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	obj, err := c.mc.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	return obj, nil
}

func (c *S3Client) Put(ctx context.Context, bucket, key string, r io.Reader, size int64) error {
	opts := minio.PutObjectOptions{}
	if size < 0 {
		size = -1 // minio interprets -1 as unknown size
	}
	_, err := c.mc.PutObject(ctx, bucket, key, r, size, opts)
	return err
}

func (c *S3Client) Delete(ctx context.Context, bucket, key string) error {
	return c.mc.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{})
}

func (c *S3Client) Exists(ctx context.Context, bucket, key string) (bool, error) {
	_, err := c.mc.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
	if err != nil {
		errResp := minio.ToErrorResponse(err)
		if errResp.Code == "NoSuchKey" {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (c *S3Client) ListPrefix(ctx context.Context, bucket, prefix string) ([]string, error) {
	var keys []string
	for obj := range c.mc.ListObjects(ctx, bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		keys = append(keys, obj.Key)
	}
	return keys, nil
}
