package lockfile_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nextgui/stableproto/lockfile"
)

func parse(t *testing.T, text string) *lockfile.Lock {
	t.Helper()
	l, err := lockfile.Parse(text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return l
}

const before = `(package example)
(message example.Note
  (field 1 id optional string)
  (field 2 title optional string))
`

const renamed = `(package example)
(message example.Note
  (field 1 id optional string)
  (field 2 heading optional string))
`

func TestNextRefusesARenameWithoutAWaiver(t *testing.T) {
	next, broken := lockfile.Next(parse(t, before), parse(t, renamed), "")
	if next != nil || len(broken) != 1 || broken[0].Kind() != "renamed" || broken[0].Owner() != "example.Note" {
		t.Fatalf("Next returned %v, %v; want one rename of example.Note refused", next, broken)
	}
}

func TestAWaiverIsRecordedAndKept(t *testing.T) {
	waived, broken := lockfile.Next(parse(t, before), parse(t, renamed), "nothing has written this yet")
	if len(broken) != 0 {
		t.Fatalf("a waived rename was refused: %v", broken)
	}
	text := waived.Render("example")
	if !strings.Contains(text, `(override 2 renamed title "nothing has written this yet")`) {
		t.Fatalf("the waiver is not in the lock:\n%s", text)
	}

	// The next ordinary update keeps it.
	again, broken := lockfile.Next(parse(t, text), parse(t, renamed), "")
	if len(broken) != 0 || again.Render("example") != text {
		t.Fatalf("the next update lost the waiver or refused: %v\n%s", broken, again.Render("example"))
	}
}

func TestNextLeavesItsArgumentsAlone(t *testing.T) {
	locked, current := parse(t, before), parse(t, renamed)
	want := current.Render("example")
	lockfile.Next(locked, current, "waived")
	if got := current.Render("example"); got != want {
		t.Fatalf("Next changed the schema it was given:\n%s", got)
	}
}

func TestMessagesAreSorted(t *testing.T) {
	l := parse(t, "(package p)\n(message p.B (field 1 a optional string))\n(message p.A (field 1 a optional string))\n")
	if got := l.Messages(); !reflect.DeepEqual(got, []string{"p.A", "p.B"}) {
		t.Fatalf("Messages = %v", got)
	}
}
