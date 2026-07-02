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

/** vocab.TermRef -- a controlled term reference. */
export interface TermRef {
  scheme: string;
  id: string;
  label: string;
}

export type SuggType = "ADD" | "REMOVE";
export type SuggStatus = "PENDING" | "APPROVED" | "REJECTED" | "DISPUTED";

/** suggest.Suggestion -- one review-queue item. */
export interface Suggestion {
  workId: string;
  term: TermRef;
  type: SuggType;
  status: SuggStatus;
  supporterCount: number;
  reasonCounts?: Record<string, number>;
  provenance: string;
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
