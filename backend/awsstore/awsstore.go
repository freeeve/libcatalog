// Package awsstore constructs the AWS-backed store.Store and blob.Store
// implementations from resolved configuration. It is the one place the AWS
// credential/endpoint machinery lives, so the config package stays SDK-free and
// the entrypoints select a backend without touching the SDK directly. Nothing
// here runs on the local/demo path -- it is reached only when a DynamoDB table
// or S3 bucket is configured.
package awsstore

import (
	"context"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/blobs3"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/store/dynamo"
)

// Dynamo builds a DynamoDB-backed document store over table. Region and
// credentials come from the standard AWS environment; a non-empty endpoint
// overrides the service endpoint (DynamoDB Local in dev/tests).
func Dynamo(ctx context.Context, table, endpoint string) (store.Store, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("awsstore: load aws config: %w", err)
	}
	client := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		if endpoint != "" {
			o.BaseEndpoint = &endpoint
		}
	})
	return dynamo.New(client, table), nil
}

// S3 builds an S3-compatible blob store over bucket. A non-empty endpoint
// overrides the service endpoint and switches on path-style addressing (MinIO
// and other S3-compatibles in dev/tests).
func S3(ctx context.Context, bucket, endpoint string) (blob.Store, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("awsstore: load aws config: %w", err)
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = &endpoint
			o.UsePathStyle = true
		}
	})
	return blobs3.New(client, bucket), nil
}
