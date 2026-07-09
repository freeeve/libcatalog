<script lang="ts">
  // Shell: boots from /config, resumes or establishes a session, and routes
  // by hash. Unknown paths fall back to the dashboard; unauthenticated users
  // land on #/login.
  import { onMount } from "svelte";
  import { loadConfig } from "./lib/config";
  import { CALLBACK_PATH, canAdmin, getToken, handleOidcCallback, logout, onSessionExpired, session } from "./lib/auth";
  import { initTheme, toggleTheme, type Theme } from "./lib/theme";
  import { resolve, navigate, type RouteDef, confirmLeave } from "./lib/router";
  import { configStore, sessionStore } from "./lib/stores";
  import { bindKeys, GLOBAL_SCOPE } from "./lib/keyboard";
  import { resetScreenStates } from "./lib/screenState.svelte";
  import { clearAllLocalDrafts } from "./lib/localdraft";
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
  import ReauthDialog from "./components/ReauthDialog.svelte";

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
  // Session-expiry re-auth (tasks/223): when the live session dies, the
  // screen stays mounted (staged edits survive) under a sign-in overlay;
  // the header identity clears immediately. expiredEmail prefills the form.
  let reauth = $state(false);
  let expiredEmail = $state("");

  onMount(() => {
    void boot();
    const offExpired = onSessionExpired(() => {
      if (!$sessionStore || reauth) return;
      expiredEmail = $sessionStore.email;
      sessionStore.set(null);
      reauth = true;
    });
    const unbind = bindGlobalKeys();
    return () => {
      offExpired();
      unbind();
    };
  });

  /** A successful re-auth resumes in place: identity back, overlay gone,
   *  no navigation and no screen-state reset. */
  function resumeSession(): void {
    sessionStore.set(session());
    reauth = false;
  }

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
    if (location.pathname === CALLBACK_PATH) {
      // Real-path OIDC redirect: the issuer returns to /_auth/callback?code=...
      // with an empty hash, so the hash router never reaches the callback
      // route. Complete the exchange here, then restore the hash root so
      // normal routing (and the auth gate) takes over.
      await handleOidcCallback();
      history.replaceState(null, "", location.origin + "/#/");
    } else if (route.name === "callback") {
      await handleOidcCallback();
    } else {
      await getToken(); // resume a refreshable session if one exists
    }
    sessionStore.set(session());
    ready = true;
    // A screen holding unsaved work registers a leave guard (tasks/199):
    // a denied navigation restores the previous hash; the restore fires a
    // second hashchange that no-ops on the equality check, so the mounted
    // screen keeps its state.
    let currentHash = location.hash;
    window.addEventListener("hashchange", () => {
      if (location.hash === currentHash) return;
      if (!confirmLeave()) {
        location.hash = currentHash;
        return;
      }
      currentHash = location.hash;
      route = resolve(routes, location.hash);
    });
    route = resolve(routes, location.hash);
  }

  // Auth gate: signed-out users go to the login screen, signed-in users
  // never see it. Callback stays untouched while the exchange completes,
  // and an expired session mid-screen re-auths in place instead of routing
  // away (tasks/223). The hash the gate bounced is stashed so signing in
  // returns there -- a reload mid-record must not strand the cataloger on
  // the dashboard (tasks/225).
  $effect(() => {
    if (!ready || route.name === "callback" || reauth) return;
    if (!$sessionStore && route.name !== "login") {
      if (location.hash && location.hash !== "#/" && !location.hash.startsWith("#/login")) {
        sessionStorage.setItem("lcat-return-to", location.hash);
      }
      navigate("/login");
    } else if ($sessionStore && route.name === "login") {
      const back = sessionStorage.getItem("lcat-return-to");
      sessionStorage.removeItem("lcat-return-to");
      navigate(back ?? "/");
    }
  });

  async function signOut(): Promise<void> {
    await logout();
    sessionStore.set(null);
    reauth = false;
    resetScreenStates();
    // Shared terminals: an explicit sign-out must not leak one cataloger's
    // staged work into the next session (tasks/225).
    clearAllLocalDrafts();
    navigate("/login");
  }
</script>

{#if !ready}
  <main><p class="muted">Loading…</p></main>
{:else if route.name === "callback"}
  <main><p class="muted">Completing sign-in…</p></main>
{:else if (!$sessionStore && !reauth) || route.name === "login"}
  <Login config={$configStore} />
{:else}
  <header class="top">
    <a class="brand" href="#/">libcat</a>
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
      {#if $sessionStore}
        <span class="who">{$sessionStore.email}</span>
      {:else}
        <!-- Deliberately NOT .who: nothing may read as a signed-in identity
             once the session died (tasks/223). -->
        <span class="expired">session expired</span>
      {/if}
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
  {:else if $sessionStore}
    <Dashboard session={$sessionStore} />
  {/if}
  {#if reauth}
    <ReauthDialog config={$configStore} email={expiredEmail} onresume={resumeSession} onsignout={() => void signOut()} />
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
  .expired {
    color: var(--danger);
    font-size: 0.9rem;
    font-weight: 600;
  }
</style>
