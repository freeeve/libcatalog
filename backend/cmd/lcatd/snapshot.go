package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/freeeve/libcat/backend/awsstore"
	"github.com/freeeve/libcat/backend/workindex"
	"github.com/freeeve/libcat/storage/blob"
)

// runWorkindexSnapshot builds the work-index snapshot from a grain store off the
// running server -- the offline seed a Lambda deployment needs so its first cold
// start loads the projection instead of scanning the corpus (tasks/155). It
// scans the store once and writes the snapshot blob back into it.
//
// The snapshot must be built against the same store backend it will serve:
// ETag schemes differ per backend (dir is content sha256, S3 is MD5-based), so
// a dir-built snapshot copied into a bucket fails every ETag diff and the
// first refresh degrades to a full rescan (tasks/162). For S3 targets, point
// the tool at the bucket itself.
//
//	lcatd workindex-snapshot (--blob-dir <dir> | --s3-bucket <bucket>)
//	  [--aws-endpoint <url>] [--out data/workindex.snapshot] [--concurrency 16]
func runWorkindexSnapshot(args []string) error {
	fs := flag.NewFlagSet("workindex-snapshot", flag.ExitOnError)
	dir := fs.String("blob-dir", "", "grain store directory (holds data/works/*.nq)")
	bucket := fs.String("s3-bucket", "", "S3 bucket holding the grain store; region/credentials from the AWS environment")
	endpoint := fs.String("aws-endpoint", "", "S3 endpoint override (MinIO and other S3-compatibles)")
	out := fs.String("out", workindex.DefaultSnapshotPath, "snapshot path within the store")
	workers := fs.Int("concurrency", 16, "parallel grain fetches")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*dir == "") == (*bucket == "") {
		return fmt.Errorf("workindex-snapshot: exactly one of --blob-dir or --s3-bucket is required (the snapshot must be built against the store it will serve -- ETag schemes differ per backend)")
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
	ix := workindex.New(bs, "data/works/")
	ix.SetSnapshotPath(*out)
	// An existing snapshot narrows the scan to the ETag delta -- the re-seed
	// path. Unreadable just means a full scan.
	if err := ix.LoadSnapshot(ctx); err != nil {
		fmt.Printf("ignoring unreadable snapshot: %v\n", err)
	}
	start := time.Now()
	last := 0
	err := ix.WarmScan(ctx, *workers, func(fetched, total int) {
		if fetched == total || fetched-last >= 1000 {
			last = fetched
			fmt.Printf("scanned %d/%d changed grains\n", fetched, total)
		}
	})
	if err != nil {
		return fmt.Errorf("scan grains: %w", err)
	}
	if primed, refetched := ix.SnapshotDrift(); primed > 0 && refetched*2 >= primed {
		fmt.Printf("note: %d/%d prior-snapshot entries had mismatched ETags -- the old snapshot was likely built against a different store backend; this run fixes it\n", refetched, primed)
	}
	if err := ix.Save(ctx); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}
	fmt.Printf("wrote %s in %s\n", *out, time.Since(start).Round(time.Second))
	return nil
}
