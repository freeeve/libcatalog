// Command sitegate-lambda is the stock static-site login gate as a
// Function-URL Lambda: auth/sitegate configured from a toml file bundled
// into the deploy zip next to the bootstrap binary, so an adopting
// deployment writes no Go -- it cross-compiles this command at a released
// version and ships a config file.
//
// The [sitegate] table of SITEGATE_CONFIG (default ./sitegate.toml) carries
// the non-secret sitegate.Config fields under kebab-case keys (client-id,
// site-domain, ...). The CloudFront signer key stays out of the file and
// arrives as SITEGATE_PRIVATE_KEY_PEM, raw PEM or base64-of-PEM.
package main

import (
	"context"
	"log"
	"os"

	"github.com/aws/aws-lambda-go/lambda"

	"github.com/freeeve/libcat/backend/auth/sitegate"
	"github.com/freeeve/libcat/backend/awslambda"
)

func main() {
	gate, err := buildGate()
	if err != nil {
		log.Fatalf("sitegate-lambda: %v", err)
	}
	lambda.Start(awslambda.Handler(gate))
}

// buildGate assembles the gate from SITEGATE_CONFIG (default
// ./sitegate.toml) and the SITEGATE_PRIVATE_KEY_PEM signer key.
func buildGate() (*sitegate.Gate, error) {
	path := os.Getenv("SITEGATE_CONFIG")
	if path == "" {
		path = "sitegate.toml"
	}
	cfg, err := loadConfig(path, os.Getenv("SITEGATE_PRIVATE_KEY_PEM"))
	if err != nil {
		return nil, err
	}
	return sitegate.New(context.Background(), cfg)
}
