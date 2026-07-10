// Mirrors the backend's JSON shapes (the API serialises Go structs). Field
// names match the Go json tags exactly -- the works listing endpoint has no
// tags, so its fields arrive with capitalized Go names.

/** GET /config -- the deployment facts the SPA boots from. */
export interface ClientConfig {
  apiBase: string; // "" = same origin
  localAuth: boolean;
  oidc?: { issuer: string; clientId: string };
  schemes?: string[];
  provider: string;
  /** Demo mode: the backend rejects edits, so the UI hides write affordances
   *  and shows a banner. */
  readOnly?: boolean;
  /** Sandbox demo: read-only, but the editor shows Save and renders edits as
   *  if committed (wiped on refresh). */
  sandbox?: boolean;
  /** Extras keys the works view facets on (LCATD_EXTRA_FACETS, tasks/171);
   *  each is a facet-rail group and its own query parameter. */
  extraFacets?: string[];
}

/** One row from GET /v1/works (ingest.WorkSummary, untagged Go fields). */
export interface WorkSummary {
  WorkID: string;
  Title: string;
  Contributors: string[];
  ISBNs: string[];
  Tags: string[];
  /** Visibility + holdings signals (tasks/078): the editor list shows
   *  everything, so rows badge what public projection would do. */
  Suppressed?: boolean;
  Tombstoned?: boolean;
  Withdrawn?: string; // date the feed reconciliation flagged it
  Kept?: boolean;
  Items?: number;
  HasAvailability?: boolean;
}

/** One facet value with its work count (tasks/168). Subject values carry
 * the vocabulary scheme the IRI resolves to (tasks/174). */
export interface FacetCount {
  value: string;
  count: number;
  scheme?: string;
}

export interface WorksPage {
  works: WorkSummary[];
  total: number;
  /** Query hits across the whole catalog (works is one window of these). */
  matched?: number;
  offset?: number;
  /** Self-excluding facet counts by group: visibility, holdings, needs,
   *  subject, tag (tasks/168). */
  facets?: Record<string, FacetCount[]>;
}

/** editor.FieldValue -- one value of a profile field, with provenance. */
export interface FieldValue {
  v: string;
  lang?: string;
  datatype?: string;
  iri?: boolean;
  prov: string;
  overridden?: boolean;
  /** Display-only qualifier from the value's structure node (e.g. a
   *  heading's bf:source label -- MARC's $2). */
  annotation?: string;
  node: string;
}

/** editor.ResourceDoc -- a work or instance with its profile fields. */
export interface ResourceDoc {
  id: string;
  fields: Record<string, FieldValue[]>;
}

/** editor.WorkDoc -- GET /v1/works/{id}/doc payload body. */
export interface WorkDoc {
  workId: string;
  profileId: string;
  work: ResourceDoc;
  instances: ResourceDoc[];
  passthrough: string[];
}

export interface WorkDocResponse {
  etag: string;
  doc: WorkDoc;
  /** The work's cover URL, or "". Not a profile field, so it is not in
   *  doc.work.fields -- the Cover panel reads it here (tasks/242). */
  cover?: string;
}

/** editor.OpValue -- one value in a field operation. */
export interface OpValue {
  v: string;
  lang?: string;
  iri?: boolean;
}

export type OpAction = "add" | "remove" | "set" | "clear";

/** editor.ResourceItems -- addresses every bf:Item in the grain at once. */
export const RESOURCE_ITEMS = "items";

/** editor.Op -- one field-level edit in a POST /v1/works/{id}/ops batch. */
export interface Op {
  resource: string; // "work", an instance id, or RESOURCE_ITEMS
  path: string;
  action: OpAction;
  value?: OpValue; // add / remove
  values?: OpValue[]; // set
  /** Items only: edit just the items whose current value is exactly this. */
  where?: string;
}

/** editor.Diff -- the exact N-Quads delta a save makes (dry-run preview). */
export interface Diff {
  added: string[];
  removed: string[];
}

/** A saved doc's identity collision with another work (tasks/068). */
export interface DuplicateMatch {
  workId: string;
  via: "identifier" | "title-author";
}

/** POST /v1/works/{id}/ops response (workId absent on dry runs). */
export interface OpsResult {
  workId?: string;
  etag: string;
  diff: Diff;
  /** Non-blocking warning: the doc now clusters with an existing work. */
  duplicate?: DuplicateMatch;
  /** Dry-run only: the materialized post-edit doc, so a demo "save" can render
   *  the result without persisting. */
  doc?: WorkDoc;
}

/** The 412 body: the fresh record state for a deliberate client rebase. */
export interface GrainConflict {
  workId: string;
  etag: string;
  nquads: string;
}

/** The SPA's draft payload: staged ops against the etag they were built on.
 *  The server stores it opaquely. */
export interface DraftBody {
  baseEtag: string;
  ops: Op[];
}

/** httpapi.draft -- one per-user editor draft. */
export interface Draft {
  id: string;
  workId?: string;
  body: DraftBody;
  updatedAt: string;
}

/** suggest.AuditEntry -- one staff action in the GET /v1/audit trail. */
export interface AuditEntry {
  workId?: string;
  at: string;
  action: string;
  actor: string;
  terms?: string[];
  note?: string;
  etag?: string;
  /** Ties a bulk run's per-record entries to its aggregate entry (tasks/239). */
  runId?: string;
}

export interface AuditPage {
  month: string;
  entries: AuditEntry[];
}

/** suggest.Session -- one contiguous editing sitting in GET /v1/stats. */
export interface Session {
  start: string;
  end: string;
  actions: number;
  works: number;
}

/** suggest.ActorStats -- one cataloger's monthly rollup. */
export interface ActorStats {
  actor: string;
  total: number;
  byAction: Record<string, number>;
  works: number;
  activeDays: number;
  first: string;
  last: string;
  sessions: Session[];
}

/** suggest.MonthStats -- the GET /v1/stats editing-activity rollup. */
export interface MonthStats {
  month: string;
  total: number;
  actors: number;
  works: number;
  byAction: Record<string, number>;
  perActor: ActorStats[];
}

/** vocab.TermRef -- a controlled term reference. */
export interface TermRef {
  scheme: string;
  id: string;
  label: string;
}

/** vocab.Term -- a full vocabulary entry (/v1/terms search, /v1/term lookup). */
export interface Term {
  scheme: string;
  id: string; // the authority URI
  labels: Record<string, string>; // lang -> prefLabel ("" key = untagged)
  altLabels?: Record<string, string[]>;
  definition?: Record<string, string>;
  broader?: string[];
  narrower?: string[];
  related?: string[];
  exactMatch?: string[];
  closeMatch?: string[];
  mergedInto?: string; // set = retired: merged into the referenced term
  /** Ancestor chain root → … → parent (search hits only, tasks/079). */
  path?: TermRef[];
}

/** bibframe.AuthorityTerm -- one local authority description (tasks/046). */
export interface AuthorityTerm {
  uri?: string; // server-assigned, read-only
  prefLabel: Record<string, string>; // lang -> label ("" = untagged)
  altLabel?: Record<string, string[]>;
  definition?: Record<string, string>;
  broader?: string[];
  narrower?: string[];
  related?: string[];
  exactMatch?: string[];
  mergedInto?: string;
}

/** GET/PUT /v1/authorities/{id} read shape. */
export interface AuthorityView {
  id: string;
  etag: string;
  term: AuthorityTerm;
}

/** POST /v1/authorities/merge response. */
export interface AuthorityMergeResult {
  loser: string;
  winner: string;
  /** Work grains repointed at the winner. On a failed merge, what landed before it stopped. */
  rewritten: number;
  /** Works naming the loser when the pass began; rewritten < carriers means retry to finish (tasks/305). */
  carriers: number;
  /** Every carrier rewritten and the loser retired. */
  complete: boolean;
}

/** profiles.Profile -- the field definitions an editor form renders from. */
export interface Profile {
  id: string;
  label: string;
  resourceType: string;
  fields: ProfileField[];
}

/** GET /v1/profiles list entry: a profile plus whether a runtime override
 *  currently shadows the shipped default. */
export interface ProfileSummary extends Profile {
  overridden?: boolean;
}

/** profiles.ValueSource -- how a field's values are entered and validated. */
export interface ProfileValueSource {
  kind: "literal" | "langLiteral" | "date" | "enum" | "vocab" | "authority" | "entity";
  ref?: string;
  options?: string[];
}

/** profiles.Field -- one editable field of a profile. */
export interface ProfileField {
  path: string;
  label: string;
  help?: string;
  min?: number;
  max?: number;
  valueSource?: ProfileValueSource;
  hidden?: boolean;
  marcHint?: string;
}

export type SuggType = "ADD" | "REMOVE" | "CONCERN";
export type SuggStatus = "PENDING" | "APPROVED" | "REJECTED" | "DISPUTED";
export type Provenance = "PATRON" | "PIPELINE" | "LIBRARIAN";

/** suggest.Suggestion -- one review-queue item. */
export interface Suggestion {
  workId: string;
  term: TermRef;
  type: SuggType;
  status: SuggStatus;
  supporterCount: number;
  reasonCounts?: Record<string, number>;
  provenance: Provenance;
  confidence?: number;
  workTitle?: string;
  sourceRef?: string;
  /** A concern's freetext (type CONCERN, tasks/210). */
  note?: string;
  createdAt: string;
  lastActivityAt: string;
  reviewedAt?: string;
  reviewedBy?: string;
  reviewNote?: string;
}

export interface QueuePage {
  items: Suggestion[];
  cursor?: string;
}

/** suggest.Decision -- one staff review action in a POST /v1/review batch. */
export interface Decision {
  workId: string;
  term: TermRef;
  type: SuggType;
  approve: boolean;
  substituteTerm?: TermRef;
  note?: string;
  tombstone?: boolean;
}

/** POST /v1/review response; publish fields appear when publish was set. */
export interface ReviewResponse {
  /** Decisions actually applied -- not the number submitted. */
  reviewed: number;
  published?: number;
  /** Suggestions the publisher skipped. Nothing to do with staleDecisions. */
  skipped?: number;
  approvedPending?: number;
  publishNote?: string;
  /** Decisions another moderator resolved first, so they were discarded
   *  rather than applied (tasks/257). */
  staleDecisions?: Decision[];
}

/** POST /v1/publish response (also the shape merged into ReviewResponse). */
export interface PublishResponse {
  published: number;
  skipped?: number;
  approvedPending?: number;
  publishNote?: string;
}

/** GET /v1/tags -- one distinct grain-tree tag with its carry count. */
export interface TagCount {
  tag: string;
  count: number;
}

/** batch.Selection -- names a set of works for a batch run (tasks/047). */
export interface Selection {
  kind: "ids" | "search" | "savedQuery" | "all" | "importBatch";
  ids?: string[];
  query?: string;
  savedQueryId?: string;
  /** Facet filters, AND across groups and OR within one, on kind=search and
   *  kind=all (tasks/254). Same groups the works rail offers. */
  facets?: Record<string, string[]>;
  /** exclude|include|only over retired records. A selection defaults to
   *  include, unlike the works listing; the works screen sends exclude. */
  tombstoned?: string;
}

/** batch.Target -- one resolved work in a selection preview. */
export interface BatchTarget {
  workId: string;
  title?: string;
}

/** batch.ItemResult -- one work's outcome in a batch run. */
export interface BatchItemResult {
  workId: string;
  etag?: string;
  error?: string;
  diff?: Diff;
}

/** batch.RunResult -- a batch run summary with per-record results. */
export interface BatchRunResult {
  dryRun: boolean;
  matched: number;
  applied: number;
  failed: number;
  added: number;
  removed: number;
  results: BatchItemResult[];
  diffsTruncated?: boolean;
}

/** batch.Param -- one macro parameter (${name} in op values). */
export interface MacroParam {
  name: string;
  label?: string;
  default?: string;
}

/** batch.Macro -- a replayable op list; shared = a modification template. */
export interface Macro {
  id: string;
  label: string;
  keys?: string;
  ops: Op[];
  params?: MacroParam[];
  shared: boolean;
  owner: string;
  createdAt: string;
  updatedAt: string;
}

/** batch.SavedQuery -- a named works search. */
export interface SavedQuery {
  id: string;
  label: string;
  query: string;
  owner: string;
  createdAt: string;
}

/** marcview.Subfield -- one (code, value) pair. */
export interface MarcSubfield {
  code: string;
  value: string;
}

/** marcview.Field -- one row of the MARC editing grid. */
export interface MarcField {
  tag: string;
  ind1?: string;
  ind2?: string;
  value?: string; // control fields (tag < 010)
  subfields?: MarcSubfield[];
  lossy?: string; // fidelity-table reason; edits persist via the verbatim sidecar
}

/** marcview.RecordDoc -- one materialized MARC record. */
export interface MarcRecordDoc {
  node: string;
  leader: string;
  fields: MarcField[];
}

/** GET /v1/works/{id}/marc payload. */
export interface MarcResponse {
  workId: string;
  etag: string;
  records: MarcRecordDoc[];
  knownLoss: Record<string, string>;
}

/** bibframe.WorkVisibility -- a work's projection stance (tasks/051). */
export interface WorkVisibility {
  tombstoned: boolean;
  redirectTo?: string;
  suppressed: boolean;
  /** Date the feed reconciliation flagged the work withdrawn (tasks/078). */
  withdrawn?: string;
  suppressedBy?: string;
  kept?: boolean;
}

/** bibframe.Item -- one holding of an instance (minimal bf:Item). */
export interface WorkItem {
  id?: string;
  callNumber?: string;
  location?: string;
  barcode?: string;
  note?: string;
}

/** batch.ItemTemplate -- a saved item field set (tasks/069). */
export interface ItemTemplate {
  id?: string;
  label: string;
  callNumber?: string;
  location?: string;
  note?: string;
  barcodePrefix?: string;
  barcodeWidth?: number;
  shared?: boolean;
  owner?: string;
  createdAt?: string;
  updatedAt?: string;
}

/** GET /v1/duplicates -- one clustering-key collision group. */
export interface DuplicateGroup {
  key: string;
  works: { workId: string; title?: string }[];
}

/** copycat.Target -- one external Z39.50/SRU search source. */
export interface CopycatTarget {
  name: string;
  url: string;
  protocol: "sru" | "z3950";
}

/** copycat.Match -- a staged record's dry-run identity resolution. */
export interface CopycatMatch {
  workId?: string;
  instanceId?: string;
  matchedWork: boolean;
  matchedInstance: boolean;
}

/** copycat.SearchResult -- one external hit, ready to stage. */
export interface CopycatSearchResult {
  target: string;
  title?: string;
  author?: string;
  date?: string;
  isbn?: string;
  edition?: string;
  lccn?: string;
  record: MarcRecordDoc;
}

/** copycat.FieldTerm -- one (access point, term) pair; terms AND together. */
export interface CopycatFieldTerm {
  index: "any" | "title" | "author" | "subject" | "isbn" | "issn" | "lccn" | "id";
  term: string;
}

/** copycat.Template -- a blank-record MARC skeleton (tasks/077). */
export interface CopycatTemplate {
  id: string;
  label: string;
  record: MarcRecordDoc;
}

/** copycat.FieldError -- a validation failure anchored to a MARC field. */
export interface MarcFieldError {
  tag: string;
  message: string;
}

/** copycat.StagedRecord -- one reviewable record of a batch. */
export interface CopycatStagedRecord {
  index: number;
  record: MarcRecordDoc;
  title?: string;
  match: CopycatMatch;
  decision: "import" | "skip";
}

export type CopycatPolicy = "replace-feed" | "fill-holes-only" | "never";

/** copycat.Batch -- one staged import. */
export interface CopycatBatch {
  id: string;
  label: string;
  source: string;
  policy: CopycatPolicy;
  status: "STAGED" | "COMMITTED" | "REVERTED";
  records: number;
  owner: string;
  createdAt: string;
  committed?: number;
  skipped?: number;
  commitAt?: string;
  reverted?: number;
  revertAt?: string;
}

/** copycat.Profile -- a saved staging configuration (tasks/068). */
export interface CopycatProfile {
  name: string;
  targets?: string[];
  policy?: CopycatPolicy;
}

/** copycat.RevertResult -- a batch revert's outcome. */
export interface CopycatRevertResult {
  batch: CopycatBatch;
  reverted: number;
  skipped?: { path: string; reason: string }[];
}

export type ExportFormat = "marc" | "nquads" | "jsonld" | "csv";
export type ExportStatus = "QUEUED" | "RUNNING" | "DONE" | "FAILED";

/** export.AuthoritySelection -- scopes an authority export (tasks/069). */
export interface AuthoritySelection {
  all?: boolean;
  vocabs?: string[];
  label?: string;
}

/** httpapi.exportView -- one export job with its download link when ready. */
export interface ExportJob {
  id: string;
  requester: string;
  format: ExportFormat;
  selection: { all?: boolean; workIds?: string[] };
  authorities?: AuthoritySelection;
  status: ExportStatus;
  records?: number;
  error?: string;
  createdAt: string;
  finishedAt?: string;
  expiresAt?: string;
  downloadUrl?: string;
}

/** suggest.Promotion -- a folk tag proposed to fold into a controlled term. */
export interface Promotion {
  tag: string;
  term: TermRef;
  status: SuggStatus;
  proposedBy: string;
  createdAt: string;
  decidedBy?: string;
  decidedAt?: string;
  works?: number;
}

/** POST /v1/promotions/decide response. */
export interface DecidePromotionResponse {
  promotion: Promotion;
  works: number;
  note?: string;
}

/** vocabsrc.Source -- one public authority source (tasks/067). */
export interface VocabSource {
  name: string;
  scheme: string;
  license?: string;
  homepage?: string;
  suggestFlavor?: string;
  suggestUrl?: string;
  suggestDataset?: string;
  snapshotUrl?: string;
  builtin?: boolean;
}

/** vocabsrc.InstallInfo -- an installed snapshot's sidecar metadata. */
export interface VocabInstall {
  source: string;
  scheme: string;
  terms: number;
  installedAt: string;
  snapshotUrl: string;
}

export type VocabJobStatus = "QUEUED" | "RUNNING" | "DONE" | "FAILED";

/** vocabsrc.Job -- one vocabulary snapshot download. */
export interface VocabJob {
  id: string;
  source: string;
  scheme: string;
  requester: string;
  status: VocabJobStatus;
  terms?: number;
  error?: string;
  createdAt: string;
  finishedAt?: string;
}

/** vocabsrc.SourceView -- a source plus install state and its latest job. */
export interface VocabSourceView extends VocabSource {
  installed?: VocabInstall;
  job?: VocabJob;
  /** The row is synthesized from an install with no source record behind it, so
   *  only Remove can act on it -- everything else answers 404 (tasks/255). */
  orphan?: boolean;
}

/** httpapi.subjectCandidate -- one external heading found by ISBN lookup
 *  (tasks/073); term set = whole-heading match in a loaded vocabulary. */
export interface SubjectCandidate {
  heading: string;
  tag: string;
  source?: string;
  count: number;
  targets: string[];
  term?: TermRef;
}

/** vocabsrc.Suggestion -- one live typeahead hit from a public source. */
export interface VocabSuggestion {
  source: string;
  scheme: string;
  id: string;
  label: string;
  description?: string;
  /** Variant/"used for" labels when the source exposes them (suggest2). */
  variants?: string[];
  exactMatch?: string[];
}
