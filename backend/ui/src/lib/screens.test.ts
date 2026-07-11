import { describe, expect, it } from "vitest";
import { NON_SCREEN_ROUTES, ROUTES } from "./router";
import { chordMap, isCurrent, paletteLabel, SCREENS, sidebarScreens } from "./screens";

// the palette, the "g <letter>" chords, and the sidebar were three
// hand-maintained lists and no two agreed. The palette answered "No matching
// commands." for Vocabularies, Withdrawals and Profiles -- which does not say
// "that screen is elsewhere", it says the thing does not exist. These tests are
// the pin: a new screen that routes but is not listed fails the build.
describe("SCREENS covers the router", () => {
  it("has an entry for every navigable route", () => {
    const navigable = ROUTES.filter((r) => !NON_SCREEN_ROUTES.has(r.name)).map((r) => r.name);
    const listed = new Set(SCREENS.map((s) => s.route));
    const missing = navigable.filter((name) => !listed.has(name));
    expect(missing, `routes with no screen entry: ${missing.join(", ")}`).toEqual([]);
  });

  it("lists no screen the router cannot resolve", () => {
    const routed = new Map(ROUTES.map((r) => [r.name, r.pattern]));
    for (const s of SCREENS) {
      expect(routed.get(s.route), `screen ${s.route} is not a route`).toBe(s.path);
    }
  });

  // A detail route lights its parent's sidebar link; it is not itself a screen.
  it("only claims alsoCurrent routes that exist and are not screens", () => {
    const routed = new Set(ROUTES.map((r) => r.name));
    const listed = new Set(SCREENS.map((s) => s.route));
    for (const s of SCREENS) {
      for (const name of s.alsoCurrent ?? []) {
        expect(routed.has(name), `${s.route}.alsoCurrent names ${name}, which does not route`).toBe(true);
        expect(listed.has(name), `${s.route}.alsoCurrent names ${name}, which is its own screen`).toBe(false);
      }
    }
  });
});

describe("the derived surfaces", () => {
  it("gives the palette every screen, including the admin-only one", () => {
    // Hiding a screen's existence from the palette is the bug; the route
    // already refuses a non-admin.
    const profiles = SCREENS.find((s) => s.route === "profiles");
    expect(profiles?.adminOnly).toBe(true);
    for (const name of ["vocabsources", "withdrawals", "profiles"]) {
      expect(SCREENS.some((s) => s.route === name)).toBe(true);
    }
  });

  it("searches the palette by the label a person would type", () => {
    const labels = SCREENS.map((s) => paletteLabel(s).toLowerCase());
    for (const typed of ["vocabularies", "withdrawals", "profiles", "duplicates", "import"]) {
      expect(labels.some((l) => l.includes(typed)), `nothing matches "${typed}"`).toBe(true);
    }
    // "Import" is the sidebar's word; the palette also answers "copy cataloging".
    expect(paletteLabel(SCREENS.find((s) => s.route === "copycat")!)).toBe("Copy cataloging (import)");
  });

  // The palette's order is muscle memory: open it, press Enter, land on Works.
  // Deriving NAV from a table made this an accident waiting to happen, so it is
  // pinned.
  it("leads with Works and trails with Dashboard", () => {
    expect(SCREENS[0].route).toBe("works");
    expect(SCREENS[1].route).toBe("authorities");
    expect(SCREENS[SCREENS.length - 1].route).toBe("dashboard");
  });

  it("assigns each chord to exactly one screen", () => {
    const chords = SCREENS.filter((s) => s.chord).map((s) => s.chord);
    expect(new Set(chords).size).toBe(chords.length);
    const map = chordMap();
    expect(map["g v"]?.[0]).toBe("/vocabularies");
    expect(map["g t"]?.[0]).toBe("/withdrawals");
    expect(map["g p"]?.[0]).toBe("/promotions");
    // Profiles had no chord; "g p" was taken, so it takes "f".
    expect(map["g f"]?.[0]).toBe("/profiles");
  });

  it("hides the admin screen from a non-admin sidebar and shows it to an admin", () => {
    const asStaff = sidebarScreens(false).map((s) => s.route);
    const asAdmin = sidebarScreens(true).map((s) => s.route);
    expect(asStaff).not.toContain("profiles");
    expect(asAdmin).toContain("profiles");
    // The dashboard is the brand link, not a nav item.
    expect(asAdmin).not.toContain("dashboard");
    // Promotions had no sidebar link at all; it does now.
    expect(asStaff).toContain("promotions");
  });

  it("marks a detail route as current on its parent's link", () => {
    const works = SCREENS.find((s) => s.route === "works")!;
    expect(isCurrent(works, "works")).toBe(true);
    expect(isCurrent(works, "work")).toBe(true);
    expect(isCurrent(works, "queue")).toBe(false);
    const authorities = SCREENS.find((s) => s.route === "authorities")!;
    expect(isCurrent(authorities, "authority")).toBe(true);
  });
});
