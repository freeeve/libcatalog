package main

import (
	"flag"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/BurntSushi/toml"
	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/export"
	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/storage"
)

// runBuild drives the whole static-site pipeline from one deployment config
// file (tasks/172): ingest every [[source]] -> serialize -> project -> export
// -> index -> hugo, so an adopter's rebuild is `lcat build`, not a shell
// script. Steps run only when their config section is present; --only narrows
// a run to named steps for iteration.
func runBuild(args []string) error {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	configPath := fs.String("config", "lcat.toml", "deployment pipeline config")
	only := fs.String("only", "", "comma-separated subset of steps to run: ingest,serialize,project,export,index,hugo")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadBuildConfig(*configPath)
	if err != nil {
		return err
	}
	run := stepFilter(splitList(*only))

	if run("ingest") {
		for _, src := range cfg.Sources {
			if err := buildIngest(cfg, src); err != nil {
				return fmt.Errorf("ingest %s: %w", src.Provider, err)
			}
		}
	}
	// After multi-source ingests catalog.nq holds only the last run's works;
	// serializing from grains restores the full corpus for the steps below.
	if run("serialize") && len(cfg.Sources) > 0 {
		n, err := bibframe.SerializeGrains(cfg.Out, storage.Dir(cfg.Out))
		if err != nil {
			return fmt.Errorf("serialize: %w", err)
		}
		fmt.Printf("serialized %d grains to %s/catalog.nq\n", n, cfg.Out)
	}
	if run("project") && cfg.Project != nil {
		if err := applySubjectSchemes(cfg.Project.SubjectSchemes); err != nil {
			return err
		}
		providers := cfg.Project.Providers
		if len(providers) == 0 {
			providers = cfg.feeds()
		}
		if err := projectCatalog(filepath.Join(cfg.Out, "catalog.nq"), providers, cfg.Project.PublicSources, cfg.Project.Out); err != nil {
			return fmt.Errorf("project: %w", err)
		}
	}
	if run("export") && cfg.Export != nil {
		if err := buildExportStep(cfg); err != nil {
			return fmt.Errorf("export: %w", err)
		}
	}
	if run("index") && cfg.Index != nil {
		if cfg.Project == nil {
			return fmt.Errorf("index: [index] needs [project] (it reads the projected catalog.json)")
		}
		if err := indexCatalog(filepath.Join(cfg.Project.Out, "catalog.json"), cfg.Index.Out); err != nil {
			return fmt.Errorf("index: %w", err)
		}
	}
	if run("hugo") && cfg.Hugo != nil {
		if err := buildHugoStep(cfg.Hugo); err != nil {
			return fmt.Errorf("hugo: %w", err)
		}
	}
	return nil
}

// buildConfig is the lcat.toml deployment pipeline config.
type buildConfig struct {
	// Out is the grain root every step shares (catalog.nq + data/works).
	Out     string        `toml:"out"`
	Sources []buildSource `toml:"source"`
	Project *projectStep  `toml:"project"`
	Export  *exportStep   `toml:"export"`
	Index   *indexStep    `toml:"index"`
	Hugo    *hugoStep     `toml:"hugo"`
}

// buildSource is one [[source]]: an ingest provider invocation.
type buildSource struct {
	// Provider is the registry name (overdrive, marc, nquads, csv, or a
	// deployment's own).
	Provider string `toml:"provider"`
	// Source is the provider input (ingest.Config.Source).
	Source string `toml:"source"`
	// Feed overrides the provenance graph name (default: the provider name).
	Feed string `toml:"feed"`
	// Mapping is shorthand for params.mapping (the generic providers' TOML).
	Mapping string `toml:"mapping"`
	// Params are provider parameters (ingest.Config.Params).
	Params map[string]string `toml:"params"`
	// Reconcile flags feed-only works the scan no longer lists: review |
	// auto-suppress (tasks/078).
	Reconcile string `toml:"reconcile"`
	// ReconcileAllowEmpty lets a zero-record scan reconcile (tasks/103).
	ReconcileAllowEmpty bool `toml:"reconcile-allow-empty"`
}

type projectStep struct {
	Out string `toml:"out"`
	// Providers are the feeds to project, first wins a shared work (default:
	// every [[source]]'s feed in config order).
	Providers []string `toml:"providers"`
	// PublicSources is the extra.sources allowlist for the public catalog;
	// empty keeps everything.
	PublicSources []string `toml:"public-sources"`
	// SubjectSchemes are authority prefix=code pairs (tasks/141).
	SubjectSchemes []string `toml:"subject-schemes"`
}

type exportStep struct {
	Out      string `toml:"out"`
	Manifest string `toml:"manifest"`
	// PublicSources overrides the [project] allowlist for the nq download;
	// unset inherits it, so one policy covers both public surfaces.
	PublicSources []string `toml:"public-sources"`
	// OrgCode is the deployment's MARC organization code: the MARC
	// downloads derive each record's 040 from graph facts (tasks/192).
	OrgCode string `toml:"org-code"`
	// CoversOut is where uploaded covers are copied (tasks/215).
	CoversOut string `toml:"covers-out"`
}

type indexStep struct {
	Out string `toml:"out"`
}

type hugoStep struct {
	// Dir is the Hugo site directory the command runs in.
	Dir string `toml:"dir"`
	// Command overrides the invocation (default ["hugo"]).
	Command []string `toml:"command"`
}

// loadBuildConfig reads and validates the pipeline config.
func loadBuildConfig(path string) (*buildConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg buildConfig
	if err := toml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if cfg.Out == "" {
		return nil, fmt.Errorf("%s: out (grain root) is required", path)
	}
	for i, src := range cfg.Sources {
		if src.Provider == "" {
			return nil, fmt.Errorf("%s: [[source]] %d: provider is required", path, i+1)
		}
	}
	if cfg.Project != nil && cfg.Project.Out == "" {
		return nil, fmt.Errorf("%s: [project] out is required", path)
	}
	if cfg.Export != nil && cfg.Export.Out == "" {
		return nil, fmt.Errorf("%s: [export] out is required", path)
	}
	if cfg.Index != nil && cfg.Index.Out == "" {
		return nil, fmt.Errorf("%s: [index] out is required", path)
	}
	if cfg.Hugo != nil && cfg.Hugo.Dir == "" {
		return nil, fmt.Errorf("%s: [hugo] dir is required", path)
	}
	return &cfg, nil
}

// feeds returns each source's provenance feed name in config order -- the
// default projection order, so the first-listed (primary) source wins shared
// works.
func (c *buildConfig) feeds() []string {
	var out []string
	for _, src := range c.Sources {
		feed := src.Feed
		if feed == "" {
			feed = src.Provider
		}
		if !slices.Contains(out, feed) {
			out = append(out, feed)
		}
	}
	return out
}

// stepFilter returns the step predicate for --only: every step when the list
// is empty, else membership.
func stepFilter(only []string) func(string) bool {
	if len(only) == 0 {
		return func(string) bool { return true }
	}
	return func(step string) bool { return slices.Contains(only, step) }
}

// buildIngest runs one [[source]] through the shared ingest pipeline.
func buildIngest(cfg *buildConfig, src buildSource) error {
	params := map[string]string{}
	maps.Copy(params, src.Params)
	if src.Mapping != "" {
		params["mapping"] = src.Mapping
	}
	ic := ingest.Config{Feed: src.Feed, Source: src.Source, Params: params}
	return runIngest(providerRegistry(), src.Provider, ic, cfg.Out, src.Reconcile, src.ReconcileAllowEmpty)
}

// buildExportStep derives the download artifacts, inheriting the [project]
// public-sources allowlist unless [export] sets its own.
func buildExportStep(cfg *buildConfig) error {
	opts := export.Options{In: cfg.Out, Out: cfg.Export.Out, OrgCode: cfg.Export.OrgCode, CoversOut: cfg.Export.CoversOut}
	publicSources := cfg.Export.PublicSources
	if publicSources == nil && cfg.Project != nil {
		publicSources = cfg.Project.PublicSources
	}
	if len(publicSources) > 0 {
		opts.PublicSources = map[string]bool{}
		for _, s := range publicSources {
			opts.PublicSources[s] = true
		}
	}
	m, err := export.Run(opts)
	if err != nil {
		return err
	}
	manifest := cfg.Export.Manifest
	if manifest == "" {
		manifest = filepath.Join(cfg.Export.Out, "downloads.json")
	}
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		return err
	}
	if err := writeJSON(manifest, m); err != nil {
		return err
	}
	fmt.Printf("exported %d works to %s; manifest %s\n", m.Works, cfg.Export.Out, manifest)
	return nil
}

// buildHugoStep runs the site generator with output passed through.
func buildHugoStep(h *hugoStep) error {
	command := h.Command
	if len(command) == 0 {
		command = []string{"hugo"}
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = h.Dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
