// Copyright 2026 Bjorn Leffler.
// SPDX-License-Identifier: Apache-2.0

// Package hasher computes a content hash for every target in a `bazel query`
// result. Each target gets two hashes:
//
//   - Self covers the target's own definition: a rule's class, attributes,
//     input labels and Starlark environment; a source file's content; a
//     generated file's generating rule.
//   - Hash covers Self plus the Hash of every dependency, recursively.
//
// A target needs rebuilding when its Hash changes. When Self changed too, the
// change is in the target itself; otherwise it came from a dependency.
package hasher

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"runtime"
	"sort"
	"sync"

	"github.com/bjornleffler/bazel-diff/internal/buildpb"
	"github.com/bjornleffler/bazel-diff/internal/label"
	"github.com/bjornleffler/bazel-diff/internal/snapshot"
	"google.golang.org/protobuf/proto"
)

// hashBytes is how many bytes of each SHA-256 are kept. 128 bits is ample to
// make collisions irrelevant and keeps snapshots small.
const hashBytes = 16

// volatileAttributes vary with where a rule sits in its BUILD file rather than
// with what it is. Hashing them would mark every rule below an inserted line
// as changed.
var volatileAttributes = map[string]bool{
	"generator_location": true,
}

// FileSource returns content hashes for main-repository files.
type FileSource interface {
	// FileHash returns the hash of a repository-relative file or directory,
	// and false when there is no such path.
	FileHash(path string) (string, bool)
}

// RepoSource returns identities for external repositories.
type RepoSource interface {
	// RepoHash returns the identity of a repository by canonical name.
	RepoHash(repo string) string
	// FileHash returns the content hash of a file in a repository whose
	// identity does not pin its contents, and false otherwise.
	FileHash(repo, path string) (string, bool)
}

// Options configures Hash.
type Options struct {
	Files FileSource
	Repos RepoSource
	// Workers bounds the goroutines computing Self hashes; GOMAXPROCS when 0.
	Workers int
}

// node is one target while hashing.
type node struct {
	target *buildpb.Target
	label  string
	kind   string
	tags   []string
	deps   []int32
	self   []byte
	total  []byte
	state  uint8
}

// DFS states for the total-hash walk.
const (
	unvisited uint8 = iota
	visiting
	visited
)

// Hash computes Self and Hash for every target and returns them sorted by
// label, with Deps indexing into the returned slice.
func Hash(targets []*buildpb.Target, opts Options) ([]snapshot.Target, error) {
	nodes, err := buildGraph(targets)
	if err != nil {
		return nil, err
	}
	if err := hashSelves(nodes, opts); err != nil {
		return nil, err
	}
	for i := range nodes {
		total(nodes, i)
	}

	out := make([]snapshot.Target, len(nodes))
	for i, n := range nodes {
		out[i] = snapshot.Target{
			Label: n.label,
			Kind:  n.kind,
			Tags:  n.tags,
			Self:  encode(n.self),
			Hash:  encode(n.total),
			Deps:  n.deps,
		}
	}
	return out, nil
}

// buildGraph sorts targets by normalized label, so that results do not depend
// on Bazel's output order, and resolves dependency labels to indices.
func buildGraph(targets []*buildpb.Target) ([]*node, error) {
	byLabel := make(map[string]*node, len(targets))
	for _, t := range targets {
		name, err := targetName(t)
		if err != nil {
			return nil, err
		}
		l := label.Normalize(name)
		if _, dup := byLabel[l]; dup {
			continue
		}
		byLabel[l] = &node{target: t, label: l, kind: targetKind(t), tags: targetTags(t)}
	}
	nodes := make([]*node, 0, len(byLabel))
	for _, n := range byLabel {
		nodes = append(nodes, n)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].label < nodes[j].label })
	index := make(map[string]int32, len(nodes))
	for i, n := range nodes {
		index[n.label] = int32(i)
	}
	for _, n := range nodes {
		for _, dep := range dependencyLabels(n.target) {
			if i, ok := index[label.Normalize(dep)]; ok {
				n.deps = append(n.deps, i)
			}
			// A dependency outside the result still counts: its label is part
			// of the rule's Self through rule_input.
		}
	}
	return nodes, nil
}

// hashSelves computes every node's Self hash in parallel. Self hashes are
// independent of each other; only file reads for local repositories and the
// protobuf encoding cost anything.
func hashSelves(nodes []*node, opts Options) error {
	workers := opts.Workers
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error
	next := make(chan int, 1024)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				self, err := selfHash(nodes[i], opts)
				if err != nil {
					once.Do(func() { firstErr = err })
					continue
				}
				nodes[i].self = self
			}
		}()
	}
	for i := range nodes {
		next <- i
	}
	close(next)
	wg.Wait()
	return firstErr
}

// total computes a node's Hash: its Self followed by each dependency's Hash.
// Query graphs should be acyclic, but if a cycle shows up the back edge
// contributes the dependency's Self instead of recursing forever.
func total(nodes []*node, i int) []byte {
	n := nodes[i]
	switch n.state {
	case visited:
		return n.total
	case visiting:
		return n.self
	}
	n.state = visiting
	h := sha256.New()
	h.Write(n.self)
	for _, d := range n.deps {
		h.Write(total(nodes, int(d)))
	}
	n.total = h.Sum(nil)[:hashBytes]
	n.state = visited
	n.target = nil // No longer needed; let the garbage collector have it.
	return n.total
}

// selfHash hashes one target's own definition.
func selfHash(n *node, opts Options) ([]byte, error) {
	t := n.target
	h := sha256.New()
	write := func(parts ...string) {
		for _, p := range parts {
			h.Write([]byte(p))
			h.Write([]byte{0})
		}
	}
	marshal := func(m proto.Message) error {
		b, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
		if err != nil {
			return fmt.Errorf("encoding %s: %w", n.label, err)
		}
		h.Write(b)
		return nil
	}

	l, err := label.Parse(n.label)
	if err != nil {
		return nil, err
	}
	if !l.IsMain() && opts.Repos != nil {
		write("repo", l.Repo, opts.Repos.RepoHash(l.Repo))
	}

	switch t.GetType() {
	case buildpb.Target_RULE:
		write("rule")
		if err := marshal(stableRule(t.GetRule())); err != nil {
			return nil, err
		}
	case buildpb.Target_SOURCE_FILE:
		write("source")
		sf := proto.Clone(t.GetSourceFile()).(*buildpb.SourceFile)
		sf.Location = nil
		if err := marshal(sf); err != nil {
			return nil, err
		}
		write(sourceContent(l, opts))
	case buildpb.Target_GENERATED_FILE:
		write("generated", n.label, label.Normalize(t.GetGeneratedFile().GetGeneratingRule()))
	case buildpb.Target_PACKAGE_GROUP:
		write("package_group")
		if err := marshal(t.GetPackageGroup()); err != nil {
			return nil, err
		}
	case buildpb.Target_ENVIRONMENT_GROUP:
		write("environment_group")
		if err := marshal(t.GetEnvironmentGroup()); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("target %s has unknown type %v", n.label, t.GetType())
	}
	return h.Sum(nil)[:hashBytes], nil
}

// sourceContent returns the content hash for a source file target.
func sourceContent(l label.Label, opts Options) string {
	if l.IsMain() {
		if opts.Files != nil {
			if h, ok := opts.Files.FileHash(l.Path()); ok {
				return h
			}
		}
		// Referenced by a target but absent: Bazel will fail on it, and the
		// file appearing later must change the hash.
		return "missing"
	}
	if opts.Repos != nil {
		if h, ok := opts.Repos.FileHash(l.Repo, l.Path()); ok {
			return h
		}
	}
	// Pinned by the repository identity already written into the hash.
	return "pinned"
}

// stableRule returns a copy of a rule without its source location, which
// carries line numbers and absolute paths.
func stableRule(r *buildpb.Rule) *buildpb.Rule {
	c := proto.Clone(r).(*buildpb.Rule)
	c.Location = nil
	c.InstantiationStack = nil
	c.DefinitionStack = nil
	kept := c.Attribute[:0]
	for _, a := range c.Attribute {
		if !volatileAttributes[a.GetName()] {
			kept = append(kept, a)
		}
	}
	c.Attribute = kept
	return c
}

// targetName returns the label of any kind of target.
func targetName(t *buildpb.Target) (string, error) {
	switch t.GetType() {
	case buildpb.Target_RULE:
		return t.GetRule().GetName(), nil
	case buildpb.Target_SOURCE_FILE:
		return t.GetSourceFile().GetName(), nil
	case buildpb.Target_GENERATED_FILE:
		return t.GetGeneratedFile().GetName(), nil
	case buildpb.Target_PACKAGE_GROUP:
		return t.GetPackageGroup().GetName(), nil
	case buildpb.Target_ENVIRONMENT_GROUP:
		return t.GetEnvironmentGroup().GetName(), nil
	}
	return "", fmt.Errorf("query result has a target of unknown type %v", t.GetType())
}

// targetKind returns a rule's class, or a fixed name for other target types.
func targetKind(t *buildpb.Target) string {
	switch t.GetType() {
	case buildpb.Target_RULE:
		return t.GetRule().GetRuleClass()
	case buildpb.Target_SOURCE_FILE:
		return snapshot.KindSourceFile
	case buildpb.Target_GENERATED_FILE:
		return snapshot.KindGeneratedFile
	case buildpb.Target_PACKAGE_GROUP:
		return snapshot.KindPackageGroup
	}
	return snapshot.KindEnvironmentGroup
}

// targetTags returns a rule's "tags" attribute.
func targetTags(t *buildpb.Target) []string {
	for _, a := range t.GetRule().GetAttribute() {
		if a.GetName() == "tags" {
			return a.GetStringListValue()
		}
	}
	return nil
}

// dependencyLabels returns the labels a target depends on.
func dependencyLabels(t *buildpb.Target) []string {
	switch t.GetType() {
	case buildpb.Target_RULE:
		return t.GetRule().GetRuleInput()
	case buildpb.Target_GENERATED_FILE:
		return []string{t.GetGeneratedFile().GetGeneratingRule()}
	}
	return nil
}

// encode renders a hash compactly for the snapshot.
func encode(b []byte) string {
	return base64.RawStdEncoding.EncodeToString(b)
}
