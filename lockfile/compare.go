package lockfile

import (
	"fmt"
	"sort"
)

// violation is one way the current schema breaks what the lock recorded.
//
// Structured rather than a sentence, because -force has to write down which
// check it waived. A sentence would mean parsing prose back into a record.
type violation struct {
	// owner is the message or enum the violation is about.
	owner string

	// number is the field or enum value concerned, or zero when the subject is
	// the whole type.
	number int32

	// kind is what was done: renamed, renumbered, retyped, relabelled,
	// requeued into a oneof, unreserved, deleted, or removed.
	kind string

	// was is what the lock recorded, in whatever spelling suits the kind.
	was string

	// detail is the sentence a person reads.
	detail string
}

func (v violation) String() string { return v.detail }

// compare reports every way the current schema breaks what the lock recorded.
//
// It is asymmetric on purpose. Adding a message, a field or an enum value is
// allowed and produces nothing; the lock is regenerated and the addition shows
// up in review as a new line. Everything else — a rename, a renumbering, a
// change of type, a deletion that reserves nothing — changes what already
// written data decodes to, and is reported.
//
// Overrides recorded in the lock are ignored here. They record a check somebody
// waived rather than suppressing one: once a forced lock has been written, the
// old spelling is gone from it and there is nothing left for this to find.
func compare(old, current *schema) []violation {
	var broken []violation
	add := func(v violation) { broken = append(broken, v) }

	for _, name := range old.names() {
		if m, ok := old.messages[name]; ok {
			switch now, ok := current.messages[name]; {
			case ok:
				compareMessage(m, now, add)
			case current.enums[name] != nil:
				add(violation{owner: name, kind: "removed", was: name,
					detail: fmt.Sprintf("%s was a message and is now an enum", name)})
			default:
				add(violation{owner: name, kind: "removed", was: name,
					detail: fmt.Sprintf("%s was deleted; a stored message cannot be removed while stored data still holds it", name)})
			}
			continue
		}
		e := old.enums[name]
		switch now, ok := current.enums[name]; {
		case ok:
			compareEnum(e, now, add)
		case current.messages[name] != nil:
			add(violation{owner: name, kind: "removed", was: name,
				detail: fmt.Sprintf("%s was an enum and is now a message", name)})
		default:
			add(violation{owner: name, kind: "removed", was: name,
				detail: fmt.Sprintf("%s was deleted; a stored enum cannot be removed while stored data still holds it", name)})
		}
	}

	sort.Slice(broken, func(i, j int) bool {
		if broken[i].owner != broken[j].owner {
			return broken[i].owner < broken[j].owner
		}
		if broken[i].number != broken[j].number {
			return broken[i].number < broken[j].number
		}
		return broken[i].kind < broken[j].kind
	})
	return broken
}

func compareMessage(old, current *message, add func(violation)) {
	for _, n := range sortedNumbers(old.fields) {
		was := old.fields[n]
		now, ok := current.fields[n]
		if !ok {
			if !reservedAny(current.reserved, n) {
				add(violation{owner: old.name, number: n, kind: "deleted", was: was.name,
					detail: fmt.Sprintf("%s field %d (%s) was deleted without reserving %d; a later field would reuse the number and decode old data as itself", old.name, n, was.name, n)})
			}
			continue
		}
		if was.name != now.name {
			add(violation{owner: old.name, number: n, kind: "renamed", was: was.name,
				detail: fmt.Sprintf("%s field %d was renamed from %s to %s; a JSON export of existing data names it %s", old.name, n, was.name, now.name, was.name)})
		}
		if was.kind != now.kind || was.typeName != now.typeName {
			add(violation{owner: old.name, number: n, kind: "retyped", was: spell(was),
				detail: fmt.Sprintf("%s field %d (%s) changed type from %s to %s", old.name, n, was.name, spell(was), spell(now))})
		}
		if was.label != now.label {
			add(violation{owner: old.name, number: n, kind: "relabelled", was: was.label,
				detail: fmt.Sprintf("%s field %d (%s) changed from %s to %s", old.name, n, was.name, was.label, now.label)})
		}
		if was.oneof != now.oneof {
			add(violation{owner: old.name, number: n, kind: "requeued", was: orNone(was.oneof),
				detail: fmt.Sprintf("%s field %d (%s) changed oneof from %s to %s", old.name, n, was.name, orNone(was.oneof), orNone(now.oneof))})
		}
	}

	// A name that moved to another number is the deletion-and-reuse case: the
	// number check above passes, because both numbers are accounted for, and a
	// JSON export still reads the old name straight into the new field.
	byName := map[string]int32{}
	for n, f := range current.fields {
		byName[f.name] = n
	}
	for _, n := range sortedNumbers(old.fields) {
		was := old.fields[n]
		if now, ok := byName[was.name]; ok && now != n {
			add(violation{owner: old.name, number: now, kind: "renumbered", was: fmt.Sprint(n),
				detail: fmt.Sprintf("%s moved the name %s from field %d to field %d", old.name, was.name, n, now)})
		}
	}

	for _, name := range sortedNames(old.reservedNames) {
		if n, ok := byName[name]; ok {
			add(violation{owner: old.name, number: n, kind: "unreserved", was: name,
				detail: fmt.Sprintf("%s reserved the name %s and now uses it again", old.name, name)})
		}
	}
}

func compareEnum(old, current *enum, add func(violation)) {
	for _, n := range sortedNumbers(old.values) {
		was := old.values[n]
		now, ok := current.values[n]
		if !ok {
			if !reservedAny(current.reserved, n) {
				add(violation{owner: old.name, number: n, kind: "deleted", was: was,
					detail: fmt.Sprintf("%s value %d (%s) was deleted without reserving %d", old.name, n, was, n)})
			}
			continue
		}
		if was != now {
			add(violation{owner: old.name, number: n, kind: "renamed", was: was,
				detail: fmt.Sprintf("%s value %d was renamed from %s to %s", old.name, n, was, now)})
		}
	}

	byName := map[string]int32{}
	for n, name := range current.values {
		byName[name] = n
	}
	for _, n := range sortedNumbers(old.values) {
		was := old.values[n]
		if now, ok := byName[was]; ok && now != n {
			add(violation{owner: old.name, number: now, kind: "renumbered", was: fmt.Sprint(n),
				detail: fmt.Sprintf("%s moved the name %s from value %d to value %d", old.name, was, n, now)})
		}
	}
}

func reservedAny(spans []span, n int32) bool {
	for _, s := range spans {
		if s.contains(n) {
			return true
		}
	}
	return false
}

// spell names a field's type the way the lock file does.
func spell(f *field) string {
	if f.typeName != "" {
		return f.kind + " " + f.typeName
	}
	return f.kind
}

func orNone(name string) string {
	if name == "" {
		return "no oneof"
	}
	return name
}

// additions lists what the schema has that the lock does not.
//
// Reached only after compare found nothing broken, so every difference left is
// something added. Computed from the two schemas rather than from the two files
// because a text diff of s-expressions reports the closing parenthesis of the
// form above as a change, which turns adding one field into three lines.
func additions(old, current *schema) []string {
	var out []string
	add := func(format string, args ...any) {
		out = append(out, fmt.Sprintf(format, args...))
	}

	for _, name := range current.names() {
		if m, ok := current.messages[name]; ok {
			was, existed := old.messages[name]
			if !existed {
				add("message %s", name)
				continue
			}
			for _, n := range sortedNumbers(m.fields) {
				if _, ok := was.fields[n]; !ok {
					add("%s %s", name, m.fields[n])
				}
			}
			for _, r := range sortedSpans(m.reserved) {
				if !reservedAny(was.reserved, r.from) {
					add("%s reserved %s", name, r)
				}
			}
			for _, n := range sortedNames(m.reservedNames) {
				if !was.reservedNames[n] {
					add("%s reserved name %s", name, n)
				}
			}
			continue
		}
		e := current.enums[name]
		was, existed := old.enums[name]
		if !existed {
			add("enum %s", name)
			continue
		}
		for _, n := range sortedNumbers(e.values) {
			if _, ok := was.values[n]; !ok {
				add("%s value %d %s", name, n, e.values[n])
			}
		}
		for _, r := range sortedSpans(e.reserved) {
			if !reservedAny(was.reserved, r.from) {
				add("%s reserved %s", name, r)
			}
		}
		for _, n := range sortedNames(e.reservedNames) {
			if !was.reservedNames[n] {
				add("%s reserved name %s", name, n)
			}
		}
	}

	sort.Strings(out)
	return out
}
