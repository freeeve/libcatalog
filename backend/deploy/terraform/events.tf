# Rebuild plumbing: publishes emit grains-changed onto the bus; a rule fans
# them into an SQS queue a rebuild worker (CI trigger, container, or a second
# lambda) consumes. Optional -- webhook or scheduled rebuilds skip all this.
resource "aws_cloudwatch_event_bus" "rebuild" {
  count = var.rebuild_events ? 1 : 0
  name  = "${var.name}-rebuild"
}

resource "aws_sqs_queue" "rebuild" {
  count                      = var.rebuild_events ? 1 : 0
  name                       = "${var.name}-rebuild"
  message_retention_seconds  = 86400
  visibility_timeout_seconds = 900
}

resource "aws_cloudwatch_event_rule" "grains_changed" {
  count          = var.rebuild_events ? 1 : 0
  name           = "${var.name}-grains-changed"
  event_bus_name = aws_cloudwatch_event_bus.rebuild[0].name
  event_pattern = jsonencode({
    detail-type = ["grains-changed"]
  })
}

resource "aws_cloudwatch_event_target" "to_sqs" {
  count          = var.rebuild_events ? 1 : 0
  rule           = aws_cloudwatch_event_rule.grains_changed[0].name
  event_bus_name = aws_cloudwatch_event_bus.rebuild[0].name
  arn            = aws_sqs_queue.rebuild[0].arn
}

resource "aws_sqs_queue_policy" "rebuild" {
  count     = var.rebuild_events ? 1 : 0
  queue_url = aws_sqs_queue.rebuild[0].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "events.amazonaws.com" }
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.rebuild[0].arn
      Condition = { ArnEquals = { "aws:SourceArn" = aws_cloudwatch_event_rule.grains_changed[0].arn } }
    }]
  })
}
