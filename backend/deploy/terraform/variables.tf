variable "region" {
  description = "AWS region"
  type        = string
  default     = "us-east-1"
}

variable "name" {
  description = "Resource-name prefix (one per deployment)"
  type        = string
  default     = "lcat"
}

variable "lambda_zip" {
  description = "Path to the lcatd-lambda deployment zip (bootstrap binary)"
  type        = string
}

variable "grain_bucket_name" {
  description = "Globally-unique S3 bucket for BIBFRAME grains and exports"
  type        = string
}

variable "allowed_origins" {
  description = "CORS origins for the API (catalog site, cataloging SPA)"
  type        = list(string)
  default     = []
}

variable "environment" {
  description = "Extra LCATD_* environment for the API lambda (issuers, role maps, provider). Secrets belong in SSM (ssm.tf), not here."
  type        = map(string)
  default     = {}
}

variable "rebuild_events" {
  description = "Create the EventBridge bus + SQS queue for grains-changed rebuild events"
  type        = bool
  default     = true
}
