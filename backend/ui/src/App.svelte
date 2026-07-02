<script lang="ts">
  // Shell: boots from /config, resumes or establishes a session, and routes
  // by hash. Unknown paths fall back to the dashboard; unauthenticated users
  // land on #/login.
  import { onMount } from "svelte";
  import { loadConfig } from "./lib/config";
  import { getToken, handleOidcCallback, logout, session } from "./lib/auth";
  import { resolve, navigate, type RouteDef } from "./lib/router";
  import { configStore, sessionStore } from "./lib/stores";
  import KeyboardHelp from "./components/KeyboardHelp.svelte";
  import Login from "./screens/Login.svelte";
  import Dashboard from "./screens/Dashboard.svelte";
  import WorkSearch from "./screens/WorkSearch.svelte";
  import WorkEditor from "./screens/WorkEditor.svelte";
  import Queue from "./screens/Queue.svelte";

  const routes: RouteDef[] = [
    { name: "dashboard", pattern: "/" },
    { name: "login", pattern: "/login" },
    { name: "callback", pattern: "/callback" },
    { name: "works", pattern: "/works" },
    { name: "work", pattern: "/works/:id" },
    { name: "queue", pattern: "/queue" },
  ];

  let route = $state(resolve(routes, location.hash));
  let ready = $state(false);

  onMount(async () => {
    configStore.set(await loadConfig());
    if (route.name === "callback") {
      await handleOidcCallback();
    } else {
      await getToken(); // resume a refreshable session if one exists
    }
    sessionStore.set(session());
    ready = true;
    window.addEventListener("hashchange", () => {
      route = resolve(routes, location.hash);
    });
    route = resolve(routes, location.hash);
  });

  // Auth gate: signed-out users go to the login screen, signed-in users
  // never see it. Callback stays untouched while the exchange completes.
  $effect(() => {
    if (!ready || route.name === "callback") return;
    if (!$sessionStore && route.name !== "login") navigate("/login");
    else if ($sessionStore && route.name === "login") navigate("/");
  });

  async function signOut(): Promise<void> {
    await logout();
    sessionStore.set(null);
    navigate("/login");
  }
</script>

{#if !ready}
  <main><p class="muted">Loading…</p></main>
{:else if route.name === "callback"}
  <main><p class="muted">Completing sign-in…</p></main>
{:else if !$sessionStore || route.name === "login"}
  <Login config={$configStore} />
{:else}
  <header class="top">
    <a class="brand" href="#/">libcatalog</a>
    <nav aria-label="Primary">
      <a href="#/works" class:current={route.name === "works" || route.name === "work"}>Works</a>
      <a href="#/queue" class:current={route.name === "queue"}>Queue</a>
    </nav>
    <span class="side">
      <span class="who">{$sessionStore.email}</span>
      <button class="button button--quiet" onclick={signOut}>Sign out</button>
    </span>
  </header>
  {#if route.name === "work"}
    <WorkEditor workId={route.params.id} />
  {:else if route.name === "works"}
    <WorkSearch />
  {:else if route.name === "queue"}
    <Queue />
  {:else}
    <Dashboard session={$sessionStore} />
  {/if}
{/if}

<KeyboardHelp />

<style>
  .top {
    display: flex;
    align-items: baseline;
    gap: 1.25rem;
    padding: 0.8rem 1.5rem;
    border-bottom: 1px solid var(--rule);
  }
  .brand {
    font-weight: 800;
    text-decoration: none;
    color: var(--ink);
  }
  nav {
    display: flex;
    gap: 1rem;
    flex: 1;
  }
  nav a {
    text-decoration: none;
    color: var(--ink-muted);
    padding-bottom: 0.1em;
    border-bottom: 2px solid transparent;
  }
  nav a:hover {
    color: var(--ink);
  }
  nav a.current {
    color: var(--ink);
    border-bottom-color: var(--accent);
  }
  .side {
    display: inline-flex;
    align-items: center;
    gap: 0.75rem;
  }
  .who {
    color: var(--ink-muted);
    font-size: 0.9rem;
  }
</style>
