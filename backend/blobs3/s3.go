// Package blobs3 implements blob.Store on S3-compatible object storage --
// AWS S3, Cloudflare R2, MinIO -- using conditional writes (If-Match /
// If-None-Match on PutObject) for the optimistic concurrency the grain-store
// contract requires, and presigned GETs for the Signer capability. The caller
// constructs and owns the *s3.Client, so credentials, region, and custom
// endpoints (path-style for MinIO) stay a deployment concern.
//
// GCS's XML interop layer does not honor If-Match on writes; a native GCS
// store (generation preconditions) can implement blob.Store separately, and
// the publisher's advisory lease is the fallback correctness mode.
package blobs3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/freeeve/libcat/storage/blob"
)

// Store implements blob.Store and blob.Signer over one bucket.
type Store struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
}

// New returns a Store over the bucket.
func New(client *s3.Client, bucket string) *Store {
	return &Store{client: client, presign: s3.NewPresignClient(client), bucket: bucket}
}

// quote adds the quotes S3 expects around ETags in precondition headers.
func quote(etag string) string {
	if strings.HasPrefix(etag, `"`) {
		return etag
	}
	return `"` + etag + `"`
}

// unquote strips ETag quotes so callers see the same opaque token shape as
// other stores.
func unquote(etag string) string {
	return strings.Trim(etag, `"`)
}

// Get returns the object's content and ETag, or blob.ErrNotFound.
func (s *Store) Get(ctx context.Context, path string) ([]byte, string, error) {
	if err := blob.ValidatePath(path); err != nil {
		return nil, "", err
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &path,
	})
	if err != nil {
		if isNoSuchKey(err) {
			return nil, "", blob.ErrNotFound
		}
		return nil, "", err
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, "", err
	}
	return data, unquote(aws.ToString(out.ETag)), nil
}

// GetRange implements the blob.RangeReader capability with an HTTP Range
// GET. S3 clamps a range that runs past the object's end and returns 416
// for one that starts past it; both normalize to short/empty reads so the
// blob.ReaderAt adapter sees io.ReaderAt semantics.
func (s *Store) GetRange(ctx context.Context, path string, offset, length int64) ([]byte, error) {
	if err := blob.ValidatePath(path); err != nil {
		return nil, err
	}
	if offset < 0 || length <= 0 {
		return nil, nil
	}
	rng := fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &path,
		Range:  &rng,
	})
	if err != nil {
		if isNoSuchKey(err) {
			return nil, blob.ErrNotFound
		}
		var ae smithy.APIError
		if errors.As(err, &ae) && ae.ErrorCode() == "InvalidRange" {
			return nil, nil
		}
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

// Put writes the object subject to opts' preconditions.
func (s *Store) Put(ctx context.Context, path string, data []byte, opts blob.PutOptions) (string, error) {
	if err := blob.ValidatePath(path); err != nil {
		return "", err
	}
	if opts.IfMatch != "" && opts.IfNoneMatch {
		return "", errors.New("blobs3: IfMatch and IfNoneMatch are mutually exclusive")
	}
	in := &s3.PutObjectInput{
		Bucket: &s.bucket,
		Key:    &path,
		Body:   bytes.NewReader(data),
	}
	if opts.ContentType != "" {
		in.ContentType = &opts.ContentType
	}
	if opts.ContentEncoding != "" {
		in.ContentEncoding = &opts.ContentEncoding
	}
	if opts.IfMatch != "" {
		in.IfMatch = aws.String(quote(opts.IfMatch))
	}
	if opts.IfNoneMatch {
		in.IfNoneMatch = aws.String("*")
	}
	out, err := s.client.PutObject(ctx, in)
	if err != nil {
		if isPreconditionFailure(err) {
			return "", blob.ErrPreconditionFailed
		}
		// If-Match against a missing key surfaces as NoSuchKey on S3;
		// the contract calls that a failed precondition too.
		if opts.IfMatch != "" && isNoSuchKey(err) {
			return "", blob.ErrPreconditionFailed
		}
		return "", err
	}
	return unquote(aws.ToString(out.ETag)), nil
}

// PutStream implements blob.StreamPutter by spooling the payload to a local
// temp file for a seekable, length-known upload body -- RAM stays at the
// copy buffer while the object rides through disk (tasks/108). Preconditions
// carry through to the PutObject exactly as in Put.
func (s *Store) PutStream(ctx context.Context, path string, r io.Reader, opts blob.PutOptions) (string, error) {
	if err := blob.ValidatePath(path); err != nil {
		return "", err
	}
	if opts.IfMatch != "" && opts.IfNoneMatch {
		return "", errors.New("blobs3: IfMatch and IfNoneMatch are mutually exclusive")
	}
	tmp, err := os.CreateTemp("", "blobs3-put-*")
	if err != nil {
		return "", err
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()
	if _, err := io.Copy(tmp, r); err != nil {
		return "", err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	in := &s3.PutObjectInput{
		Bucket: &s.bucket,
		Key:    &path,
		Body:   tmp,
	}
	if opts.ContentType != "" {
		in.ContentType = &opts.ContentType
	}
	if opts.ContentEncoding != "" {
		in.ContentEncoding = &opts.ContentEncoding
	}
	if opts.IfMatch != "" {
		in.IfMatch = aws.String(quote(opts.IfMatch))
	}
	if opts.IfNoneMatch {
		in.IfNoneMatch = aws.String("*")
	}
	out, err := s.client.PutObject(ctx, in)
	if err != nil {
		if isPreconditionFailure(err) {
			return "", blob.ErrPreconditionFailed
		}
		if opts.IfMatch != "" && isNoSuchKey(err) {
			return "", blob.ErrPreconditionFailed
		}
		return "", err
	}
	return unquote(aws.ToString(out.ETag)), nil
}

// List yields entries under prefix in lexicographic path order (S3's native
// ListObjectsV2 order), paginating internally.
func (s *Store) List(ctx context.Context, prefix string) iter.Seq2[blob.Entry, error] {
	return func(yield func(blob.Entry, error) bool) {
		paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
			Bucket: &s.bucket,
			Prefix: &prefix,
		})
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(ctx)
			if err != nil {
				yield(blob.Entry{}, err)
				return
			}
			for _, obj := range page.Contents {
				entry := blob.Entry{
					Path: aws.ToString(obj.Key),
					ETag: unquote(aws.ToString(obj.ETag)),
					Size: aws.ToInt64(obj.Size),
				}
				if !yield(entry, nil) {
					return
				}
			}
		}
	}
}

// Delete removes the object, or returns blob.ErrNotFound. S3 deletes are
// idempotent-silent, so existence is checked first (racy but conformant --
// the contract's ErrNotFound is a debugging courtesy, not a lock).
func (s *Store) Delete(ctx context.Context, path string) error {
	if err := blob.ValidatePath(path); err != nil {
		return err
	}
	if _, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &path}); err != nil {
		if isNoSuchKey(err) {
			return blob.ErrNotFound
		}
		return err
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &path})
	return err
}

// SignedGetURL implements blob.Signer with a presigned GET.
func (s *Store) SignedGetURL(ctx context.Context, path string, ttl time.Duration) (string, error) {
	if err := blob.ValidatePath(path); err != nil {
		return "", err
	}
	req, err := s.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &path,
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", err
	}
	return req.URL, nil
}

func isNoSuchKey(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return true
		}
	}
	return false
}

func isPreconditionFailure(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "PreconditionFailed", "ConditionalRequestConflict":
			return true
		}
	}
	return false
}
