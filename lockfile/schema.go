package lockfile

import (
	"fmt"
	"sort"

	"google.golang.org/protobuf/types/descriptorpb"
)

// schema is everything about a set of .proto files that a stored message
// depends on, and nothing else.
//
// Comments, option values, service definitions and declaration order are all
// absent, because none of them change how a byte on disk is read. What is here
// is what a reader of old data relies on: which number carries which
// field, what type that field is, and what name protojson will print it under.
type schema struct {
	messages map[string]*message
	enums    map[string]*enum

	// removed holds an override for a message or enum that is no longer in the
	// schema at all, which has nowhere else to live.
	removed []override
}

// message is one message's fields, by number.
type message struct {
	name          string
	fields        map[int32]*field
	reserved      []span
	reservedNames map[string]bool

	// overrides are checks somebody waived with -force. They come from the lock
	// file, never from a descriptor, and are carried forward every time the
	// lock is regenerated.
	overrides []override
}

// field is one field of a message.
type field struct {
	number   int32
	name     string
	label    string // "optional", "repeated", or "explicit"
	kind     string // "int64", "string", "message", "map<string,int64>", ...
	typeName string // fully-qualified, for message and enum kinds
	oneof    string // the oneof it belongs to, or empty

	// Set only for map fields, which name their key and value types rather
	// than the entry message the compiler generated for them.
	mapKey   string
	mapValue string
}

// enum is one enumeration's values, by number.
type enum struct {
	name          string
	values        map[int32]string
	reserved      []span
	reservedNames map[string]bool
	overrides     []override
}

// override records a compatibility check that was waived.
//
// It suppresses nothing. Once a forced lock is written the old spelling is gone
// from it, so the check would not fire again anyway. What this is for is the
// record: a bypass leaves a line in the file that stays there, shows up in the
// diff beside the field it concerns, and can be grepped for across the
// repository. Without it, deleting the lock and regenerating produces a
// one-line diff indistinguishable from an ordinary update.
type override struct {
	// number is the field or enum value waived, or zero for a whole type.
	number int32

	// kind is which check was waived: renamed, retyped, deleted, and so on.
	kind string

	// was is what the lock recorded before the override.
	was string

	// reason is what the person passed to -reason.
	reason string
}

// asOverride turns a violation into the record of it being waived.
func (v violation) asOverride(reason string) override {
	return override{number: v.number, kind: v.kind, was: v.was, reason: reason}
}

// span is an inclusive range of reserved numbers.
type span struct {
	from, to int32
}

func (s span) contains(n int32) bool { return n >= s.from && n <= s.to }

func (s span) String() string {
	if s.from == s.to {
		return fmt.Sprint(s.from)
	}
	return fmt.Sprintf("%d-%d", s.from, s.to)
}

// read builds a schema from the files in a descriptor set.
//
// Only the named files are locked. A descriptor set carries a file's imports
// as well as the file itself, and locking those would make one package's
// schema fail on an unrelated package's edit.
func read(set *descriptorpb.FileDescriptorSet, want map[string]bool) (*schema, error) {
	s := &schema{messages: map[string]*message{}, enums: map[string]*enum{}}

	// Map entries are collected from every file, including imports, because a
	// map field in a locked file can have its entry type declared elsewhere and
	// the lock states the key and value types rather than the entry's name.
	entries := map[string]*descriptorpb.DescriptorProto{}
	for _, file := range set.GetFile() {
		collectEntries(file.GetPackage(), file.GetMessageType(), entries)
	}

	seen := map[string]bool{}
	for _, file := range set.GetFile() {
		if !want[file.GetName()] {
			continue
		}
		seen[file.GetName()] = true
		pkg := file.GetPackage()
		if err := s.readMessages(pkg, file.GetMessageType(), entries); err != nil {
			return nil, fmt.Errorf("%s: %w", file.GetName(), err)
		}
		for _, e := range file.GetEnumType() {
			s.readEnum(pkg, e)
		}
	}
	for name := range want {
		if !seen[name] {
			return nil, fmt.Errorf("the descriptor set has no file named %q", name)
		}
	}
	return s, nil
}

// collectEntries indexes the synthetic messages the compiler generates for map
// fields, so a map field can be described by what it holds.
func collectEntries(prefix string, msgs []*descriptorpb.DescriptorProto, into map[string]*descriptorpb.DescriptorProto) {
	for _, m := range msgs {
		full := join(prefix, m.GetName())
		if m.GetOptions().GetMapEntry() {
			into["."+full] = m
		}
		collectEntries(full, m.GetNestedType(), into)
	}
}

func (s *schema) readMessages(prefix string, msgs []*descriptorpb.DescriptorProto, entries map[string]*descriptorpb.DescriptorProto) error {
	for _, m := range msgs {
		full := join(prefix, m.GetName())
		// A map entry is not a message anybody declared, so locking it would
		// pin a name the compiler chose rather than one a person wrote.
		if !m.GetOptions().GetMapEntry() {
			out, err := readMessage(full, m, entries)
			if err != nil {
				return err
			}
			s.messages[full] = out
		}
		if err := s.readMessages(full, m.GetNestedType(), entries); err != nil {
			return err
		}
		for _, e := range m.GetEnumType() {
			s.readEnum(full, e)
		}
	}
	return nil
}

func readMessage(full string, m *descriptorpb.DescriptorProto, entries map[string]*descriptorpb.DescriptorProto) (*message, error) {
	out := &message{name: full, fields: map[int32]*field{}, reservedNames: map[string]bool{}}
	for _, r := range m.GetReservedRange() {
		// A descriptor's reserved end is exclusive; the lock states an
		// inclusive range, because that is what the .proto file says.
		out.reserved = append(out.reserved, span{from: r.GetStart(), to: r.GetEnd() - 1})
	}
	for _, n := range m.GetReservedName() {
		out.reservedNames[n] = true
	}
	for _, f := range m.GetField() {
		got, err := readField(f, m, entries)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", full, err)
		}
		if prev, ok := out.fields[got.number]; ok {
			return nil, fmt.Errorf("%s: field %d is both %q and %q", full, got.number, prev.name, got.name)
		}
		out.fields[got.number] = got
	}
	return out, nil
}

func readField(f *descriptorpb.FieldDescriptorProto, m *descriptorpb.DescriptorProto, entries map[string]*descriptorpb.DescriptorProto) (*field, error) {
	out := &field{number: f.GetNumber(), name: f.GetName(), typeName: f.GetTypeName()}

	switch f.GetLabel() {
	case descriptorpb.FieldDescriptorProto_LABEL_REPEATED:
		out.label = "repeated"
	default:
		out.label = "optional"
	}
	// A proto3 `optional` field is distinguished from a plain one, because the
	// two serialise differently: an explicit-presence field writes a zero value
	// where a plain one omits it.
	if f.GetProto3Optional() {
		out.label = "explicit"
	}

	kind, ok := kindNames[f.GetType()]
	if !ok {
		return nil, fmt.Errorf("field %q has unknown type %v", f.GetName(), f.GetType())
	}
	out.kind = kind

	// A map field is a repeated message of a synthetic entry type. Stating the
	// key and value types instead pins what actually has to keep working.
	if entry, isMap := entries[f.GetTypeName()]; isMap && out.label == "repeated" {
		key, value, err := mapTypes(entry, entries)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", f.GetName(), err)
		}
		out.kind = fmt.Sprintf("map<%s,%s>", key, value)
		out.mapKey, out.mapValue = key, value
		out.label = "optional"
		out.typeName = ""
	}

	if f.OneofIndex != nil && !f.GetProto3Optional() {
		idx := f.GetOneofIndex()
		if int(idx) >= len(m.GetOneofDecl()) {
			return nil, fmt.Errorf("field %q names oneof %d, which does not exist", f.GetName(), idx)
		}
		out.oneof = m.GetOneofDecl()[idx].GetName()
	}
	return out, nil
}

// mapTypes describes a map entry's key and value.
func mapTypes(entry *descriptorpb.DescriptorProto, entries map[string]*descriptorpb.DescriptorProto) (string, string, error) {
	var key, value string
	for _, f := range entry.GetField() {
		kind, ok := kindNames[f.GetType()]
		if !ok {
			return "", "", fmt.Errorf("unknown type %v", f.GetType())
		}
		if kind == "message" || kind == "enum" {
			kind = f.GetTypeName()
		}
		switch f.GetNumber() {
		case 1:
			key = kind
		case 2:
			value = kind
		}
	}
	if key == "" || value == "" {
		return "", "", fmt.Errorf("map entry %q has no key or no value", entry.GetName())
	}
	return key, value, nil
}

func (s *schema) readEnum(prefix string, e *descriptorpb.EnumDescriptorProto) {
	full := join(prefix, e.GetName())
	out := &enum{name: full, values: map[int32]string{}, reservedNames: map[string]bool{}}
	for _, r := range e.GetReservedRange() {
		// An enum's reserved end is inclusive, unlike a message's.
		out.reserved = append(out.reserved, span{from: r.GetStart(), to: r.GetEnd()})
	}
	for _, n := range e.GetReservedName() {
		out.reservedNames[n] = true
	}
	for _, v := range e.GetValue() {
		// An alias declares two names for one number. The first is the one
		// protojson prints, so it is the one worth pinning.
		if _, ok := out.values[v.GetNumber()]; !ok {
			out.values[v.GetNumber()] = v.GetName()
		}
	}
	s.enums[full] = out
}

func join(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// kindNames spells each wire type the way a .proto file does.
var kindNames = map[descriptorpb.FieldDescriptorProto_Type]string{
	descriptorpb.FieldDescriptorProto_TYPE_DOUBLE:   "double",
	descriptorpb.FieldDescriptorProto_TYPE_FLOAT:    "float",
	descriptorpb.FieldDescriptorProto_TYPE_INT64:    "int64",
	descriptorpb.FieldDescriptorProto_TYPE_UINT64:   "uint64",
	descriptorpb.FieldDescriptorProto_TYPE_INT32:    "int32",
	descriptorpb.FieldDescriptorProto_TYPE_FIXED64:  "fixed64",
	descriptorpb.FieldDescriptorProto_TYPE_FIXED32:  "fixed32",
	descriptorpb.FieldDescriptorProto_TYPE_BOOL:     "bool",
	descriptorpb.FieldDescriptorProto_TYPE_STRING:   "string",
	descriptorpb.FieldDescriptorProto_TYPE_GROUP:    "group",
	descriptorpb.FieldDescriptorProto_TYPE_MESSAGE:  "message",
	descriptorpb.FieldDescriptorProto_TYPE_BYTES:    "bytes",
	descriptorpb.FieldDescriptorProto_TYPE_UINT32:   "uint32",
	descriptorpb.FieldDescriptorProto_TYPE_ENUM:     "enum",
	descriptorpb.FieldDescriptorProto_TYPE_SFIXED32: "sfixed32",
	descriptorpb.FieldDescriptorProto_TYPE_SFIXED64: "sfixed64",
	descriptorpb.FieldDescriptorProto_TYPE_SINT32:   "sint32",
	descriptorpb.FieldDescriptorProto_TYPE_SINT64:   "sint64",
}

// names lists every message and enum, sorted.
func (s *schema) names() []string {
	out := make([]string, 0, len(s.messages)+len(s.enums))
	for name := range s.messages {
		out = append(out, name)
	}
	for name := range s.enums {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// sortOverrides puts overrides in a stable order so a regenerated lock does not
// churn.
func sortOverrides(in []override) []override {
	out := append([]override(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].number != out[j].number {
			return out[i].number < out[j].number
		}
		if out[i].kind != out[j].kind {
			return out[i].kind < out[j].kind
		}
		return out[i].reason < out[j].reason
	})
	return out
}

// mergeOverrides adds new overrides to the ones already recorded, dropping an
// exact repeat so that forcing the same waiver twice does not accumulate.
func mergeOverrides(existing, added []override) []override {
	seen := map[override]bool{}
	var out []override
	for _, o := range append(append([]override(nil), existing...), added...) {
		if seen[o] {
			continue
		}
		seen[o] = true
		out = append(out, o)
	}
	return sortOverrides(out)
}

func sortedSpans(in []span) []span {
	out := append([]span(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].from < out[j].from })
	return out
}

func sortedNames(in map[string]bool) []string {
	out := make([]string, 0, len(in))
	for name := range in {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func sortedNumbers[T any](in map[int32]T) []int32 {
	out := make([]int32, 0, len(in))
	for n := range in {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// String renders one field the way the lock file spells it, for a message that
// has to name one.
func (f *field) String() string { return fieldNode(f).String() }
