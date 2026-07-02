output "api_endpoint" {
  value = aws_apigatewayv2_api.api.api_endpoint
}

output "grain_bucket" {
  value = aws_s3_bucket.grains.bucket
}

output "sidecar_table" {
  value = aws_dynamodb_table.sidecar.name
}

output "rebuild_queue_url" {
  value = var.rebuild_events ? aws_sqs_queue.rebuild[0].url : null
}
