# 165: Explore ethical (non-AWS) hosting for the backend

Explore running lcatd production off AWS in a green/ethical datacenter, using
the seams that already exist -- this should be configuration and ops work, not
code changes.

## What we already have

- `cmd/lcatd` is a first-class standalone/container entrypoint (the Lambda
  shape is secondary; writable-Lambda workers were deferred anyway, and a
  long-lived container runs the vocab/export drains natively).
- `LCATD_AWS_ENDPOINT` redirects both the S3 and DynamoDB clients to any
  compatible endpoint (blobs3 already does path-style for MinIO).
- Store is an interface: `store/dynamo` uses conditional puts/version checks
  but no TransactWriteItems, which stays inside what ScyllaDB Alternator
  (open-source DynamoDB-compatible API) supports. `store/mem` + snapshot is
  the small-scale fallback.
- Triggers sit behind `trigger.Trigger` (tasks/159); an in-process ticker or
  NATS replaces EventBridge/SQS.

## Candidate stack

lcatd container + MinIO or Garage for blobs + ScyllaDB/Alternator (or
mem+snapshot) for the store, `LCATD_AWS_ENDPOINT` pointed locally.

## Provider notes (researched 2026-07-08)

- Hetzner: hydro (DE) / wind+hydro (FI) directly sourced; cheapest; the only
  one with US locations -- Ashburn VA + Hillsboro OR, but those are
  cloud-VM-only rented colo: no object storage in US regions (self-host
  MinIO/Garage on volumes) and the renewable story is documented for the
  DE/FI parks, much less so for the US sites.
- Leafcloud (NL): strongest ethical-datacenter story (servers in residential
  buildings, waste heat warms their water); OpenStack + k8s. EU only.
- Infomaniak (CH): all-hydro, heat reuse, strong privacy posture; OpenStack.
  EU only.
- Scaleway (FR): 100% renewable, most AWS-shaped (S3-compatible storage,
  serverless containers). EU only.
- Context: Green Web Foundation stopped accepting carbon offsets as a
  fossil-free claim (2026-01), which favors direct-renewable providers over
  offset-heavy hyperscalers.

## Scaleway cost sketch (Paris list prices, 2026-07, excl. VAT)

Sizing driver is workindex RSS (~57KB/work, tasks/085). Instance list prices
include egress; object storage egress is free to 75GB/mo then EUR 0.01/GB;
add ~EUR 5/mo fixed (flexible IPv4 EUR 0.004/h + ~20GB block storage OS disk).

| Catalog | Workindex RAM | Instance | EUR/mo |
|---|---|---|---|
| <=50k works | ~3GB | PRO2-XXS 2c/8GB | 41 |
| ~250k works | ~14GB | PRO2-XS 4c/16GB | 82 |
| ~1M works | ~57GB | PRO2-M 16c/64GB (or POP2-HM 8c/64GB, 301) | 326 |

- Store: mem+snapshot to object storage adds ~nothing; a single-node ScyllaDB
  (Alternator) on its own PRO2-XXS adds EUR 41/mo -- only needed once
  durability/writes outgrow snapshots.
- Object storage: Standard Multi-AZ EUR 0.01606/GB/mo -- 50GB of blobs is
  EUR 0.80/mo, rounding error.
- So: small library ~EUR 45/mo all-in; mid-size ~EUR 90; 1M works ~EUR 330-370
  with a Scylla node. 10M works (~570GB RSS) has no sane single instance --
  that is the tasks/085 memory wall; the workindex persisted-snapshot work
  (tasks/154, tasks/162 workindex) is what changes that curve.
- Serverless Containers are a poor fit: the resident workindex makes cold
  starts expensive; a long-lived instance is the right shape.
- Measured 2026-07-08 on the playground: lcatd RSS is ~1.25GB with a 31-work
  catalog and the full LCSH+LCSHAC+LCGFT vocab snapshots (254MB on disk)
  installed -- at small catalog sizes the vocabulary term index dominates
  memory, not the workindex. `lcat vocab-subset` snapshots collapse it to
  tens of MB, which is what makes a ~1GB (PLAY2-PICO/DEV-class) deployment
  realistic for small libraries; the table above prices the
  full-LCSH-resident shape.

## Open questions

- The sizing table's ~57KB/work is the projection stage's peak RSS
  (tasks/085), used as a proxy -- lcatd's actual resident footprint is
  unmeasured. Filed as queerbooks-demo tasks/017 (48.5k-work corpus);
  fold the measured number back into the table here.
- Latency: if users/QLL are US-based, EU-only providers may be a real cost;
  Hetzner US is the compromise (with self-hosted object storage).
- Verify Alternator against the store contract: run `store/storetest` against
  a local ScyllaDB Alternator container (the same way dynamo is tested against
  DynamoDB-local).
- Backup/restore story for MinIO/Garage + Scylla off-AWS.
- Egress pricing comparison vs current AWS bill.

Deliverable: pick a provider, stand up a prototype deploy of the candidate
stack, document the recipe (compose file or small k8s manifest) under docs/.
