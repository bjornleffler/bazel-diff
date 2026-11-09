// Copyright 2026 Bjorn Leffler.
// All rights reserved.

// Tests for the cache package.

package test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bjornleffler/bazel-diff/internal/cache"
)

// TestDirStore puts and gets a file in a directory store.
func TestDirStore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	for _, url := range []string{root, "file://" + root} {
		store, err := cache.Open(url)
		if err != nil {
			t.Fatal(err)
		}
		key := cache.SnapshotKey("abc", "fp")
		dst := filepath.Join(t.TempDir(), "out")
		if found, err := store.Get(ctx, "missing/"+key, dst); found || err != nil {
			t.Errorf("Get(missing) = %v, %v", found, err)
		}
		src := filepath.Join(t.TempDir(), "in")
		os.WriteFile(src, []byte("data"), 0o644)
		if err := store.Put(ctx, key, src); err != nil {
			t.Fatal(err)
		}
		if found, err := store.Get(ctx, key, dst); !found || err != nil {
			t.Fatalf("Get = %v, %v", found, err)
		}
		if b, _ := os.ReadFile(dst); string(b) != "data" {
			t.Errorf("content = %q", b)
		}
	}
}

// TestOpen accepts the supported URL schemes only.
func TestOpen(t *testing.T) {
	for _, ok := range []string{"gs://b/p", "s3://b/p", "/tmp/x"} {
		if _, err := cache.Open(ok); err != nil {
			t.Errorf("Open(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"", "http://x"} {
		if _, err := cache.Open(bad); err == nil {
			t.Errorf("Open(%q) succeeded", bad)
		}
	}
}
