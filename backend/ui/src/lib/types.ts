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
}

/** One row from GET /v1/works (ingest.WorkSummary, untagged Go fields). */
export interface WorkSummary {
  WorkID: string;
  Title: string;
  Contributors: string[];
  ISBNs: string[];
  Tags: string[];
}

export interface WorksPage {
  works: WorkSummary[];
  total: number;
}

/** editor.FieldValue -- one value of a profile field, with provenance. */
export interface FieldValue {
  v: string;
  lang?: string;
  datatype?: string;
  iri?: boolean;
  prov: string;
  overridden?: boolean;
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
}

/** editor.OpValue -- one value in a field operation. */
export interface OpValue {
  v: string;
  lang?: string;
  iri?: boolean;
}

export type OpAction = "add" | "remove" | "set" | "clear";

/** editor.Op -- one field-level edit in a POST /v1/works/{id}/ops batch. */
export interface Op {
  resource: string; // "work" or an instance id
  path: string;
  action: OpAction;
  value?: OpValue; // add / remove
  values?: OpValue[]; // set
}

/** editor.Diff -- the exact N-Quads delta a save makes (dry-run preview). */
export interface Diff {
  added: string[];
  removed: string[];
}

/** POST /v1/works/{id}/ops response (workId absent on dry runs). */
export interface OpsResult {
  workId?: string;
  etag: string;
  diff: Diff;
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
}

export interface AuditPage {
  month: string;
  entries: AuditEntry[];
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
}

export type SuggType = "ADD" | "REMOVE";
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
  reviewed: number;
  published?: number;
  skipped?: number;
  approvedPending?: number;
  publishNote?: string;
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
