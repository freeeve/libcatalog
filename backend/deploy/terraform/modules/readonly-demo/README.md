# Terraform module: read-only lcatd demo (Lambda + Function URL + CloudFront)

A reusable module for a **public read-only** lcatd demo (`LCATD_READ_ONLY=1`):
an arm64 Lambda (the API + embedded SPA) behind a Function URL, fronted by
CloudFront. No DynamoDB or S3 -- the in-memory document store plus grains
bundled in the zip. Each cold start re-creates the bootstrap admin.

CloudFront caches the SPA's hashed `/assets/*` at the edge and passes
`/config` + `/v1/*` through to the function, so a page renders from the edge and
the cold start is only felt on the first API call.

## Build the zip

`build-zip.sh` embeds the SPA, cross-compiles the Lambda `bootstrap`, and bundles
your projected grains:

```sh
./build-zip.sh /path/to/grains ./lcatd-demo.zip
```

## Wire it up (what a consumer, e.g. the demo site, writes)

```hcl
provider "aws" {
  region = "us-east-1" # CloudFront + any alias ACM cert must be us-east-1
}

module "demo" {
  source     = "github.com/freeeve/libcat//backend/deploy/terraform/modules/readonly-demo?ref=v0.4.2"
  name       = "eves-library"
  lambda_zip = "./lcatd-demo.zip"

  environment = {
    LCATD_READ_ONLY         = "1"
    LCATD_BLOB_DIR          = "/var/task/grains"
    LCATD_LOCAL_AUTH        = "1"
    LCATD_BOOTSTRAP_ADMIN   = "demo@example.org:demopass1"
    LCATD_LOCAL_SIGNING_KEY = var.signing_key # stable, so a warm session survives
    LCATD_ABUSE_SECRET      = var.abuse_secret
    LCATD_PROVIDER          = "marc"
    LCATD_VOCAB_SCHEMES     = "homosaurus" # trim the vocab load -> faster cold start
  }

  # Optional custom domain (ACM cert in us-east-1 covering the alias):
  # aliases             = ["try.example.org"]
  # acm_certificate_arn = aws_acm_certificate.demo.arn
}

output "url" { value = module.demo.cloudfront_domain }
```

Then `terraform apply`, and point a CNAME/alias at `cloudfront_domain` (or use it
directly). On redeploy, update the zip and (because HTML is served fresh) it
shows immediately; `/assets/*` change hash so they never go stale.

## Notes / knobs

- **Cold starts:** trimming `LCATD_VOCAB_SCHEMES` to what the demo needs is the
  biggest lever -- vocab loading dominates the boot. Watch `Init Duration` in the
  Lambda's CloudWatch logs. `memory_size` (default 1024) also trades $ for a
  faster init.
- **Sessions across instances:** concurrent cold instances have separate
  in-memory stores, so token *refresh* can miss; use a generous access-token TTL.
- **Hardening:** the Function URL is public (`authorization_type = NONE`), which
  is fine for a read-only demo. Locking the origin to CloudFront (OAC + IAM auth)
  is a later option.
- **Writable production** is a different shape (DynamoDB/S3 + a worker model);
  see the parent `deploy/terraform/` stack and libcat `tasks/099`.
