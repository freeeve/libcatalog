# libcatalog Tier 2 backend -- AWS reference deployment.
# Self-contained: point terraform at your account, supply the lambda zip
# (build with: cd backend && GOOS=linux GOARCH=arm64 go build -o bootstrap
# ./cmd/lcatd-lambda && zip lcatd-lambda.zip bootstrap).

terraform {
  required_version = ">= 1.5"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.60"
    }
  }
}

provider "aws" {
  region = var.region
}
