<script lang="ts">
  // Shows where a field value came from: "feed:<provider>" grey with the
  // provider name, "editorial:" green, "enrichment:<source>" blue. Anything
  // unrecognised falls back to the raw string in grey.
  let { prov }: { prov: string } = $props();

  const kind = $derived(
    prov.startsWith("feed:") ? "feed" : prov.startsWith("editorial") ? "editorial" : prov.startsWith("enrichment:") ? "enrichment" : "other",
  );
  const label = $derived(
    kind === "feed"
      ? prov.slice("feed:".length) || "feed"
      : kind === "editorial"
        ? "editorial"
        : kind === "enrichment"
          ? "enrichment: " + prov.slice("enrichment:".length)
          : prov,
  );
</script>

<span class="badge badge--{kind}" title={prov}>{label}</span>

<style>
  .badge {
    display: inline-block;
    font-size: 0.72rem;
    font-weight: 600;
    letter-spacing: 0.03em;
    padding: 0.1em 0.55em;
    border-radius: 999px;
    border: 1px solid transparent;
    white-space: nowrap;
  }
  .badge--feed,
  .badge--other {
    background: #ecebe6;
    color: #494f4b;
    border-color: #d9d5cb;
  }
  .badge--editorial {
    background: #e4efe8;
    color: #14523a;
    border-color: #bcd6c6;
  }
  .badge--enrichment {
    background: #e5edf5;
    color: #1d4260;
    border-color: #bdd2e4;
  }
</style>
