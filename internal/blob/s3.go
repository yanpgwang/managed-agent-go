// Package blob implements durable byte storage for public File resources.
package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/yanpgwang/managed-agent-go/internal/app"
)

type S3Config struct {
	Endpoint      string
	Region        string
	Bucket        string
	AccessKey     string
	SecretKey     string
	UsePathStyle  bool
	UploadTempDir string
	CreateBucket  bool
}

type S3Store struct {
	client        *s3.Client
	bucket        string
	uploadTempDir string
}

func NewS3Store(ctx context.Context, cfg S3Config) (*S3Store, error) {
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("blob: S3 bucket is required")
	}
	loadOptions := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}
	if cfg.AccessKey != "" || cfg.SecretKey != "" {
		if cfg.AccessKey == "" || cfg.SecretKey == "" {
			return nil, fmt.Errorf("blob: S3 access key and secret key must be configured together")
		}
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return nil, fmt.Errorf("blob: load AWS configuration: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		if cfg.Endpoint != "" {
			options.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		options.UsePathStyle = cfg.UsePathStyle
	})
	store := &S3Store{client: client, bucket: cfg.Bucket, uploadTempDir: cfg.UploadTempDir}
	if _, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(cfg.Bucket)}); err != nil {
		if !cfg.CreateBucket {
			return nil, fmt.Errorf("blob: access bucket %q: %w", cfg.Bucket, err)
		}
		createInput := &s3.CreateBucketInput{Bucket: aws.String(cfg.Bucket)}
		if cfg.Region != "us-east-1" {
			createInput.CreateBucketConfiguration = &types.CreateBucketConfiguration{
				LocationConstraint: types.BucketLocationConstraint(cfg.Region),
			}
		}
		if _, createErr := client.CreateBucket(ctx, createInput); createErr != nil {
			// Another local API process may have created the bucket after our
			// HeadBucket call. Accept that race only after access is confirmed.
			if _, headErr := client.HeadBucket(ctx, &s3.HeadBucketInput{
				Bucket: aws.String(cfg.Bucket),
			}); headErr != nil {
				return nil, fmt.Errorf("blob: create bucket %q: %w", cfg.Bucket, createErr)
			}
		}
	}
	return store, nil
}

func (s *S3Store) Put(
	ctx context.Context,
	key string,
	contentType string,
	body io.Reader,
	maxBytes int64,
) (app.BlobInfo, error) {
	temp, err := os.CreateTemp(s.uploadTempDir, "mango-file-upload-*")
	if err != nil {
		return app.BlobInfo{}, fmt.Errorf("blob: create upload spool: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName) //nolint:errcheck // best-effort cleanup after close
	defer temp.Close()        //nolint:errcheck // PutObject result is authoritative

	hash := sha256.New()
	limited := &io.LimitedReader{R: body, N: maxBytes + 1}
	size, err := io.Copy(io.MultiWriter(temp, hash), limited)
	if err != nil {
		return app.BlobInfo{}, fmt.Errorf("blob: spool upload: %w", err)
	}
	if size > maxBytes {
		return app.BlobInfo{}, app.ErrBlobTooLarge
	}
	if _, err := temp.Seek(0, io.SeekStart); err != nil {
		return app.BlobInfo{}, fmt.Errorf("blob: rewind upload spool: %w", err)
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          temp,
		ContentLength: aws.Int64(size),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		return app.BlobInfo{}, fmt.Errorf("blob: put object: %w", err)
	}
	return app.BlobInfo{
		SizeBytes: size, ChecksumSHA256: hex.EncodeToString(hash.Sum(nil)),
	}, nil
}

func (s *S3Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("blob: get object: %w", err)
	}
	return result.Body, nil
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("blob: delete object: %w", err)
	}
	return nil
}
