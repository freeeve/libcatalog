// BISAC subject headings (MARC 072 $2 bisacsh) rendered as headings rather
// than raw codes: "DRA000000" reads as "Drama / General". Only the ~55
// top-level section names ship as data -- the full ~5,000-entry list is
// licensed by BISG -- so a specific subheading falls back to its section
// name with the code riding along as a muted chip, and an unknown code
// renders raw.

export interface BisacTerm {
  label: string;
  code: string;
  /** False when only the section resolved (show the code alongside). */
  exact: boolean;
}

const SECTIONS: Record<string, string> = {
  ANT: "Antiques & Collectibles",
  ARC: "Architecture",
  ART: "Art",
  BIB: "Bibles",
  BIO: "Biography & Autobiography",
  BUS: "Business & Economics",
  CGN: "Comics & Graphic Novels",
  CKB: "Cooking",
  COM: "Computers",
  CRA: "Crafts & Hobbies",
  DES: "Design",
  DRA: "Drama",
  EDU: "Education",
  FAM: "Family & Relationships",
  FIC: "Fiction",
  FOR: "Foreign Language Study",
  GAM: "Games & Activities",
  GAR: "Gardening",
  HEA: "Health & Fitness",
  HIS: "History",
  HOM: "House & Home",
  HUM: "Humor",
  JNF: "Juvenile Nonfiction",
  JUV: "Juvenile Fiction",
  LAN: "Language Arts & Disciplines",
  LAW: "Law",
  LCO: "Literary Collections",
  LIT: "Literary Criticism",
  MAT: "Mathematics",
  MED: "Medical",
  MUS: "Music",
  NAT: "Nature",
  NON: "Non-Classifiable",
  OCC: "Body, Mind & Spirit",
  PER: "Performing Arts",
  PET: "Pets",
  PHI: "Philosophy",
  PHO: "Photography",
  POE: "Poetry",
  POL: "Political Science",
  PSY: "Psychology",
  REF: "Reference",
  REL: "Religion",
  SCI: "Science",
  SEL: "Self-Help",
  SOC: "Social Science",
  SPO: "Sports & Recreation",
  STU: "Study Aids",
  TEC: "Technology & Engineering",
  TRA: "Transportation",
  TRU: "True Crime",
  TRV: "Travel",
  YAF: "Young Adult Fiction",
  YAN: "Young Adult Nonfiction",
};

/** The displayable heading for a BISAC code, or undefined (renders raw). */
export function bisacTerm(value: string): BisacTerm | undefined {
  const code = value.trim().toUpperCase();
  const m = /^([A-Z]{3})(\d{6})$/.exec(code);
  if (!m) return undefined;
  const section = SECTIONS[m[1]];
  if (!section) return undefined;
  if (m[2] === "000000") return { label: section + " / General", code, exact: true };
  return { label: section, code, exact: false };
}
