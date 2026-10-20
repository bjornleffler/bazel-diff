// Copyright 2026 Bjorn Leffler.
// All rights reserved.

// Package diff compares two snapshots and reports which targets are impacted,
// how far each one is from the change that impacted it, and whether the
// change is one that forces a full build.
package diff

import (
	"fmt"
	"sort"

	"github.com/bjornleffler/bazel-diff2/internal/snapshot"
)

// Result is the outcome of comparing a base snapshot with a head snapshot.
type Result struct {
	BaseCommit string
	HeadCommit string
	// FullBuild is set when the change cannot be scoped to targets, for
	// example because a seed file changed. Reasons says why.
	FullBuild bool
	Reasons   []string
	// Impacted lists every head target whose hash differs from base, or which
	// is new, sorted by label.
	Impacted []Impacted
	// Deleted lists base targets that no longer exist.
	Deleted []string
	// HeadTargets is the number of targets in the head snapshot.
	HeadTargets int
}

// Impacted is one target that needs rebuilding.
type Impacted struct {
	Label string   `json:"label"`
	Kind  string   `json:"kind"`
	Tags  []string `json:"tags,omitempty"`
	// Test is set for test rules.
	Test bool `json:"test,omitempty"`
	// Direct is set when the target's own definition or content changed, or
	// the target is new.
	Direct bool `json:"direct,omitempty"`
	New    bool `json:"new,omitempty"`
	// Distance counts dependency edges from the nearest directly changed
	// target: 0 for a direct change, -1 when no path was found.
	Distance int `json:"distance"`
	// Via is the next target on the path toward Cause; empty when Direct.
	Via string `json:"via,omitempty"`
	// Cause is the directly changed target this impact traces back to.
	Cause string `json:"cause,omitempty"`
}

// External reports whether the target is in an external repository.
func (i *Impacted) External() bool {
	return len(i.Label) > 0 && i.Label[0] == '@'
}

// Compare diffs two snapshots.
func Compare(base, head *snapshot.Snapshot) *Result {
	r := &Result{BaseCommit: base.Commit, HeadCommit: head.Commit, HeadTargets: len(head.Targets)}
	r.Reasons = incompatibilities(base, head)

	baseIndex := base.Index()
	n := len(head.Targets)
	changed := make([]bool, n)
	direct := make([]bool, n)
	isNew := make([]bool, n)
	seen := make(map[string]bool, n)
	for i := range head.Targets {
		t := &head.Targets[i]
		seen[t.Label] = true
		b, ok := baseIndex[t.Label]
		switch {
		case !ok:
			changed[i], direct[i], isNew[i] = true, true, true
		case base.Targets[b].Hash != t.Hash:
			changed[i] = true
			direct[i] = base.Targets[b].Self != t.Self
		}
	}
	for i := range base.Targets {
		if !seen[base.Targets[i].Label] {
			r.Deleted = append(r.Deleted, base.Targets[i].Label)
		}
	}
	sort.Strings(r.Deleted)

	dist, via, cause := trace(head, changed, direct)
	for i := range head.Targets {
		if !changed[i] {
			continue
		}
		t := &head.Targets[i]
		imp := Impacted{
			Label:    t.Label,
			Kind:     t.Kind,
			Tags:     t.Tags,
			Test:     t.IsTest(),
			Direct:   direct[i],
			New:      isNew[i],
			Distance: dist[i],
		}
		if via[i] >= 0 {
			imp.Via = head.Targets[via[i]].Label
		}
		if cause[i] >= 0 {
			imp.Cause = head.Targets[cause[i]].Label
		}
		r.Impacted = append(r.Impacted, imp)
	}
	r.FullBuild = len(r.Reasons) > 0
	return r
}

// incompatibilities lists the reasons, if any, that the two snapshots cannot
// be scoped to a target list.
func incompatibilities(base, head *snapshot.Snapshot) []string {
	var reasons []string
	if base.SchemaVersion != head.SchemaVersion {
		reasons = append(reasons, fmt.Sprintf("snapshot schema changed (%d -> %d)", base.SchemaVersion, head.SchemaVersion))
	}
	if base.Fingerprint != head.Fingerprint {
		reasons = append(reasons, "snapshots were made with different settings (query, flags or seeds)")
	}
	keys := make(map[string]bool)
	for k := range base.Seeds {
		keys[k] = true
	}
	for k := range head.Seeds {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		b, inBase := base.Seeds[k]
		h, inHead := head.Seeds[k]
		switch {
		case !inBase:
			reasons = append(reasons, "seed file added: "+k)
		case !inHead:
			reasons = append(reasons, "seed file removed: "+k)
		case b != h:
			reasons = append(reasons, "seed file changed: "+k)
		}
	}
	return reasons
}

// trace runs a breadth-first search from every directly changed target along
// reverse dependency edges, through changed targets only, recording for each
// changed target its distance from the nearest direct change, the next hop
// toward it, and the direct change itself.
func trace(head *snapshot.Snapshot, changed, direct []bool) (dist, via, cause []int) {
	n := len(head.Targets)
	dist = make([]int, n)
	via = make([]int, n)
	cause = make([]int, n)
	rdeps := make([][]int32, n)
	for i := range head.Targets {
		if !changed[i] {
			continue
		}
		for _, d := range head.Targets[i].Deps {
			if changed[d] {
				rdeps[d] = append(rdeps[d], int32(i))
			}
		}
	}
	queue := make([]int, 0, n)
	for i := range dist {
		dist[i], via[i], cause[i] = -1, -1, -1
		if direct[i] {
			dist[i], cause[i] = 0, i
			queue = append(queue, i)
		}
	}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		for _, v := range rdeps[u] {
			if dist[v] < 0 {
				dist[v], via[v], cause[v] = dist[u]+1, u, cause[u]
				queue = append(queue, int(v))
			}
		}
	}
	return dist, via, cause
}
