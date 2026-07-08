// Package awstrigger delivers trigger events over AWS messaging: an SQS
// queue (a worker consumes and rebuilds) or an EventBridge bus (fan-out to
// CodeBuild/Step Functions). Callers construct and own the clients.
package awstrigger

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/freeeve/libcat/backend/trigger"
)

// NewSQS builds an SQS notifier from the standard AWS environment; a
// non-empty endpoint overrides the service endpoint (LocalStack in dev).
func NewSQS(ctx context.Context, queueURL, endpoint string) (SQS, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return SQS{}, fmt.Errorf("awstrigger: load aws config: %w", err)
	}
	client := sqs.NewFromConfig(cfg, func(o *sqs.Options) {
		if endpoint != "" {
			o.BaseEndpoint = &endpoint
		}
	})
	return SQS{Client: client, QueueURL: queueURL}, nil
}

// NewEventBridge builds an EventBridge notifier from the standard AWS
// environment; a non-empty endpoint overrides the service endpoint.
func NewEventBridge(ctx context.Context, bus, source, endpoint string) (EventBridge, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return EventBridge{}, fmt.Errorf("awstrigger: load aws config: %w", err)
	}
	client := eventbridge.NewFromConfig(cfg, func(o *eventbridge.Options) {
		if endpoint != "" {
			o.BaseEndpoint = &endpoint
		}
	})
	return EventBridge{Client: client, Bus: bus, Source: source}, nil
}

// SQS sends each event as one message.
type SQS struct {
	Client   *sqs.Client
	QueueURL string
}

// Notify implements trigger.Notifier.
func (s SQS) Notify(ctx context.Context, e trigger.Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = s.Client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    &s.QueueURL,
		MessageBody: aws.String(string(body)),
	})
	return err
}

// EventBridge puts each event on a bus.
type EventBridge struct {
	Client *eventbridge.Client
	Bus    string
	Source string // e.g. "lcatd"
}

// Notify implements trigger.Notifier.
func (e EventBridge) Notify(ctx context.Context, ev trigger.Event) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	source := e.Source
	if source == "" {
		source = "lcatd"
	}
	_, err = e.Client.PutEvents(ctx, &eventbridge.PutEventsInput{
		Entries: []ebtypes.PutEventsRequestEntry{{
			EventBusName: aws.String(e.Bus),
			Source:       aws.String(source),
			DetailType:   aws.String(ev.Kind),
			Detail:       aws.String(string(body)),
		}},
	})
	return err
}
