package sitegate_test

import (
	"context"
	"log"
	"os"

	"github.com/aws/aws-lambda-go/lambda"

	"github.com/freeeve/libcat/backend/auth/sitegate"
	"github.com/freeeve/libcat/backend/awslambda"
)

// Example is the whole of an adopter's Function-URL Lambda main: load
// config, wrap the gate with the API-Gateway-v2 adapter (Function URLs share
// its payload shape), start. Compiled but not executed (no Output comment).
func Example() {
	gate, err := sitegate.New(context.Background(), sitegate.Config{
		Issuer:        os.Getenv("GATE_ISSUER"),
		ClientID:      os.Getenv("GATE_CLIENT_ID"),
		SiteDomain:    os.Getenv("GATE_SITE_DOMAIN"),
		KeyPairID:     os.Getenv("GATE_KEY_PAIR_ID"),
		PrivateKeyPEM: os.Getenv("GATE_CF_PRIVATE_KEY_PEM"),
	})
	if err != nil {
		log.Fatal(err)
	}
	lambda.Start(awslambda.Handler(gate))
}
