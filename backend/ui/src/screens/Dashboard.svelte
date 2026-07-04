<script lang="ts">
  // Landing screen: who is signed in, the catalog at a glance (work count,
  // installed vocabularies, waiting review work), and where to go. Single
  // letters jump straight to a screen (the same letters the global "g"
  // sequences use). Each stat tile is a link; a tile whose endpoint fails
  // or sits above the viewer's role simply stays away.
  import { onMount } from "svelte";
  import { canAdmin, canModerate, canPublish, type Session } from "../lib/auth";
  import {
    fetchCopycatBatches,
    fetchDuplicates,
    fetchQueue,
    fetchStats,
    fetchVocabSources,
    fetchWithdrawn,
    fetchWorks,
  } from "../lib/api";
  import type { MonthStats } from "../lib/types";
  import { bindKeys, popScope, pushScope, type BindingSpec } from "../lib/keyboard";
  import { navigate } from "../lib/router";

  let { session }: { session: Session } = $props();

  const SCOPE = "dashboard";
  const JUMPS: [string, string, string][] = [
    ["w", "/works", "go to works"],
    ["a", "/authorities", "go to authorities"],
    ["q", "/queue", "go to the queue"],
    ["b", "/batch", "go to batch operations"],
    ["m", "/macros", "go to macros"],
    ["e", "/exports", "go to exports"],
    ["i", "/copycat", "go to import"],
    ["u", "/duplicates", "go to duplicates"],
    ["p", "/promotions", "go to promotions"],
    ["t", "/withdrawals", "go to withdrawals"],
  ];

  /** Tile order on the glance row. */
  const STAT_ORDER = ["#/works", "#/vocabularies", "#/copycat", "#/duplicates", "#/withdrawals"];

  interface Stat {
    label: string;
    href: string;
    value: number;
    /** Quiet second line ("3 installed", "merge candidates"). */
    sub?: string;
    /** Attention tiles get the accent rail when nonzero (waiting work). */
    attention?: boolean;
  }

  let stats = $state<Stat[]>([]);
  let pending = $state<number | null>(null);
  let queueError = $state("");

  /** Editing-activity rollup (librarian only); the section stays hidden on
   *  error or when the viewer can't see it. */
  const currentMonth = new Date().toISOString().slice(0, 7);
  let activityMonth = $state(currentMonth);
  let activity = $state<MonthStats | null>(null);
  let activityError = $state("");

  onMount(() => {
    pushScope(SCOPE);
    const specs: Record<string, BindingSpec> = {};
    for (const [key, path, description] of JUMPS) {
      specs[key] = {
        description,
        legend: "jump to screen",
        keyLabel: "w/a/q/…",
        hidden: key !== "w",
        handler: () => navigate(path),
      };
    }
    const unbind = bindKeys(SCOPE, specs);
    void loadPending();
    void loadStats();
    void loadActivity();
    return () => {
      unbind();
      popScope(SCOPE);
    };
  });

  async function loadActivity(): Promise<void> {
    if (!canPublish(session)) return;
    activityError = "";
    try {
      activity = await fetchStats(activityMonth);
    } catch {
      activity = null;
      activityError = "activity unavailable";
    }
  }

  async function loadPending(): Promise<void> {
    if (!canModerate(session)) return;
    try {
      const page = await fetchQueue({ status: "PENDING" });
      pending = page.items.length;
    } catch {
      queueError = "queue unavailable";
    }
  }

  function upsert(s: Stat): void {
    stats = [...stats.filter((x) => x.href !== s.href), s].sort(
      (a, b) => STAT_ORDER.indexOf(a.href) - STAT_ORDER.indexOf(b.href),
    );
  }

  /** Fires the count queries concurrently; each tile appears as its number
   *  lands, in a stable order. Failures just leave the tile out. */
  function loadStats(): void {
    if (!canPublish(session)) return;
    fetchWorks("", 1).then(
      (page) => upsert({ label: "Works", href: "#/works", value: page.total }),
      () => {},
    );
    fetchVocabSources().then((res) => {
      const installed = (res.sources ?? []).filter((s) => s.installed);
      const terms = installed.reduce((n, s) => n + (s.installed?.terms ?? 0), 0);
      upsert({
        label: "Vocabulary terms",
        href: "#/vocabularies",
        value: terms,
        sub: `${installed.length} installed vocabular${installed.length === 1 ? "y" : "ies"}`,
      });
    }, () => {});
    fetchCopycatBatches().then((res) => {
      const staged = (res.batches ?? []).filter((b) => b.status === "STAGED").length;
      upsert({ label: "Staged batches", href: "#/copycat", value: staged, sub: "awaiting review", attention: true });
    }, () => {});
    fetchDuplicates().then(
      (res) => upsert({ label: "Duplicate groups", href: "#/duplicates", value: (res.groups ?? []).length, sub: "merge candidates", attention: true }),
      () => {},
    );
    fetchWithdrawn().then(
      (res) => upsert({ label: "Withdrawals", href: "#/withdrawals", value: (res.works ?? []).length, sub: "gone from their feed", attention: true }),
      () => {},
    );
  }
</script>

<main class="wide">
  <h1>Dashboard</h1>
  <p>
    Signed in as <strong>{session.email}</strong>
    {#if session.roles.length > 0}
      <span class="muted">({session.roles.join(", ")})</span>
    {/if}
  </p>

  {#if stats.length > 0}
    <ul class="stats" aria-label="Catalog at a glance">
      {#each stats as s (s.href)}
        <li>
          <a href={s.href} class:attention={s.attention && s.value > 0}>
            <span class="stat-label">{s.label}</span>
            <span class="stat-value">{s.value.toLocaleString()}</span>
            {#if s.sub}<span class="stat-sub muted">{s.sub}</span>{/if}
          </a>
        </li>
      {/each}
    </ul>
  {/if}

  {#if canPublish(session)}
    <section class="activity" aria-label="Editing activity">
      <div class="activity-head">
        <h2>Editing activity</h2>
        <label class="month">
          <span class="muted">Month</span>
          <input
            type="month"
            bind:value={activityMonth}
            max={currentMonth}
            onchange={() => void loadActivity()}
          />
        </label>
      </div>
      {#if activityError}
        <p class="muted">{activityError}</p>
      {:else if activity && activity.total > 0}
        <p class="muted summary">
          {activity.total.toLocaleString()} edit{activity.total === 1 ? "" : "s"}
          by {activity.actors} catalog{activity.actors === 1 ? "er" : "ers"}
          across {activity.works.toLocaleString()} work{activity.works === 1 ? "" : "s"}.
        </p>
        <table class="catalogers">
          <thead>
            <tr>
              <th scope="col">Cataloger</th>
              <th scope="col" class="num">Edits</th>
              <th scope="col" class="num">Works</th>
              <th scope="col" class="num">Sessions</th>
              <th scope="col" class="num">Days</th>
              <th scope="col">Last active</th>
            </tr>
          </thead>
          <tbody>
            {#each activity.perActor as a (a.actor)}
              <tr>
                <td>{a.actor}</td>
                <td class="num">{a.total.toLocaleString()}</td>
                <td class="num">{a.works.toLocaleString()}</td>
                <td class="num">{a.sessions.length.toLocaleString()}</td>
                <td class="num">{a.activeDays.toLocaleString()}</td>
                <td class="muted">{new Date(a.last).toLocaleString()}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {:else}
        <p class="muted">No recorded activity in {activityMonth}.</p>
      {/if}
    </section>
  {/if}

  <nav aria-label="Sections">
    <ul class="cards">
      <li>
        <a href="#/works">
          <h2>Work search</h2>
          <p class="muted">Find and open catalog records.</p>
        </a>
      </li>
      {#if canModerate(session)}
        <li>
          <a href="#/queue">
            <h2>Review queue</h2>
            <p class="muted">
              {#if pending !== null}
                {pending} pending suggestion{pending === 1 ? "" : "s"}
              {:else if queueError}
                {queueError}
              {:else}
                Loading…
              {/if}
            </p>
          </a>
        </li>
        <li>
          <a href="#/promotions">
            <h2>Tag promotions</h2>
            <p class="muted">Fold community tags into controlled vocabulary.</p>
          </a>
        </li>
      {/if}
      {#if canAdmin(session)}
        <li>
          <a href="#/profiles">
            <h2>Editing profiles</h2>
            <p class="muted">Edit the field definitions the editor renders from.</p>
          </a>
        </li>
      {/if}
    </ul>
  </nav>
</main>

<style>
  /* The glance row: hero numbers in text ink (no decorative color), label
     above, quiet context below. Attention tiles carry the accent rail only
     when there is actually waiting work. */
  .stats {
    list-style: none;
    padding: 0;
    margin: 1rem 0 1.4rem;
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(11rem, 1fr));
    gap: 0.8rem;
    max-width: 72rem;
  }
  .stats a {
    display: flex;
    flex-direction: column;
    gap: 0.1rem;
    border: 1px solid var(--rule);
    border-radius: 8px;
    padding: 0.65rem 0.95rem 0.7rem;
    text-decoration: none;
    color: inherit;
  }
  .stats a:hover {
    border-color: var(--accent);
  }
  .stats a.attention {
    box-shadow: inset 3px 0 0 var(--accent);
  }
  .stat-label {
    font-size: 0.72rem;
    font-weight: 650;
    text-transform: uppercase;
    letter-spacing: 0.07em;
    color: var(--ink-muted);
  }
  .stat-value {
    font-size: 1.9rem;
    font-weight: 650;
    line-height: 1.15;
    font-variant-numeric: tabular-nums;
  }
  .stat-sub {
    font-size: 0.78rem;
  }
  .activity {
    max-width: 72rem;
    margin: 0 0 1.6rem;
  }
  .activity-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 1rem;
    flex-wrap: wrap;
  }
  .activity-head h2 {
    margin: 0.2rem 0;
    color: var(--accent);
  }
  .activity .month {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    font-size: 0.85rem;
  }
  .activity .month input {
    font: inherit;
    color: var(--ink);
    background: var(--bg);
    border: 1px solid var(--rule);
    border-radius: 4px;
    padding: 0.3em 0.4em;
  }
  .summary {
    margin: 0.2rem 0 0.6rem;
  }
  .catalogers {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.9rem;
  }
  .catalogers th,
  .catalogers td {
    text-align: left;
    padding: 0.4rem 0.6rem;
    border-bottom: 1px solid var(--rule);
  }
  .catalogers th {
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--ink-muted);
  }
  .catalogers .num {
    text-align: right;
    font-variant-numeric: tabular-nums;
  }
  .cards {
    list-style: none;
    padding: 0;
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(16rem, 1fr));
    gap: 1rem;
    max-width: 72rem;
  }
  .cards a {
    display: block;
    border: 1px solid var(--rule);
    border-radius: 8px;
    padding: 0.75rem 1.1rem;
    text-decoration: none;
    color: inherit;
  }
  .cards a:hover {
    border-color: var(--accent);
  }
  .cards h2 {
    margin: 0.2rem 0;
    color: var(--accent);
  }
  .cards p {
    margin: 0.2rem 0 0.4rem;
  }
</style>
