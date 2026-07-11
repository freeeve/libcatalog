// The record editor's field presentation, split from ProfileForm so the
// spec-building logic is unit-testable (tasks/295). A profile (the deployment's
// editing framework) owns the *set* of fields, their order, labels, and hidden
// flags; the presentation table here owns the concerns the server has no
// vocabulary for -- the editable kind, closed-list options, coded-value decode,
// worksheet section, and column width -- keyed by path. buildFieldSpecs merges
// the two so an override of work-monograph reshapes the editor while
// SearchSelect/bisac stay where they belong.
import { type SearchOption } from "./SearchSelect.svelte";
import { bisacTerm, type BisacTerm } from "../lib/bisac";
import { LANGUAGES } from "../lib/languages";
import { CARRIER_TYPES, CONTENT_TYPES, MEDIA_TYPES, type RdaTerm } from "../lib/rdaterms";
import type { ProfileField, ProfileValueSource } from "../lib/types";

export type FieldKind = "single" | "langLiteral" | "iri" | "vocab" | "tag" | "literal" | "readonly";

export interface FieldSpec {
  path: string;
  label: string;
  kind: FieldKind;
  hint?: string;
  /** Closed-list IRI fields (RDA media/carrier, MARC languages) get a
   *  searchable picker and labeled chips instead of raw URLs. */
  options?: SearchOption[];
  /** "more" fields fold into the default-collapsed "Additional details"
   *  disclosure (tasks/083). */
  section?: "more";
  /** Prose fields (summary) span every worksheet column. */
  wide?: boolean;
  /** Decodes a stored literal into a heading + code (BISAC). */
  decode?: (v: string) => BisacTerm | undefined;
}

/** Closed-list terms as searchable-picker entries. */
export function termOptions(terms: RdaTerm[]): SearchOption[] {
  return terms.map((t) => ({ value: t.iri, label: t.label, code: t.code, group: t.group }));
}

// The shipped work-monograph and instance-ebook presentation (profiles/defaults).
// "readonly" fields surface values living inside typed blank structures
// (contributions, notes, publication), edited via the MARC tab until the op
// layer builds structures (tasks/083).
export const WORK_FIELDS: FieldSpec[] = [
  { path: "title", label: "Title", kind: "single" },
  { path: "subtitle", label: "Subtitle", kind: "single" },
  { path: "contributors", label: "Contributors", kind: "readonly" },
  { path: "summary", label: "Summary", kind: "langLiteral", wide: true },
  { path: "language", label: "Language", kind: "iri", options: termOptions(LANGUAGES) },
  { path: "subjectLabels", label: "Subject headings", kind: "readonly" },
  { path: "subjects", label: "Subjects", kind: "vocab" },
  { path: "tags", label: "Tags", kind: "tag" },
  { path: "genreForm", label: "Genre / form", kind: "readonly", section: "more" },
  { path: "content", label: "Content type", kind: "iri", options: termOptions(CONTENT_TYPES), section: "more" },
  { path: "classification", label: "Classification", kind: "readonly", section: "more", decode: bisacTerm },
];

export const INSTANCE_FIELDS: FieldSpec[] = [
  { path: "isbn", label: "Identifiers", kind: "literal", hint: "9780000000000" },
  { path: "media", label: "Media type", kind: "iri", options: termOptions(MEDIA_TYPES) },
  { path: "carrier", label: "Carrier type", kind: "iri", options: termOptions(CARRIER_TYPES) },
  { path: "links", label: "Links", kind: "iri", hint: "https://…" },
  { path: "responsibility", label: "Responsibility", kind: "single", section: "more" },
  { path: "edition", label: "Edition", kind: "single", section: "more" },
  { path: "series", label: "Series", kind: "literal", hint: "Series statement as transcribed", section: "more" },
  { path: "seriesEnumeration", label: "Series enumeration", kind: "single", section: "more" },
  { path: "publicationPlace", label: "Publication place", kind: "readonly", section: "more" },
  { path: "publisher", label: "Publisher", kind: "readonly", section: "more" },
  { path: "publicationDate", label: "Publication date", kind: "readonly", section: "more" },
  { path: "extent", label: "Extent", kind: "readonly", section: "more" },
  { path: "duration", label: "Duration", kind: "single", section: "more" },
  { path: "notes", label: "Notes", kind: "readonly", section: "more" },
  { path: "format", label: "Digital format", kind: "readonly", section: "more" },
  { path: "issuance", label: "Issuance", kind: "readonly", section: "more" },
];

/** A field the profile declares but the presentation table does not describe
 *  still has to render: derive an editable kind from its value source so a new
 *  profile field is enterable, not invisible (tasks/295). */
export function kindFromValueSource(vs?: ProfileValueSource): FieldKind {
  switch (vs?.kind) {
    case "langLiteral":
      return "langLiteral";
    case "vocab":
    case "authority":
      return "vocab";
    case "enum":
      return "iri";
    default:
      return "single";
  }
}

/** Merge a resource's profile fields onto the local presentation table: the
 *  profile owns the set/order/labels/hidden, the table owns the presentation.
 *  Without a profile (fields undefined/empty) the shipped presentation stands,
 *  so a caller that has not fetched a profile keeps the default shape. */
export function buildFieldSpecs(presentation: FieldSpec[], fields?: ProfileField[]): FieldSpec[] {
  if (!fields || fields.length === 0) return presentation;
  const byPath = new Map(presentation.map((s) => [s.path, s]));
  return fields
    .filter((f) => !f.hidden)
    .map((f) => {
      const base = byPath.get(f.path);
      return base ? { ...base, label: f.label } : { path: f.path, label: f.label, kind: kindFromValueSource(f.valueSource) };
    });
}
