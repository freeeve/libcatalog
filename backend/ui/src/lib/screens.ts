// The one table of navigable screens.
//
// There used to be three: the palette's NAV, App.svelte's "g <letter>" chords,
// and the sidebar's links. No two agreed. The palette could not reach
// Vocabularies, Withdrawals or Profiles -- it answered "No matching commands.",
// which does not say "that screen is elsewhere", it says the thing does not
// exist. Promotions had no sidebar link; Profiles had no chord.
//
// Each surface is now derived from this table, so "the palette reaches every
// screen" is true by construction rather than by vigilance, and every omission
// is a flag someone had to write down.

/** A screen a person can navigate to. Detail routes (/works/:id) and the auth
 *  routes are not screens: nothing navigates to them by name. */
export interface Screen {
  /** The router's name for this path, so the sidebar can mark itself current. */
  route: string;
  path: string;
  /** The sidebar and palette label. */
  label: string;
  /** The palette's label, when the sidebar's is too terse to search for. */
  paletteLabel?: string;
  /** The letter of its "g <letter>" chord, or null for none. */
  chord: string | null;
  /** Whether the primary nav links it. */
  sidebar: boolean;
  /** Only visible to admins. */
  adminOnly?: boolean;
  /** Detail routes that should light this screen's sidebar link. */
  alsoCurrent?: string[];
}

// Order is the surfaces' order: the sidebar reads left to right, and the
// palette lists these before its work and macro results. Works leads because it
// is what a cataloger wants nine times in ten; Dashboard trails because it is
// where they already are.
export const SCREENS: Screen[] = [
  { route: "works", path: "/works", label: "Works", chord: "w", sidebar: true, alsoCurrent: ["work"] },
  { route: "authorities", path: "/authorities", label: "Authorities", chord: "a", sidebar: true, alsoCurrent: ["authority"] },
  { route: "vocabsources", path: "/vocabularies", label: "Vocabularies", chord: "v", sidebar: true },
  { route: "batch", path: "/batch", label: "Batch", paletteLabel: "Batch operations", chord: "b", sidebar: true },
  { route: "macros", path: "/macros", label: "Macros", chord: "m", sidebar: true },
  { route: "exports", path: "/exports", label: "Exports", chord: "e", sidebar: true },
  { route: "copycat", path: "/copycat", label: "Import", paletteLabel: "Copy cataloging (import)", chord: "i", sidebar: true },
  { route: "duplicates", path: "/duplicates", label: "Duplicates", chord: "u", sidebar: true },
  { route: "withdrawals", path: "/withdrawals", label: "Withdrawals", chord: "t", sidebar: true },
  { route: "queue", path: "/queue", label: "Queue", chord: "q", sidebar: true },
  { route: "promotions", path: "/promotions", label: "Promotions", chord: "p", sidebar: true },
  // "g p" is Promotions, so Profiles takes "f". Admin-only, and the palette
  // lists it for everyone -- the route already refuses a non-admin, and hiding
  // a screen's existence from the palette is what this task is about.
  { route: "profiles", path: "/profiles", label: "Profiles", chord: "f", sidebar: true, adminOnly: true },
  // The patron-suggestion policy editor: opt-in switch, scheme
  // allowlist, free-text mode. Admin-only like its config routes; the public
  // read that hides the discovery affordance needs no screen.
  { route: "suggestions", path: "/suggestions", label: "Suggestions", paletteLabel: "Suggestion policy", chord: "s", sidebar: true, adminOnly: true },
  // The audit-log reader: the month's entries unfiltered by work, so
  // the system-level actions (users, roles, profiles, imports, batch runs) that
  // carry no workId -- invisible in a work's History tab -- have a screen.
  // Librarian-gated like its route; a moderator cannot read the trail.
  { route: "audit", path: "/audit", label: "Audit", paletteLabel: "Audit log", chord: "l", sidebar: true },
  // The content-diversity audit: coverage-first category distribution over
  // the live work index, methodology inline. "Diversity audit" everywhere
  // user-facing -- "Audit" alone is the log reader above.
  { route: "diversity", path: "/diversity", label: "Diversity", paletteLabel: "Diversity audit", chord: "y", sidebar: true },
  // The crosswalk editor behind the audit. Off the sidebar (the Diversity
  // screen links it); the palette still reaches it by name.
  { route: "diversityconfig", path: "/diversity/config", label: "Diversity setup", paletteLabel: "Diversity crosswalk setup", chord: null, sidebar: false },
  // Last in the palette, and absent from the nav: the brand link is its door.
  { route: "dashboard", path: "/", label: "Dashboard", chord: "d", sidebar: false },
];

/** The palette's label for a screen: the searchable one when they differ. */
export function paletteLabel(s: Screen): string {
  return s.paletteLabel ?? s.label;
}

/** The screens the primary nav links, for the given admin-ness. */
export function sidebarScreens(isAdmin: boolean): Screen[] {
  return SCREENS.filter((s) => s.sidebar && (!s.adminOnly || isAdmin));
}

/** Whether a screen's sidebar link should read as current for a route name. */
export function isCurrent(s: Screen, routeName: string): boolean {
  return s.route === routeName || (s.alsoCurrent?.includes(routeName) ?? false);
}

/** The "g <letter>" chords, as bindKeys wants them. */
export function chordMap(): Record<string, [string, string]> {
  const out: Record<string, [string, string]> = {};
  for (const s of SCREENS) {
    if (s.chord) out[`g ${s.chord}`] = [s.path, `go to ${s.label.toLowerCase()}`];
  }
  return out;
}
