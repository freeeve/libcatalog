// Typed client for the cataloging API. Injects the bearer token and retries
// once through a refresh on 401; a second 401 surfaces as an ApiError the
// shell turns into the login screen.
import { apiBase } from "./config";
import { expireSession, getToken, invalidateAccess } from "./auth";
import type {
  AuditPage,
  AuthorityMergeResult,
  AuthorityUnmergeResult,
  AuthoritySelection,
  AuthorityTerm,
  AuthorityView,
  BatchRunResult,
  BatchTarget,
  CopycatBatch,
  CopycatFieldTerm,
  CopycatPolicy,
  CopycatProfile,
  CopycatRevertResult,
  CopycatSearchResult,
  CopycatStagedRecord,
  CopycatTarget,
  CopycatTemplate,
  MarcFieldError,
  DecidePromotionResponse,
  AuditTermsPage,
  CrosswalkCategory,
  CrosswalkView,
  DiversityReport,
  DiversitySnapshot,
  DuplicateGroup,
  EnrichJob,
  EnrichRunResult,
  WorkItem,
  WorkVisibility,
  Decision,
  Draft,
  DraftBody,
  ExportFormat,
  ExportJob,
  GrainConflict,
  ItemTemplate,
  Macro,
  MarcRecordDoc,
  MarcResponse,
  MonthStats,
  Op,
  OpsResult,
  Profile,
  ProfileSummary,
  Promotion,
  PublishResponse,
  QueueApproveJob,
  QueuePage,
  ReviewResponse,
  SavedQuery,
  Selection,
  SubjectCandidate,
  TagCount,
  TermEquivalent,
  Term,
  TermRef,
  VocabJob,
  SuggestionPolicy,
  VocabSource,
  VocabSourceView,
  VocabSuggestion,
  WorkDocResponse,
  WorksPage,
  WorkSummary,
} from "./types";

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

/** A cataloger-facing message for a failed call: service-internal prefixes
 * ("batch: invalid request:") never escape to the UI; anything
 *  that is not an ApiError gets the caller's fallback copy. */
export function humanApiMessage(e: unknown, fallback: string): string {
  if (!(e instanceof ApiError)) return fallback;
  const msg = e.message.replace(/^[a-z]+: (invalid (request|source): )?/, "").trim();
  return msg ? msg.charAt(0).toUpperCase() + msg.slice(1) : fallback;
}

/** A 412 from an If-Match write: the record moved underneath the client.
 *  Carries the fresh state so the editor can rebase deliberately. */
export class ConflictError extends ApiError {
  constructor(public state: GrainConflict) {
    super(412, "the record changed since it was loaded");
  }
}

/** A refusal carrying field-anchored validation errors. */
export class FieldedApiError extends ApiError {
  constructor(
    status: number,
    message: string,
    public fields: MarcFieldError[],
  ) {
    super(status, message);
  }
}

// callRaw is call for non-JSON request bodies (cover uploads): the body
// passes through untouched under its own content type.
async function callRaw<T>(method: string, path: string, body: BodyInit, contentType: string): Promise<T> {
  for (let attempt = 0; attempt < 2; attempt++) {
    const token = await getToken();
    if (!token) throw new ApiError(401, "not signed in");
    const res = await fetch(apiBase() + path, {
      method,
      headers: { Authorization: `Bearer ${token}`, "Content-Type": contentType },
      body,
    });
    if (res.status === 401 && attempt === 0) {
      invalidateAccess();
      continue;
    }
    if (!res.ok) {
      const msg = (await res.json().catch(() => null)) as { error?: string } | null;
      throw new ApiError(res.status, msg?.error ?? res.statusText);
    }
    return (await res.json()) as T;
  }
  expireSession();
  throw new ApiError(401, "session expired");
}

async function call<T>(method: string, path: string, body?: unknown, headers?: Record<string, string>): Promise<T> {
  for (let attempt = 0; attempt < 2; attempt++) {
    const token = await getToken();
    if (!token) throw new ApiError(401, "not signed in");
    const res = await fetch(apiBase() + path, {
      method,
      headers: {
        Authorization: `Bearer ${token}`,
        ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
        ...headers,
      },
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
    if (res.status === 401 && attempt === 0) {
      invalidateAccess(); // retry refreshes via the (still valid) refresh token
      continue;
    }
    if (res.status === 412) {
      const state = (await res.json().catch(() => null)) as GrainConflict | null;
      throw new ConflictError(state ?? { workId: "", etag: "", nquads: "" });
    }
    if (!res.ok) {
      const detail = (await res.json().catch(() => ({}))) as { error?: string; fields?: MarcFieldError[] };
      if (Array.isArray(detail.fields) && detail.fields.length > 0) {
        throw new FieldedApiError(res.status, detail.error || res.statusText, detail.fields);
      }
      throw new ApiError(res.status, detail.error || res.statusText);
    }
    if (res.status === 204) return undefined as T; // e.g. folk governance
    return (await res.json()) as T;
  }
  // A refreshed token that still 401s: the session is dead server-side.
  // expireSession swaps the shell into its re-auth prompt.
  expireSession();
  throw new ApiError(401, "authentication failed");
}

/** The withdrawal review queue: reconciliation-flagged works
 *  awaiting a suppress-or-keep decision (librarian). */
export function fetchWithdrawn(): Promise<{ works: WorkSummary[] }> {
  return call("GET", "/v1/withdrawn");
}

/** Decides one withdrawn work: "suppress" hides it, "keep" clears the flag
 *  and pins the decision so reconciliation never re-flags it (librarian). */
export function decideWithdrawn(workId: string, action: "keep" | "suppress"): Promise<WorkVisibility> {
  return call("POST", `/v1/works/${encodeURIComponent(workId)}/withdrawn`, { action });
}

/** Facet selections for the works list, keyed by group:
 *  visibility, holdings, needs, subject, tag. Values within a group OR;
 *  groups AND. */
export type WorkFilters = Record<string, string[]>;

/** How the works list treats retired records. The server defaults to
 *  `exclude`: a tombstone says "this record is retired", and a cataloger
 *  searching for a book is not looking for one. `only` answers "what did I
 *  retire?". */
export type TombstoneMode = "exclude" | "include" | "only";

/** Work search over the grain tree (librarian); offset pages the matches.
 *
 *  `tombstoned` is sent to the server rather than filtered here: a client-side
 *  filter over a paged response reports "10 matches" and renders one. */
export function fetchWorks(
  q: string,
  limit = 50,
  offset = 0,
  filters: WorkFilters = {},
  tombstoned: TombstoneMode = "exclude",
): Promise<WorksPage> {
  const params = new URLSearchParams({ q, limit: String(limit) });
  if (offset > 0) params.set("offset", String(offset));
  if (tombstoned !== "exclude") params.set("tombstoned", tombstoned);
  for (const [group, values] of Object.entries(filters)) {
    for (const v of values) params.append(group, v);
  }
  return call("GET", `/v1/works?${params}`);
}

/** The profile-shaped editing document for one work (librarian). A work
 *  without recognized instances or passthrough statements arrives with those
 *  Go nil slices as null; normalize so screens can index them. */
export async function fetchWorkDoc(id: string): Promise<WorkDocResponse> {
  const res: WorkDocResponse = await call("GET", `/v1/works/${encodeURIComponent(id)}/doc`);
  res.doc.instances ??= [];
  res.doc.passthrough ??= [];
  return res;
}

/** Ships a field-op batch for one work (librarian). Writes require ifMatch
 *  (the doc's etag); dryRun validates and returns the exact quad delta
 *  without writing. A concurrent write surfaces as ConflictError. */
export function postOps(workId: string, ops: Op[], opts: { ifMatch?: string; dryRun?: boolean } = {}): Promise<OpsResult> {
  return call(
    "POST",
    `/v1/works/${encodeURIComponent(workId)}/ops`,
    { ops, ...(opts.dryRun ? { dryRun: true } : {}) },
    opts.ifMatch ? { "If-Match": opts.ifMatch } : undefined,
  );
}

/** The MARC materialization of a work's records (librarian). */
export function fetchMarc(workId: string): Promise<MarcResponse> {
  return call("GET", `/v1/works/${encodeURIComponent(workId)}/marc`);
}

/** Saves one edited MARC record as an editorial diff (librarian). dryRun
 *  returns the exact quad delta; writes need ifMatch and surface concurrent
 *  edits as ConflictError. */
export function postMarc(
  workId: string,
  index: number,
  record: MarcRecordDoc,
  opts: { ifMatch?: string; dryRun?: boolean } = {},
): Promise<OpsResult> {
  return call(
    "POST",
    `/v1/works/${encodeURIComponent(workId)}/marc`,
    { index, record, ...(opts.dryRun ? { dryRun: true } : {}) },
    opts.ifMatch ? { "If-Match": opts.ifMatch } : undefined,
  );
}

/** Creates a per-user editor draft (librarian). */
export function createDraft(workId: string, body: DraftBody): Promise<Draft> {
  return call("POST", "/v1/drafts", { workId, body });
}

/** Every draft belonging to the signed-in user (librarian). */
export function fetchDrafts(): Promise<{ drafts: Draft[] }> {
  return call("GET", "/v1/drafts");
}

/** One draft by id (librarian). */
export function fetchDraft(id: string): Promise<Draft> {
  return call("GET", `/v1/drafts/${encodeURIComponent(id)}`);
}

/** Overwrites an existing draft (librarian). */
export function updateDraft(id: string, workId: string, body: DraftBody): Promise<Draft> {
  return call("PUT", `/v1/drafts/${encodeURIComponent(id)}`, { workId, body });
}

/** Deletes a draft; resolves on the server's 204 (librarian). */
export function deleteDraft(id: string): Promise<void> {
  return call("DELETE", `/v1/drafts/${encodeURIComponent(id)}`);
}

/** The audit trail for one month, optionally narrowed to a work (librarian). */
export function fetchAudit(month: string, workId?: string): Promise<AuditPage> {
  const params = new URLSearchParams({ month });
  if (workId) params.set("workId", workId);
  return call("GET", `/v1/audit?${params}`);
}

/** Editing-activity rollup for a month, YYYY-MM (librarian). */
export function fetchStats(month: string): Promise<MonthStats> {
  return call("GET", `/v1/stats?${new URLSearchParams({ month })}`);
}

/** The content-diversity audit over the live work index (librarian).
 *  Filters are `key=value` terms matched against work extras, ANDed.
 *  With `simulate`, the response also carries the read-only "if we accepted the
 *  pending ADD queue" projection (report.simulation) so the screen can diff
 *  current vs projected coverage. */
export function fetchDiversityAudit(
  filters: string[] = [],
  opts: { source?: string; simulate?: boolean } = {},
): Promise<DiversityReport> {
  const q = new URLSearchParams();
  for (const f of filters) q.append("filter", f);
  if (opts.source) q.set("source", opts.source);
  if (opts.simulate) q.set("simulate", "queue");
  const qs = q.toString();
  return call("GET", `/v1/audit/diversity${qs ? `?${qs}` : ""}`);
}

/** A term's cross-scheme equivalents: skos matches both directions plus
 *  one-hop pivots, strength-labeled (public vocab read). */
export function fetchTermEquivalents(id: string): Promise<{ equivalents: TermEquivalent[] }> {
  return call("GET", `/v1/terms/equivalents?id=${encodeURIComponent(id)}`);
}

/** The dated audit-snapshot series for a scope (librarian). */
export function fetchDiversitySnapshots(filters: string[] = []): Promise<{ snapshots: DiversitySnapshot[] }> {
  const q = new URLSearchParams();
  for (const f of filters) q.append("filter", f);
  const qs = q.toString();
  return call("GET", `/v1/audit/diversity/snapshots${qs ? `?${qs}` : ""}`);
}

/** Records today's audit for the scope -- one per scope per day (librarian). */
export function recordDiversitySnapshot(filters: string[] = []): Promise<DiversitySnapshot> {
  const q = new URLSearchParams();
  for (const f of filters) q.append("filter", f);
  const qs = q.toString();
  return call("POST", `/v1/audit/diversity/snapshots${qs ? `?${qs}` : ""}`);
}

/** The crosswalk-configuration view: seed, persisted override, effective
 *  merge (librarian). */
export function fetchDiversityCrosswalk(): Promise<CrosswalkView> {
  return call("GET", "/v1/audit/diversity/crosswalk");
}

/** Persists the operator's crosswalk override -- structured categories or a
 *  pasted TOML document, exactly one (librarian). */
export function saveDiversityCrosswalk(body: { categories?: CrosswalkCategory[]; toml?: string }): Promise<CrosswalkView> {
  return call("PUT", "/v1/audit/diversity/crosswalk", body);
}

/** Removes the override; the audit returns to the built-in seed (librarian). */
export function deleteDiversityCrosswalk(): Promise<void> {
  return call("DELETE", "/v1/audit/diversity/crosswalk");
}

/** Runs the content audit with a candidate crosswalk WITHOUT persisting it --
 *  the facet builder's live preview (librarian). */
export function previewDiversityCrosswalk(
  body: { categories?: CrosswalkCategory[]; toml?: string },
  filters: string[] = [],
): Promise<DiversityReport> {
  const q = new URLSearchParams();
  for (const f of filters) q.append("filter", f);
  const qs = q.toString();
  return call("POST", `/v1/audit/diversity/preview${qs ? `?${qs}` : ""}`, body);
}

/** The configured enrichment sources (admin). */
export function fetchEnrichSources(): Promise<{ sources: string[]; targets?: Record<string, string> }> {
  return call("GET", "/v1/enrich");
}

/** Runs one enrichment source synchronously -- small scoped runs only; a
 *  corpus-scale pass belongs in a job (admin). */
export function runEnrichSource(source: string, filters: string[] = []): Promise<EnrichRunResult> {
  const q = new URLSearchParams();
  for (const f of filters) q.append("filter", f);
  const qs = q.toString();
  return call("POST", `/v1/enrich/${encodeURIComponent(source)}/run${qs ? `?${qs}` : ""}`);
}

/** Queues an asynchronous enrichment run; a worker executes it and the job
 *  record carries live counters while it runs (admin). */
export function createEnrichJob(source: string, filters: string[] = [], hosts: string[] = []): Promise<EnrichJob> {
  const q = new URLSearchParams();
  for (const f of filters) q.append("filter", f);
  if (hosts.length > 0) q.set("hosts", hosts.join(","));
  const qs = q.toString();
  return call("POST", `/v1/enrich/${encodeURIComponent(source)}/jobs${qs ? `?${qs}` : ""}`);
}

/** Recent enrichment jobs, newest first (admin). */
export function fetchEnrichJobs(): Promise<{ jobs: EnrichJob[] }> {
  return call("GET", "/v1/enrich/jobs");
}

/** The subject-term histogram the facet builder picks from (librarian). */
export function fetchAuditTerms(filters: string[] = [], limit?: number): Promise<AuditTermsPage> {
  const q = new URLSearchParams();
  for (const f of filters) q.append("filter", f);
  if (limit) q.set("limit", String(limit));
  const qs = q.toString();
  return call("GET", `/v1/audit/terms${qs ? `?${qs}` : ""}`);
}

/** The suggestion review queue, optionally filtered (moderator). */
export function fetchQueue(
  params: {
    status?: string;
    scheme?: string;
    provenance?: string;
    type?: string;
    cursor?: string;
    limit?: number;
    /** Explicit floor override; 0 shows everything below the deployment default. */
    minConfidence?: number;
  } = {},
): Promise<QueuePage> {
  const q = new URLSearchParams({ status: params.status ?? "PENDING" });
  if (params.scheme) q.set("scheme", params.scheme);
  if (params.provenance) q.set("provenance", params.provenance);
  if (params.type) q.set("type", params.type);
  if (params.cursor) q.set("cursor", params.cursor);
  if (params.limit) q.set("limit", String(params.limit));
  if (params.minConfidence !== undefined) q.set("minConfidence", String(params.minConfidence));
  return call("GET", `/v1/queue?${q}`);
}

/** Ships a staged decision batch; publish=true also runs the publisher
 *  in the same call (librarian). */
export function postReview(decisions: Decision[], publish: boolean): Promise<ReviewResponse> {
  return call("POST", "/v1/review", { decisions, publish });
}

/** Publishes approved-but-unpublished suggestions (librarian). */
export function postPublish(): Promise<PublishResponse> {
  return call("POST", "/v1/publish");
}

/** The scope of a bulk approve-all: the same PENDING-queue filters the review
 *  screen offers, minus status (always PENDING). */
export interface ApproveAllScope {
  scheme?: string;
  provenance?: string;
  type?: string;
  minConfidence?: number;
}

/** The dry-run answer to approve-all: how many pending rows the filter would
 *  approve. confirmRequired marks that a second call carrying `confirm=count`
 *  is needed to actually run it; message explains a zero count. */
export interface ApproveAllPreview {
  count: number;
  confirmRequired?: boolean;
  message?: string;
}

/** approveAllScope renders a scope into query params (status is forced PENDING
 *  server-side, so it is never sent). */
function approveAllScope(scope: ApproveAllScope): URLSearchParams {
  const q = new URLSearchParams();
  if (scope.scheme) q.set("scheme", scope.scheme);
  if (scope.provenance) q.set("provenance", scope.provenance);
  if (scope.type) q.set("type", scope.type);
  if (scope.minConfidence !== undefined) q.set("minConfidence", String(scope.minConfidence));
  return q;
}

/** Dry-runs approve-all: returns the count of pending rows the filter matches
 *  without approving anything, so the librarian confirms against a real number
 *  (librarian). */
export function previewApproveAll(scope: ApproveAllScope = {}): Promise<ApproveAllPreview> {
  const qs = approveAllScope(scope).toString();
  return call("POST", `/v1/queue/approve-all${qs ? `?${qs}` : ""}`);
}

/** Runs approve-all after a dry-run: `confirm` must equal the count the preview
 *  returned or the server refuses (409) because the queue moved. Returns the
 *  async QUEUE_APPROVE job that carries live progress; the run stays reversible
 *  (nothing is published) (librarian). */
export function confirmApproveAll(scope: ApproveAllScope, confirm: number): Promise<QueueApproveJob> {
  const q = approveAllScope(scope);
  q.set("confirm", String(confirm));
  return call("POST", `/v1/queue/approve-all?${q}`);
}

/** Controlled-vocabulary autocomplete (full terms with relations). */
export function searchTerms(scheme: string, q: string): Promise<{ terms: Term[] }> {
  const params = new URLSearchParams({ scheme, q });
  return call("GET", `/v1/terms?${params}`);
}

/** Accepted community tags matching q (scheme=folk serves flat TermRefs). */
export function searchFolkTerms(q: string): Promise<{ terms: TermRef[] }> {
  const params = new URLSearchParams({ scheme: "folk", q });
  return call("GET", `/v1/terms?${params}`);
}

/** One vocabulary term by URI (uncached; see resolveTerm). */
export function fetchTerm(scheme: string, id: string): Promise<Term> {
  const params = new URLSearchParams({ scheme, id });
  return call("GET", `/v1/term?${params}`);
}

const termCache = new Map<string, Promise<Term>>();

/** fetchTerm through a module-lifetime cache -- neighborhood panels resolve
 *  the same broader/narrower/related URIs repeatedly. Failures stay uncached
 *  so a transient error does not poison the entry. */
export function resolveTerm(scheme: string, id: string): Promise<Term> {
  // "|" cannot occur in a scheme or in an IRI, so it separates the two without
  // a control byte in the source: a raw NUL here made the whole file binary to
  // grep and git diff, and a silent grep miss reads as "the symbol is not
  // there" rather than "I did not look".
  const key = `${scheme}|${id}`;
  let p = termCache.get(key);
  if (!p) {
    p = fetchTerm(scheme, id).catch((e: unknown) => {
      termCache.delete(key);
      throw e;
    });
    termCache.set(key, p);
  }
  return p;
}

/** Test seam: drops the resolveTerm cache. */
export function clearTermCache(): void {
  termCache.clear();
}

/** Distinct grain-tree tags with carry counts, the typeahead nudge (staff). */
export function fetchTags(q: string): Promise<{ tags: TagCount[] }> {
  const params = new URLSearchParams({ q });
  return call("GET", `/v1/tags?${params}`);
}

/** Folk-term governance: accept joins the autocomplete, block refuses future
 *  suggestions of the tag (librarian). */
export function setFolkTermStatus(action: "acceptFolk" | "blockFolk", folkTerm: string): Promise<void> {
  return call("POST", "/v1/terms", { action, folkTerm });
}

/** A work's visibility stance (librarian). */
export function fetchVisibility(workId: string): Promise<WorkVisibility> {
  return call("GET", `/v1/works/${encodeURIComponent(workId)}/visibility`);
}

/** Tombstones (optionally with a redirect target), restores, suppresses, or
 *  unsuppresses a work (librarian). */
export function setVisibility(
  workId: string,
  action: "tombstone" | "untombstone" | "suppress" | "unsuppress",
  redirectTo?: string,
): Promise<WorkVisibility> {
  return call("POST", `/v1/works/${encodeURIComponent(workId)}/visibility`, { action, redirectTo });
}

/** A work's holdings by instance (librarian). */
export function fetchItems(workId: string): Promise<{ etag: string; items: Record<string, WorkItem[]> }> {
  return call("GET", `/v1/works/${encodeURIComponent(workId)}/items`);
}

/** Replaces one instance's holdings (librarian).
 *
 *  This is a read-modify-write: the panel loads a list, a human edits it, and
 *  the whole list is written back. `ifMatch` is the etag `fetchItems` returned,
 *  and it is required -- without it the save would delete any copy another
 * cataloger added while the panel was open, and report success.
 *  A concurrent write surfaces as ConflictError carrying the fresh state. */
export function putItems(
  workId: string,
  instanceId: string,
  items: WorkItem[],
  ifMatch: string,
): Promise<{ etag: string }> {
  return call("PUT", `/v1/works/${encodeURIComponent(workId)}/items`, { instanceId, items }, { "If-Match": ifMatch });
}

/** The duplicate-detection worklist: same-clustering-key work groups
 *  (librarian). */
export function fetchDuplicates(): Promise<{ groups: DuplicateGroup[] }> {
  return call("GET", "/v1/duplicates");
}

/** Merges one work into a survivor -- the same endpoint and semantics as the
 *  CLI merge markers (librarian). */
export function mergeWorks(from: string, to: string): Promise<{ from: string; to: string; etag: string }> {
  return call("POST", "/v1/works/merge", { From: from, To: to });
}

/** Splits instances off a work into a freshly minted one (librarian). */
export function splitWork(from: string, instances: string[]): Promise<{ newWork: string; etag: string }> {
  return call("POST", "/v1/works/split", { from, instances });
}

/** Clones a work into a fresh suppressed editorial-only draft (librarian). */
export function cloneWork(from: string): Promise<{ workId: string; from: string; etag: string }> {
  return call("POST", `/v1/works/${encodeURIComponent(from)}/clone`);
}

/** Configured external search targets (librarian). */
export function fetchCopycatTargets(): Promise<{ targets: CopycatTarget[] }> {
  return call("GET", "/v1/copycat/targets");
}

/** The curated open presets, served from the same Go table the seeder uses so
 * the UI cannot keep a copy that drifts. Blurbs are added UI-side. */
export function fetchSuggestedTargets(): Promise<{ targets: CopycatTarget[] }> {
  return call("GET", "/v1/copycat/targets/suggested");
}

/** Creates or replaces a search target (admin). */
export function putCopycatTarget(t: CopycatTarget): Promise<CopycatTarget> {
  return call("POST", "/v1/copycat/targets", t);
}

/** Removes a search target (admin). */
export function deleteCopycatTarget(name: string): Promise<void> {
  return call("DELETE", `/v1/copycat/targets/${encodeURIComponent(name)}`);
}

/** Fans a query out to the external targets (librarian). Fielded terms AND
 * onto the free-text query; per-target failures come back in
 *  `failures` rather than failing the search.
 *
 *  `warnings` names the targets that answered incompletely -- a stream that
 *  broke partway, or one the search limit cut short. Their hits ARE in
 *  `results`, so a warning must never suppress them; it tells the cataloger
 *  that "not in this result set" does not mean "not in this catalog"
 *. */
export function copycatSearch(query: string, fields?: CopycatFieldTerm[], targets?: string[]): Promise<{
  results: CopycatSearchResult[];
  failures: Record<string, string>;
  warnings: Record<string, string>;
}> {
  return call("POST", "/v1/copycat/search", {
    query,
    ...(fields?.length ? { fields } : {}),
    ...(targets?.length ? { targets } : {}),
  });
}

/** Stages records (from search results, or a base64 .mrc upload) into a
 *  reviewable batch with per-record match banners (librarian). An optional
 *  policy (a staging profile's choice) pre-sets the overlay policy. */
export function stageCopycatBatch(req: {
  label?: string;
  source?: string;
  records?: unknown[];
  mrc?: string;
  policy?: CopycatPolicy;
}): Promise<{ batch: CopycatBatch; records: CopycatStagedRecord[] }> {
  return call("POST", "/v1/copycat/batches", req);
}

/** The blank-record MARC skeletons for original cataloging (librarian). */
export function fetchCopycatTemplates(): Promise<{ templates: CopycatTemplate[] }> {
  return call("GET", "/v1/copycat/templates");
}

/** Stages one editor-born record as a source "original" batch (librarian).
 *  A record failing minimum viability rejects with FieldedApiError. */
export function stageOriginalRecord(
  label: string,
  record: MarcRecordDoc,
): Promise<{ batch: CopycatBatch; records: CopycatStagedRecord[] }> {
  return call("POST", "/v1/copycat/original", { label, record });
}

/** Every staged import, newest first (librarian). */
export function fetchCopycatBatches(): Promise<{ batches: CopycatBatch[] }> {
  return call("GET", "/v1/copycat/batches");
}

/** One batch with its reviewable records (librarian). */
export function fetchCopycatBatch(id: string): Promise<{ batch: CopycatBatch; records: CopycatStagedRecord[] }> {
  return call("GET", `/v1/copycat/batches/${encodeURIComponent(id)}`);
}

/** Updates a batch's overlay policy and per-record decisions (librarian). */
export function reviewCopycatBatch(
  id: string,
  req: { policy?: CopycatPolicy; decisions?: Record<string, "import" | "skip"> },
): Promise<CopycatBatch> {
  return call("POST", `/v1/copycat/batches/${encodeURIComponent(id)}/review`, req);
}

/** Commits a batch through the shared ingest pipeline (librarian). */
export function commitCopycatBatch(id: string): Promise<CopycatBatch> {
  return call("POST", `/v1/copycat/batches/${encodeURIComponent(id)}/commit`);
}

/** Deletes a staged batch (librarian). */
export function deleteCopycatBatch(id: string): Promise<void> {
  return call("DELETE", `/v1/copycat/batches/${encodeURIComponent(id)}`);
}

/** Rolls a committed batch back grain by grain; post-commit editorial edits
 * survive as reported skips (librarian). */
export function revertCopycatBatch(id: string): Promise<CopycatRevertResult> {
  return call("POST", `/v1/copycat/batches/${encodeURIComponent(id)}/revert`);
}

/** The saved staging profiles (librarian). */
export function fetchCopycatProfiles(): Promise<{ profiles: CopycatProfile[] }> {
  return call("GET", "/v1/copycat/profiles");
}

/** Creates or replaces a staging profile (librarian). */
export function putCopycatProfile(p: CopycatProfile): Promise<CopycatProfile> {
  return call("POST", "/v1/copycat/profiles", p);
}

/** Removes a staging profile (librarian). */
export function deleteCopycatProfile(name: string): Promise<void> {
  return call("DELETE", `/v1/copycat/profiles/${encodeURIComponent(name)}`);
}

/** The caller's export jobs, newest first (librarian). */
export function fetchExports(): Promise<{ exports: ExportJob[] }> {
  return call("GET", "/v1/exports");
}

/** Creates an export job: the batch selection compiles to the exact id list
 *  at create time; small selections finish synchronously (librarian). */
export function createExport(format: ExportFormat, batchSelection: Selection): Promise<ExportJob> {
  return call("POST", "/v1/exports", { format, batchSelection });
}

/** Selection preview: how many works a batch selection matches (librarian). */
export function resolveBatch(selection: Selection): Promise<{ matched: number; works: BatchTarget[] }> {
  return call("POST", "/v1/batch/resolve", { selection });
}

/** Runs an op list (or a macro with params) over a selection; dryRun
 *  returns exact per-work quad deltas without writing (librarian). */
export function runBatch(req: {
  selection: Selection;
  ops?: Op[];
  macroId?: string;
  params?: Record<string, string>;
  dryRun?: boolean;
}): Promise<BatchRunResult> {
  return call("POST", "/v1/batch/ops", req);
}

/** The live editing profiles, for the batch op builder (librarian). Each
 *  carries an `overridden` flag for the profile admin surface. */
export function fetchProfiles(): Promise<{ profiles: Record<string, ProfileSummary> }> {
  return call("GET", "/v1/profiles");
}

/** One editing profile with its override etag ("" for a shipped default). */
export function fetchProfile(id: string): Promise<{ profile: Profile; etag: string; isDefault: boolean }> {
  return call("GET", `/v1/profiles/${encodeURIComponent(id)}`);
}

/** Saves a profile override (admin); a blank etag creates the first override,
 *  a non-blank one guards against a concurrent edit. */
export function putProfile(id: string, profile: unknown, etag: string): Promise<{ id: string; etag: string }> {
  return call("PUT", `/v1/profiles/${encodeURIComponent(id)}`, profile, etag ? { "If-Match": etag } : undefined);
}

/** Reverts a profile to its shipped default (admin). */
export function deleteProfileOverride(id: string): Promise<void> {
  return call("DELETE", `/v1/profiles/${encodeURIComponent(id)}`);
}

/** The caller's macros plus every shared macro (librarian). */
export function fetchMacros(): Promise<{ macros: Macro[] }> {
  return call("GET", "/v1/macros");
}

/** Records a new macro (librarian). */
export function createMacro(m: {
  label: string;
  keys?: string;
  shared?: boolean;
  ops: Op[];
  params?: { name: string; label?: string; default?: string }[];
}): Promise<Macro> {
  return call("POST", "/v1/macros", m);
}

/** Replaces an owned macro's definition (librarian). */
export function updateMacro(id: string, m: Partial<Macro>): Promise<Macro> {
  return call("PUT", `/v1/macros/${encodeURIComponent(id)}`, m);
}

/** Deletes an owned macro (librarian). */
export function deleteMacro(id: string): Promise<void> {
  return call("DELETE", `/v1/macros/${encodeURIComponent(id)}`);
}

/** The caller's saved queries (librarian). */
export function fetchSavedQueries(): Promise<{ queries: SavedQuery[] }> {
  return call("GET", "/v1/queries");
}

/** Names a works search for reuse in selections (librarian). */
export function createSavedQuery(label: string, query: string): Promise<SavedQuery> {
  return call("POST", "/v1/queries", { label, query });
}

/** Deletes one of the caller's saved queries (librarian). */
export function deleteSavedQuery(id: string): Promise<void> {
  return call("DELETE", `/v1/queries/${encodeURIComponent(id)}`);
}

/** Local-authority listing (q="") or label search (librarian). `total` is the
    true count of local headings (or matches), so the screen can tell a full
    page from a capped one. */
export function fetchAuthorities(q: string, limit = 50): Promise<{ terms: Term[]; total?: number }> {
  const params = new URLSearchParams({ q, limit: String(limit) });
  return call("GET", `/v1/authorities?${params}`);
}

/** One local authority term with its concurrency token (librarian). */
export function fetchAuthority(id: string): Promise<AuthorityView> {
  return call("GET", `/v1/authorities/${encodeURIComponent(id)}`);
}

/** The authority editing profile the form renders from (librarian). */
export function fetchAuthorityProfile(): Promise<Profile> {
  return call("GET", "/v1/authorities/profile");
}

/** Mints a new local authority term (librarian). */
export function createAuthority(term: AuthorityTerm): Promise<{ id: string; uri: string; etag: string }> {
  return call("POST", "/v1/authorities", term);
}

/** Replaces a term's description under its If-Match token (librarian).
 *  A concurrent write surfaces as ConflictError; re-fetch to rebase. */
export function updateAuthority(id: string, term: AuthorityTerm, ifMatch: string): Promise<{ id: string; etag: string }> {
  return call("PUT", `/v1/authorities/${encodeURIComponent(id)}`, term, { "If-Match": ifMatch });
}

/** Merges local term loser (id) into winner: retires the loser and rewrites
 *  every referencing work (librarian). */
export function mergeAuthority(loser: string, winner: TermRef): Promise<AuthorityMergeResult> {
  return call("POST", "/v1/authorities/merge", { loser, winner });
}

/** Reverses a recorded merge: replays the merge's manifest backwards, revives
 *  the term, and reports what it restored vs skipped (librarian). */
export function unmergeAuthority(id: string): Promise<AuthorityUnmergeResult> {
  return call("POST", `/v1/authorities/${encodeURIComponent(id)}/unmerge`);
}

/** Tag promotions, optionally filtered by status (moderator). */
export function fetchPromotions(status?: string): Promise<{ promotions: Promotion[] }> {
  return call("GET", status ? `/v1/promotions?${new URLSearchParams({ status })}` : "/v1/promotions");
}

/** Proposes folding a folk tag into a controlled term (moderator). A 409
 *  ApiError means the tag already has an open proposal. */
export function proposePromotion(tag: string, term: { scheme: string; id: string }): Promise<Promotion> {
  return call("POST", "/v1/promotions", { tag, term });
}

/** Decides a pending promotion; approval runs the batch rewrite first and only
 *  stamps APPROVED when it succeeds, so a 500 here leaves the promotion PENDING
 * and retryable. Reports the touched work count (librarian). */
export function decidePromotion(tag: string, approve: boolean): Promise<DecidePromotionResponse> {
  return call("POST", "/v1/promotions/decide", { tag, approve });
}

/** Removes a promotion record outright, freeing the tag to be proposed again
 *  (librarian). The escape hatch for a record the one-way state machine cannot
 * leave -- notably an approval made with no publisher wired. */
export function deletePromotion(tag: string): Promise<void> {
  return call("DELETE", `/v1/promotions/${encodeURIComponent(tag)}`);
}

/** The authority-source list: registry, install state, latest jobs (librarian). */
export function fetchVocabSources(): Promise<{ sources: VocabSourceView[] }> {
  return call("GET", "/v1/vocabsources");
}

/** The patron-suggestion policy (admin). */
export function fetchSuggestionPolicy(): Promise<SuggestionPolicy> {
  return call("GET", "/v1/config/suggestions");
}

/** Stores the patron-suggestion policy; the write is audited (admin).
 *  Returns the normalized policy the server saved. */
export function putSuggestionPolicy(policy: SuggestionPolicy): Promise<SuggestionPolicy> {
  return call("PUT", "/v1/config/suggestions", policy);
}

/** Queues a vocabulary snapshot download; the worker installs and swaps the
 *  index (admin). Downloading an installed source refreshes it in place. */
export function downloadVocabSource(name: string): Promise<VocabJob> {
  return call("POST", `/v1/vocabsources/${encodeURIComponent(name)}/download`);
}

/** Removes an installed snapshot; its terms leave the index (admin). */
export function removeVocabSnapshot(name: string): Promise<{ removed: boolean }> {
  return call("DELETE", `/v1/vocabsources/${encodeURIComponent(name)}/snapshot`);
}

/** Registers (or overrides) a drop-in authority source (admin). */
export function putVocabSource(src: VocabSource): Promise<VocabSource> {
  return call("POST", "/v1/vocabsources", src);
}

/** Installs a hand-supplied SKOS dump (N-Triples/N-Quads, optionally
 *  gzipped) for a registered source -- the escape hatch when the
 *  publisher's download URL is unreachable (admin). Synchronous. */
export async function uploadVocabSnapshot(name: string, dump: Blob): Promise<{ installed: boolean; terms: number }> {
  for (let attempt = 0; attempt < 2; attempt++) {
    const token = await getToken();
    if (!token) throw new ApiError(401, "not signed in");
    const res = await fetch(apiBase() + `/v1/vocabsources/${encodeURIComponent(name)}/snapshot`, {
      method: "PUT",
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/octet-stream" },
      body: dump,
    });
    if (res.status === 401 && attempt === 0) {
      invalidateAccess();
      continue;
    }
    if (!res.ok) {
      const detail = (await res.json().catch(() => ({}))) as { error?: string };
      throw new ApiError(res.status, detail.error || res.statusText);
    }
    return (await res.json()) as { installed: boolean; terms: number };
  }
  throw new ApiError(401, "authentication failed");
}

/** Deletes a registered source; a same-named builtin's shipped definition
 *  returns (admin). */
export function deleteVocabSource(name: string): Promise<{ deleted: boolean }> {
  return call("DELETE", `/v1/vocabsources/${encodeURIComponent(name)}`);
}

/** Live typeahead against a registered suggest source, proxied server-side
 *  (librarian). */
export function vocabSuggest(source: string, q: string, limit = 10): Promise<{ suggestions: VocabSuggestion[] }> {
  const params = new URLSearchParams({ source, q, limit: String(limit) });
  return call("GET", `/v1/vocabsuggest?${params}`);
}

/** The caller's item templates plus every shared one (librarian). */
export function fetchItemTemplates(): Promise<{ templates: ItemTemplate[] }> {
  return call("GET", "/v1/item-templates");
}

/** Saves an item template; shared templates are library-wide (librarian). */
export function createItemTemplate(t: ItemTemplate): Promise<ItemTemplate> {
  return call("POST", "/v1/item-templates", t);
}

/** Updates an owned item template -- rename or re-field (librarian). */
export function updateItemTemplate(id: string, t: ItemTemplate): Promise<ItemTemplate> {
  return call("PUT", `/v1/item-templates/${encodeURIComponent(id)}`, t);
}

/** Removes an owned item template (librarian). */
export function deleteItemTemplate(id: string): Promise<void> {
  return call("DELETE", `/v1/item-templates/${encodeURIComponent(id)}`);
}

/** Bulk item creation: N copies with auto-incrementing, collision-checked
 * barcodes; dryRun previews the generated list (librarian). */
export function bulkAddItems(
  workId: string,
  req: {
    instanceId: string;
    count: number;
    callNumber?: string;
    location?: string;
    note?: string;
    barcodePrefix: string;
    barcodeWidth?: number;
    dryRun?: boolean;
  },
): Promise<{ workId: string; etag?: string; items: WorkItem[] }> {
  return call("POST", `/v1/works/${encodeURIComponent(workId)}/items/bulk`, req);
}

/** Creates an authority export job: all / by-vocabulary / label-filtered
 * terms as MARC authority, SKOS N-Quads, or JSON-LD (librarian). */
export function createAuthorityExport(format: ExportFormat, authorities: AuthoritySelection): Promise<ExportJob> {
  return call("POST", "/v1/exports", { format, authorities });
}

/** Live MARC preview: the staged ops applied to the current doc server-side
 *  and re-encoded as MARC -- nothing written. Empty ops previews the saved
 * state (librarian). */
export function marcPreview(workId: string, ops: Op[]): Promise<MarcResponse> {
  return call("POST", `/v1/works/${encodeURIComponent(workId)}/marc/preview`, { ops });
}

/** Uploads a work's cover image (raw body, typed); returns the cover URL
 * the editorial extra now points at. */
export async function putCover(workId: string, file: File): Promise<{ workId: string; cover: string; etag: string }> {
  return callRaw("PUT", `/v1/works/${encodeURIComponent(workId)}/cover`, file, file.type || "image/jpeg");
}

/** Removes a work's editorial cover and its stored bytes. */
export function deleteCover(workId: string): Promise<void> {
  return call("DELETE", `/v1/works/${encodeURIComponent(workId)}/cover`);
}

/** One zip entry's fate in a batch cover upload.
 *
 * `skipped` and `failed` are different outcomes. A skipped entry
 *  was rejected before anything was written -- a bad name, a wrong format. A
 *  failed entry asked the stores to do the work and they did not, so it is
 *  worth retrying. `changed` marks the entry whose cover statement was written
 *  and could not be undone: that record claims an image whose bytes are
 *  missing, and only a person can repair it. */
export interface CoverBatchResult {
  file: string;
  workId?: string;
  cover?: string;
  skipped?: string;
  failed?: string;
  changed?: boolean;
}

/** The batch's own account of the run: the three counts are disjoint. */
export interface CoverBatchResponse {
  applied: number;
  skipped: number;
  failed: number;
  results: CoverBatchResult[];
}

/** Uploads a zip of covers named <workId>.<ext> or <isbn>.<ext>; returns
 * per-entry results (librarian). A partial failure answers 207,
 *  which `callRaw` treats as success -- the per-entry detail is the point. */
export async function postCoverBatch(file: File): Promise<CoverBatchResponse> {
  return callRaw("POST", "/v1/covers/batch", file, "application/zip");
}

/** A work's staff attachments (librarian). */
export function fetchAttachments(workId: string): Promise<{ attachments: string[] }> {
  return call("GET", `/v1/works/${encodeURIComponent(workId)}/attachments`);
}

/** Uploads one staff attachment (raw body; the name rides the query). */
export async function putAttachment(workId: string, file: File, name = file.name): Promise<{ workId: string; name: string; etag: string }> {
  return callRaw(
    "POST",
    `/v1/works/${encodeURIComponent(workId)}/attachments?name=${encodeURIComponent(name)}`,
    file,
    file.type || "application/octet-stream",
  );
}

/** Reports why a filename cannot be attached, or "" when it can.
 *
 *  Attachments keep the name the cataloger's file already has, in whatever
 *  script it is written in; the server derives a safe blob path from the name
 *  rather than forcing the name to be a safe path. So this rejects only what
 *  no encoding can rescue -- emptiness, path separators, control characters,
 *  reserved names, and the server's 100-byte cap -- and never rewrites the
 *  name. A silent rename is how two documents came to share one file. */
export function attachmentNameError(name: string): string {
  if (!name) return "that file has no name";
  if (name === "." || name === "..") return "that filename is reserved";
  if (/[/\\]/.test(name)) return "a filename cannot contain a slash";
  for (const ch of name) {
    const cp = ch.codePointAt(0) ?? 0;
    if (cp < 0x20 || cp === 0x7f) return "that filename contains control characters";
  }
  if (new TextEncoder().encode(name).length > 100) return "that filename is too long (100 bytes maximum)";
  return "";
}

/** Removes one staff attachment: statement and bytes (librarian). */
export function deleteAttachment(workId: string, name: string): Promise<void> {
  return call("DELETE", `/v1/works/${encodeURIComponent(workId)}/attachments/${encodeURIComponent(name)}`);
}

/** Fetches an attachment's bytes with the bearer attached (an <a href>
 *  cannot carry the Authorization header); caller object-URLs it. */
export async function fetchAttachmentBlob(workId: string, name: string): Promise<Blob> {
  const token = await getToken();
  if (!token) throw new ApiError(401, "not signed in");
  const res = await fetch(apiBase() + `/v1/works/${encodeURIComponent(workId)}/attachments/${encodeURIComponent(name)}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!res.ok) throw new ApiError(res.status, "download failed");
  return res.blob();
}

/** One linked work in a relations listing. */
export interface RelationEntry {
  workId: string;
  title?: string;
}

/** A work's editorial hasPart/partOf links with titles (librarian). */
export function fetchRelations(workId: string): Promise<{ hasPart: RelationEntry[]; partOf: RelationEntry[] }> {
  return call("GET", `/v1/works/${encodeURIComponent(workId)}/relations`);
}

/** Links two works; the inverse is written on the target (librarian). */
export function addRelation(workId: string, kind: "hasPart" | "partOf", target: string): Promise<void> {
  return call("POST", `/v1/works/${encodeURIComponent(workId)}/relations`, { kind, target });
}

/** Unlinks two works, retracting both directions (librarian). */
export function removeRelation(workId: string, kind: "hasPart" | "partOf", target: string): Promise<void> {
  return call("DELETE", `/v1/works/${encodeURIComponent(workId)}/relations`, { kind, target });
}

/** One computed neighbour of a work. `shared` names the attribute
 *  values that put it on the list -- the answer to "why is this here?". */
export interface SimilarNeighbor {
  workId: string;
  title: string;
  /** extra.cover passthrough: relative editorial blob path or absolute CDN URL. */
  cover?: string;
  score: number;
  shared?: string[];
}

/** A work's computed neighbours, scored live over the admin corpus (librarian).
 *  The OPAC precomputes the same rail at build time, so this is what a reader
 *  will see after the next publish. */
export function fetchSimilar(workId: string, limit?: number): Promise<{ similar: SimilarNeighbor[] }> {
  const q = limit ? `?limit=${limit}` : "";
  return call("GET", `/v1/works/${encodeURIComponent(workId)}/similar${q}`);
}

/** Batch scheme-agnostic term resolve: stored subject URIs to full terms;
 * unresolvable URIs are absent from the map. */
export function resolveTermURIs(ids: string[]): Promise<{ terms: Record<string, Term> }> {
  const params = new URLSearchParams();
  for (const id of ids) params.append("id", id);
  return call("GET", `/v1/terms/resolve?${params}`);
}

/** Caches a live pick's label and exactMatch links into the local index so
 * the subject resolves forever (librarian). */
export function cacheVocabTerm(sugg: VocabSuggestion): Promise<{ cached: boolean }> {
  return call("POST", "/v1/vocabcache", sugg);
}

/** Searches the copycat targets by the work's ISBNs and returns their 6XX
 *  headings, deduped and reconciled against the local index (librarian,
 *). Seconds-slow: target fan-out.
 *
 *  `warnings` names targets whose answer was incomplete; their candidates are
 * included, but the heading list may be short. */
export function lookupSubjects(
  workId: string,
  targets?: string[],
): Promise<{ candidates: SubjectCandidate[]; failures: Record<string, string>; warnings: Record<string, string> }> {
  return call("POST", `/v1/works/${encodeURIComponent(workId)}/subjects/lookup`, targets?.length ? { targets } : {});
}

/** Each identifier value mapped to its BIBFRAME kind (isbn/issn/id) so the
 * editor can badge the Identifiers field (librarian). */
export function fetchIdentifierKinds(workId: string): Promise<{ workId: string; kinds: Record<string, string> }> {
  return call("GET", `/v1/works/${encodeURIComponent(workId)}/identifiers`);
}
