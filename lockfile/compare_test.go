package lockfile

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// lockOf reads a schema out of a lock file, so a test can state the thing it is
// testing as the file a person would open.
func lockOf(t *testing.T, text string) *schema {
	t.Helper()
	s, err := parseLock(text)
	if err != nil {
		t.Fatalf("parsing the lock: %v", err)
	}
	return s
}

// only asserts a single failure and returns it, so a test that means to check
// one rule does not quietly pass on a different rule firing.
func only(t *testing.T, broken []violation) string {
	t.Helper()
	if len(broken) != 1 {
		t.Fatalf("wanted exactly one failure, got %d: %v", len(broken), broken)
	}
	return broken[0].detail
}

func TestARenamedFieldIsRefused(t *testing.T) {
	old := lockOf(t, "(message M (field 1 amount optional int64))")
	now := lockOf(t, "(message M (field 1 total optional int64))")

	got := only(t, compare(old, now))
	if !strings.Contains(got, "renamed from amount to total") {
		t.Errorf("the failure does not say what was renamed: %s", got)
	}
}

func TestARenumberedFieldIsRefused(t *testing.T) {
	old := lockOf(t, "(message M (field 1 amount optional int64))")
	now := lockOf(t, "(message M (field 2 amount optional int64) (reserved 1))")

	// Reserving 1 answers the deletion rule, so what is left is the name having
	// moved. Without that rule a renumbering would pass as a delete plus an add.
	got := only(t, compare(old, now))
	if !strings.Contains(got, "moved the name amount from field 1 to field 2") {
		t.Errorf("the failure does not say the name moved: %s", got)
	}
}

func TestARetypedFieldIsRefused(t *testing.T) {
	old := lockOf(t, "(message M (field 1 amount optional int64))")
	now := lockOf(t, "(message M (field 1 amount optional string))")

	got := only(t, compare(old, now))
	if !strings.Contains(got, "changed type from int64 to string") {
		t.Errorf("the failure does not say what the type was: %s", got)
	}
}

func TestAFieldThatBecomesRepeatedIsRefused(t *testing.T) {
	old := lockOf(t, "(message M (field 1 tag optional string))")
	now := lockOf(t, "(message M (field 1 tag repeated string))")

	got := only(t, compare(old, now))
	if !strings.Contains(got, "changed from optional to repeated") {
		t.Errorf("the failure does not say what changed: %s", got)
	}
}

func TestAFieldMovedIntoAOneofIsRefused(t *testing.T) {
	old := lockOf(t, "(message M (field 1 amount optional int64))")
	now := lockOf(t, "(message M (field 1 amount optional int64 (oneof payload)))")

	got := only(t, compare(old, now))
	if !strings.Contains(got, "changed oneof from no oneof to payload") {
		t.Errorf("the failure does not name the oneof: %s", got)
	}
}

func TestADeletedFieldIsRefusedUnlessItsNumberIsReserved(t *testing.T) {
	old := lockOf(t, "(message M (field 1 amount optional int64) (field 2 note optional string))")

	gone := lockOf(t, "(message M (field 1 amount optional int64))")
	got := only(t, compare(old, gone))
	if !strings.Contains(got, "deleted without reserving 2") {
		t.Errorf("the failure does not say to reserve the number: %s", got)
	}

	reserved := lockOf(t, "(message M (field 1 amount optional int64) (reserved 2))")
	if broken := compare(old, reserved); len(broken) != 0 {
		t.Errorf("reserving the number should have been enough: %v", broken)
	}
}

func TestAReservedNameCannotComeBack(t *testing.T) {
	// The number is reserved, so the wire format is safe. The name is not, and
	// a JSON export of existing data would read `note` straight into field 3.
	old := lockOf(t, "(message M (field 2 note optional string))")
	now := lockOf(t, "(message M (field 3 note optional string) (reserved 2))")

	got := only(t, compare(old, now))
	if !strings.Contains(got, "moved the name note from field 2 to field 3") {
		t.Errorf("the failure does not say the name came back: %s", got)
	}
}

func TestAReservedNameStaysReserved(t *testing.T) {
	old := lockOf(t, "(message M (reserved 2) (reserved-name note))")
	now := lockOf(t, "(message M (field 3 note optional string) (reserved 2))")

	got := only(t, compare(old, now))
	if !strings.Contains(got, "reserved the name note and now uses it again") {
		t.Errorf("the failure does not mention the reserved name: %s", got)
	}
}

func TestADeletedMessageIsRefused(t *testing.T) {
	old := lockOf(t, "(message M (field 1 amount optional int64))")
	now := lockOf(t, "(message N (field 1 amount optional int64))")

	got := only(t, compare(old, now))
	if !strings.Contains(got, "M was deleted") {
		t.Errorf("the failure does not name the message: %s", got)
	}
}

func TestARenamedEnumValueIsRefused(t *testing.T) {
	old := lockOf(t, "(enum E (value 0 UNSPECIFIED) (value 1 OPEN))")
	now := lockOf(t, "(enum E (value 0 UNSPECIFIED) (value 1 ACTIVE))")

	got := only(t, compare(old, now))
	if !strings.Contains(got, "value 1 was renamed from OPEN to ACTIVE") {
		t.Errorf("the failure does not say what was renamed: %s", got)
	}
}

func TestAddingIsAllowed(t *testing.T) {
	old := lockOf(t, "(message M (field 1 amount optional int64))")
	now := lockOf(t, `
(message M
  (field 1 amount optional int64)
  (field 2 note optional string))

(message N
  (field 1 id optional string))

(enum E
  (value 0 UNSPECIFIED))
`)

	if broken := compare(old, now); len(broken) != 0 {
		t.Errorf("adding should not have failed: %v", broken)
	}
}

func TestAMessageThatBecomesAnEnumIsRefused(t *testing.T) {
	old := lockOf(t, "(message M (field 1 amount optional int64))")
	now := lockOf(t, "(enum M (value 0 UNSPECIFIED))")

	got := only(t, compare(old, now))
	if !strings.Contains(got, "M was a message and is now an enum") {
		t.Errorf("the failure does not say what M became: %s", got)
	}
}

func TestALockSurvivesTheRoundTrip(t *testing.T) {
	// The comparison reads the checked-in lock back into the same shape a
	// descriptor set produces. If rendering and parsing disagreed, every check
	// would report a change that is not there.
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{sample()}}
	current, err := read(set, map[string]bool{"sample.proto": true})
	if err != nil {
		t.Fatalf("reading the descriptor set: %v", err)
	}

	text := render("example.sample", current)
	back, err := parseLock(text)
	if err != nil {
		t.Fatalf("parsing what was rendered: %v", err)
	}

	if again := render("example.sample", back); again != text {
		t.Errorf("the round trip changed the lock\n got:\n%s\nwant:\n%s", again, text)
	}
	if broken := compare(back, current); len(broken) != 0 {
		t.Errorf("a lock should not be incompatible with the schema it came from: %v", broken)
	}
}

func TestAMapFieldIsLockedByWhatItHolds(t *testing.T) {
	// A map field's entry type is named by the compiler, not by a person. The
	// lock states the key and value types so that a rename of the message the
	// map lives in does not read as a change of the map's type.
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{sample()}}
	current, err := read(set, map[string]bool{"sample.proto": true})
	if err != nil {
		t.Fatalf("reading the descriptor set: %v", err)
	}

	m := current.messages["example.sample.Thing"]
	if m == nil {
		t.Fatalf("the sample has no Thing; it has %v", current.names())
	}
	if got := m.fields[3].kind; got != "map<string,int64>" {
		t.Errorf("the map field is locked as %q, want map<string,int64>", got)
	}
	if len(current.messages) != 1 {
		t.Errorf("the synthetic map entry should not be locked as a message: %v", current.names())
	}
}

func TestAFileOutsideTheLockIsIgnored(t *testing.T) {
	// A descriptor set carries a file's imports. Locking those would make one
	// package's check fail on an unrelated package's edit.
	other := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("other.proto"),
		Package: proto.String("example.other"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("Elsewhere")},
		},
	}
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{sample(), other}}

	current, err := read(set, map[string]bool{"sample.proto": true})
	if err != nil {
		t.Fatalf("reading the descriptor set: %v", err)
	}
	if current.messages["example.other.Elsewhere"] != nil {
		t.Errorf("an imported file was locked: %v", current.names())
	}
}

func TestAMissingFileIsRefused(t *testing.T) {
	// A typo in the file list would otherwise lock nothing and pass, which is
	// the worst outcome available: a check that reports success and checks
	// nothing.
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{sample()}}

	_, err := read(set, map[string]bool{"sample.proto": true, "typo.proto": true})
	if err == nil {
		t.Fatal("a file that is not in the descriptor set should have been refused")
	}
	if !strings.Contains(err.Error(), "typo.proto") {
		t.Errorf("the failure does not name the missing file: %v", err)
	}
}

// sample is a descriptor exercising the shapes the lock has to spell: a scalar,
// an explicit-presence field, a map, a oneof, a nested enum and a reservation.
func sample() *descriptorpb.FileDescriptorProto {
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	repeated := descriptorpb.FieldDescriptorProto_LABEL_REPEATED
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING
	i64 := descriptorpb.FieldDescriptorProto_TYPE_INT64
	msg := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	enum := descriptorpb.FieldDescriptorProto_TYPE_ENUM

	return &descriptorpb.FileDescriptorProto{
		Name:    proto.String("sample.proto"),
		Package: proto.String("example.sample"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Thing"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: proto.String("id"), Number: proto.Int32(1), Label: &optional, Type: &str},
				{Name: proto.String("note"), Number: proto.Int32(2), Label: &optional, Type: &str, Proto3Optional: proto.Bool(true), OneofIndex: proto.Int32(1)},
				{Name: proto.String("counts"), Number: proto.Int32(3), Label: &repeated, Type: &msg, TypeName: proto.String(".example.sample.Thing.CountsEntry")},
				{Name: proto.String("state"), Number: proto.Int32(4), Label: &optional, Type: &enum, TypeName: proto.String(".example.sample.Thing.State")},
				{Name: proto.String("cash"), Number: proto.Int32(5), Label: &optional, Type: &i64, OneofIndex: proto.Int32(0)},
			},
			OneofDecl: []*descriptorpb.OneofDescriptorProto{
				{Name: proto.String("payload")},
				{Name: proto.String("_note")},
			},
			NestedType: []*descriptorpb.DescriptorProto{{
				Name:    proto.String("CountsEntry"),
				Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("key"), Number: proto.Int32(1), Label: &optional, Type: &str},
					{Name: proto.String("value"), Number: proto.Int32(2), Label: &optional, Type: &i64},
				},
			}},
			EnumType: []*descriptorpb.EnumDescriptorProto{{
				Name: proto.String("State"),
				Value: []*descriptorpb.EnumValueDescriptorProto{
					{Name: proto.String("STATE_UNSPECIFIED"), Number: proto.Int32(0)},
				},
			}},
			// A message's reserved end is exclusive, so this is 8 and 9.
			ReservedRange: []*descriptorpb.DescriptorProto_ReservedRange{
				{Start: proto.Int32(8), End: proto.Int32(10)},
			},
			ReservedName: []string{"old_note"},
		}},
	}
}
