<script lang="ts">
  // Vocabularies: the click-to-download authority-source list.
  // Built-in and registered sources show their capabilities (live typeahead,
  // downloadable snapshot), license, and install state; Download fetches the
  // source's SKOS dump into the vocab index (a worker job -- the row polls
  // until it lands), Refresh re-downloads in place, Remove drops the terms.
  // Admins register drop-in sources (docs/authority-sources.md) right here:
  // a form over POST /v1/vocabsources plus one-click suggestions, and
  // registered (non-builtin) sources can be deleted.
  import { onMount } from "svelte";
  import {
    humanApiMessage,
    deleteVocabSource,
    downloadVocabSource,
    fetchVocabSources,
    putVocabSource,
    removeVocabSnapshot,
    uploadVocabSnapshot,
  } from "../lib/api";
  import { isReadOnly } from "../lib/config";
  import { bindKeys, popScope, pushScope } from "../lib/keyboard";
  import { sessionStore } from "../lib/stores";
  import type { VocabSource, VocabSourceView } from "../lib/types";

  /** Drop-in sources from docs/authority-sources.md whose dumps the
   *  converter reads directly (N-Triples, optionally gzipped). Verify the
   *  URL against the project's downloads page before installing. */
  const SUGGESTED_SOURCES: (VocabSource & { blurb: string })[] = [
    {
      name: "homosaurus",
      scheme: "homosaurus",
      license: "CC-BY-4.0",
      homepage: "https://homosaurus.org",
      snapshotUrl: "https://homosaurus.org/v5.nt",
      blurb: "Homosaurus (LGBTQ+ vocabulary)",
    },
    {
      name: "gnd",
      scheme: "gnd",
      license: "CC0",
      homepage: "https://lobid.org/gnd",
      snapshotUrl: "https://data.dnb.de/opendata/authorities-gnd_lds.nt.gz",
      blurb: "GND (German authority file)",
    },
  ];

  const BLANK_SOURCE: VocabSource = { name: "", scheme: "", license: "", homepage: "", snapshotUrl: "", suggestFlavor: "", suggestUrl: "", suggestDataset: "" };

  const SCOPE = "vocabsources";
  const POLL_MS = 4000;
  const readOnly = isReadOnly();

  let sources = $state<VocabSourceView[]>([]);
  let selected = $state(0);
  let busy = $state("");
  let error = $state("");
  let status = $state("");
  let newSource = $state<VocabSource>({ ...BLANK_SOURCE });
  let timer: ReturnType<typeof setInterval> | undefined;

  const hasActive = $derived(sources.some((s) => s.job?.status === "QUEUED" || s.job?.status === "RUNNING"));
  const isAdmin = $derived(($sessionStore?.roles ?? []).includes("admin"));
  const suggestions = $derived(SUGGESTED_SOURCES.filter((s) => !sources.some((v) => v.name === s.name)));

  onMount(() => {
    pushScope(SCOPE);
    const unbind = bindKeys(SCOPE, {
      j: { description: "next source", legend: "move", keyLabel: "j/k", handler: () => move(1) },
      k: { description: "previous source", hidden: true, handler: () => move(-1) },
      ArrowDown: { description: "next source", hidden: true, handler: () => move(1) },
      ArrowUp: { description: "previous source", hidden: true, handler: () => move(-1) },
      d: { description: "download / refresh the selected source", legend: "download", handler: () => void downloadSelected() },
      x: { description: "remove the selected snapshot", legend: "remove", handler: () => void removeSelected() },
      r: { description: "refresh the list", legend: "refresh", handler: () => void refresh() },
    });
    void refresh();
    timer = setInterval(() => {
      if (hasActive) void refresh();
    }, POLL_MS);
    return () => {
      unbind();
      popScope(SCOPE);
      clearInterval(timer);
    };
  });

  function move(delta: number): void {
    if (sources.length === 0) return;
    selected = Math.min(sources.length - 1, Math.max(0, selected + delta));
    document.querySelectorAll("tbody tr")[selected]?.scrollIntoView?.({ block: "nearest" });
  }

  async function refresh(): Promise<void> {
    try {
      sources = (await fetchVocabSources()).sources ?? [];
      selected = Math.min(selected, Math.max(0, sources.length - 1));
    } catch (e) {
      error = humanApiMessage(e, "loading sources failed");
    }
  }

  async function download(s: VocabSourceView): Promise<void> {
    busy = s.name;
    error = "";
    status = "";
    try {
      await downloadVocabSource(s.name);
      status = `${s.name} queued -- the worker downloads and installs it shortly`;
      await refresh();
    } catch (e) {
      error = humanApiMessage(e, "queuing the download failed");
    } finally {
      busy = "";
    }
  }

  async function remove(s: VocabSourceView): Promise<void> {
    if (!s.installed) return;
    busy = s.name;
    error = "";
    status = "";
    try {
      await removeVocabSnapshot(s.name);
      status = `${s.name} removed -- its terms left the index`;
      await refresh();
    } catch (e) {
      error = humanApiMessage(e, "removing the snapshot failed");
    } finally {
      busy = "";
    }
  }

  function downloadSelected(): void {
    if (readOnly) return;
    const s = sources[selected];
    if (s?.snapshotUrl) void download(s);
  }

  function removeSelected(): void {
    if (readOnly) return;
    const s = sources[selected];
    if (s?.installed) void remove(s);
  }

  function working(s: VocabSourceView): boolean {
    return s.job?.status === "QUEUED" || s.job?.status === "RUNNING";
  }

  /** Installs a hand-picked local dump file for the source -- the escape
   *  hatch when the publisher's site is down or the source has no URL. */
  async function upload(s: VocabSourceView, ev: Event): Promise<void> {
    const input = ev.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    if (!file) return;
    busy = s.name;
    error = "";
    status = "";
    try {
      const res = await uploadVocabSnapshot(s.name, file);
      status = `${s.name}: ${res.terms.toLocaleString()} terms installed from ${file.name}`;
      await refresh();
    } catch (e) {
      error = humanApiMessage(e, "the upload failed");
    } finally {
      busy = "";
      input.value = "";
    }
  }

  /** Registers a drop-in source (or a same-named override of a builtin). */
  async function register(src: VocabSource): Promise<void> {
    error = "";
    status = "";
    try {
      const clean = Object.fromEntries(Object.entries(src).filter(([k, v]) => v !== "" && k !== "blurb")) as unknown as VocabSource;
      await putVocabSource(clean);
      status = `${src.name} registered -- download its snapshot to install`;
      newSource = { ...BLANK_SOURCE };
      await refresh();
    } catch (e) {
      error = humanApiMessage(e, "registering the source failed");
    }
  }

  /** Deletes a registered source (a same-named builtin's shipped
   *  definition returns). */
  async function unregister(s: VocabSourceView): Promise<void> {
    error = "";
    status = "";
    try {
      await deleteVocabSource(s.name);
      status = `${s.name} deleted`;
      await refresh();
    } catch (e) {
      error = humanApiMessage(e, "deleting the source failed");
    }
  }
</script>

<main class="wide" id="main" tabindex="-1">
  <h1>Vocabularies</h1>
  <p class="muted intro">
    Public authority sources ready to use. <strong>Live</strong> sources answer the term picker's typeahead through
    their public APIs; <strong>downloadable</strong> sources install a snapshot into the local index for instant,
    offline search. Downloading again refreshes an installed snapshot in place.
  </p>

  <p aria-live="polite">
    {#if status}<span class="ok">{status}</span>{/if}
    {#if error}<span class="error">{error}</span>{/if}
  </p>

  {#if sources.length === 0}
    <p class="muted">No authority sources are registered.</p>
  {:else}
    <table>
      <thead>
        <tr>
          <th scope="col">Source</th>
          <th scope="col">Scheme</th>
          <th scope="col">Capabilities</th>
          <th scope="col">License</th>
          <th scope="col">Installed</th>
          <th scope="col">Status</th>
          <th scope="col">Actions</th>
        </tr>
      </thead>
      <tbody>
        {#each sources as s, i (s.name)}
          <tr class:selected={i === selected} onfocusin={() => (selected = i)}>
            <td>
              <span class="name">{s.name}</span>
              {#if s.homepage}<a class="home" href={s.homepage} target="_blank" rel="noreferrer">about</a>{/if}
            </td>
            <td class="mono">{s.scheme}</td>
            <td>
              {#if s.suggestUrl}<span class="badge">live</span>{/if}
              {#if s.snapshotUrl}<span class="badge">downloadable</span>{/if}
              {#if s.orphan}
                <span class="badge badge--orphan"
                  title="This vocabulary is installed, but no registered source describes it -- from an offline vocab-install or a registry reset. It can be removed; to manage it, register a source named {s.name} below.">
                  unregistered
                </span>
              {/if}
            </td>
            <td class="muted">{s.license ?? ""}</td>
            <td>
              {#if s.installed}
                {s.installed.terms.toLocaleString()} terms
                <span class="muted">({new Date(s.installed.installedAt).toLocaleDateString()})</span>
                <!-- How the scheme is served, so a memory profile has a per-row
                     answer: sidecar-backed schemes keep their terms
                     on disk, holding only a live-pick overlay resident. -->
                {#if s.sidecar}
                  <span class="badge" title="Served from sidecar artifacts on disk; {(s.residentTerms ?? 0).toLocaleString()} terms resident (live-pick overlay)">sidecar</span>
                {:else if s.residentTerms}
                  <span class="muted">· {s.residentTerms.toLocaleString()} resident</span>
                {/if}
              {:else if s.snapshotUrl}
                <span class="muted">not installed</span>
              {:else}
                <span class="muted">live only</span>
              {/if}
            </td>
            <td>
              {#if s.job}
                <span class="badge" data-status={s.job.status}>{s.job.status}</span>
                {#if s.job.error}<span class="error">{s.job.error}</span>{/if}
              {/if}
            </td>
            <td class="actions">
              {#if s.snapshotUrl && !readOnly}
                <button class="button" onclick={() => void download(s)} disabled={busy === s.name || working(s)}>
                  {working(s) ? "Working…" : s.installed ? "Refresh" : "Download"}
                </button>
              {/if}
              {#if s.installed && !readOnly}
                <button class="button button--quiet" onclick={() => void remove(s)} disabled={busy === s.name || working(s)}>
                  Remove
                </button>
              {/if}
              <!-- Upload and Delete both need a source record to act on; an orphan
                   row has none, so the server can only answer 404. -->
              {#if isAdmin && !readOnly && !s.orphan}
                <label class="button button--quiet upload-btn"
                  title="Install a local SKOS dump: .nt/.nq, optionally gzipped. Uploads are size-capped (512MB unless LCATD_VOCAB_UPLOAD_CAP_MB raises it) -- gzip large dumps.">
                  Upload… <input type="file" accept=".nt,.nq,.gz,.nt.gz,.nq.gz" onchange={(ev) => void upload(s, ev)} hidden disabled={busy === s.name || working(s)} />
                </label>
              {/if}
              {#if isAdmin && !s.builtin && !readOnly && !s.orphan}
                <button class="button button--quiet" onclick={() => void unregister(s)} disabled={busy === s.name || working(s)}
                  title="Delete this registered source definition (an installed snapshot must be removed first)">
                  Delete source
                </button>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
    {#if isAdmin && !readOnly}
      <details class="register">
        <summary>Register a drop-in source…</summary>
        <p class="note">
          A source needs a downloadable SKOS dump (<strong>N-Triples or N-Quads</strong>, optionally gzipped -- not
          zip, not Turtle) and/or a live typeahead API in one of the implemented dialects. Registering a builtin's
          name overrides it (delete the override to restore the shipped definition). Details:
          <code>docs/authority-sources.md</code>.
        </p>
        {#if suggestions.length > 0}
          <div class="suggested">
            <span class="muted">Suggested:</span>
            {#each suggestions as s (s.name)}
              <button class="button button--quiet srcbtn" title={s.snapshotUrl} onclick={() => void register(s)}>
                + {s.blurb}
              </button>
            {/each}
          </div>
        {/if}
        <form
          class="srcform"
          onsubmit={(ev) => {
            ev.preventDefault();
            void register($state.snapshot(newSource));
          }}
        >
          <input bind:value={newSource.name} aria-label="Source name" placeholder="name (e.g. mesh)" required />
          <input bind:value={newSource.scheme} aria-label="Vocab scheme" placeholder="scheme (e.g. mesh)" required />
          <input class="wide2" bind:value={newSource.snapshotUrl} aria-label="Snapshot URL" placeholder="snapshot URL (.nt / .nq, optionally .gz)" />
          <input bind:value={newSource.license} aria-label="License" placeholder="license (e.g. CC0)" />
          <input class="wide2" bind:value={newSource.homepage} aria-label="Homepage" placeholder="homepage URL" />
          <select bind:value={newSource.suggestFlavor} aria-label="Live typeahead dialect">
            <option value="">no live typeahead</option>
            <option value="suggest2">suggest2 (id.loc.gov)</option>
            <option value="wikidata">wikidata (wbsearchentities)</option>
            <option value="viaf">viaf (AutoSuggest)</option>
            <option value="searchfast">searchfast (OCLC fastsuggest)</option>
          </select>
          {#if newSource.suggestFlavor}
            <input class="wide2" bind:value={newSource.suggestUrl} aria-label="Suggest URL" placeholder="suggest API URL" />
            {#if newSource.suggestFlavor === "suggest2"}
              <input bind:value={newSource.suggestDataset} aria-label="Suggest dataset" placeholder="dataset (authorities/subjects)" />
            {/if}
          {/if}
          <button class="button" type="submit" disabled={!newSource.name.trim() || !newSource.scheme.trim()}>Register</button>
        </form>
      </details>
    {:else}
      <p class="note">
        Additional sources (GND, Getty, MeSH, Homosaurus, …) register as drop-in configs -- an admin does this from
        this screen (or <code>POST /v1/vocabsources</code>; see <code>docs/authority-sources.md</code>). Download and
        remove need the admin role too.
      </p>
    {/if}
  {/if}
</main>

<style>
  .intro {
    max-width: 46rem;
  }
  table {
    border-collapse: collapse;
    width: 100%;
    font-size: 0.9rem;
  }
  th,
  td {
    text-align: left;
    padding: 0.35rem 0.6rem;
    border-bottom: 1px solid var(--rule);
  }
  tbody tr.selected {
    background: var(--surface);
  }
  tbody tr.selected td:first-child {
    box-shadow: inset 3px 0 0 var(--accent);
  }
  .name {
    font-weight: 600;
  }
  .home {
    margin-left: 0.4rem;
    font-size: 0.8rem;
  }
  .mono {
    font-family: var(--mono);
    font-size: 0.82rem;
  }
  .badge {
    font-size: 0.72rem;
    font-weight: 700;
    border-radius: 999px;
    padding: 0.1em 0.7em;
    border: 1px solid var(--rule);
    margin-right: 0.25rem;
  }
  .badge[data-status="DONE"] {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--accent-ink);
  }
  .badge[data-status="FAILED"] {
    background: var(--danger);
    border-color: var(--danger);
    color: var(--danger-ink);
  }
  /* Provisional, not failed: a dashed edge, the same "shadowed, not erased"
     register the provenance rail uses for an overridden value. Tokenised ink on
     an untinted badge, so both themes hold AA. */
  .badge--orphan {
    color: var(--ink-muted);
    border-style: dashed;
  }
  .actions {
    white-space: nowrap;
  }
  .actions .button {
    margin-right: 0.35rem;
  }
  .note {
    font-size: 0.87rem;
    color: var(--ink-muted);
    max-width: 46rem;
    border-left: 3px solid var(--rule);
    padding-left: 0.7rem;
  }
  .register {
    margin-top: 0.9rem;
  }
  .register summary {
    cursor: pointer;
    color: var(--ink-muted);
    font-weight: 600;
  }
  .suggested {
    display: flex;
    gap: 0.4rem;
    align-items: center;
    flex-wrap: wrap;
    margin: 0.5rem 0;
    font-size: 0.85rem;
  }
  .srcbtn {
    font-size: 0.8rem;
    padding: 0.1em 0.7em;
  }
  .upload-btn {
    cursor: pointer;
  }
  .srcform {
    display: flex;
    gap: 0.45rem;
    align-items: center;
    flex-wrap: wrap;
    margin: 0.5rem 0 0.2rem;
  }
  .srcform input,
  .srcform select {
    font-size: 0.85rem;
    min-height: 1.9rem;
  }
  .srcform .wide2 {
    min-width: 22rem;
    font-family: var(--mono);
    font-size: 0.8rem;
  }
  .ok {
    color: var(--accent);
  }
</style>
