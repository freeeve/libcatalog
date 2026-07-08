package blobs3_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/freeeve/libcat/backend/blobs3"
	"github.com/freeeve/libcat/storage/blob"
)

// TestS3Conformance runs against an S3-compatible endpoint (MinIO):
//
//	docker run -p 9000:9000 minio/minio server /data
//	S3_TEST_ENDPOINT=http://localhost:9000 go test ./blobs3/
func TestS3Conformance(t *testing.T) {
	endpoint := os.Getenv("S3_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("S3_TEST_ENDPOINT not set; skipping S3 conformance run")
	}
	client := s3.New(s3.Options{
		Region:       "us-east-1",
		BaseEndpoint: &endpoint,
		UsePathStyle: true,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{
				AccessKeyID:     envOr("S3_TEST_ACCESS_KEY", "minioadmin"),
				SecretAccessKey: envOr("S3_TEST_SECRET_KEY", "minioadmin"),
			}, nil
		}),
	})
	bucket := fmt.Sprintf("lcat-conformance-%d", time.Now().UnixNano())
	if _, err := client.CreateBucket(t.Context(), &s3.CreateBucketInput{Bucket: &bucket}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	st := blobs3.New(client, bucket)

	// The blob package's conformance behaviors, inlined against the live
	// endpoint (the shared suite lives in storage/blob's internal tests).
	ctx := t.Context()
	if _, _, err := st.Get(ctx, "missing"); err != blob.ErrNotFound {
		t.Fatalf("Get missing: %v", err)
	}
	tag1, err := st.Put(ctx, "data/works/aa/w1.nq", []byte("one"), blob.PutOptions{IfNoneMatch: true, ContentType: "application/n-quads"})
	if err != nil {
		t.Fatalf("create-only Put: %v", err)
	}
	if _, err := st.Put(ctx, "data/works/aa/w1.nq", []byte("clobber"), blob.PutOptions{IfNoneMatch: true}); err != blob.ErrPreconditionFailed {
		t.Fatalf("create-only on existing: %v", err)
	}
	tag2, err := st.Put(ctx, "data/works/aa/w1.nq", []byte("two"), blob.PutOptions{IfMatch: tag1})
	if err != nil {
		t.Fatalf("IfMatch current: %v", err)
	}
	if _, err := st.Put(ctx, "data/works/aa/w1.nq", []byte("three"), blob.PutOptions{IfMatch: tag1}); err != blob.ErrPreconditionFailed {
		t.Fatalf("IfMatch stale: %v", err)
	}
	data, tag, err := st.Get(ctx, "data/works/aa/w1.nq")
	if err != nil || string(data) != "two" || tag != tag2 {
		t.Fatalf("Get = %q %q %v", data, tag, err)
	}
	if _, err := st.Put(ctx, "data/works/ab/w2.nq", []byte("second"), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for e, err := range st.List(ctx, "data/works/") {
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		paths = append(paths, e.Path)
	}
	if len(paths) != 2 || paths[0] != "data/works/aa/w1.nq" || paths[1] != "data/works/ab/w2.nq" {
		t.Fatalf("List = %v", paths)
	}
	// Signer: presigned URL fetches without credentials.
	url, err := st.SignedGetURL(ctx, "data/works/aa/w1.nq", time.Minute)
	if err != nil {
		t.Fatalf("SignedGetURL: %v", err)
	}
	resp, err := http.Get(url)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("presigned fetch: %v (%v)", resp, err)
	}
	resp.Body.Close()
	if err := st.Delete(ctx, "data/works/aa/w1.nq"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := st.Delete(ctx, "data/works/aa/w1.nq"); err != blob.ErrNotFound {
		t.Fatalf("Delete missing: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
