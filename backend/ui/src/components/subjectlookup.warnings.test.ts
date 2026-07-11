// a copycat target whose stream broke partway used to answer as a
// clean success. Downstream, "The targets' records carry no headings this work
// lacks" is a claim about records that were never read. A partial answer must
// be visible, and it must not suppress the headings that did arrive.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";

const lookupSubjects = vi.fn();

vi.mock("../lib/api", async () => {
  const actual = await vi.importActual<typeof import("../lib/api")>("../lib/api");
  return { ...actual, lookupSubjects };
});

const SubjectLookup = (await import("./SubjectLookup.svelte")).default;

let app: Record<string, unknown> | null = null;

function candidate(heading: string) {
  return { tag: "650", heading, source: "loc", count: 1, targets: ["loc"], ids: [], term: null };
}

/** Mounts the panel and clicks its lookup button, settling the async load. */
async function lookup(): Promise<HTMLElement> {
  const host = document.createElement("div");
  document.body.appendChild(host);
  app = mount(SubjectLookup, { target: host, props: { workId: "wabc123def456", onadd: () => {} } }) as Record<
    string,
    unknown
  >;
  flushSync();
  const button = host.querySelector("button") as HTMLButtonElement;
  button.click();
  flushSync();
  await Promise.resolve();
  await Promise.resolve();
  flushSync();
  return host;
}

beforeEach(() => {
  lookupSubjects.mockReset();
});

afterEach(() => {
  if (app) unmount(app);
  app = null;
  document.body.innerHTML = "";
});

describe("SubjectLookup incomplete answers", () => {
  it("shows a target's warning and still lists the headings that arrived", async () => {
    lookupSubjects.mockResolvedValue({
      candidates: [candidate("Lesbian authors")],
      failures: {},
      warnings: { loc: "partial results: the stream broke after 1 record(s): XML syntax error" },
    });
    const host = await lookup();
    expect(host.textContent).toContain("Lesbian authors");
    expect(host.textContent).toContain("incomplete");
    expect(host.textContent).toContain("XML syntax error");
  });

  it("does not claim the targets carry no headings when an answer was cut short", async () => {
    lookupSubjects.mockResolvedValue({
      candidates: [],
      failures: {},
      warnings: { loc: "result set truncated at the search limit" },
    });
    const host = await lookup();
    expect(host.textContent).not.toContain("carry no headings this work lacks");
    expect(host.textContent).toContain("cut short");
  });

  it("still makes the negative claim when every target answered in full", async () => {
    lookupSubjects.mockResolvedValue({ candidates: [], failures: {}, warnings: {} });
    const host = await lookup();
    expect(host.textContent).toContain("carry no headings this work lacks");
  });

  it("treats an outright failure as an incomplete answer too", async () => {
    lookupSubjects.mockResolvedValue({ candidates: [], failures: { loc: "connection refused" }, warnings: {} });
    const host = await lookup();
    expect(host.textContent).not.toContain("carry no headings this work lacks");
  });

  it("tolerates a server that omits warnings entirely", async () => {
    lookupSubjects.mockResolvedValue({ candidates: [candidate("Trans archives")] });
    const host = await lookup();
    expect(host.textContent).toContain("Trans archives");
  });
});
