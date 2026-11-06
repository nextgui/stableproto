package lockfile

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/types/descriptorpb"
)

func TestAFieldIsAlwaysOneLine(t *testing.T) {
	// A lock file's job is to make a breaking change visible in review. A
	// printer that wrapped a long field would reflow the fields around the one
	// that changed, and a rename would read as a five-line diff.
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{sample()}}
	current, err := read(set, map[string]bool{"sample.proto": true})
	if err != nil {
		t.Fatalf("reading the descriptor set: %v", err)
	}

	for _, line := range strings.Split(render("example.sample", current), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "(field ") {
			continue
		}
		if opens, closes := strings.Count(trimmed, "("), strings.Count(trimmed, ")"); opens != closes {
			t.Errorf("a field is split across lines: %q", line)
		}
	}
}

func TestMalformedInputIsRefused(t *testing.T) {
	// The syntax layer catches these, so nothing that reads a field ever has to
	// consider whether it is looking at one.
	for _, tc := range []struct{ name, text, want string }{
		{"unclosed list", "(message M", "never closed"},
		{"stray close", "(message M))", "nothing open"},
		{"unclosed string", `(message "M)`, "never closed"},
		{"unknown top form", "(service S)", "unknown top-level form"},
		{"field outside a message", "(enum E (field 1 a optional string))", "unknown form inside enum"},
		{"field with no type", "(message M (field 1 a optional))", "is (field"},
		{"non-numeric field number", "(message M (field x a optional string))", "is not a number"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseLock(tc.text)
			if err == nil {
				t.Fatalf("%q should have been refused", tc.text)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the failure does not say %q: %v", tc.want, err)
			}
		})
	}
}

func TestAnAdditionIsReportedAsOneLine(t *testing.T) {
	// The failure a person sees most often. A text diff of s-expressions would
	// report the closing parenthesis of the form above as a change too, so a
	// one-field addition would read as three lines.
	old := lockOf(t, "(message M (field 1 amount optional int64))")
	now := lockOf(t, "(message M (field 1 amount optional int64) (field 2 note optional string))")

	got := additions(old, now)
	if len(got) != 1 {
		t.Fatalf("wanted one addition, got %d: %v", len(got), got)
	}
	if got[0] != "M (field 2 note optional string)" {
		t.Errorf("the addition reads %q", got[0])
	}
}

func TestCommentsAndBlankLinesAreIgnored(t *testing.T) {
	s, err := parseLock("; a comment\n\n(message M ; trailing\n  (field 1 a optional string))\n")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if got := s.messages["M"]; got == nil || got.fields[1].name != "a" {
		t.Errorf("the message did not survive the comments: %#v", s.messages)
	}
}

func TestAWaivedCheckIsRecordedBesideTheField(t *testing.T) {
	// The whole point of the record. A bypass that left no trace would be
	// indistinguishable in review from an ordinary lock update, which is what
	// deleting the lock file used to be.
	old := lockOf(t, "(message M (field 1 amount optional int64))")
	current := lockOf(t, "(message M (field 1 total optional int64))")

	broken := compare(old, current)
	if len(broken) != 1 {
		t.Fatalf("wanted one violation, got %v", broken)
	}
	waive(current, broken, "nothing has written this yet")

	text := render("example.sample", current)
	if !strings.Contains(text, `(override 1 renamed amount "nothing has written this yet")`) {
		t.Errorf("the override is not in the lock:\n%s", text)
	}
	// Immediately above the field, where a reader looking at that field sees it.
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.Contains(line, "(override 1 ") {
			if i+1 >= len(lines) || !strings.Contains(lines[i+1], "(field 1 total") {
				t.Errorf("the override is not directly above its field:\n%s", text)
			}
		}
	}
}

func TestAWaivedCheckSurvivesTheNextRegeneration(t *testing.T) {
	// A waiver that lasted until the next ordinary update would be worthless:
	// the next person to add a field would quietly erase the record.
	old := lockOf(t, `(message M
  (override 1 renamed amount "nothing has written this yet")
  (field 1 total optional int64))`)
	current := lockOf(t, "(message M (field 1 total optional int64) (field 2 note optional string))")

	carry(old, current)
	text := render("example.sample", current)
	if !strings.Contains(text, `(override 1 renamed amount "nothing has written this yet")`) {
		t.Errorf("the override was dropped:\n%s", text)
	}
}

func TestAWaiverIsNotWrittenTwice(t *testing.T) {
	old := lockOf(t, `(message M
  (override 1 renamed amount "reason")
  (field 1 total optional int64))`)
	current := lockOf(t, `(message M
  (override 1 renamed amount "reason")
  (field 1 total optional int64))`)

	carry(old, current)
	if got := strings.Count(render("p", current), "(override 1 renamed"); got != 1 {
		t.Errorf("the override appears %d times, want 1", got)
	}
}

func TestAnOverrideDoesNotSuppressAnything(t *testing.T) {
	// An override is a record, not a rule. A second rename of the same field
	// has to fail as loudly as the first did.
	old := lockOf(t, `(message M
  (override 1 renamed amount "reason")
  (field 1 total optional int64))`)
	current := lockOf(t, "(message M (field 1 sum optional int64))")

	if broken := compare(old, current); len(broken) != 1 {
		t.Fatalf("a later rename should still be refused, got %v", broken)
	}
}

func TestAWaivedDeletionIsKeptWhenItsFieldIsGone(t *testing.T) {
	// The field the override names no longer exists, so it cannot sit above it.
	// Dropping it would erase the record on the next regeneration.
	current := lockOf(t, `(message M
  (override 7 deleted old_note "the record was never shipped")
  (field 1 amount optional int64))`)

	text := render("p", current)
	if !strings.Contains(text, `(override 7 deleted old_note "the record was never shipped")`) {
		t.Errorf("an override for a departed field was dropped:\n%s", text)
	}
	back, err := parseLock(text)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if len(back.messages["M"].overrides) != 1 {
		t.Errorf("the override did not survive the round trip: %#v", back.messages["M"].overrides)
	}
}
