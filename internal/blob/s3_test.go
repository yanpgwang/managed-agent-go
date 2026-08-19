package blob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/yanpgwang/mango/internal/app"
)

func TestS3Store_PutOpenDeleteAndLimit(t *testing.T) {
	endpoint := os.Getenv("MANGO_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("MANGO_TEST_S3_ENDPOINT not set; skipping S3 conformance")
	}
	store, err := NewS3Store(context.Background(), S3Config{
		Endpoint: endpoint, Region: "us-east-1",
		Bucket:       os.Getenv("MANGO_TEST_S3_BUCKET"),
		AccessKey:    os.Getenv("MANGO_TEST_S3_ACCESS_KEY"),
		SecretKey:    os.Getenv("MANGO_TEST_S3_SECRET_KEY"),
		UsePathStyle: true, CreateBucket: true, UploadTempDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}
	key := "tests/" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = store.Delete(context.Background(), key) })

	info, err := store.Put(context.Background(), key, "text/plain", bytes.NewBufferString("hello"), 5)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if info != app.ComputeBlobInfo([]byte("hello")) {
		t.Fatalf("blob info = %+v", info)
	}
	body, err := store.Open(context.Background(), key)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	data, readErr := io.ReadAll(body)
	if closeErr := body.Close(); readErr == nil {
		readErr = closeErr
	}
	if readErr != nil || string(data) != "hello" {
		t.Fatalf("Open body = %q, %v", data, readErr)
	}
	if err := store.Delete(context.Background(), key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Open(context.Background(), key); err == nil {
		t.Fatal("deleted object remains readable")
	}

	_, err = store.Put(context.Background(), key+"-large", "application/octet-stream",
		bytes.NewBufferString("too large"), 3)
	if !errors.Is(err, app.ErrBlobTooLarge) {
		t.Fatalf("oversize error = %v", err)
	}
}
