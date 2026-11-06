// Command stableproto keeps a stored schema from changing underneath the data
// already written with it.
//
// For a message that is stored, the field numbers and the field names are a
// published interface: the number is what the wire format reads, and the name
// is what a JSON export of the same record prints. Renaming a field is invisible
// in a wire-format round trip and silently renames a column in everybody's
// export. Renumbering one silently changes what old bytes decode to.
//
// This tool writes those facts out to a lock file that is checked in beside the
// .proto, and fails when the schema and the lock disagree.
//
//	-check   compare the schema with the lock and fail on a difference
//	-update  rewrite the lock, refusing changes that would break stored data
//	-force   with -update, waive the checks and record why, in the lock
//	-init    with -update, write a lock for a schema that has none yet
//
// The refusal in -update is the point. A check whose failure message is "run
// the update command" is not a check, because the update command is the first
// thing anybody runs. So -update applies the same compatibility rules -check
// does: adding a field regenerates cleanly, and renaming one does not.
//
// # Waiving a check
//
// Sometimes the waiver is right, for a schema nothing has ever written, say.
// That needs -force and a -reason, which is written into the lock beside the
// field it concerns and carried forward by every later regeneration:
//
//	(override 2 renamed accounts "nothing has ever written this schema")
//	(field 2 account_list repeated message .example.Account)
//
// Deleting the lock and regenerating it would produce a diff indistinguishable
// from an ordinary update, so -update refuses a missing or empty lock unless
// -init says the schema is new.
//
// # What this cannot do
//
// The lock file is the only state, so anyone who can write it can bypass the
// check by editing a line by hand. This raises the cost of a silent bypass and
// makes a declared one permanent; it does not make one impossible. Only
// comparing against a known-good revision would, and that needs the
// repository's history, which a Bazel action does not have.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/nextgui/stableproto/lockfile"
)

// files collects a repeatable flag.
type files []string

func (f *files) String() string     { return strings.Join(*f, ",") }
func (f *files) Set(v string) error { *f = append(*f, v); return nil }

func main() {
	var (
		descriptorSet = flag.String("descriptor_set", "", "Path to a serialised FileDescriptorSet holding the schema.")
		lock          = flag.String("lock", "", "Path to the lock file.")
		pkg           = flag.String("package", "", "The proto package being locked, for the lock file's header.")
		check         = flag.Bool("check", false, "Compare the schema with the lock and fail on a difference.")
		update        = flag.Bool("update", false, "Rewrite the lock, refusing changes that break stored data.")
		stamp         = flag.String("stamp", "", "Path to write on success, so a build action has an output.")
		force         = flag.Bool("force", false, "With -update, waive the compatibility checks and record why in the lock.")
		reason        = flag.String("reason", "", "Why the checks are being waived. Required with -force, and written into the lock.")
		initial       = flag.Bool("init", false, "With -update, write a lock for a schema that has none yet.")
		srcs          files
	)
	flag.Var(&srcs, "file", "A .proto file in the descriptor set to lock, by its import path. Repeatable.")
	flag.Parse()

	if err := run(*descriptorSet, srcs, *lock, *pkg, *check, *update, *stamp, *force, *reason, *initial); err != nil {
		fmt.Fprintf(os.Stderr, "stableproto: %v\n", err)
		os.Exit(1)
	}
}

func run(descriptorSet string, srcs files, lock, pkg string, check, update bool, stamp string, force bool, reason string, initial bool) error {
	switch {
	case descriptorSet == "" || lock == "" || len(srcs) == 0:
		return errors.New("-descriptor_set, -lock and at least one -file are required")
	case check == update:
		return errors.New("pass exactly one of -check and -update")
	case force && !update:
		return errors.New("-force applies to -update")
	case initial && !update:
		return errors.New("-init applies to -update")
	case force && strings.TrimSpace(reason) == "":
		// A waiver with no reason is the thing this flag exists to prevent. The
		// reason is written into the lock and read by whoever finds it later.
		return errors.New("-force needs -reason explaining why the check is being waived")
	case reason != "" && !force:
		return errors.New("-reason applies to -force")
	}

	// Under `bazel run` the working directory is a runfiles tree, and writing
	// the lock there would update a copy nobody ever reads again. Bazel says
	// where the real source is.
	if root := os.Getenv("BUILD_WORKSPACE_DIRECTORY"); update && root != "" && !filepath.IsAbs(lock) {
		lock = filepath.Join(root, lock)
	}

	raw, err := os.ReadFile(descriptorSet)
	if err != nil {
		return fmt.Errorf("reading the descriptor set: %w", err)
	}
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(raw, &set); err != nil {
		return fmt.Errorf("parsing the descriptor set: %w", err)
	}
	current, err := lockfile.FromDescriptors(&set, srcs)
	if err != nil {
		return err
	}

	locked, err := os.ReadFile(lock)
	switch {
	case errors.Is(err, os.ErrNotExist) || len(bytes.TrimSpace(locked)) == 0:
		// A lock that is absent or empty is a schema nobody has locked yet, and
		// also what deleting a lock leaves behind. Those are indistinguishable
		// from here, so writing one is deliberate rather than automatic.
		switch {
		case check:
			return fmt.Errorf("%s is missing or empty; run the update with -init to write it", lock)
		case !initial:
			return fmt.Errorf(`%s is missing or empty.

If this schema has never been locked, pass -init. If the lock was deleted, put
it back: regenerating it from scratch would silently accept a change the check
exists to refuse.
`, lock)
		}
		return write(lock, current.Render(pkg), stamp)
	case err != nil:
		return fmt.Errorf("reading the lock: %w", err)
	case initial:
		return fmt.Errorf("%s already exists; -init is for a schema that has never been locked", lock)
	}

	old, err := lockfile.Parse(string(locked))
	if err != nil {
		return fmt.Errorf("reading %s: %w", lock, err)
	}

	waiver := ""
	if force {
		waiver = strings.TrimSpace(reason)
	}
	next, broken := lockfile.Next(old, current, waiver)
	if len(broken) > 0 {
		return incompatible(lock, broken)
	}

	rendered := next.Render(pkg)
	if string(locked) == rendered {
		return write("", "", stamp)
	}
	if update {
		return write(lock, rendered, stamp)
	}
	return fmt.Errorf(`the schema and %s disagree.

%s
Nothing here breaks data already written, so regenerate the lock:

  bazel run %s

`, lock, added(old, current), updateTarget(lock))
}

// incompatible is the failure this tool exists for, so it says what broke and
// what to do instead, rather than pointing at a command that will not work.
func incompatible(lock string, broken []lockfile.Violation) error {
	var b strings.Builder
	fmt.Fprintf(&b, "the schema changes what data already written with it means:\n\n")
	for _, v := range broken {
		fmt.Fprintf(&b, "  %s\n", v)
	}
	fmt.Fprintf(&b, `
Data written with the previous schema is still out there and still has to be
read. Regenerating %s will not help; the update refuses these too.

Reserve what you are removing and add a new field beside it:

  reserved 7;
  reserved "old_name";

If the schema really has to change shape, that is a new schema version with a
migration, not an edit to this one.

If it genuinely has to be waived, for a schema nothing has written yet, say,
run the update with -force and -reason. That writes the lock and records the
waiver in it, beside the field, where it stays:

  bazel run %s -- -force -reason "nothing has ever written this schema"
`, lock, updateTarget(lock))
	return errors.New(b.String())
}

// updateTarget guesses the Bazel label that rewrites a lock, so the failure
// message is a command rather than a description of one.
func updateTarget(lock string) string {
	dir, file, ok := cutLast(lock, "/")
	if !ok {
		return "//:" + strings.TrimSuffix(file, ".lock") + "_lock_update"
	}
	return fmt.Sprintf("//%s:%s_lock_update", dir, strings.TrimSuffix(file, ".lock"))
}

func cutLast(s, sep string) (string, string, bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return "", s, false
	}
	return s[:i], s[i+len(sep):], true
}

func write(lock, content, stamp string) error {
	if lock != "" {
		if err := os.WriteFile(lock, []byte(content), 0o644); err != nil {
			return fmt.Errorf("writing the lock: %w", err)
		}
		fmt.Printf("stableproto: wrote %s\n", lock)
	}
	if stamp != "" {
		if err := os.WriteFile(stamp, []byte("ok\n"), 0o644); err != nil {
			return fmt.Errorf("writing the stamp: %w", err)
		}
	}
	return nil
}

// added renders what the schema has that the lock does not.
func added(old, current *lockfile.Lock) string {
	extra := lockfile.Additions(old, current)
	if len(extra) == 0 {
		// The schemas agree and only the text differs, which happens when the
		// header changes. Worth saying, because "they disagree" followed by an
		// empty list would read as a bug in this tool.
		return "The schema itself is unchanged; only the lock file's text differs.\n"
	}
	var b strings.Builder
	for _, line := range extra {
		fmt.Fprintf(&b, "  + %s\n", line)
	}
	return b.String()
}
