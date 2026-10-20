// Copyright 2026 Bjorn Leffler.
// All rights reserved.

// Package cache stores snapshots by commit, so the base side of a diff is
// usually a download instead of a Bazel query.
//
// A store is named by URL:
//
//	/some/dir or file:///some/dir   a local or mounted directory
//	gs://bucket/prefix              Google Cloud Storage, via `gcloud storage`
//	s3://bucket/prefix              Amazon S3, via `aws s3`
//
// The cloud stores shell out to the providers' CLIs rather than linking their
// SDKs: CI images already carry the CLIs and their credentials setup.
package cache

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Store gets and puts files by key.
type Store interface {
	// Get downloads key to dst. It returns false, with no error, when the key
	// does not exist.
	Get(ctx context.Context, key, dst string) (bool, error)
	// Put uploads src under key.
	Put(ctx context.Context, key, src string) error
	// String describes the store for log messages.
	String() string
}

// SnapshotKey names the cached snapshot of a commit made with the settings
// identified by fingerprint.
func SnapshotKey(commit, fingerprint string) string {
	return commit + "/" + fingerprint + ".json.gz"
}

// Open returns the store for a URL.
func Open(url string) (Store, error) {
	switch {
	case url == "":
		return nil, fmt.Errorf("empty cache URL")
	case strings.HasPrefix(url, "gs://"):
		return cliStore{
			url: strings.TrimSuffix(url, "/"),
			cp:  []string{"gcloud", "storage", "cp", "--quiet"},
			ls:  []string{"gcloud", "storage", "ls"},
		}, nil
	case strings.HasPrefix(url, "s3://"):
		return cliStore{
			url: strings.TrimSuffix(url, "/"),
			cp:  []string{"aws", "s3", "cp", "--only-show-errors"},
			ls:  []string{"aws", "s3", "ls"},
		}, nil
	case strings.Contains(url, "://") && !strings.HasPrefix(url, "file://"):
		return nil, fmt.Errorf("unsupported cache URL %q (want a directory, gs:// or s3://)", url)
	}
	return dirStore{root: strings.TrimPrefix(url, "file://")}, nil
}

// dirStore keeps files in a directory tree.
type dirStore struct {
	root string
}

// Get implements Store.
func (d dirStore) Get(ctx context.Context, key, dst string) (bool, error) {
	src, err := os.Open(filepath.Join(d.root, filepath.FromSlash(key)))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer src.Close()
	return true, copyTo(dst, src)
}

// Put implements Store.
func (d dirStore) Put(ctx context.Context, key, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return copyTo(filepath.Join(d.root, filepath.FromSlash(key)), in)
}

// String implements Store.
func (d dirStore) String() string {
	return d.root
}

// copyTo writes r to path through a temporary file and a rename, so
// concurrent readers never see a partial file.
func copyTo(path string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cache-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// cliStore copies files with a cloud provider's command-line tool.
type cliStore struct {
	url string
	cp  []string
	ls  []string
}

// Get implements Store. It lists the object first, so that "not found" is
// not confused with an authentication or network failure.
func (c cliStore) Get(ctx context.Context, key, dst string) (bool, error) {
	object := c.url + "/" + key
	if err := run(ctx, append(c.ls, object)...); err != nil {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	if err := run(ctx, append(c.cp, object, dst)...); err != nil {
		return false, err
	}
	return true, nil
}

// Put implements Store.
func (c cliStore) Put(ctx context.Context, key, src string) error {
	return run(ctx, append(c.cp, src, c.url+"/"+key)...)
}

// String implements Store.
func (c cliStore) String() string {
	return c.url
}

// run executes a command, folding its standard error into the error.
func run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
