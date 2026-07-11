// MARC grid helpers: the grid edits a data field's subfields as
// one "$a Value $b Value" line (the familiar MARC editor syntax), converted
// losslessly to and from the structured shape the API speaks.
import type { MarcField, MarcSubfield } from "./types";

/** Renders subfields as an editable "$a Foo $b Bar" line. */
export function subfieldsToLine(subfields: MarcSubfield[] | undefined): string {
  return (subfields ?? []).map((sf) => `$${sf.code} ${sf.value}`).join(" ");
}

/** Parses a "$a Foo $b Bar" line back to subfields. Text before the first
 *  delimiter becomes $a (typing without delimiters still works).
 *
 *  A delimiter is "$" + one alphanumeric code standing ALONE: preceded by
 *  line start or whitespace and followed by whitespace or line end. A
 *  literal dollar amount ("$24.95", "$5.00") therefore stays inside its
 * value instead of silently reparsing as a bogus subfield --
 *  the serializer always emits the spaced form, so real delimiters always
 *  qualify. The cost is that "$aFoo" (no space) no longer starts a
 *  subfield; it reads as literal text. */
export function lineToSubfields(line: string): MarcSubfield[] {
  const out: MarcSubfield[] = [];
  const trimmed = line.trim();
  if (trimmed === "") return out;
  const re = /(^|\s)\$([a-z0-9])(?=\s|$)/gi;
  let match = re.exec(trimmed);
  if (!match) return [{ code: "a", value: trimmed }];
  const firstStart = match.index + match[1].length;
  if (firstStart > 0) out.push({ code: "a", value: trimmed.slice(0, firstStart).trim() });
  while (match) {
    const code = match[2].toLowerCase();
    const valueStart = match.index + match[0].length;
    const next = re.exec(trimmed);
    const nextStart = next ? next.index + next[1].length : undefined;
    out.push({ code, value: trimmed.slice(valueStart, nextStart).trim() });
    match = next;
  }
  return out.filter((sf) => sf.value !== "");
}

/** True for control fields (no indicators/subfields). */
export function isControlTag(tag: string): boolean {
  return tag < "010";
}

/** True for the positionally-defined fields the builder grid understands. */
export function isFixedTag(tag: string): boolean {
  return tag === "006" || tag === "007" || tag === "008";
}

/** A blank data-field row ready for the grid. */
export function blankField(tag = ""): MarcField {
  return { tag, ind1: " ", ind2: " ", subfields: [{ code: "a", value: "" }] };
}

/** One position (or run) of a fixed field, for the builder grid. */
export interface FixedSlot {
  /** Byte offset within the field value (leader offsets are absolute). */
  offset: number;
  length: number;
  label: string;
  /** Known values, rendered as a datalist; free entry stays allowed. */
  options?: { value: string; label: string }[];
}

/** Leader positions catalogers actually set; the rest are computed by the
 *  writer (lengths, base address) or fixed. */
export const LEADER_SLOTS: FixedSlot[] = [
  {
    offset: 5,
    length: 1,
    label: "Record status",
    options: [
      { value: "n", label: "new" },
      { value: "c", label: "corrected" },
      { value: "d", label: "deleted" },
      { value: "p", label: "prepublication" },
    ],
  },
  {
    offset: 6,
    length: 1,
    label: "Type of record",
    options: [
      { value: "a", label: "language material" },
      { value: "i", label: "nonmusical sound" },
      { value: "j", label: "musical sound" },
      { value: "m", label: "computer file" },
    ],
  },
  {
    offset: 7,
    length: 1,
    label: "Bibliographic level",
    options: [
      { value: "m", label: "monograph" },
      { value: "s", label: "serial" },
      { value: "a", label: "part of monograph" },
    ],
  },
  { offset: 17, length: 1, label: "Encoding level" },
  {
    offset: 18,
    length: 1,
    label: "Cataloging form",
    options: [
      { value: "a", label: "AACR2" },
      { value: "i", label: "ISBD" },
      { value: " ", label: "non-ISBD" },
    ],
  },
];

/** 008 general + books slice: the positions shared across material types
 *  plus the book-specific run, which covers the text/ebook records the MARC
 *  ramp ingests. Other material types edit the raw value line. */
export const F008_SLOTS: FixedSlot[] = [
  { offset: 0, length: 6, label: "Date entered (yymmdd)" },
  {
    offset: 6,
    length: 1,
    label: "Date type",
    options: [
      { value: "s", label: "single" },
      { value: "r", label: "reprint" },
      { value: "t", label: "publication + copyright" },
      { value: "n", label: "unknown" },
    ],
  },
  { offset: 7, length: 4, label: "Date 1" },
  { offset: 11, length: 4, label: "Date 2" },
  { offset: 15, length: 3, label: "Place of publication" },
  { offset: 35, length: 3, label: "Language" },
  {
    offset: 39,
    length: 1,
    label: "Cataloging source",
    options: [
      { value: " ", label: "national bibliography" },
      { value: "c", label: "cooperative program" },
      { value: "d", label: "other" },
    ],
  },
];

/** 006 and 007 lead with their category byte; the rest is edited raw with
 *  the category labeled, which is honest about how variable they are. */
export const F006_SLOTS: FixedSlot[] = [
  {
    offset: 0,
    length: 1,
    label: "Form of material",
    options: [
      { value: "m", label: "computer file / electronic" },
      { value: "a", label: "language material" },
      { value: "i", label: "nonmusical sound" },
    ],
  },
  { offset: 1, length: 17, label: "Material details (positional)" },
];

export const F007_SLOTS: FixedSlot[] = [
  {
    offset: 0,
    length: 1,
    label: "Category of material",
    options: [
      { value: "c", label: "electronic resource" },
      { value: "s", label: "sound recording" },
      { value: "v", label: "videorecording" },
      { value: "t", label: "text" },
    ],
  },
  { offset: 1, length: 22, label: "Specific details (positional)" },
];

/** The builder definition for a fixed tag ("LDR" for the leader). */
export function fixedSlots(tag: string): FixedSlot[] {
  switch (tag) {
    case "LDR":
      return LEADER_SLOTS;
    case "006":
      return F006_SLOTS;
    case "007":
      return F007_SLOTS;
    case "008":
      return F008_SLOTS;
  }
  return [];
}

/** Reads a slot's run from the value, space-padded to length. */
export function slotValue(value: string, slot: FixedSlot): string {
  const padded = value.padEnd(slot.offset + slot.length, " ");
  return padded.slice(slot.offset, slot.offset + slot.length);
}

/** Writes a slot's run into the value, preserving everything else. */
export function withSlotValue(value: string, slot: FixedSlot, run: string): string {
  const padded = value.padEnd(slot.offset + slot.length, " ");
  const clipped = run.padEnd(slot.length, " ").slice(0, slot.length);
  return padded.slice(0, slot.offset) + clipped + padded.slice(slot.offset + slot.length);
}
