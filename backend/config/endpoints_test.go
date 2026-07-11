package config

import "testing"

// The off-AWS deployment is two unrelated servers: MinIO for blobs, DynamoDB
// Local (or ScyllaDB Alternator) for documents. A single endpoint override
// cannot address it, which is the whole reason the per-service overrides exist
// .
func TestPerServiceEndpointsAddressDifferentHosts(t *testing.T) {
	cfg := Config{
		S3Endpoint:     "http://minio:9000",
		DynamoEndpoint: "http://dynamodb:8000",
	}
	if got := cfg.ResolvedS3Endpoint(); got != "http://minio:9000" {
		t.Errorf("S3 endpoint = %q", got)
	}
	if got := cfg.ResolvedDynamoEndpoint(); got != "http://dynamodb:8000" {
		t.Errorf("dynamo endpoint = %q", got)
	}
}

// One compatible endpoint for everything (LocalStack) keeps working unchanged.
func TestAWSEndpointStillFeedsBothStores(t *testing.T) {
	cfg := Config{AWSEndpoint: "http://localstack:4566"}
	if got := cfg.ResolvedS3Endpoint(); got != "http://localstack:4566" {
		t.Errorf("S3 endpoint = %q", got)
	}
	if got := cfg.ResolvedDynamoEndpoint(); got != "http://localstack:4566" {
		t.Errorf("dynamo endpoint = %q", got)
	}
}

// A service-specific override wins over the shared one, per service and
// independently: overriding S3 must not drag DynamoDB along with it.
func TestServiceEndpointOverridesTheSharedOne(t *testing.T) {
	cfg := Config{AWSEndpoint: "http://shared:4566", S3Endpoint: "http://minio:9000"}
	if got := cfg.ResolvedS3Endpoint(); got != "http://minio:9000" {
		t.Errorf("S3 endpoint = %q, want the override", got)
	}
	if got := cfg.ResolvedDynamoEndpoint(); got != "http://shared:4566" {
		t.Errorf("dynamo endpoint = %q, want the shared endpoint", got)
	}
}

// Nothing set means real AWS, resolved from the region.
func TestNoEndpointMeansRealAWS(t *testing.T) {
	var cfg Config
	if got := cfg.ResolvedS3Endpoint(); got != "" {
		t.Errorf("S3 endpoint = %q, want empty", got)
	}
	if got := cfg.ResolvedDynamoEndpoint(); got != "" {
		t.Errorf("dynamo endpoint = %q, want empty", got)
	}
}

func TestEndpointsFromEnv(t *testing.T) {
	t.Setenv("LCATD_ABUSE_SECRET", "test-0123456789abcdef01234567")
	t.Setenv("LCATD_AWS_ENDPOINT", "http://shared:4566")
	t.Setenv("LCATD_S3_ENDPOINT", "http://minio:9000")
	t.Setenv("LCATD_DYNAMO_ENDPOINT", "http://dynamodb:8000")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ResolvedS3Endpoint() != "http://minio:9000" || cfg.ResolvedDynamoEndpoint() != "http://dynamodb:8000" {
		t.Fatalf("env endpoints not read: %+v", cfg)
	}
}
