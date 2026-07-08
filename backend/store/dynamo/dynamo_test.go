package dynamo_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/freeeve/libcat/backend/store/dynamo"
	"github.com/freeeve/libcat/backend/store/storetest"
)

// TestDynamoConformance runs the shared store conformance suite against a
// live DynamoDB endpoint (DynamoDB-local):
//
//	docker run -p 8000:8000 amazon/dynamodb-local
//	DYNAMO_ENDPOINT=http://localhost:8000 go test ./store/dynamo/
func TestDynamoConformance(t *testing.T) {
	endpoint := os.Getenv("DYNAMO_ENDPOINT")
	if endpoint == "" {
		t.Skip("DYNAMO_ENDPOINT not set; skipping DynamoDB conformance run")
	}
	client := dynamodb.New(dynamodb.Options{
		Region:       "local",
		BaseEndpoint: &endpoint,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "local", SecretAccessKey: "local"}, nil
		}),
	})
	table := fmt.Sprintf("lcat-conformance-%d", time.Now().UnixNano())
	ctx := t.Context()
	_, err := client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:   &table,
		BillingMode: types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("sk"), AttributeType: types.ScalarAttributeTypeS},
		},
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: types.KeyTypeRange},
		},
	})
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	t.Cleanup(func() {
		_, _ = client.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: &table})
	})
	if _, err := client.UpdateTimeToLive(ctx, &dynamodb.UpdateTimeToLiveInput{
		TableName: &table,
		TimeToLiveSpecification: &types.TimeToLiveSpecification{
			AttributeName: aws.String("expireAt"),
			Enabled:       aws.Bool(true),
		},
	}); err != nil {
		t.Logf("UpdateTimeToLive (non-fatal on local): %v", err)
	}
	// StrictTTL false: DynamoDB expires lazily.
	storetest.Run(t, dynamo.New(client, table), storetest.Options{StrictTTL: false})
}
