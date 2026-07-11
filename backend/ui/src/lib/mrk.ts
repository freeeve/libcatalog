// mrk-style text serialization of a MarcRecordDoc: one line per
// field ("245 14 $a The Dutch house : $b a novel /"), the leader and control
// fields as raw lines ("LDR ...", "008 ..."), subfield lines in exactly the
// grid's "$a … $b …" syntax (shared helpers), so grid and text are two views
// of one doc and round-trip losslessly. Parsing reports per-line errors
// without discarding the buffer.
import { isControlTag, lineToSubfields, subfieldsToLine } from "./marc";
import type { MarcField, MarcRecordDoc } from "./types";

/** One parse failure, anchored to a 1-based buffer line. */
export interface MrkError {
  line: number;
  message: string;
}

export interface MrkParseResult {
  /** The parsed doc (node and untouched metadata carried from `base`);
   *  absent when any line failed. */
  record?: MarcRecordDoc;
  errors: MrkError[];
}

/** Serializes one field as its text line. */
export function serializeField(f: MarcField): string {
  if (isControlTag(f.tag)) return `${f.tag} ${f.value ?? ""}`;
  return `${f.tag} ${(f.ind1 ?? " ").padEnd(1)}${(f.ind2 ?? " ").padEnd(1)} ${subfieldsToLine(f.subfields)}`;
}

/** Serializes the whole record: LDR line first, then fields in doc order. */
export function serializeRecord(doc: MarcRecordDoc): string {
  return [`LDR ${doc.leader}`, ...doc.fields.map(serializeField)].join("\n");
}

/** Parses an mrk-style buffer back into `base`'s shape. Blank lines are
 *  skipped; the leader comes from the LDR line (or stays `base`'s); lossy
 *  annotations reattach from `knownLoss` by tag. Any error leaves the buffer
 *  authoritative: no record is returned, every failing line is reported. */
export function parseRecord(text: string, base: MarcRecordDoc, knownLoss: Record<string, string> = {}): MrkParseResult {
  const errors: MrkError[] = [];
  const fields: MarcField[] = [];
  let leader: string | undefined;
  const lines = text.split("\n");
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const n = i + 1;
    if (line.trim() === "") continue;
    if (line.startsWith("LDR")) {
      if (leader !== undefined) {
        errors.push({ line: n, message: "duplicate LDR line" });
        continue;
      }
      if (line.length < 4 || line[3] !== " ") {
        errors.push({ line: n, message: "LDR needs a space before the leader value" });
        continue;
      }
      leader = line.slice(4);
      continue;
    }
    const tag = line.slice(0, 3);
    if (!/^[0-9A-Za-z]{3}$/.test(tag)) {
      errors.push({ line: n, message: `bad tag ${JSON.stringify(tag)} -- three characters, then a space` });
      continue;
    }
    if (line.length > 3 && line[3] !== " ") {
      errors.push({ line: n, message: "a space must follow the tag" });
      continue;
    }
    if (isControlTag(tag)) {
      const f: MarcField = { tag, value: line.slice(4) };
      if (knownLoss[tag]) f.lossy = knownLoss[tag];
      fields.push(f);
      continue;
    }
    // Data field: two indicator characters, a space, then subfields.
    const ind = line.slice(4, 6);
    if (ind.length < 2 || (line.length > 6 && line[6] !== " ")) {
      errors.push({ line: n, message: "missing indicators -- expected two characters (spaces count) then a space" });
      continue;
    }
    if (!/^[0-9a-z ]{2}$/.test(ind)) {
      errors.push({ line: n, message: `bad indicators ${JSON.stringify(ind)} -- digits, lowercase letters, or spaces` });
      continue;
    }
    const subfields = lineToSubfields(line.slice(7));
    if (subfields.length === 0) {
      errors.push({ line: n, message: "no subfield content -- expected $a …" });
      continue;
    }
    const f: MarcField = { tag, ind1: ind[0], ind2: ind[1], subfields };
    if (knownLoss[tag]) f.lossy = knownLoss[tag];
    fields.push(f);
  }
  if (errors.length > 0) return { errors };
  return { record: { ...base, leader: leader ?? base.leader, fields }, errors };
}
