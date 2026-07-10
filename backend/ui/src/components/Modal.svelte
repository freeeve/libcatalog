<script lang="ts">
  // The one dialog shell: scrim, raised panel, focus trap, Escape-to-close,
  // and opener-focus restore. Content owns everything inside the panel; a
  // [data-autofocus] descendant takes focus on mount, else the panel itself.
  // Callers that need a keyboard scope push it themselves (the trap only
  // handles Tab and Escape).
  import { onMount, type Snippet } from "svelte";

  let {
    ariaLabel,
    onclose,
    width = "32rem",
    placement = "center",
    children,
  }: {
    ariaLabel: string;
    onclose: () => void;
    width?: string;
    placement?: "center" | "top";
    children: Snippet;
  } = $props();

  let panel = $state<HTMLElement | null>(null);

  onMount(() => {
    const opener = document.activeElement as HTMLElement | null;
    const auto = panel?.querySelector<HTMLElement>("[data-autofocus]");
    (auto ?? panel)?.focus();
    // Bound to the window (capture) rather than the panel so the trap keeps
    // working when the dialog's content unmounts the focused control and the
    // browser drops focus to <body>: a panel-scoped listener goes deaf the
    // instant focus leaves it, which killed Escape and let Tab wander onto
    // controls behind the scrim (tasks/250).
    window.addEventListener("keydown", onKeydown, true);
    return () => {
      window.removeEventListener("keydown", onKeydown, true);
      opener?.focus?.();
    };
  });

  /** Focus trap: Escape closes from anywhere; Tab keeps the cycle inside the
      panel and pulls focus back in if it has already escaped. */
  function onKeydown(ev: KeyboardEvent): void {
    if (!panel) return;
    if (ev.key === "Escape") {
      ev.stopPropagation();
      onclose();
      return;
    }
    if (ev.key !== "Tab") return;
    const focusables = panel.querySelectorAll<HTMLElement>('button, input, select, textarea, [tabindex]:not([tabindex="-1"])');
    if (focusables.length === 0) {
      ev.preventDefault();
      panel.focus();
      return;
    }
    const first = focusables[0];
    const last = focusables[focusables.length - 1];
    const active = document.activeElement;
    if (!panel.contains(active)) {
      // Content unmounted whatever held focus; recapture it into the dialog.
      ev.preventDefault();
      (ev.shiftKey ? last : first).focus();
      return;
    }
    if (ev.shiftKey && active === first) {
      ev.preventDefault();
      last.focus();
    } else if (!ev.shiftKey && active === last) {
      ev.preventDefault();
      first.focus();
    }
  }
</script>

<div class="scrim" class:scrim--top={placement === "top"}>
  <div
    class="panel"
    role="dialog"
    aria-modal="true"
    aria-label={ariaLabel}
    tabindex="-1"
    bind:this={panel}
    style:width={`min(${width}, 94vw)`}
  >
    {@render children()}
  </div>
</div>

<style>
  .scrim {
    position: fixed;
    inset: 0;
    background: rgba(20, 22, 25, 0.55);
    display: grid;
    place-items: center;
    z-index: 50;
  }
  .scrim--top {
    place-items: start center;
    padding-top: 12vh;
  }
  .panel {
    background: var(--bg);
    border: 1px solid var(--rule);
    border-radius: 8px;
    padding: 1rem 1.25rem;
    max-height: 85vh;
    overflow-y: auto;
  }
</style>
