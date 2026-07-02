// Command lcatd-lambda serves the libcatalog dynamic backend under AWS Lambda
// behind an API Gateway v2 HTTP API (provided.al2023, handler "bootstrap").
package main

import (
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"

	"github.com/freeeve/libcatalog/backend/awslambda"
	"github.com/freeeve/libcatalog/backend/httpapi"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	handler := httpapi.New(httpapi.Deps{Logger: logger})
	lambda.Start(awslambda.Handler(handler))
}
