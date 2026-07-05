<script lang="ts">
  // Shell: boots from /config, resumes or establishes a session, and routes
  // by hash. Unknown paths fall back to the dashboard; unauthenticated users
  // land on #/login.
  import { onMount } from "svelte";
  import { loadConfig } from "./lib/config";
  import { canAdmin, getToken, handleOidcCallback, logout, session } from "./lib/auth";
  import { initTheme, toggleTheme, type Theme } from "./lib/theme";
  import { resolve, navigate, type RouteDef } from "./lib/router";
  import { configStore, sessionStore } from "./lib/stores";
  import { bindKeys, GLOBAL_SCOPE } from "./lib/keyboard";
  import { resetScreenStates } from "./lib/screenState.svelte";
  import KbdLegend from "./components/KbdLegend.svelte";
  import KeyboardHelp from "./components/KeyboardHelp.svelte";
  import Login from "./screens/Login.svelte";
  import Dashboard from "./screens/Dashboard.svelte";
  import WorkSearch from "./screens/WorkSearch.svelte";
  import WorkEditor from "./screens/WorkEditor.svelte";
  import Queue from "./screens/Queue.svelte";
  import Promotions from "./screens/Promotions.svelte";
  import Authorities from "./screens/Authorities.svelte";
  import AuthorityEditor from "./screens/AuthorityEditor.svelte";
  import BatchOps from "./screens/BatchOps.svelte";
  import Macros from "./screens/Macros.svelte";
  import Exports from "./screens/Exports.svelte";
  import VocabSources from "./screens/VocabSources.svelte";
  import CopyCat from "./screens/CopyCat.svelte";
  import NewRecord from "./screens/NewRecord.svelte";
  import Duplicates from "./screens/Duplicates.svelte";
  import Withdrawals from "./screens/Withdrawals.svelte";
  import Profiles from "./screens/Profiles.svelte";
  import CommandPalette from "./components/CommandPalette.svelte";

  const routes: RouteDef[] = [
    { name: "dashboard", pattern: "/" },
    { name: "login", pattern: "/login" },
    { name: "callback", pattern: "/callback" },
    { name: "works", pattern: "/works" },
    { name: "work", pattern: "/works/:id" },
    { name: "authorities", pattern: "/authorities" },
    { name: "authority", pattern: "/authorities/:id" },
    { name: "vocabsources", pattern: "/vocabularies" },
    { name: "batch", pattern: "/batch" },
    { name: "macros", pattern: "/macros" },
    { name: "exports", pattern: "/exports" },
    { name: "copycat", pattern: "/copycat" },
    { name: "newrecord", pattern: "/copycat/new" },
    { name: "duplicates", pattern: "/duplicates" },
    { name: "withdrawals", pattern: "/withdrawals" },
    { name: "queue", pattern: "/queue" },
    { name: "promotions", pattern: "/promotions" },
    { name: "profiles", pattern: "/profiles" },
  ];

  let route = $state(resolve(routes, location.hash));
  let theme = $state<Theme>(initTheme());
  let ready = $state(false);
  let paletteOpen = $state(false);

  onMount(() => {
    void boot();
    return bindGlobalKeys();
  });

  // Signed-in-only global keys: the palette chord plus "g <letter>" jumps to
  // every screen, including the ones the top nav leaves out.
  function bindGlobalKeys(): () => void {
    const goTo: Record<string, [string, string]> = {
      "g d": ["/", "go to the dashboard"],
      "g w": ["/works", "go to works"],
      "g a": ["/authorities", "go to authorities"],
      "g v": ["/vocabularies", "go to vocabularies"],
      "g q": ["/queue", "go to the queue"],
      "g b": ["/batch", "go to batch operations"],
      "g m": ["/macros", "go to macros"],
      "g e": ["/exports", "go to exports"],
      "g i": ["/copycat", "go to import"],
      "g u": ["/duplicates", "go to duplicates"],
      "g t": ["/withdrawals", "go to withdrawals"],
      "g p": ["/promotions", "go to promotions"],
    };
    const specs: Parameters<typeof bindKeys>[1] = {
      "mod+k": {
        description: "open the command palette",
        legend: "palette",
        handler: () => {
          if ($sessionStore) paletteOpen = !paletteOpen;
        },
      },
    };
    for (const [key, [path, description]] of Object.entries(goTo)) {
      specs[key] = {
        description,
        legend: "go to screen",
        handler: () => {
          if ($sessionStore) navigate(path);
        },
      };
    }
    return bindKeys(GLOBAL_SCOPE, specs);
  }

  async function boot(): Promise<void> {
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
  }

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
    resetScreenStates();
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
      <a href="#/authorities" class:current={route.name === "authorities" || route.name === "authority"}>Authorities</a>
      <a href="#/vocabularies" class:current={route.name === "vocabsources"}>Vocabularies</a>
      <a href="#/batch" class:current={route.name === "batch"}>Batch</a>
      <a href="#/macros" class:current={route.name === "macros"}>Macros</a>
      <a href="#/exports" class:current={route.name === "exports"}>Exports</a>
      <a href="#/copycat" class:current={route.name === "copycat"}>Import</a>
      <a href="#/duplicates" class:current={route.name === "duplicates"}>Duplicates</a>
      <a href="#/withdrawals" class:current={route.name === "withdrawals"}>Withdrawals</a>
      <a href="#/queue" class:current={route.name === "queue"}>Queue</a>
      {#if canAdmin($sessionStore)}
        <a href="#/profiles" class:current={route.name === "profiles"}>Profiles</a>
      {/if}
    </nav>
    <span class="side">
      <span class="who">{$sessionStore.email}</span>
      <button
        class="button button--quiet"
        onclick={() => (theme = toggleTheme())}
        aria-pressed={theme === "dark"}
        title="Switch to {theme === 'dark' ? 'light' : 'dark'} mode"
      >
        {theme === "dark" ? "Light" : "Dark"} mode
      </button>
      <button class="button button--quiet" onclick={signOut}>Sign out</button>
    </span>
  </header>
  {#if $configStore.readOnly}
    <div class="readonly-banner" role="status">
      {#if $configStore.sandbox}
        Sandbox demo — edit freely; your changes render but are never saved (refresh to reset).
      {:else}
        Read-only demo — explore freely; edits and publishes are previewed but not saved.
      {/if}
    </div>
  {/if}
  {#if route.name === "work"}
    <!-- Keyed so a direct hash jump between works remounts a fresh editor
         session (staged ops and drafts are per-work). -->
    {#key route.params.id}
      <WorkEditor workId={route.params.id} />
    {/key}
  {:else if route.name === "works"}
    <WorkSearch />
  {:else if route.name === "authority"}
    {#key route.params.id}
      <AuthorityEditor authorityId={route.params.id} />
    {/key}
  {:else if route.name === "authorities"}
    <Authorities />
  {:else if route.name === "vocabsources"}
    <VocabSources />
  {:else if route.name === "batch"}
    <BatchOps initialMacro={route.query.get("macro") ?? ""} />
  {:else if route.name === "macros"}
    <Macros />
  {:else if route.name === "copycat"}
    <CopyCat batchId={route.query.get("batch") ?? ""} />
  {:else if route.name === "newrecord"}
    <NewRecord />
  {:else if route.name === "duplicates"}
    <Duplicates />
  {:else if route.name === "withdrawals"}
    <Withdrawals />
  {:else if route.name === "exports"}
    <Exports
      initialKind={route.query.get("kind") ?? ""}
      initialQuery={route.query.get("q") ?? ""}
      initialIds={route.query.get("ids") ?? ""}
      initialSavedQuery={route.query.get("sq") ?? ""}
    />
  {:else if route.name === "queue"}
    <Queue />
  {:else if route.name === "promotions"}
    <Promotions />
  {:else if route.name === "profiles"}
    <Profiles />
  {:else}
    <Dashboard session={$sessionStore} />
  {/if}
{/if}

{#if paletteOpen}
  <CommandPalette onclose={() => (paletteOpen = false)} />
{/if}

{#if ready && $sessionStore}
  <KbdLegend />
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
  .readonly-banner {
    padding: 0.45rem 1.5rem;
    font-size: 0.85rem;
    font-weight: 600;
    color: var(--ink);
    background: color-mix(in srgb, var(--accent) 14%, transparent);
    border-bottom: 1px solid var(--accent);
    text-align: center;
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
