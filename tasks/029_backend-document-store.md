# 029 -- Backend document store (`backend/store`)

## Context

The sidecar data (suggestion queue, users, drafts, audit, rate counters, jobs,
leases) needs a portable serverless-friendly store. qllpoc proved a single-table
DynamoDB design; the interface here generalizes it so Firestore/Cosmos-style
stores can implement later. No transactions or GSIs in the interface -- the
service layer maintains explicit index items any pk/sk store can hold.

## Scope

1. `backend/store/store.go`: `Key{PK, SK}`, `Record{Key, Data []byte,
   Version int64, ExpireAt time.Time}`, ops `Get`, `Put(r, Cond)`
   (`CondNone|CondIfAbsent|CondIfVersion`), `Delete(k, Cond)`,
   `Query(pk, skPrefix, QueryOpt) iter.Seq2[Record, error]`,
   `Increment(k, delta, expireAt) (int64, error)`.
   Errors `ErrNotFound`, `ErrConditionFailed`.
2. `backend/store/mem.go`: in-memory impl with exact semantics (tests, dev).
3. `backend/store/dynamo/dynamo.go`: single-table DynamoDB impl
   (aws-sdk-go-v2; Version -> condition expression; TTL attribute; Query on
   pk + begins_with sk).
4. Document the single-table layout (WORK#/STATUS#/FOLK#/AUDIT#/RATE#/USER#/
   DRAFT#/JOB#/LEASE# partitions) in the package doc.

## Acceptance

- Shared conformance suite runs against mem always, and against DynamoDB behind
  a build tag/env (`DYNAMO_ENDPOINT`, local DynamoDB) -- covering conditional
  put/delete races, version bumps, TTL field round-trip, query pagination and
  prefix filtering, Increment atomicity.
