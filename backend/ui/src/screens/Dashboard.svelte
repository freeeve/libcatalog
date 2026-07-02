<script lang="ts">
  // Landing screen: who is signed in, where to go, and -- for moderators --
  // how much review work is waiting.
  import { onMount } from "svelte";
  import { canModerate, type Session } from "../lib/auth";
  import { fetchQueue } from "../lib/api";

  let { session }: { session: Session } = $props();

  let pending = $state<number | null>(null);
  let queueError = $state("");

  onMount(async () => {
    if (!canModerate(session)) return;
    try {
      const page = await fetchQueue({ status: "PENDING" });
      pending = page.items.length;
    } catch {
      queueError = "queue unavailable";
    }
  });
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
