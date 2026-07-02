# The grain store: versioning on (grain history is the edit history's raw
# form), exports expire via lifecycle. Conditional writes (If-Match) are an
# S3 API feature -- nothing to enable.
resource "aws_s3_bucket" "grains" {
  bucket = var.grain_bucket_name
}

resource "aws_s3_bucket_versioning" "grains" {
  bucket = aws_s3_bucket.grains.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_public_access_block" "grains" {
  bucket                  = aws_s3_bucket.grains.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# Export outputs are download-linked (presigned) and short-lived.
resource "aws_s3_bucket_lifecycle_configuration" "grains" {
  bucket = aws_s3_bucket.grains.id
  rule {
    id     = "expire-exports"
    status = "Enabled"
    filter {
      prefix = "exports/"
    }
    expiration {
      days = 2
    }
  }
}
