// Package objectstore is the S3/MinIO access layer for large immutable blobs: raw DMARC
// XML, compressed logs, forensic reports. Object keys are deterministic and tenant-
// prefixed for isolation and lifecycle policies (see docs/data-model.md).
package objectstore

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/zezekim/mxsentinel/internal/config"
)

// Store wraps a MinIO/S3 client bound to a single bucket.
type Store struct {
	client *minio.Client
	bucket string
}

// New constructs the client and verifies the endpoint is reachable.
func New(ctx context.Context, cfg config.ObjectStoreConfig) (*Store, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("create object-store client: %w", err)
	}
	s := &Store{client: client, bucket: cfg.Bucket}

	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := client.BucketExists(checkCtx, cfg.Bucket); err != nil {
		return nil, fmt.Errorf("reach object store: %w", err)
	}
	return s, nil
}

// EnsureBucket creates the configured bucket if it does not already exist.
func (s *Store) EnsureBucket(ctx context.Context, region string) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check bucket: %w", err)
	}
	if exists {
		return nil
	}
	if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: region}); err != nil {
		return fmt.Errorf("make bucket %q: %w", s.bucket, err)
	}
	return nil
}

// Put stores bytes at key with the given content type and returns the key.
func (s *Store) Put(ctx context.Context, key string, data []byte, contentType string) (string, error) {
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return "", fmt.Errorf("put object %q: %w", key, err)
	}
	return key, nil
}

// DMARCRawKey builds the deterministic, tenant-prefixed key for an archived DMARC
// aggregate report: dmarc-raw/<tenant>/<yyyy>/<mm>/<report-id>.xml.gz
func DMARCRawKey(tenantID string, when time.Time, reportID string) string {
	w := when.UTC()
	return path.Join("dmarc-raw", tenantID,
		fmt.Sprintf("%04d", w.Year()), fmt.Sprintf("%02d", int(w.Month())),
		reportID+".xml.gz")
}
