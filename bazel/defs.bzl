"""Bazel rules that pin a stored schema against the data written with it.

A schema whose messages are only ever sent between two processes we deploy
together can change freely: both ends are rebuilt at once. A schema whose
messages are stored cannot, because the data outlives every deployment that
touched it.

`proto_schema_lock` marks the difference. It writes what every field is called
and what number carries it into a lock file checked in beside the .proto, and
fails the build when the two disagree. See the README for what the lock records
and why.
"""

load("@protobuf//bazel/common:proto_info.bzl", "ProtoInfo")

def _locked_sources(ctx, proto_info):
    """The proto files this lock covers.

    A proto_library can hold both stored messages and messages that only ever
    travel between two processes deployed together. Only the first kind needs
    locking, so `files` names them; locking the rest would make an ordinary
    refactor fail a check meant for a different problem.
    """
    if not ctx.attr.files:
        return proto_info.direct_sources

    by_name = {src.basename: src for src in proto_info.direct_sources}
    out = []
    for name in ctx.attr.files:
        if name not in by_name:
            fail("%s names %s, which %s does not have. It has: %s" % (
                ctx.label,
                name,
                ctx.attr.proto.label,
                ", ".join(sorted(by_name.keys())),
            ))
        out.append(by_name[name])
    return out

def _args(ctx, proto_info, lock_path):
    """The arguments both the check and the update take."""
    args = [
        "-descriptor_set",
        proto_info.direct_descriptor_set.short_path,
        "-lock",
        lock_path,
        "-package",
        ctx.attr.package,
    ]
    for src in _locked_sources(ctx, proto_info):
        args += ["-file", _import_path(proto_info, src)]
    return args

def _proto_schema_lock_impl(ctx):
    proto_info = ctx.attr.proto[ProtoInfo]
    stamp = ctx.actions.declare_file(ctx.label.name + ".checked")

    args = ctx.actions.args()
    args.add("-check")
    args.add("-descriptor_set", proto_info.direct_descriptor_set)
    args.add("-lock", ctx.file.lock)
    args.add("-package", ctx.attr.package)
    args.add("-stamp", stamp)
    args.add_all(
        [_import_path(proto_info, src) for src in _locked_sources(ctx, proto_info)],
        before_each = "-file",
    )

    ctx.actions.run(
        outputs = [stamp],
        inputs = depset([proto_info.direct_descriptor_set, ctx.file.lock]),
        executable = ctx.executable._protolock,
        arguments = [args],
        mnemonic = "ProtoSchemaLock",
        progress_message = "Checking %s against its schema lock" % ctx.file.lock.short_path,
    )

    return [DefaultInfo(files = depset([stamp]))]

def _import_path(proto_info, src):
    """The name the descriptor set gives this file: its proto import path.

    That is the string another .proto would write in an `import`, and it is what
    protoc records. It is not the file's location on disk. The two agree for a
    proto_library in the main repository with no prefix rewriting, which is why
    passing the workspace-relative path worked until one did not: a
    proto_library with `import_prefix` compiles from a _virtual_imports tree,
    where the path on disk carries the external repository and the virtual root
    and the import path carries neither.

    Stripping proto_source_root is what turns one into the other. It is empty
    or "." for the ordinary case, so this reduces to the old behaviour there.
    """
    root = proto_info.proto_source_root
    if root and root != ".":
        return src.path[len(root) + 1:]
    return src.path

_proto_schema_lock = rule(
    implementation = _proto_schema_lock_impl,
    doc = "Fails the build when a proto_library and its schema lock disagree.",
    attrs = {
        "proto": attr.label(
            providers = [ProtoInfo],
            mandatory = True,
            doc = "The proto_library whose direct sources are locked.",
        ),
        "lock": attr.label(
            allow_single_file = [".lock"],
            mandatory = True,
            doc = "The checked-in lock file.",
        ),
        "package": attr.string(
            mandatory = True,
            doc = "The proto package being locked, for the lock file's header.",
        ),
        "files": attr.string_list(
            doc = "Basenames of the proto's sources to lock. Empty locks all of them.",
        ),
        "_protolock": attr.label(
            default = Label("//cmd/stableproto"),
            executable = True,
            cfg = "exec",
        ),
    },
)

def _proto_schema_lock_update_impl(ctx):
    proto_info = ctx.attr.proto[ProtoInfo]
    runner = ctx.actions.declare_file(ctx.label.name + ".sh")

    # The lock is rewritten in the source tree, not in the output tree, so the
    # path handed over is the workspace-relative one and the tool resolves it
    # against BUILD_WORKSPACE_DIRECTORY.
    args = _args(ctx, proto_info, ctx.file.lock.short_path)

    ctx.actions.write(
        runner,
        # "$@" so the waiver flags reach the tool: `bazel run <target> --
        # -force -reason "..."`. Without it they would be silently dropped and
        # the run would look like an ordinary regeneration that did nothing.
        "#!/usr/bin/env bash\nset -euo pipefail\nexec {tool} -update {args} \"$@\"\n".format(
            tool = ctx.executable._protolock.short_path,
            args = " ".join(args),
        ),
        is_executable = True,
    )

    return [DefaultInfo(
        executable = runner,
        runfiles = ctx.runfiles(files = [
            proto_info.direct_descriptor_set,
            ctx.executable._protolock,
        ]),
    )]

_proto_schema_lock_update = rule(
    implementation = _proto_schema_lock_update_impl,
    doc = "Rewrites a schema lock, refusing changes that break stored data.",
    executable = True,
    attrs = {
        "proto": attr.label(providers = [ProtoInfo], mandatory = True),
        "lock": attr.label(allow_single_file = [".lock"], mandatory = True),
        "package": attr.string(mandatory = True),
        "files": attr.string_list(),
        "_protolock": attr.label(
            default = Label("//cmd/stableproto"),
            executable = True,
            cfg = "exec",
        ),
    },
)

def proto_schema_lock(name, proto, package, lock = None, files = None):
    """Pins a stored proto schema to a checked-in lock file.

    Declares two targets:

      <name>         fails the build when the schema and the lock disagree
      <name>_update  rewrites the lock, refusing an incompatible change

    Args:
      name: target name. Conventionally the lock's basename plus `_lock`.
      proto: the proto_library to lock. Only its direct sources are read.
      package: the proto package, written into the lock file's header.
      lock: the lock file. Defaults to `<name>.lock` minus a trailing `_lock`.
      files: basenames of the sources to lock. Defaults to all of them.
    """
    if lock == None:
        lock = name[:-len("_lock")] + ".lock" if name.endswith("_lock") else name + ".lock"

    _proto_schema_lock(
        name = name,
        proto = proto,
        lock = lock,
        package = package,
        files = files or [],
    )
    _proto_schema_lock_update(
        name = name + "_update",
        proto = proto,
        lock = lock,
        package = package,
        files = files or [],
        tags = ["manual"],
    )
