# stableproto

Keep protobuf schemas compatible with the data already written in them.

`stableproto` records each locked message's field names and numbers in a lock file checked in beside the `.proto`. The build fails when the schema and the lock disagree. Adding a field is allowed. Renaming, renumbering, retyping or deleting one is refused, because each of those changes what already-stored data decodes to.

It is for schemas whose messages are **stored**: files, databases, encrypted vaults, anything that outlives the code that wrote it. A schema that only travels between two services deployed together can change freely, and doesn't need this.

## Why names matter too

Most compatibility checkers protect field numbers, because numbers are what the binary wire format reads. Names matter for stored data as well:

- A JSON export of a record prints field names. Renaming a field silently renames a column in everybody's export.
- A field deleted without being reserved lets a later field reuse its number or name, and old data then decodes into the new field.

So a lock records both, and refuses a change to either.

## A lock file

One form per message and one line per field, in s-expressions:

```lisp
(message example.notes.v1.Note
  (field 1 id optional string)
  (field 2 body optional string)
  (field 3 tags repeated string)
  (field 5 created optional message .google.protobuf.Timestamp)
  (reserved 4)
  (reserved-name title))
```

A field is never wrapped onto two lines, so renaming one is a one-line diff and the fields around it don't move.

## With Bazel

```starlark
# MODULE.bazel
bazel_dep(name = "stableproto")
git_override(
    module_name = "stableproto",
    remote = "https://github.com/nextgui/stableproto.git",
    commit = "<commit>",
)
```

stableproto is not in the Bazel Central Registry yet, so it is pinned to a commit.

```starlark
# BUILD.bazel
load("@stableproto//bazel:defs.bzl", "proto_schema_lock")

proto_schema_lock(
    name = "notes_lock",
    proto = ":notes_proto",
    package = "example.notes.v1",
)
```

This declares two targets. [`examples/notes`](examples/notes) is a working example.

| Target | What it does |
|---|---|
| `:notes_lock` | Fails the build when `notes.proto` and `notes.lock` disagree. |
| `:notes_lock_update` | Rewrites `notes.lock`, refusing the same changes the check refuses. |

Create an empty lock file, write the first lock, then keep it current:

```bash
touch example/notes.lock
```

```bash
bazel run //example:notes_lock_update -- -init
```

```bash
bazel run //example:notes_lock_update
```

`files` limits a lock to some of a `proto_library`'s sources, for a library that mixes stored messages with ones that only travel between services.

## Without Bazel

```bash
go install github.com/nextgui/stableproto/cmd/stableproto@latest
```

```bash
protoc --include_imports -o notes.binpb notes.proto
stableproto -check -descriptor_set notes.binpb -file notes.proto -lock notes.lock -package example.notes.v1
```

| Flag | Meaning |
|---|---|
| `-check` | Compare the schema with the lock; exit non-zero on any difference. |
| `-update` | Rewrite the lock, refusing changes that break stored data. |
| `-init` | With `-update`, write a lock for a schema that has none yet. |
| `-force -reason "..."` | With `-update`, waive the checks and record why in the lock. |

## When a change really is safe

Sometimes a rename is right, for example in a schema nothing has ever written. That needs `-force` and a reason:

```bash
bazel run //example:notes_lock_update -- -force -reason "nothing has ever written this schema"
```

The reason is written into the lock beside the field it concerns, and kept by every later update:

```lisp
(override 2 renamed accounts "nothing has ever written this schema")
(field 2 account_list repeated message .example.Account)
```

A waiver can't be hidden. It shows in the diff beside the change, stays in the file, and can be found with grep. Deleting the lock to start over is refused too: `-update` won't replace a missing or empty lock without `-init`.

## What it can't do

The lock file is the only record, so anyone who can edit it can bypass the check by editing a line by hand. `stableproto` makes a silent bypass harder and a declared one permanent. It doesn't make one impossible: that needs a comparison against an earlier revision, which a Bazel action can't see.

## Compared with other tools

| | stableproto | [`buf breaking`](https://buf.build/docs/breaking/) | [nilslice/protolock](https://github.com/nilslice/protolock) |
|---|---|---|---|
| Compares against | A lock file checked in beside the `.proto` | An earlier version: a git ref, the Buf Schema Registry, a tarball or a built image | A `proto.lock` JSON file |
| Lock readable as a diff | One line per field | No lock | JSON |
| Field number changed | Refused | Refused | Refused |
| Field type changed | Refused | Refused | Refused |
| Field renamed | Refused | Refused (`WIRE_JSON`, `FILE`, `PACKAGE`) | Refused in strict mode |
| Field deleted without reserving it | Refused | Refused | Refused |
| Reserved number or name removed | Refused | Refused | Refused in strict mode |
| Optional to repeated, or into a oneof | Refused | Refused (rule depends on category) | Not listed |
| Enum value renamed or deleted | Refused | Refused | Not listed |
| Message deleted | Refused | Refused (`FILE`, `PACKAGE`) | Not listed |
| Services and RPCs | Not checked | Checked | Checked |
| Waiving a check | `-force -reason`, recorded in the lock beside the field | `except`, `ignore` and `ignore_only` in `buf.yaml`; no reason recorded | `--force` rewrites the lock; no reason recorded |
| Bazel | Native rule, `proto_schema_lock` | `buf_breaking_test` in rules_buf (beta), against a checked-in image | Not documented |

`buf breaking` suits a project that already uses Buf, or needs to check services and generated-code compatibility. stableproto is for stored schemas in a Bazel build: it checks only what changes how stored data decodes, needs no earlier revision to compare against, and keeps every waiver, with its reason, in the lock file.

## License

Apache 2.0. Copyright 2026 Next Generation Interfaces Inc.
