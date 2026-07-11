package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/freeeve/libcat/backend/awsstore"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/vocab"
	"github.com/freeeve/libcat/backend/vocabsrc"
	"github.com/freeeve/libcat/storage/blob"
)

// runVocabInstall installs a vocabulary snapshot into a blob store off the
// running server -- how a serverless deployment adds a vocabulary.
// On Lambda the async download worker never runs, and without a document store
// the source registry resets every cold start; the durable artifacts are the
// blob-side snapshot and sidecar, which this writes through the same
// converter, caps, and layout as a server-side install. The server loads the
// scheme at its next boot or authority-edit reload.
//
//	lcatd vocab-install (--blob-dir <dir> | --s3-bucket <bucket>) [--aws-endpoint <url>]
//	  --name homosaurus [--scheme <key>] (--url https://homosaurus.org/v5.nt | --file dump.nt[.gz])
//	  [--authorities-prefix data/authorities/] [--max-mb N]
func runVocabInstall(args []string) error {
	fs := flag.NewFlagSet("vocab-install", flag.ExitOnError)
	dir := fs.String("blob-dir", "", "blob store directory of the target deployment")
	bucket := fs.String("s3-bucket", "", "S3 bucket of the target deployment; region/credentials from the AWS environment")
	endpoint := fs.String("aws-endpoint", "", "S3 endpoint override (MinIO and other S3-compatibles)")
	name := fs.String("name", "", "source name; also the snapshot filename under <prefix>vocab/")
	scheme := fs.String("scheme", "", "vocab scheme key the terms load under (default: the name)")
	dumpURL := fs.String("url", "", "SKOS N-Triples/N-Quads dump URL, optionally gzipped")
	file := fs.String("file", "", "local dump path instead of --url")
	prefix := fs.String("authorities-prefix", "data/authorities/", "authority tree prefix within the store")
	maxMB := fs.Int("max-mb", 0, "decompressed dump size cap in MB (0 = the 4GB default)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("vocab-install: --name is required")
	}
	if (*dumpURL == "") == (*file == "") {
		return fmt.Errorf("vocab-install: exactly one of --url or --file is required")
	}
	if (*dir == "") == (*bucket == "") {
		return fmt.Errorf("vocab-install: exactly one of --blob-dir or --s3-bucket is required")
	}
	ctx := context.Background()
	bs := blob.Store(nil)
	if *bucket != "" {
		var err error
		if bs, err = awsstore.S3(ctx, *bucket, *endpoint); err != nil {
			return err
		}
	} else {
		bs = blob.NewDir(*dir)
	}
	if *scheme == "" {
		scheme = name
	}
	// A throwaway in-memory registry and a nil index: the install's durable
	// side effects are the snapshot and sidecar in the blob store.
	svc := &vocabsrc.Service{
		DB: store.NewMem(), Blob: bs,
		AuthoritiesPrefix: *prefix, MaxSnapshotMB: *maxMB,
	}
	src := vocabsrc.Source{Name: *name, Scheme: *scheme, SnapshotURL: *dumpURL}
	if err := svc.PutSource(ctx, src); err != nil {
		return fmt.Errorf("vocab-install: %w", err)
	}
	start := time.Now()
	var terms int
	if *file != "" {
		f, err := os.Open(*file)
		if err != nil {
			return err
		}
		defer f.Close()
		if terms, err = svc.InstallUpload(ctx, *name, f); err != nil {
			return fmt.Errorf("vocab-install: %w", err)
		}
	} else {
		job, err := svc.CreateDownload(ctx, "vocab-install", *name)
		if err != nil {
			return fmt.Errorf("vocab-install: %w", err)
		}
		if err := svc.RunDownload(ctx, job.ID); err != nil {
			return fmt.Errorf("vocab-install: %w", err)
		}
		done, err := svc.GetJob(ctx, job.ID)
		if err != nil {
			return fmt.Errorf("vocab-install: %w", err)
		}
		if done.Status != vocabsrc.StatusDone {
			return fmt.Errorf("vocab-install: %s", done.Error)
		}
		terms = done.Terms
	}
	fmt.Printf("installed %d %s terms as scheme %q under %svocab/%s.nq in %s -- the server loads it at its next boot or authority reload\n",
		terms, *name, *scheme, *prefix, *name, time.Since(start).Round(time.Second))
	return nil
}

// runVocabIndex (re)builds the sidecar index artifacts for already-installed
// vocabulary snapshots -- how an existing deployment adopts
// range-served vocabularies without reinstalling the dumps.
//
//	lcatd vocab-index (--blob-dir <dir> | --s3-bucket <bucket>) [--aws-endpoint <url>]
//	  (--name <snapshot> [--scheme <key>] | --all) [--authorities-prefix data/authorities/]
func runVocabIndex(args []string) error {
	fs := flag.NewFlagSet("vocab-index", flag.ExitOnError)
	dir := fs.String("blob-dir", "", "blob store directory of the target deployment")
	bucket := fs.String("s3-bucket", "", "S3 bucket of the target deployment; region/credentials from the AWS environment")
	endpoint := fs.String("aws-endpoint", "", "S3 endpoint override (MinIO and other S3-compatibles)")
	name := fs.String("name", "", "installed snapshot name under <prefix>vocab/")
	scheme := fs.String("scheme", "", "vocab scheme key of the snapshot (default: the name)")
	all := fs.Bool("all", false, "rebuild every installed snapshot")
	prefix := fs.String("authorities-prefix", "data/authorities/", "authority tree prefix within the store")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*name == "") == !*all {
		return fmt.Errorf("vocab-index: exactly one of --name or --all is required")
	}
	if (*dir == "") == (*bucket == "") {
		return fmt.Errorf("vocab-index: exactly one of --blob-dir or --s3-bucket is required")
	}
	ctx := context.Background()
	bs := blob.Store(nil)
	if *bucket != "" {
		var err error
		if bs, err = awsstore.S3(ctx, *bucket, *endpoint); err != nil {
			return err
		}
	} else {
		bs = blob.NewDir(*dir)
	}
	type target struct{ name, scheme string }
	var targets []target
	if *all {
		svc := &vocabsrc.Service{DB: store.NewMem(), Blob: bs, AuthoritiesPrefix: *prefix}
		installed, err := svc.Installed(ctx)
		if err != nil {
			return fmt.Errorf("vocab-index: %w", err)
		}
		for _, info := range installed {
			targets = append(targets, target{name: info.Source, scheme: info.Scheme})
		}
	} else {
		s := *scheme
		if s == "" {
			s = *name
		}
		targets = append(targets, target{name: *name, scheme: s})
	}
	for _, t := range targets {
		start := time.Now()
		source := *prefix + "vocab/" + t.name + ".nq"
		m, err := vocab.BuildSidecar(ctx, bs, *prefix, t.scheme, source)
		if err != nil {
			return fmt.Errorf("vocab-index: %s: %w", t.name, err)
		}
		fmt.Printf("indexed %s: %d terms (%d live) as scheme %q in %s\n",
			t.name, m.Terms, m.Live, m.Scheme, time.Since(start).Round(time.Millisecond))
	}
	return nil
}
