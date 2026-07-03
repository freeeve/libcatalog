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
    return () => opener?.focus?.();
  });

  /** Focus trap: Tab cycles inside the dialog, Escape closes it. */
  function onKeydown(ev: KeyboardEvent): void {
    if (ev.key === "Escape") {
      ev.stopPropagation();
      onclose();
      return;
    }
    if (ev.key !== "Tab" || !panel) return;
    const focusables = panel.querySelectorAll<HTMLElement>('button, input, select, textarea, [tabindex]:not([tabindex="-1"])');
    if (focusables.length === 0) return;
    const first = focusables[0];
    const last = focusables[focusables.length - 1];
    if (ev.shiftKey && document.activeElement === first) {
      ev.preventDefault();
      last.focus();
    } else if (!ev.shiftKey && document.activeElement === last) {
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
    onkeydown={onKeydown}
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
