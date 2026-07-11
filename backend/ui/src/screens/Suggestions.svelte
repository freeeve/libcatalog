<script lang="ts">
  // The patron-suggestion policy editor: the opt-in switch, the
  // free-text mode, and the allowlist of controlled vocabularies patrons may
  // propose terms from. Admin-only, like its config routes. Off by default;
  // catalogers add any term through the review queue regardless, and every save
  // is audited. Enforcement lives in the backend (suggest.Service); this screen
  // only edits the stored policy over GET/PUT /v1/config/suggestions.
  import { onMount } from "svelte";
  import { fetchSuggestionPolicy, fetchVocabSources, humanApiMessage, putSuggestionPolicy } from "../lib/api";
  import type { FreeTextMode } from "../lib/types";

  let loading = $state(true);
  let saving = $state(false);
  let status = $state("");
  let error = $state("");

  let enabled = $state(false);
  let freeText = $state<FreeTextMode>("off");
  // The allowlist as a set of scheme tokens; empty means every loaded scheme.
  let selected = $state<Set<string>>(new Set());
  // Options are the registered vocabularies, unioned with any scheme the saved
  // policy already names -- so a scheme absent from the registry still shows and
  // round-trips rather than silently vanishing on the next save.
  let schemeOptions = $state<string[]>([]);

  onMount(() => void load());

  async function load(): Promise<void> {
    loading = true;
    error = "";
    try {
      const [policy, vs] = await Promise.all([
        fetchSuggestionPolicy(),
        fetchVocabSources().catch(() => ({ sources: [] })),
      ]);
      enabled = policy.enabled;
      freeText = policy.freeText;
      selected = new Set(policy.schemes ?? []);
      const opts = new Set<string>();
      for (const s of vs.sources ?? []) if (s.scheme) opts.add(s.scheme);
      for (const s of policy.schemes ?? []) opts.add(s);
      schemeOptions = [...opts].sort();
    } catch (e) {
      error = humanApiMessage(e, "loading the policy failed");
    } finally {
      loading = false;
    }
  }

  function toggleScheme(scheme: string): void {
    const next = new Set(selected);
    if (next.has(scheme)) next.delete(scheme);
    else next.add(scheme);
    selected = next;
  }

  async function save(): Promise<void> {
    saving = true;
    error = "";
    status = "";
    try {
      const saved = await putSuggestionPolicy({ enabled, freeText, schemes: [...selected] });
      enabled = saved.enabled;
      freeText = saved.freeText;
      selected = new Set(saved.schemes ?? []);
      status = "Saved.";
    } catch (e) {
      error = humanApiMessage(e, "saving the policy failed");
    } finally {
      saving = false;
    }
  }
</script>

<main class="wide" id="main" tabindex="-1">
  <h1>Suggestion policy</h1>
  <p class="lede muted">
    Whether the public catalog accepts anonymous subject and tag suggestions, and what patrons may propose. Off by
    default; catalogers add any term through the review queue regardless of this, and every save is recorded in the
    audit log.
  </p>
  <p aria-live="polite" class="status">
    {#if loading}<span class="muted">Loading…</span>{/if}
    {#if status}<span class="ok">{status}</span>{/if}
    {#if error}<span class="error">{error}</span>{/if}
  </p>

  {#if !loading}
    <form
      onsubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <label class="toggle">
        <input type="checkbox" bind:checked={enabled} />
        Accept patron suggestions
      </label>

      <fieldset disabled={!enabled}>
        <legend>Free-text tags</legend>
        <label><input type="radio" name="freetext" value="off" bind:group={freeText} /> Not accepted</label>
        <label><input type="radio" name="freetext" value="existing" bind:group={freeText} /> Only tags already in use</label>
        <label><input type="radio" name="freetext" value="any" bind:group={freeText} /> Any tag</label>
      </fieldset>

      <fieldset disabled={!enabled}>
        <legend>Controlled vocabularies</legend>
        <p class="hint muted">
          Which vocabularies patrons may propose terms from. Select none to allow every loaded vocabulary.
        </p>
        {#if schemeOptions.length === 0}
          <p class="muted">No vocabularies are registered yet.</p>
        {:else}
          <ul class="schemes">
            {#each schemeOptions as scheme (scheme)}
              <li>
                <label>
                  <input type="checkbox" checked={selected.has(scheme)} onchange={() => toggleScheme(scheme)} />
                  {scheme}
                </label>
              </li>
            {/each}
          </ul>
        {/if}
      </fieldset>

      <div class="actions">
        <button class="button" type="submit" disabled={saving}>{saving ? "Saving…" : "Save policy"}</button>
      </div>
    </form>
  {/if}
</main>

<style>
  .lede {
    margin: 0.2rem 0 0.6rem;
    max-width: 46rem;
  }
  .status {
    min-height: 1.2em;
    font-size: 0.85rem;
    margin: 0.3rem 0;
  }
  form {
    max-width: 40rem;
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }
  .toggle {
    font-weight: 600;
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
  }
  fieldset {
    border: 1px solid var(--rule, #d9d5cb);
    border-radius: 6px;
    padding: 0.6rem 0.9rem 0.8rem;
  }
  fieldset:disabled {
    opacity: 0.55;
  }
  legend {
    font-weight: 600;
    padding: 0 0.35rem;
  }
  fieldset label {
    display: block;
    padding: 0.15rem 0;
  }
  .hint {
    margin: 0.1rem 0 0.5rem;
    font-size: 0.82rem;
  }
  .schemes {
    list-style: none;
    margin: 0;
    padding: 0;
    columns: 2;
  }
  .schemes label {
    font-family: var(--mono);
    font-size: 0.85rem;
  }
  .actions {
    margin-top: 0.2rem;
  }
  .ok {
    color: var(--accent);
  }
</style>
