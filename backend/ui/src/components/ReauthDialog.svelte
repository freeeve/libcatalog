<script lang="ts">
  // Session-expiry re-auth: overlays the still-mounted screen
  // when the live session dies (token aged out, sibling-tab sign-out), so
  // staged edits survive. Signing in here is a resumption -- the shell
  // restores the identity and closes the overlay without navigating or
  // resetting screen state. OIDC deployments get a redirect button instead;
  // the redirect unloads the page, which is the issuer flow's cost.
  import { loginLocal, session, startOidcLogin } from "../lib/auth";
  import type { ClientConfig } from "../lib/types";
  import Modal from "./Modal.svelte";

  let {
    config,
    email: lastEmail = "",
    onresume,
    onsignout,
  }: { config: ClientConfig; email?: string; onresume: () => void; onsignout: () => void } = $props();

  // svelte-ignore state_referenced_locally
  let email = $state(lastEmail);
  let password = $state("");
  let error = $state("");
  let busy = $state(false);

  async function submit(ev: SubmitEvent): Promise<void> {
    ev.preventDefault();
    error = "";
    busy = true;
    try {
      await loginLocal(email, password);
      if (session()) onresume();
    } catch (e) {
      error = e instanceof Error ? e.message : "sign-in failed";
    } finally {
      busy = false;
    }
  }
</script>

<Modal ariaLabel="Session expired" onclose={() => {}} width="24rem">
  <h2>Session expired</h2>
  <p class="muted">Your sign-in ended. Sign back in to keep working — staged edits are still here.</p>
  {#if config.localAuth}
    <form onsubmit={submit} aria-label="Sign back in">
      <label for="reauth-email">Email</label>
      <input id="reauth-email" type="email" autocomplete="username" required bind:value={email} data-autofocus />
      <label for="reauth-password">Password</label>
      <input id="reauth-password" type="password" autocomplete="current-password" required bind:value={password} />
      {#if error}
        <p class="error" role="alert">{error}</p>
      {/if}
      <button class="button" type="submit" disabled={busy}>{busy ? "Signing in…" : "Sign back in"}</button>
    </form>
  {/if}
  {#if config.oidc}
    {#if config.localAuth}<p class="muted">or</p>{/if}
    <button class="button button--quiet" onclick={() => startOidcLogin()}>Sign in with SSO</button>
    {#if !config.localAuth}
      <p class="muted">The SSO redirect reloads the page; unsaved staged edits will not survive it.</p>
    {/if}
  {/if}
  <p class="leave"><button class="link-button" onclick={onsignout}>Discard and go to sign-in</button></p>
</Modal>

<style>
  form {
    display: grid;
    gap: 0.35rem;
    margin: 0.6rem 0 0;
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
  .leave {
    margin: 0.9rem 0 0;
    font-size: var(--fs-meta);
  }
  .link-button {
    background: none;
    border: none;
    padding: 0;
    font: inherit;
    color: var(--ink-muted);
    text-decoration: underline;
    cursor: pointer;
  }
</style>
