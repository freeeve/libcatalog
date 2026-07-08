// Package dynamo implements store.Store on a single DynamoDB table
// (hash key "pk", range key "sk", TTL attribute "expireAt"). The caller
// constructs and owns the *dynamodb.Client, so credentials, region, and
// endpoint (production or DynamoDB-local) stay a deployment concern.
//
// TTL caveat: DynamoDB deletes expired items lazily, so expired records can
// remain visible for a time. Services already tolerate this (per the
// store.Record contract); windowed keys (RATE#.../HOUR#13) carry the exact
// semantics.
package dynamo

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/freeeve/libcat/backend/store"
)

const (
	attrPK       = "pk"
	attrSK       = "sk"
	attrData     = "data"
	attrVersion  = "version"
	attrExpireAt = "expireAt"
	attrCount    = "cnt"
)

// Store implements store.Store on one DynamoDB table.
type Store struct {
	client *dynamodb.Client
	table  string
}

// New returns a Store over the given table.
func New(client *dynamodb.Client, table string) *Store {
	return &Store{client: client, table: table}
}

func keyAttrs(k store.Key) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		attrPK: &types.AttributeValueMemberS{Value: k.PK},
		attrSK: &types.AttributeValueMemberS{Value: k.SK},
	}
}

// Get returns the record at k, or store.ErrNotFound.
func (s *Store) Get(ctx context.Context, k store.Key) (store.Record, error) {
	if err := validateKey(k); err != nil {
		return store.Record{}, err
	}
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      &s.table,
		Key:            keyAttrs(k),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return store.Record{}, err
	}
	if out.Item == nil {
		return store.Record{}, store.ErrNotFound
	}
	return itemToRecord(out.Item)
}

// Put writes r subject to cond via an atomic UpdateItem that increments the
// stored version.
func (s *Store) Put(ctx context.Context, r store.Record, cond store.Cond) (store.Record, error) {
	if err := validateKey(r.Key); err != nil {
		return store.Record{}, err
	}
	// DynamoDB rejects an empty Binary value, but the store contract admits
	// data-less records (index/existence markers). Set #d only when there is
	// data and REMOVE it otherwise; itemToRecord leaves Data nil when #d is
	// absent, so the empty case round-trips.
	set := []string{"#v = if_not_exists(#v, :zero) + :one"}
	remove := []string{}
	names := map[string]string{"#d": attrData, "#v": attrVersion, "#exp": attrExpireAt}
	values := map[string]types.AttributeValue{
		":zero": &types.AttributeValueMemberN{Value: "0"},
		":one":  &types.AttributeValueMemberN{Value: "1"},
	}
	if len(r.Data) > 0 {
		set = append(set, "#d = :d")
		values[":d"] = &types.AttributeValueMemberB{Value: append([]byte(nil), r.Data...)}
	} else {
		remove = append(remove, "#d")
	}
	if r.ExpireAt.IsZero() {
		remove = append(remove, "#exp")
	} else {
		set = append(set, "#exp = :exp")
		values[":exp"] = &types.AttributeValueMemberN{Value: strconv.FormatInt(r.ExpireAt.Unix(), 10)}
	}
	update := "SET " + strings.Join(set, ", ")
	if len(remove) > 0 {
		update += " REMOVE " + strings.Join(remove, ", ")
	}
	var condition *string
	switch cond {
	case store.CondIfAbsent:
		condition = aws.String("attribute_not_exists(#p)")
		names["#p"] = attrPK
	case store.CondIfVersion:
		if r.Version == 0 {
			condition = aws.String("attribute_not_exists(#p)")
			names["#p"] = attrPK
		} else {
			condition = aws.String("#v = :want")
			values[":want"] = &types.AttributeValueMemberN{Value: strconv.FormatInt(r.Version, 10)}
		}
	}
	out, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 &s.table,
		Key:                       keyAttrs(r.Key),
		UpdateExpression:          &update,
		ConditionExpression:       condition,
		ExpressionAttributeNames:  names,
		ExpressionAttributeValues: values,
		ReturnValues:              types.ReturnValueAllNew,
	})
	if err != nil {
		if isConditionFailure(err) {
			return store.Record{}, store.ErrConditionFailed
		}
		return store.Record{}, err
	}
	return itemToRecord(out.Attributes)
}

// Delete removes the record at r.Key subject to cond.
func (s *Store) Delete(ctx context.Context, r store.Record, cond store.Cond) error {
	if err := validateKey(r.Key); err != nil {
		return err
	}
	condition := "attribute_exists(#p)"
	names := map[string]string{"#p": attrPK}
	var values map[string]types.AttributeValue
	if cond == store.CondIfVersion {
		condition += " AND #v = :want"
		names["#v"] = attrVersion
		values = map[string]types.AttributeValue{
			":want": &types.AttributeValueMemberN{Value: strconv.FormatInt(r.Version, 10)},
		}
	}
	_, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:                 &s.table,
		Key:                       keyAttrs(r.Key),
		ConditionExpression:       &condition,
		ExpressionAttributeNames:  names,
		ExpressionAttributeValues: values,
	})
	if err != nil {
		if !isConditionFailure(err) {
			return err
		}
		// Distinguish missing (ErrNotFound) from stale version.
		if _, getErr := s.Get(ctx, r.Key); errors.Is(getErr, store.ErrNotFound) {
			return store.ErrNotFound
		}
		if cond == store.CondIfVersion {
			return store.ErrConditionFailed
		}
		return store.ErrNotFound
	}
	return nil
}

// Query yields the partition's records in SK order, paginating internally.
func (s *Store) Query(ctx context.Context, pk, skPrefix string, opt store.QueryOpt) iter.Seq2[store.Record, error] {
	return func(yield func(store.Record, error) bool) {
		keyCond := "#p = :pk"
		names := map[string]string{"#p": attrPK}
		values := map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: pk},
		}
		if skPrefix != "" {
			keyCond += " AND begins_with(#s, :prefix)"
			names["#s"] = attrSK
			values[":prefix"] = &types.AttributeValueMemberS{Value: skPrefix}
		}
		var startKey map[string]types.AttributeValue
		if opt.StartAfter != "" {
			startKey = keyAttrs(store.Key{PK: pk, SK: opt.StartAfter})
		}
		count := 0
		for {
			in := &dynamodb.QueryInput{
				TableName:                 &s.table,
				KeyConditionExpression:    &keyCond,
				ExpressionAttributeNames:  names,
				ExpressionAttributeValues: values,
				ScanIndexForward:          aws.Bool(!opt.Descending),
				ExclusiveStartKey:         startKey,
				ConsistentRead:            aws.Bool(true),
			}
			// Push the limit down so a small page does not read (and pay
			// for) a full 1MB partition page (tasks/115). +1 covers the
			// client-side cursor check without a second round trip.
			if opt.Limit > 0 {
				in.Limit = aws.Int32(int32(opt.Limit - count + 1))
			}
			out, err := s.client.Query(ctx, in)
			if err != nil {
				yield(store.Record{}, err)
				return
			}
			for _, item := range out.Items {
				rec, err := itemToRecord(item)
				if err != nil {
					yield(store.Record{}, err)
					return
				}
				if opt.Limit > 0 && count >= opt.Limit {
					return
				}
				count++
				if !yield(rec, nil) {
					return
				}
			}
			if out.LastEvaluatedKey == nil {
				return
			}
			startKey = out.LastEvaluatedKey
		}
	}
}

// Increment atomically adds delta to the counter at k.
func (s *Store) Increment(ctx context.Context, k store.Key, delta int64, expireAt time.Time) (int64, error) {
	if err := validateKey(k); err != nil {
		return 0, err
	}
	update := "ADD #c :delta"
	names := map[string]string{"#c": attrCount}
	values := map[string]types.AttributeValue{
		":delta": &types.AttributeValueMemberN{Value: strconv.FormatInt(delta, 10)},
	}
	if !expireAt.IsZero() {
		update += " SET #exp = :exp"
		names["#exp"] = attrExpireAt
		values[":exp"] = &types.AttributeValueMemberN{Value: strconv.FormatInt(expireAt.Unix(), 10)}
	}
	out, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 &s.table,
		Key:                       keyAttrs(k),
		UpdateExpression:          &update,
		ExpressionAttributeNames:  names,
		ExpressionAttributeValues: values,
		ReturnValues:              types.ReturnValueAllNew,
	})
	if err != nil {
		return 0, err
	}
	n, ok := out.Attributes[attrCount].(*types.AttributeValueMemberN)
	if !ok {
		return 0, fmt.Errorf("dynamo: counter attribute missing on %v", k)
	}
	return strconv.ParseInt(n.Value, 10, 64)
}

func validateKey(k store.Key) error {
	if k.PK == "" || k.SK == "" {
		return errors.New("dynamo: empty key component")
	}
	return nil
}

func isConditionFailure(err error) bool {
	var cond *types.ConditionalCheckFailedException
	return errors.As(err, &cond)
}

func itemToRecord(item map[string]types.AttributeValue) (store.Record, error) {
	rec := store.Record{}
	pk, ok := item[attrPK].(*types.AttributeValueMemberS)
	sk, ok2 := item[attrSK].(*types.AttributeValueMemberS)
	if !ok || !ok2 {
		return store.Record{}, errors.New("dynamo: item missing key attributes")
	}
	rec.Key = store.Key{PK: pk.Value, SK: sk.Value}
	if d, ok := item[attrData].(*types.AttributeValueMemberB); ok {
		rec.Data = d.Value
	}
	if v, ok := item[attrVersion].(*types.AttributeValueMemberN); ok {
		n, err := strconv.ParseInt(v.Value, 10, 64)
		if err != nil {
			return store.Record{}, fmt.Errorf("dynamo: bad version: %w", err)
		}
		rec.Version = n
	}
	if exp, ok := item[attrExpireAt].(*types.AttributeValueMemberN); ok {
		n, err := strconv.ParseInt(exp.Value, 10, 64)
		if err != nil {
			return store.Record{}, fmt.Errorf("dynamo: bad expireAt: %w", err)
		}
		rec.ExpireAt = time.Unix(n, 0).UTC()
	}
	return rec, nil
}
