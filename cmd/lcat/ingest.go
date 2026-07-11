package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/storage/blob"
)

// runIngestCmd ingests any registered provider into canonical grains under --out:
// `lcat ingest --provider <name> --source <input> --out <dir> [--feed <name>]
// [--reconcile review|auto-suppress]`.
// Which provider runs is a runtime selection against the built-in registry, so
// enabling a source is a config/flag change, not a code change (ARCHITECTURE §9a,
// tasks/006). The OverDrive `lcat overdrive` command is a convenience alias for
// `--provider overdrive` that also offers the MARC-fixture export.
func runIngestCmd(args []string) error {
	reg := providerRegistry()
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	provider := fs.String("provider", "", fmt.Sprintf("registered provider to run %v", reg.Names()))
	source := fs.String("source", "", "provider input (e.g. an OverDrive page-cache directory)")
	out := fs.String("out", "", "output directory for canonical grains and catalog.nq")
	feed := fs.String("feed", "", "provenance graph feed:<name> (default: the provider name)")
	mapping := fs.String("mapping", "", "mapping TOML for the generic providers (nquads, csv); shorthand for --param mapping=<path>")
	params := paramFlags{}
	fs.Var(params, "param", "provider parameter key=value, repeatable (ingest.Config.Params)")
	reconcile := fs.String("reconcile", "", "flag feed-only works this scan no longer lists: review | auto-suppress")
	allowEmpty := fs.Bool("reconcile-allow-empty", false, "let a zero-record scan reconcile (withdraws every feed-only work)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *provider == "" {
		return fmt.Errorf("--provider is required (registered: %v)", reg.Names())
	}
	if *out == "" {
		return fmt.Errorf("--out is required")
	}
	if *mapping != "" {
		params["mapping"] = *mapping
	}
	cfg := ingest.Config{Feed: *feed, Source: *source, Params: params}
	return runIngest(reg, *provider, cfg, *out, *reconcile, *allowEmpty)
}

// paramFlags collects repeatable --param key=value pairs into
// ingest.Config.Params, so a provider option needs no dedicated flag.
type paramFlags map[string]string

func (p paramFlags) String() string { return fmt.Sprint(map[string]string(p)) }

func (p paramFlags) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || k == "" {
		return fmt.Errorf("bad --param %q (want key=value)", v)
	}
	p[k] = val
	return nil
}

// runIngest constructs the named provider from reg and runs the shared ingest
// pipeline into out, surfacing resolver conflicts on stderr and a run summary
// on stdout. With a reconcile policy it then diffs the corpus against the
// scan and flags feed-only works the scan no longer lists (tasks/078). It is
// shared by `lcat ingest` and the `lcat overdrive` alias.
func runIngest(reg *ingest.Registry, name string, cfg ingest.Config, out, reconcile string, reconcileAllowEmpty bool) error {
	prov, err := reg.New(name, cfg)
	if err != nil {
		return err
	}
	res, err := ingest.Run(prov, out)
	if err != nil {
		return err
	}
	for _, c := range res.Conflicts {
		fmt.Fprintln(os.Stderr, "conflict:", c)
	}
	fmt.Printf("built %d works from %d instances under %s (feed:%s); minted %d works, %d instances; retired %d works\n",
		res.Stats.Grains, res.Stats.Records, out, prov.Name(), res.MintedWorks, res.MintedInstances, res.Retired)
	if reconcile == "" {
		return nil
	}
	// A zero-record scan reconciled against the corpus withdraws every
	// feed-only work in one pass -- almost always a misconfigured source, not
	// an emptied feed, so refuse without the explicit override (tasks/103).
	if len(res.WorkIDs) == 0 && !reconcileAllowEmpty {
		return fmt.Errorf("refusing to reconcile a zero-record scan (it would withdraw every feed:%s work); re-run with --reconcile-allow-empty if the feed really is empty", prov.Name())
	}
	present := make(map[string]bool, len(res.WorkIDs))
	for _, id := range res.WorkIDs {
		present[id] = true
	}
	date := time.Now().UTC().Format("2006-01-02")
	rec, err := ingest.Reconcile(context.Background(), blob.NewDir(out), "", prov.Name(), present, reconcile, date)
	if err != nil {
		return err
	}
	fmt.Printf("reconciled feed:%s (%s): %d flagged withdrawn, %d auto-suppressed, %d cleared, %d unsuppressed\n",
		prov.Name(), reconcile, rec.Flagged, rec.Suppressed, rec.Cleared, rec.Unsuppressed)
	for _, id := range rec.FlaggedIDs {
		fmt.Fprintln(os.Stderr, "withdrawn:", id)
	}
	return nil
}
