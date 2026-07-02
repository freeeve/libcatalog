<script lang="ts">
  // Sign-in: local email/password when the deployment has built-in users,
  // an SSO button when an OIDC issuer is configured -- or both.
  import { loginLocal, startOidcLogin, session } from "../lib/auth";
  import { navigate } from "../lib/router";
  import { sessionStore } from "../lib/stores";
  import type { ClientConfig } from "../lib/types";

  let { config }: { config: ClientConfig } = $props();

  let email = $state("");
  let password = $state("");
  let error = $state("");
  let busy = $state(false);

  async function submit(ev: SubmitEvent): Promise<void> {
    ev.preventDefault();
    error = "";
    busy = true;
    try {
      await loginLocal(email, password);
      sessionStore.set(session());
      navigate("/");
    } catch (e) {
      error = e instanceof Error ? e.message : "login failed";
    } finally {
      busy = false;
    }
  }
</script>

<main class="login">
  <h1>libcatalog</h1>
  <p class="muted">Cataloging sign-in</p>

  {#if config.localAuth}
    <form onsubmit={submit} aria-label="Local sign-in">
      <label for="login-email">Email</label>
      <input id="login-email" type="email" autocomplete="username" required bind:value={email} />
      <label for="login-password">Password</label>
      <input id="login-password" type="password" autocomplete="current-password" required bind:value={password} />
      {#if error}
        <p class="error" role="alert">{error}</p>
      {/if}
      <button class="button" type="submit" disabled={busy}>{busy ? "Signing in…" : "Sign in"}</button>
    </form>
  {/if}

  {#if config.oidc}
    {#if config.localAuth}<p class="muted or">or</p>{/if}
    <button class="button button--quiet" onclick={() => startOidcLogin()}>Sign in with SSO</button>
  {/if}

  {#if !config.localAuth && !config.oidc}
    <p class="error">No sign-in method is configured on this deployment.</p>
  {/if}
</main>

<style>
  .login {
    max-width: 22rem;
    padding-top: 4rem;
  }
  form {
    display: grid;
    gap: 0.35rem;
    margin: 1rem 0;
  }
  label {
    font-size: 0.85rem;
    font-weight: 600;
    margin-top: 0.5rem;
  }
  .button {
    margin-top: 0.9rem;
    justify-self: start;
  }
  .or {
    margin: 0.75rem 0 0.5rem;
  }
</style>
