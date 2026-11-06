// Package lockfile reads, writes and compares schema lock files.
//
// A lock records what each field of a set of messages and enums is called and
// what number carries it. Those two facts are a published interface for any
// message that is stored: the number is what the wire format reads, and the
// name is what a JSON export prints. [Compare] reports every change to either
// that would change what already-written data decodes to. Adding is always
// allowed.
//
// A lock is built from a descriptor set with [FromDescriptors] or read from a
// file with [Parse], and written with [Lock.Render]. A Lock does not change
// once built.
package lockfile

import (
	"sort"

	"google.golang.org/protobuf/types/descriptorpb"
)

// Lock is the locked part of a schema: messages and enums, their fields and
// values, what is reserved, and any checks that were waived.
type Lock struct {
	s *schema
}

// FromDescriptors builds a lock from the named files in a descriptor set. The
// set may carry other files, such as imports, which are not locked.
func FromDescriptors(set *descriptorpb.FileDescriptorSet, files []string) (*Lock, error) {
	want := make(map[string]bool, len(files))
	for _, f := range files {
		want[f] = true
	}
	s, err := read(set, want)
	if err != nil {
		return nil, err
	}
	return &Lock{s: s}, nil
}

// Parse reads a lock file.
func Parse(text string) (*Lock, error) {
	s, err := parseLock(text)
	if err != nil {
		return nil, err
	}
	return &Lock{s: s}, nil
}

// Render writes the lock file, with pkg as its package.
func (l *Lock) Render(pkg string) string { return render(pkg, l.s) }

// Messages returns the full name of every locked message, sorted.
func (l *Lock) Messages() []string {
	out := make([]string, 0, len(l.s.messages))
	for name := range l.s.messages {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Violation is one way a schema breaks what its lock recorded.
type Violation struct {
	v violation
}

// String is the sentence a person reads.
func (v Violation) String() string { return v.v.detail }

// Owner is the full name of the message or enum the violation is about.
func (v Violation) Owner() string { return v.v.owner }

// Kind is what was done: renamed, renumbered, retyped, relabelled, requeued
// into a oneof, unreserved, deleted, or removed.
func (v Violation) Kind() string { return v.v.kind }

// Compare reports every way current breaks what locked recorded. Additions are
// not violations.
func Compare(locked, current *Lock) []Violation {
	broken := compare(locked.s, current.s)
	out := make([]Violation, 0, len(broken))
	for _, v := range broken {
		out = append(out, Violation{v: v})
	}
	return out
}

// Additions lists what current has that locked does not, one line each, for a
// message saying why a lock needs regenerating.
func Additions(locked, current *Lock) []string { return additions(locked.s, current.s) }

// Next returns the lock to write in place of locked: current, carrying the
// waivers locked already records, so a waiver survives every later update.
//
// With a non-empty waiver, each violation of current against locked is waived
// and recorded beside the field it concerns, with waiver as the reason. With an
// empty one, the violations are returned and the lock is not built.
func Next(locked, current *Lock, waiver string) (*Lock, []Violation) {
	next, err := parseLock(render("", current.s))
	if err != nil {
		// render's output always parses; this would be a bug in this package.
		panic("lockfile: a rendered lock does not parse: " + err.Error())
	}
	carry(locked.s, next)
	broken := compare(locked.s, next)
	if len(broken) > 0 {
		if waiver == "" {
			out := make([]Violation, 0, len(broken))
			for _, v := range broken {
				out = append(out, Violation{v: v})
			}
			return nil, out
		}
		waive(next, broken, waiver)
	}
	return &Lock{s: next}, nil
}

// carry copies overrides recorded in the lock onto the schema about to be
// rendered, so a waiver is not lost the next time the lock is regenerated.
func carry(old, current *schema) {
	for name, was := range old.messages {
		if now, ok := current.messages[name]; ok {
			now.overrides = mergeOverrides(now.overrides, was.overrides)
		}
	}
	for name, was := range old.enums {
		if now, ok := current.enums[name]; ok {
			now.overrides = mergeOverrides(now.overrides, was.overrides)
		}
	}
	current.removed = mergeOverrides(current.removed, old.removed)
}

// waive records each refused check as an override on the schema being written.
func waive(current *schema, broken []violation, reason string) {
	for _, v := range broken {
		o := v.asOverride(reason)
		switch {
		case current.messages[v.owner] != nil:
			m := current.messages[v.owner]
			m.overrides = mergeOverrides(m.overrides, []override{o})
		case current.enums[v.owner] != nil:
			e := current.enums[v.owner]
			e.overrides = mergeOverrides(e.overrides, []override{o})
		default:
			// The message or enum is gone, so the override has nowhere to
			// attach and goes at the top of the file instead.
			current.removed = mergeOverrides(current.removed, []override{o})
		}
	}
}
