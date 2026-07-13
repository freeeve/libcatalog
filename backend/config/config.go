// Package config carries the backend's deployment configuration, read from
// the environment. It is deliberately SDK-free: cloud specifics (bucket names,
// table names, secret lookups) resolve to plain values before they reach the
// core, so the same configuration surface serves a container, a Lambda, and a
// laptop.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the resolved backend configuration.
type Config struct {
	// ListenAddr is the HTTP listen address for the standalone server
	// (ignored under Lambda). Default ":8080".
	ListenAddr string
	// BlobDir, when set, selects a local-directory grain store rooted there.
	// Ignored when S3Bucket is set.
	BlobDir string
	// S3Bucket, when set, selects an S3-compatible grain store (takes
	// precedence over BlobDir). Credentials and region come from the standard
	// AWS environment; AWSEndpoint overrides the endpoint for MinIO/local.
	S3Bucket string
	// DynamoTable, when set, selects a DynamoDB document store for the KV
	// surface (audit trail, queue, drafts, copycat, ...). Empty keeps the
	// in-memory store -- the local/demo default, which resets on restart.
	DynamoTable string
	// StoreDir, when set (and DynamoTable is not), selects the persistent
	// local document store: a journal-backed directory, so the moderation
	// queue, promotions, review decisions, audit trail, drafts, and job
	// records survive restarts without any AWS dependency.
	StoreDir string
	// AWSEndpoint overrides the AWS service endpoint for every AWS client, for
	// a single compatible endpoint such as LocalStack. Empty uses the real AWS
	// endpoints resolved from the region.
	AWSEndpoint string
	// S3Endpoint and DynamoEndpoint override AWSEndpoint for one service each.
	// The off-AWS deployment is two unrelated servers -- MinIO for blobs,
	// ScyllaDB Alternator or DynamoDB Local for documents -- so one endpoint
	// for both stores cannot address it. Empty falls
	// back to AWSEndpoint.
	S3Endpoint     string
	DynamoEndpoint string
	// ShutdownDelay, when positive, holds the server open after SIGTERM with
	// readiness already failing, so an orchestrator has time to take this
	// replica out of its load balancer before connections start being refused
	//. Zero (the default) drains immediately, which is what a
	// single-process or local run wants. In Kubernetes set it to comfortably
	// more than one readiness period, e.g. 5s.
	ShutdownDelay time.Duration
	// ReadOnly puts the instance in demo mode: the blob store is wrapped
	// read-only and editorial/config writes are rejected, so a public
	// playground can be explored without persisting. Auth, reads, search, and
	// dry-run previews still work.
	ReadOnly bool
	// Sandbox is read-only plus a client hint: the editor shows Save and
	// renders edits as if committed (from the dry-run's materialized doc),
	// wiped on refresh. It implies ReadOnly (nothing ever persists).
	Sandbox bool

	// LocalAuth enables built-in user management.
	LocalAuth bool
	// LocalIssuer is the built-in issuer string (must differ from any OIDC
	// issuer). Default "lcatd-local".
	LocalIssuer string
	// LocalSigningKey is the base64 (raw url or std) Ed25519 seed or private
	// key for access tokens. Empty = ephemeral key per boot (dev only:
	// restarts invalidate sessions).
	LocalSigningKey string
	// BootstrapAdmin is an "email:password" spec ensuring a first admin
	// exists at boot.
	BootstrapAdmin string

	// OIDCIssuer enables external-SSO verification when set.
	OIDCIssuer string
	// OIDCAudience, when set, must appear in tokens' aud.
	OIDCAudience string
	// OIDCRoleClaim is the role-bearing claim. Default "role".
	OIDCRoleClaim string
	// OIDCRoleMap maps issuer role names to backend roles
	// ("subject_moderator=moderator,staff=librarian").
	OIDCRoleMap map[string]string
	// OIDCClientID and OIDCClientSecret configure the PKCE token-exchange
	// proxy; an empty secret leaves the proxy returning 503.
	OIDCClientID     string
	OIDCClientSecret string

	// VocabSchemes lists the controlled vocabularies to load from the blob
	// store's authorities tree (comma-separated; empty = all found).
	VocabSchemes []string
	// ExtraFacets lists the lcat:extra/* keys the admin works view facets
	// on (LCATD_EXTRA_FACETS, comma-separated). Defaults to "sources" --
	// the provenance dimension; set it empty to disable.
	ExtraFacets []string
	// VocabUploadCapMB bounds hand-uploaded vocabulary dumps (0 = the 512MB
	// default). Synchronous in-memory installs need some ceiling; size it
	// to the deployment's RAM.
	VocabUploadCapMB int
	// VocabSnapshotCapMB bounds a downloaded snapshot dump's decompressed
	// size (0 = the 4GB default) -- the defensive ceiling against a hostile
	// or misconfigured snapshot endpoint.
	VocabSnapshotCapMB int
	// AuthoritiesPrefix is the blob path prefix holding authority grains.
	// Default "data/authorities/".
	AuthoritiesPrefix string

	// AbuseSecret (>=16 bytes) keys IP pseudonymization and challenge
	// tokens; setting it enables the anonymous suggestion endpoints.
	AbuseSecret string

	// WebhookURL, when set, receives HMAC-signed grains-changed events after
	// each publish (WebhookSecret signs them).
	WebhookURL    string
	WebhookSecret string
	// RebuildCmd, when set, runs after each publish (sh -c; changed paths in
	// $LCAT_CHANGED_PATHS) -- the local dev loop: reserialize/reproject into
	// a running hugo server's data dir for instant preview. RebuildDir is
	// its working directory.
	RebuildCmd string
	RebuildDir string
	// TriggerSQSURL / TriggerEventBus dispatch grains-changed events to AWS
	// messaging -- the async job seam: a queue worker (ECS
	// RunTask, Step Functions) runs the incremental rebuild instead of a
	// synchronous local command.
	TriggerSQSURL   string
	TriggerEventBus string
	// RebuildDebounce, when positive, coalesces a burst of publishes into one
	// trigger event delivered after that quiet period.
	RebuildDebounce time.Duration

	// BrandCSS, when set, is the path of a CSS file that re-brands the SPA
	// at boot without a rebuild: the server reads it once,
	// serves it at /brand.css, and links it from index.html after app.css,
	// so its rules (typically :root / html[data-theme="dark"] token
	// overrides) win the cascade. An unreadable file fails the boot.
	BrandCSS string

	// Provider names the primary feed graph (CSV export projection).
	// Default "overdrive".
	Provider string

	// DisableTickers skips the container-shaped background workers (the
	// 15s vocab-download and export-job drains). Set programmatically by
	// serverless entrypoints, which drain on scheduled invocations instead
	// -- a frozen Lambda never advances a ticker goroutine.
	DisableTickers bool

	// OrgCode is the deployment's MARC organization code. When set, MARC
	// surfaces (the MARC view, exports) derive each record's 040 cataloging
	// source from graph facts at decode time: locally edited
	// records gain this code as a modifying-agency $d, born-digital records
	// synthesize 040 $a/$c. Empty disables the derivation.
	OrgCode string

	// QueueMinConfidence is the review queue's default confidence floor
	// in [0,1]: PIPELINE suggestions below it stay stored but hidden from
	// the default view (a request may override with ?minConfidence=). 0
	// (the default) shows everything.
	QueueMinConfidence float64

	// SIP2Addr, when set (host:port), mounts the public SIP2 availability
	// bridge at POST /v1/availability/sip2: the OPAC's proxied transport for
	// live shelf status, with the ILS credentials held server-side.
	SIP2Addr string
	// SIP2User/SIP2Pass, when set, log the session in (SIP2 93/94).
	SIP2User string
	SIP2Pass string
	// SIP2Location is the login location code (CP); optional.
	SIP2Location string
	// SIP2Institution rides each item request as AO; optional.
	SIP2Institution string
	// SIP2ErrorDetection appends AY/AZ checksums to outbound messages, for
	// ACS servers that require them.
	SIP2ErrorDetection bool

	// EnrichLocsh enables the id.loc.gov LCSH reconciliation source when set
	// to "queue" (moderated) or "direct" (auto-approve).
	EnrichLocsh string

	// EnrichOpenLibrary enables the OpenLibrary external-identity source
	// when set to "queue" or "direct". It needs
	// EnrichOpenLibraryDump; without it the source stays off.
	EnrichOpenLibrary string
	// EnrichOpenLibraryDump is the path to an OpenLibrary editions dump the
	// source builds its ISBN -> work index from at boot (offline, no live API).
	EnrichOpenLibraryDump string

	// EnrichWikidataEndpoint overrides the SPARQL endpoint the wikidata
	// source queries (default: the public Wikidata Query Service). Point it
	// at a mirror (a QLever instance, a corporate proxy) for rate-limit or
	// availability reasons; the query dialect must stay WDQS-compatible.
	EnrichWikidataEndpoint string

	// EnrichWikidata enables the creator-demographics source (resolve
	// creators via cataloged ISBNs against the Wikidata Query Service and
	// cache their explicitly-stated claims). Opt-in and direct-only:
	// creator claims are entity statements, not subject candidates, so
	// there is nothing for the moderation queue to moderate. Set "direct"
	// to enable.
	EnrichWikidata string
	// EnrichBiblioCommons enables the peer-library subject harvest: one or
	// more BiblioCommons subdomains, comma-separated (e.g. "ccslib" or
	// "ccslib,seattle,sfpl"). The harvest drives subject searches from a
	// loaded vocabulary's terms and queues moderated suggestions on ISBN-
	// or title+author-matched works; several hosts turn the run into a
	// consensus vote. Queue-only: another library's cataloging is a
	// candidate, not an assertion. Jobs may override the list per run.
	EnrichBiblioCommons string
	// EnrichBiblioCommonsScheme picks the driver vocabulary (default
	// "homosaurus"); it must be loaded. EnrichBiblioCommonsMaxPages caps RSS
	// pages fetched per term (default 6 -- 600 items at 100/page).
	// EnrichBiblioCommonsConcurrency caps how many peer hosts crawl at once
	// (default 4; politeness stays per host) -- raise it for wide consensus
	// runs over many peers.
	EnrichBiblioCommonsScheme      string
	EnrichBiblioCommonsMaxPages    int
	EnrichBiblioCommonsConcurrency int
	// EnrichVega enables the III Vega Discover peer harvest: comma-separated
	// tenants as <siteCode>.<region> (e.g. "nypl.na2,mdpls.na" -- the
	// library's catalog subdomain and Vega region). Queue-only, like the
	// BiblioCommons harvest it parallels; the concept model states its
	// vocabulary explicitly (source=homoit), so a match is a peer's own
	// Homosaurus assertion.
	EnrichVega string
	// EnrichVegaScheme picks the driver vocabulary (default "homosaurus");
	// EnrichVegaMaxPages caps resources pages per concept (default 6).
	EnrichVegaScheme   string
	EnrichVegaMaxPages int
	// EnrichTLC enables the TLC LS2 PAC peer harvest: comma-separated
	// catalog subdomains of <tenant>.tlcdelivers.com (e.g. "nbpl").
	// Queue-only; the subject index is unscoped, so like the BiblioCommons
	// harvest the match is the exact Homosaurus prefLabel, ISBN-joined.
	EnrichTLC string
	// EnrichTLCScheme picks the driver vocabulary (default "homosaurus");
	// EnrichTLCMaxPages caps search pages per term (default 6 x 24 hits).
	EnrichTLCScheme   string
	EnrichTLCMaxPages int
	// EnrichSirsiDynix enables the SirsiDynix Enterprise peer harvest:
	// comma-separated <host>[/<profile>] entries. A host with no dot is a
	// bare Enterprise subdomain expanded to <host>.ent.sirsidynix.net (e.g.
	// "winca"); the profile defaults to "default". Queue-only; the subject
	// index is unscoped, so like the BiblioCommons harvest the match is the
	// exact Homosaurus prefLabel, ISBN-joined.
	EnrichSirsiDynix string
	// EnrichSirsiDynixScheme picks the driver vocabulary (default
	// "homosaurus").
	EnrichSirsiDynixScheme string
}

// FromEnv reads configuration from LCATD_-prefixed environment variables.
func FromEnv() (Config, error) {
	cfg := Config{
		ListenAddr:                envOr("LCATD_LISTEN_ADDR", ":8080"),
		BlobDir:                   os.Getenv("LCATD_BLOB_DIR"),
		StoreDir:                  os.Getenv("LCATD_STORE_DIR"),
		SIP2Addr:                  os.Getenv("LCATD_SIP2_ADDR"),
		SIP2User:                  os.Getenv("LCATD_SIP2_USER"),
		SIP2Pass:                  os.Getenv("LCATD_SIP2_PASS"),
		SIP2Location:              os.Getenv("LCATD_SIP2_LOCATION"),
		SIP2Institution:           os.Getenv("LCATD_SIP2_INSTITUTION"),
		SIP2ErrorDetection:        os.Getenv("LCATD_SIP2_ERROR_DETECTION") == "1",
		S3Bucket:                  os.Getenv("LCATD_S3_BUCKET"),
		DynamoTable:               os.Getenv("LCATD_DYNAMO_TABLE"),
		AWSEndpoint:               os.Getenv("LCATD_AWS_ENDPOINT"),
		S3Endpoint:                os.Getenv("LCATD_S3_ENDPOINT"),
		DynamoEndpoint:            os.Getenv("LCATD_DYNAMO_ENDPOINT"),
		ReadOnly:                  os.Getenv("LCATD_READ_ONLY") == "1" || os.Getenv("LCATD_READ_ONLY") == "true",
		Sandbox:                   os.Getenv("LCATD_SANDBOX") == "1" || os.Getenv("LCATD_SANDBOX") == "true",
		LocalAuth:                 os.Getenv("LCATD_LOCAL_AUTH") == "1" || os.Getenv("LCATD_LOCAL_AUTH") == "true",
		LocalIssuer:               envOr("LCATD_LOCAL_ISSUER", "lcatd-local"),
		LocalSigningKey:           os.Getenv("LCATD_LOCAL_SIGNING_KEY"),
		BootstrapAdmin:            os.Getenv("LCATD_BOOTSTRAP_ADMIN"),
		OIDCIssuer:                os.Getenv("LCATD_OIDC_ISSUER"),
		OIDCAudience:              os.Getenv("LCATD_OIDC_AUDIENCE"),
		OIDCRoleClaim:             envOr("LCATD_OIDC_ROLE_CLAIM", "role"),
		OIDCClientID:              os.Getenv("LCATD_OIDC_CLIENT_ID"),
		OIDCClientSecret:          os.Getenv("LCATD_OIDC_CLIENT_SECRET"),
		AuthoritiesPrefix:         envOr("LCATD_AUTHORITIES_PREFIX", "data/authorities/"),
		AbuseSecret:               os.Getenv("LCATD_ABUSE_SECRET"),
		WebhookURL:                os.Getenv("LCATD_WEBHOOK_URL"),
		WebhookSecret:             os.Getenv("LCATD_WEBHOOK_SECRET"),
		BrandCSS:                  os.Getenv("LCATD_BRAND_CSS"),
		RebuildCmd:                os.Getenv("LCATD_REBUILD_CMD"),
		RebuildDir:                os.Getenv("LCATD_REBUILD_DIR"),
		TriggerSQSURL:             os.Getenv("LCATD_TRIGGER_SQS_URL"),
		TriggerEventBus:           os.Getenv("LCATD_TRIGGER_EVENT_BUS"),
		Provider:                  envOr("LCATD_PROVIDER", "overdrive"),
		OrgCode:                   os.Getenv("LCATD_ORG_CODE"),
		EnrichLocsh:               os.Getenv("LCATD_ENRICH_LOCSH"),
		EnrichOpenLibrary:         os.Getenv("LCATD_ENRICH_OPENLIBRARY"),
		EnrichOpenLibraryDump:     os.Getenv("LCATD_ENRICH_OPENLIBRARY_DUMP"),
		EnrichWikidata:            os.Getenv("LCATD_ENRICH_WIKIDATA"),
		EnrichWikidataEndpoint:    os.Getenv("LCATD_ENRICH_WIKIDATA_ENDPOINT"),
		EnrichBiblioCommons:       os.Getenv("LCATD_ENRICH_BIBLIOCOMMONS"),
		EnrichBiblioCommonsScheme: envOr("LCATD_ENRICH_BIBLIOCOMMONS_SCHEME", "homosaurus"),
		EnrichVega:                os.Getenv("LCATD_ENRICH_VEGA"),
		EnrichVegaScheme:          envOr("LCATD_ENRICH_VEGA_SCHEME", "homosaurus"),
		EnrichTLC:                 os.Getenv("LCATD_ENRICH_TLC"),
		EnrichTLCScheme:           envOr("LCATD_ENRICH_TLC_SCHEME", "homosaurus"),
		EnrichSirsiDynix:          os.Getenv("LCATD_ENRICH_SIRSIDYNIX"),
		EnrichSirsiDynixScheme:    envOr("LCATD_ENRICH_SIRSIDYNIX_SCHEME", "homosaurus"),
	}
	if cfg.Sandbox {
		cfg.ReadOnly = true // sandbox never persists
	}
	if cfg.EnrichLocsh != "" && cfg.EnrichLocsh != "queue" && cfg.EnrichLocsh != "direct" {
		return Config{}, fmt.Errorf("config: LCATD_ENRICH_LOCSH must be queue or direct")
	}
	if cfg.EnrichOpenLibrary != "" && cfg.EnrichOpenLibrary != "queue" && cfg.EnrichOpenLibrary != "direct" {
		return Config{}, fmt.Errorf("config: LCATD_ENRICH_OPENLIBRARY must be queue or direct")
	}
	if cfg.EnrichWikidata != "" && cfg.EnrichWikidata != "direct" {
		return Config{}, fmt.Errorf("config: LCATD_ENRICH_WIKIDATA must be direct (creator claims are not queue-moderated)")
	}
	if cfg.EnrichWikidataEndpoint != "" && !strings.HasPrefix(cfg.EnrichWikidataEndpoint, "http") {
		return Config{}, fmt.Errorf("config: LCATD_ENRICH_WIKIDATA_ENDPOINT must be an http(s) URL")
	}
	if cfg.EnrichOpenLibrary != "" && cfg.EnrichOpenLibraryDump == "" {
		return Config{}, fmt.Errorf("config: LCATD_ENRICH_OPENLIBRARY needs LCATD_ENRICH_OPENLIBRARY_DUMP (the editions dump path)")
	}
	for _, h := range strings.Split(cfg.EnrichBiblioCommons, ",") {
		if strings.ContainsAny(strings.TrimSpace(h), "./:") {
			return Config{}, fmt.Errorf("config: LCATD_ENRICH_BIBLIOCOMMONS wants bare BiblioCommons subdomains (e.g. ccslib,seattle), not URLs")
		}
	}
	if raw := os.Getenv("LCATD_ENRICH_BIBLIOCOMMONS_MAX_PAGES"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("config: LCATD_ENRICH_BIBLIOCOMMONS_MAX_PAGES must be a positive integer")
		}
		cfg.EnrichBiblioCommonsMaxPages = n
	}
	for _, h := range strings.Split(cfg.EnrichTLC, ",") {
		if strings.ContainsAny(strings.TrimSpace(h), "./:") {
			return Config{}, fmt.Errorf("config: LCATD_ENRICH_TLC wants bare tlcdelivers.com subdomains (e.g. nbpl), not URLs")
		}
	}
	if raw := os.Getenv("LCATD_ENRICH_TLC_MAX_PAGES"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("config: LCATD_ENRICH_TLC_MAX_PAGES must be a positive integer")
		}
		cfg.EnrichTLCMaxPages = n
	}
	if raw := os.Getenv("LCATD_ENRICH_VEGA_MAX_PAGES"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("config: LCATD_ENRICH_VEGA_MAX_PAGES must be a positive integer")
		}
		cfg.EnrichVegaMaxPages = n
	}
	if raw := os.Getenv("LCATD_ENRICH_BIBLIOCOMMONS_CONCURRENCY"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("config: LCATD_ENRICH_BIBLIOCOMMONS_CONCURRENCY must be a positive integer")
		}
		cfg.EnrichBiblioCommonsConcurrency = n
	}
	if raw := os.Getenv("LCATD_QUEUE_MIN_CONFIDENCE"); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || v < 0 || v > 1 {
			return Config{}, fmt.Errorf("config: LCATD_QUEUE_MIN_CONFIDENCE must be a number in [0,1]")
		}
		cfg.QueueMinConfidence = v
	}
	if raw := os.Getenv("LCATD_VOCAB_UPLOAD_CAP_MB"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("config: LCATD_VOCAB_UPLOAD_CAP_MB must be a positive integer of megabytes")
		}
		cfg.VocabUploadCapMB = n
	}
	if raw := os.Getenv("LCATD_VOCAB_SNAPSHOT_CAP_MB"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("config: LCATD_VOCAB_SNAPSHOT_CAP_MB must be a positive integer of megabytes")
		}
		cfg.VocabSnapshotCapMB = n
	}
	if raw := os.Getenv("LCATD_VOCAB_SCHEMES"); raw != "" {
		for s := range strings.SplitSeq(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				cfg.VocabSchemes = append(cfg.VocabSchemes, s)
			}
		}
	}
	cfg.ExtraFacets = []string{"sources"}
	if raw, ok := os.LookupEnv("LCATD_EXTRA_FACETS"); ok {
		cfg.ExtraFacets = nil
		for s := range strings.SplitSeq(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				cfg.ExtraFacets = append(cfg.ExtraFacets, s)
			}
		}
	}
	if raw := os.Getenv("LCATD_REBUILD_DEBOUNCE"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("config: LCATD_REBUILD_DEBOUNCE must be a positive duration (e.g. 5s)")
		}
		cfg.RebuildDebounce = d
	}
	if raw := os.Getenv("LCATD_SHUTDOWN_DELAY"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d < 0 {
			return Config{}, fmt.Errorf("config: LCATD_SHUTDOWN_DELAY must be a non-negative duration (e.g. 5s)")
		}
		cfg.ShutdownDelay = d
	}
	if cfg.ListenAddr == "" {
		return Config{}, fmt.Errorf("config: empty LCATD_LISTEN_ADDR")
	}
	if raw := os.Getenv("LCATD_OIDC_ROLE_MAP"); raw != "" {
		cfg.OIDCRoleMap = map[string]string{}
		for pair := range strings.SplitSeq(raw, ",") {
			from, to, ok := strings.Cut(strings.TrimSpace(pair), "=")
			if !ok || from == "" || to == "" {
				return Config{}, fmt.Errorf("config: bad LCATD_OIDC_ROLE_MAP entry %q", pair)
			}
			cfg.OIDCRoleMap[from] = to
		}
	}
	if cfg.LocalAuth && cfg.OIDCIssuer != "" && cfg.LocalIssuer == cfg.OIDCIssuer {
		return Config{}, fmt.Errorf("config: local and OIDC issuers must differ")
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ResolvedS3Endpoint is the endpoint the S3 client should use: the
// service-specific override when set, else the all-services endpoint, else the
// empty string (meaning real AWS, resolved from the region).
func (c Config) ResolvedS3Endpoint() string {
	if c.S3Endpoint != "" {
		return c.S3Endpoint
	}
	return c.AWSEndpoint
}

// ResolvedDynamoEndpoint is the endpoint the DynamoDB client should use.
func (c Config) ResolvedDynamoEndpoint() string {
	if c.DynamoEndpoint != "" {
		return c.DynamoEndpoint
	}
	return c.AWSEndpoint
}
