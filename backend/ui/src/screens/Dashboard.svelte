<script lang="ts">
  // Landing screen: who is signed in, where to go, and -- for moderators --
  // how much review work is waiting. Single letters jump straight to a
  // screen (the same letters the global "g" sequences use).
  import { onMount } from "svelte";
  import { canModerate, type Session } from "../lib/auth";
  import { fetchQueue } from "../lib/api";
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
  ];

  let pending = $state<number | null>(null);
  let queueError = $state("");

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
    return () => {
      unbind();
      popScope(SCOPE);
    };
  });

  async function loadPending(): Promise<void> {
    if (!canModerate(session)) return;
    try {
      const page = await fetchQueue({ status: "PENDING" });
      pending = page.items.length;
    } catch {
      queueError = "queue unavailable";
    }
  }
</script>

<main>
  <h1>Dashboard</h1>
  <p>
    Signed in as <strong>{session.email}</strong>
    {#if session.roles.length > 0}
      <span class="muted">({session.roles.join(", ")})</span>
    {/if}
  </p>

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
    </ul>
  </nav>
</main>

<style>
  .cards {
    list-style: none;
    padding: 0;
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(16rem, 1fr));
    gap: 1rem;
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
