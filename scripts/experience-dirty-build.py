#!/usr/bin/env python3
"""Independent dirty-candidate evidence; no Git and no clean-Git G3 authority.

Freeze requires paused writers. Read-only modes and before/after hashes cannot
prevent an owner changing bytes and restoring them during a Docker build.
Filename exclusions do not prove secret-free contents or reproducible apt/pip.
Buildx ls probes configured builders; Name/Driver metadata does not prove health.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import subprocess
from datetime import datetime, timezone

PROFILE = "experience_four_images_1011_v1"
ROLES = ("backend", "web", "worker", "assistant")
DOCKERFILES = dict(zip(ROLES, ("images/backend/Dockerfile", "images/web/Dockerfile",
                             "images/worker/Dockerfile", "images/assistant-litellm/Dockerfile")))
PINS = {
    ".dockerignore": "3bf9ee5a167130ecfdfd58c8233b3606b108b06ca5eb9206bb18d191bda28cf1",
    DOCKERFILES["backend"]: "24184d1fec5781546b0deb3c45696b6a49cabea5cddc2928d146c4c2572f287b",
    DOCKERFILES["web"]: "f34a1950ddce6b0a690f4f139fd063fca2f5ca9c789cece05df89997df8c2961",
    DOCKERFILES["worker"]: "eba28becbcb247e06bec474c734c5433329b232d35c7507b0bcccd9a500d9a99",
    DOCKERFILES["assistant"]: "1f53a662de561bb51ae092c49600127b65c6a53fc976dc2c51cb2e5646690c9c",
}
DIR_ROOTS = ("cmd", "internal", "web", "images/worker/litellm")
FILE_ROOTS = tuple(PINS) + ("go.mod", "go.sum", "images/web/nginx.conf",
                          "images/worker/package.json", "images/worker/package-lock.json",
                          "images/assistant-litellm/config.yaml", "images/assistant-litellm/entrypoint.sh")
REQUIRED_FILES = FILE_ROOTS + ("web/package.json", "web/package-lock.json")
DENY_SEGMENTS = (".git", ".scratch", "node_modules", ".aws", ".ssh", ".codex", ".docker",
                 ".env", ".npmrc", ".pypirc", "auth.json", "credentials",
                 "credential-mounts", "config-mounts", "switchback-secrets")
WEB_OUTPUTS = ("web/dist", "web/playwright-report", "web/test-results")
DENY_RULES = list(DENY_SEGMENTS) + [".env.*", "*.pem", "*.key", "id_rsa*", "id_ed25519*"] + list(WEB_OUTPUTS)
APPEND_IGNORE = ("\n# experience_four_images_1011_v1: assistant inputs then final deny\n"
                 "!images/assistant-litellm/\n!images/assistant-litellm/**\n" +
                 "".join("**/" + rule + "\n" for rule in DENY_RULES if not rule.startswith("web/")) +
                 "".join(rule + "\n" for rule in WEB_OUTPUTS)).encode("utf-8")
SOURCE_SCHEMA = "experience.dirty_build_source.v1"
BUILD_SCHEMA = "experience.dirty_build_provenance.v1"
WARNINGS = ["Writers must be paused during capture and build.",
            "Read-only modes and before/after hashes are not strong immutability.",
            "Filename filtering does not prove secret-free content or reproducible dependencies.",
            "This dirty evidence does not close G3/G4 or verify deployed container image IDs."]


class EvidenceError(ValueError):
    pass


def require(condition, message):
    if not condition:
        raise EvidenceError(message)


def now():
    return datetime.now(timezone.utc).isoformat()


def sha(data):
    return hashlib.sha256(data).hexdigest()


def compact(value):
    return json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def valid_relative(name):
    path = PurePosixPath(name)
    require(name and not path.is_absolute() and str(path) == name and
            all(x not in (".", "..") for x in path.parts), "unsafe relative path")
    return path.parts


def absolute_directory(path):
    path = Path(os.path.abspath(path))
    for part in reversed((path,) + tuple(path.parents)):
        info = part.lstat()
        require(stat.S_ISDIR(info.st_mode), "symlink/non-directory ancestor: " + str(part))
    return path


def mode_of(info):
    mode = stat.S_IMODE(info.st_mode)
    require(not mode & 0o7000, "special permission bits forbidden")
    return mode


def signature(info):
    return (info.st_dev, info.st_ino, info.st_mode, info.st_size, info.st_mtime_ns, info.st_ctime_ns)


def open_parent(root, name):
    parts = valid_relative(name)
    fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        for part in parts[:-1]:
            child = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
            os.close(fd)
            fd = child
        return fd, parts[-1]
    except BaseException:
        os.close(fd)
        raise


def safe_read(root, name):
    parent, leaf = open_parent(root, name)
    try:
        before = os.stat(leaf, dir_fd=parent, follow_symlinks=False)
        require(stat.S_ISREG(before.st_mode), "not regular: " + name)
        mode_of(before)
        fd = os.open(leaf, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
        try:
            require(signature(before) == signature(os.fstat(fd)), "file changed before read: " + name)
            with os.fdopen(fd, "rb", closefd=False) as stream:
                data = stream.read()
            require(signature(before) == signature(os.fstat(fd)) ==
                    signature(os.stat(leaf, dir_fd=parent, follow_symlinks=False)), "file changed during read: " + name)
        finally:
            os.close(fd)
        return data, mode_of(before)
    finally:
        os.close(parent)


def exclusive(path, data):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "wb") as stream:
        stream.write(data)


def deny(name):
    for segment in valid_relative(name):
        if (segment in DENY_SEGMENTS or segment.startswith(".env.") or
                segment.endswith((".pem", ".key")) or segment.startswith(("id_rsa", "id_ed25519"))):
            return "segment:" + segment
    for output in WEB_OUTPUTS:
        if name == output or name.startswith(output + "/"):
            return "build-output:" + output
    return None


def inventory(root, limited):
    """Traverse directories via no-follow descriptors; denied bytes are never read."""
    entries, directories, excluded = {}, {}, {}

    def visit(name):
        valid_relative(name)
        rule = deny(name) if limited else None
        if rule:
            excluded[name] = rule
            return
        require(name not in entries and name not in directories, "duplicate path: " + name)
        parent, leaf = open_parent(root, name)
        try:
            before = os.stat(leaf, dir_fd=parent, follow_symlinks=False)
            mode = mode_of(before)
            if stat.S_ISDIR(before.st_mode):
                directories[name] = mode
                fd = os.open(leaf, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
                try:
                    require(signature(before) == signature(os.fstat(fd)), "directory replaced: " + name)
                    children = sorted(os.listdir(fd))
                    for child in children:
                        visit(name + "/" + child)
                    require(signature(before) == signature(os.fstat(fd)), "directory changed: " + name)
                finally:
                    os.close(fd)
            else:
                require(stat.S_ISREG(before.st_mode), "symlink/special file: " + name)
                data, read_mode = safe_read(root, name)
                require(mode == read_mode, "mode changed: " + name)
                entries[name] = {"path": name, "mode": mode, "size": len(data), "sha256": sha(data)}
        finally:
            os.close(parent)

    if limited:
        for name in DIR_ROOTS + FILE_ROOTS:
            visit(name)
        for name in tuple(entries) + tuple(directories):
            for parent in PurePosixPath(name).parents:
                text = str(parent)
                if text != "." and text not in directories:
                    fd, leaf = open_parent(root, text)
                    try:
                        info = os.stat(leaf, dir_fd=fd, follow_symlinks=False)
                        require(stat.S_ISDIR(info.st_mode), "invalid parent: " + text)
                        directories[text] = mode_of(info)
                    finally:
                        os.close(fd)
    else:
        for name in sorted(os.listdir(root)):
            visit(name)
    return {"entries": [entries[x] for x in sorted(entries)],
            "directories": [{"path": x, "mode": directories[x]} for x in sorted(directories)],
            "excluded": [{"path": x, "rule": excluded[x]} for x in sorted(excluded)]}


def profile_check(root, original=False):
    for name, digest in PINS.items():
        data, _ = safe_read(root, name)
        if name == ".dockerignore" and not original:
            require(data.endswith(APPEND_IGNORE), "effective ignore transform missing")
            data = data[:-len(APPEND_IGNORE)]
        require(sha(data) == digest, "fixed profile changed: " + name)
    for name in REQUIRED_FILES:
        require(not deny(name), "required input denied: " + name)
        safe_read(root, name)
    for name in DIR_ROOTS:
        parent, leaf = open_parent(root, name)
        try:
            require(stat.S_ISDIR(os.stat(leaf, dir_fd=parent, follow_symlinks=False).st_mode), "required directory missing")
        finally:
            os.close(parent)
    for dockerfile in DOCKERFILES.values():
        specific = root / (dockerfile + ".dockerignore")
        require(not os.path.lexists(specific), "Dockerfile-specific ignore forbidden")


def freeze(worktree, evidence, base_commit):
    require(re.fullmatch(r"[0-9a-fA-F]{40}", base_commit) is not None, "base commit must be 40 hex reference")
    root = absolute_directory(worktree)
    worktree_root_mode = mode_of(root.lstat())
    evidence = Path(os.path.abspath(evidence))
    absolute_directory(evidence.parent)
    require(evidence != root and not any(evidence.is_relative_to(root / name) for name in DIR_ROOTS) and
            not any(evidence == root / name or (root / name).is_relative_to(evidence) for name in FILE_ROOTS),
            "evidence must be outside captured allow roots")
    evidence.mkdir(mode=0o700)
    os.chmod(evidence, 0o700)
    source = evidence / "source"
    source.mkdir(mode=0o700)
    profile_check(root, original=True)
    before = inventory(root, limited=True)
    original, _ = safe_read(root, ".dockerignore")
    exclusive(evidence / "dockerignore.original", original)
    for directory in before["directories"]:
        (source / directory["path"]).mkdir(mode=0o700, parents=True, exist_ok=True)
    entries = []
    for entry in before["entries"]:
        data, mode = safe_read(root, entry["path"])
        require((sha(data), len(data), mode) == (entry["sha256"], entry["size"], entry["mode"]), "source changed while copying")
        if entry["path"] == ".dockerignore":
            data += APPEND_IGNORE
        target = source / entry["path"]
        exclusive(target, data)
        snapshot_mode = 0o555 if mode & 0o111 else 0o444
        os.chmod(target, snapshot_mode)
        entries.append({"path": entry["path"], "source_mode": mode, "snapshot_mode": snapshot_mode,
                        "size": len(data), "sha256": sha(data)})
    require(before == inventory(root, limited=True), "source inventory changed during capture")
    profile_check(root, original=True)
    root_after = root.lstat()
    require(stat.S_ISDIR(root_after.st_mode) and mode_of(root_after) == worktree_root_mode,
            "source root type/mode changed during capture")
    directories = [{"path": x["path"], "source_mode": x["mode"], "snapshot_mode": 0o555}
                   for x in before["directories"]]
    for directory in reversed(directories):
        os.chmod(source / directory["path"], 0o555)
    os.chmod(source, 0o500)
    actual = inventory(source, limited=False)
    require(actual["entries"] == [{"path": x["path"], "mode": x["snapshot_mode"], "size": x["size"], "sha256": x["sha256"]} for x in entries] and
            actual["directories"] == [{"path": x["path"], "mode": x["snapshot_mode"]} for x in directories], "snapshot differs from captured inventory")
    manifest = {"schema": SOURCE_SCHEMA, "dirty_candidate": True, "base_reference_commit": base_commit.lower(),
                "worktree": str(root), "captured_at": now(), "profile": PROFILE,
                "allow_roots": list(DIR_ROOTS + FILE_ROOTS), "deny_rules": DENY_RULES,
                "excluded": before["excluded"], "directories": directories, "entries": entries,
                "source_root_mode": 0o500, "source_manifest_sha256": sha(compact(entries)),
                "ignore_transform": {"original_sha256": sha(original), "effective_sha256": sha(original + APPEND_IGNORE),
                                     "append_utf8": APPEND_IGNORE.decode("utf-8")}, "warnings": WARNINGS}
    data = compact(manifest)
    exclusive(evidence / "source-manifest.json", data)
    try:
        return verify_source(evidence, sha(data))
    except Exception:
        # Only this invocation's newly created manifest; never clean/reuse evidence.
        (evidence / "source-manifest.json").unlink()
        raise


def verify_source(evidence, expected_manifest_sha256):
    require(re.fullmatch(r"[0-9a-f]{64}", expected_manifest_sha256) is not None, "caller must pin manifest file SHA256")
    evidence = absolute_directory(evidence)
    require(mode_of(evidence.lstat()) == 0o700, "evidence root must be private 0700")
    data, _ = safe_read(evidence, "source-manifest.json")
    require(sha(data) == expected_manifest_sha256, "manifest file digest mismatch")
    manifest = json.loads(data)
    require(manifest["schema"] == SOURCE_SCHEMA and manifest["profile"] == PROFILE and
            manifest["dirty_candidate"] is True and manifest["allow_roots"] == list(DIR_ROOTS + FILE_ROOTS) and
            manifest["deny_rules"] == DENY_RULES, "source schema/profile mismatch")
    entries = manifest["entries"]
    require(entries == sorted(entries, key=lambda x: x["path"]) and
            sha(compact(entries)) == manifest["source_manifest_sha256"], "entries digest/order mismatch")
    source = absolute_directory(evidence / "source")
    require(mode_of(source.lstat()) == manifest["source_root_mode"] == 0o500, "snapshot root mode mismatch")
    expected_entries = []
    for entry in entries:
        require(not deny(entry["path"]), "denied snapshot input")
        mode = 0o555 if entry["source_mode"] & 0o111 else 0o444
        require(entry["snapshot_mode"] == mode and not entry["source_mode"] & 0o7000, "invalid file modes")
        expected_entries.append({"path": entry["path"], "mode": mode, "size": entry["size"], "sha256": entry["sha256"]})
    expected_dirs = []
    for directory in manifest["directories"]:
        require(directory["snapshot_mode"] == 0o555 and not directory["source_mode"] & 0o7000, "invalid directory mode")
        expected_dirs.append({"path": directory["path"], "mode": 0o555})
    actual = inventory(source, limited=False)
    require(actual["entries"] == expected_entries and actual["directories"] == expected_dirs, "snapshot inventory mismatch")
    original, _ = safe_read(evidence, "dockerignore.original")
    effective, _ = safe_read(source, ".dockerignore")
    require(sha(original) == PINS[".dockerignore"] and effective == original + APPEND_IGNORE and
            manifest["ignore_transform"] == {"original_sha256": sha(original), "effective_sha256": sha(effective),
                                            "append_utf8": APPEND_IGNORE.decode("utf-8")}, "ignore evidence mismatch")
    profile_check(source)
    return {"schema": SOURCE_SCHEMA, "verified": True, "manifest_file_sha256": sha(data),
            "source_manifest_sha256": manifest["source_manifest_sha256"], "checked_at": now(), "warnings": WARNINGS}


def build_arguments(role, context, builder, platform):
    require(role in ROLES, "unknown role")
    for identity in (context, builder):
        require(isinstance(identity, str) and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,127}", identity) is not None,
                "context/builder must be a safe Docker identity")
    require(platform in ("linux/amd64", "linux/arm64"), "unsupported platform")


def commands(evidence, role, context, builder, platform):
    build_arguments(role, context, builder, platform)
    prefix = ["docker", "--context", context]
    source = str(evidence / "source")
    return {
        "engine": prefix + ["info", "--format", '{"ID":{{json .ID}},"Name":{{json .Name}},"ServerVersion":{{json .ServerVersion}},"OSType":{{json .OSType}},"Architecture":{{json .Architecture}}}'],
        "builder": prefix + ["buildx", "ls", "--format", '{{if eq .Builder.Name "' + builder + '"}}{"Name":{{json .Builder.Name}},"Driver":{{json .Builder.Driver}}}{{end}}'],
        "build": prefix + ["buildx", "build", "--builder", builder, "--platform", platform, "--load",
                           "--iidfile", str(evidence / "build" / role / "image.iid"),
                           "-f", str(evidence / "source" / DOCKERFILES[role]), source],
    }


def inspect_command(context, iid):
    return ["docker", "--context", context, "image", "inspect", "--format",
            '{"Id":{{json .Id}},"Os":{{json .Os}},"Architecture":{{json .Architecture}}}', iid]


def subprocess_runner(argv):
    # stdout/stderr remain raw bytes. No shell, env dump, Docker config, or info dump.
    return subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)


def material(root, name):
    data, _ = safe_read(root, name)
    return {"path": name, "bytes": len(data), "sha256": sha(data)}


def save_json(path, value):
    exclusive(path, compact(value))


def source_receipt(evidence, pin):
    try:
        return verify_source(evidence, pin)
    except Exception as error:
        return {"verified": False, "manifest_file_sha256": pin, "checked_at": now(),
                "error_type": type(error).__name__, "error": str(error)}


def run_docker(role_dir, label, argv, runner):
    started = now()
    error, exit_code, stdout, stderr = None, None, b"", b""
    try:
        result = runner(argv)
        exit_code, stdout, stderr = result.returncode, result.stdout, result.stderr
        require(isinstance(exit_code, int) and isinstance(stdout, bytes) and isinstance(stderr, bytes),
                "Docker runner must return raw bytes and integer exit code")
    except Exception as failure:
        error = {"type": type(failure).__name__}
        exit_code = getattr(failure, "returncode", None)
        # Preserve partial subprocess bytes without serializing arbitrary exception/env text.
        stdout = getattr(failure, "stdout", None) or b""
        stderr = getattr(failure, "stderr", None) or b""
        require(isinstance(stdout, bytes) and isinstance(stderr, bytes), "partial logs must be raw bytes")
    exclusive(role_dir / (label + ".stdout"), stdout)
    exclusive(role_dir / (label + ".stderr"), stderr)
    call = {"argv": argv, "started_at": started, "finished_at": now(), "exit_code": exit_code,
            "signal": -exit_code if isinstance(exit_code, int) and exit_code < 0 else None,
            "error": error, "stdout": material(role_dir, label + ".stdout"),
            "stderr": material(role_dir, label + ".stderr")}
    save_json(role_dir / (label + ".json"), call)
    require(error is None and exit_code == 0, "Docker " + label + " failed")
    return stdout


def metadata(data, keys):
    value = json.loads(data)
    require(isinstance(value, dict) and set(value) == set(keys) and
            all(isinstance(value[k], str) and value[k] for k in keys), "invalid bounded Docker metadata")
    return value


def builder_metadata(data, expected_name):
    # ls renders the builder again for each node. Only identical pairs coalesce.
    def unique_fields(pairs):
        require(len(pairs) == len(dict(pairs)), "duplicate builder metadata field")
        return dict(pairs)

    identity = None
    for line in data.splitlines():
        if not line.strip():
            continue
        value = json.loads(line, object_pairs_hook=unique_fields)
        require(isinstance(value, dict) and set(value) == {"Name", "Driver"} and
                all(isinstance(value[key], str) and value[key] for key in ("Name", "Driver")),
                "invalid bounded builder metadata")
        require(value["Name"] == expected_name, "builder identity mismatch")
        require(identity is None or identity == value, "conflicting builder metadata")
        identity = value
    require(identity is not None, "empty builder metadata")
    return identity


def actual_image(role_dir, context, platform, runner):
    raw, _ = safe_read(role_dir, "image.iid")
    iid = raw.decode("ascii").strip()
    require(re.fullmatch(r"sha256:[0-9a-f]{64}", iid) is not None, "invalid image IID")
    value = metadata(run_docker(role_dir, "inspect", inspect_command(context, iid), runner),
                     ("Id", "Os", "Architecture"))
    require(value["Id"] == iid and value["Os"] + "/" + value["Architecture"] == platform,
            "IID/inspect ID or platform mismatch")
    return value


def build_role(evidence, expected_manifest_sha256, role, context, builder, platform, docker_runner=subprocess_runner):
    build_arguments(role, context, builder, platform)
    evidence = absolute_directory(evidence)
    build_dir = evidence / "build"
    if not os.path.lexists(build_dir):
        build_dir.mkdir(mode=0o700)
    absolute_directory(build_dir)
    require(mode_of(build_dir.lstat()) == 0o700, "build evidence must be private")
    sequence = ROLES.index(role)
    require(sorted(os.listdir(build_dir)) == sorted(ROLES[:sequence]), "roles must run once in backend/web/worker/assistant order")
    if sequence:
        verify_roles(evidence, expected_manifest_sha256, ROLES[:sequence])
    role_dir = build_dir / role
    role_dir.mkdir(mode=0o700)
    argv = commands(evidence, role, context, builder, platform)
    before = source_receipt(evidence, expected_manifest_sha256)
    save_json(role_dir / "source-before.json", before)
    record = {"role": role, "sequence": sequence, "context": context, "builder": builder, "platform": platform,
              "manifest_file_sha256": expected_manifest_sha256, "source_manifest_sha256": before.get("source_manifest_sha256"),
              "started_at": now(), "commands": argv, "image": None, "error": None}
    try:
        require(before["verified"], "source-before verification failed")
        record["engine"] = metadata(run_docker(role_dir, "engine", argv["engine"], docker_runner),
                                    ("ID", "Name", "ServerVersion", "OSType", "Architecture"))
        record["builder_identity"] = builder_metadata(run_docker(role_dir, "builder", argv["builder"], docker_runner), builder)
        run_docker(role_dir, "build", argv["build"], docker_runner)
        record["image"] = actual_image(role_dir, context, platform, docker_runner)
    except Exception as error:
        record["error"] = {"type": type(error).__name__, "message": str(error)}
    finally:
        after = source_receipt(evidence, expected_manifest_sha256)
        save_json(role_dir / "source-after.json", after)
        record["finished_at"] = now()
        save_json(role_dir / "role.json", record)
    require(record["error"] is None and after["verified"], "build or source-after verification failed; evidence retained")
    verified = verify_roles(evidence, expected_manifest_sha256, ROLES[:sequence + 1])
    if role == ROLES[-1]:
        save_json(evidence / "build-provenance.json", verified)
    return verified


def read_json(root, name):
    raw, _ = safe_read(root, name)
    return json.loads(raw)


def verify_roles(evidence, pin, roles):
    source = verify_source(evidence, pin)
    summaries = {}
    shared_identity, previous_finished = None, None
    for index, role in enumerate(roles):
        root = absolute_directory(evidence / "build" / role)
        require(mode_of(root.lstat()) == 0o700, "role evidence must be private")
        record = read_json(root, "role.json")
        build_arguments(role, record["context"], record["builder"], record["platform"])
        require(record["role"] == role and record["sequence"] == index and record["error"] is None and
                record["manifest_file_sha256"] == pin and record["source_manifest_sha256"] == source["source_manifest_sha256"],
                "role/source identity mismatch")
        identity = (record["context"], record["builder"], record["platform"], record["engine"], record["builder_identity"])
        require(shared_identity is None or shared_identity == identity, "four roles must share Docker identity/platform")
        shared_identity = identity
        expected_commands = commands(evidence, role, *identity[:3])
        require(record["commands"] == expected_commands, "build argv source binding mismatch")
        receipts = {label: read_json(root, label + ".json") for label in ("source-before", "source-after")}
        for label, receipt in receipts.items():
            require(receipt["verified"] is True and receipt["manifest_file_sha256"] == pin and
                    receipt["source_manifest_sha256"] == source["source_manifest_sha256"], "source receipt failed or mismatched")
            require(receipt["checked_at"] <= record["started_at"] if label == "source-before" else
                    record["started_at"] <= receipt["checked_at"] <= record["finished_at"], "source receipt order mismatch")
        iid, _ = safe_read(root, "image.iid")
        iid = iid.decode("ascii").strip()
        require(re.fullmatch(r"sha256:[0-9a-f]{64}", iid) is not None, "invalid image IID")
        expected_commands["inspect"] = inspect_command(record["context"], iid)
        files = ["role.json", "source-before.json", "source-after.json", "image.iid"]
        call_order = []
        for label in ("engine", "builder", "build", "inspect"):
            call = read_json(root, label + ".json")
            require(call["argv"] == expected_commands[label] and call["exit_code"] == 0 and
                    call["signal"] is None and call["error"] is None, "call argv/result mismatch")
            require(call["started_at"] <= call["finished_at"], "call time reversed")
            call_order.append((call["started_at"], call["finished_at"]))
            for suffix in ("stdout", "stderr"):
                require(call[suffix] == material(root, label + "." + suffix), "raw log missing/modified")
            files += [label + ".json", label + ".stdout", label + ".stderr"]
        require(record["started_at"] <= call_order[0][0] and
                call_order[-1][1] <= receipts["source-after"]["checked_at"] <= record["finished_at"] and
                all(call_order[i][1] <= call_order[i + 1][0] for i in range(3)) and
                (previous_finished is None or previous_finished <= record["started_at"]), "role/call order mismatch")
        previous_finished = record["finished_at"]
        require(set(os.listdir(root)) == set(files), "extra/missing role materials")
        engine = metadata(safe_read(root, "engine.stdout")[0], ("ID", "Name", "ServerVersion", "OSType", "Architecture"))
        builder = builder_metadata(safe_read(root, "builder.stdout")[0], record["builder"])
        image = metadata(safe_read(root, "inspect.stdout")[0], ("Id", "Os", "Architecture"))
        require(engine == record["engine"] and builder == record["builder_identity"] and builder["Name"] == record["builder"], "metadata binding mismatch")
        require(image == record["image"] and image["Id"] == iid and image["Os"] + "/" + image["Architecture"] == record["platform"], "image binding mismatch")
        summaries[role] = {"image_id": iid, "materials": [material(root, name) for name in sorted(files)]}
    return {"schema": BUILD_SCHEMA, "dirty_candidate": True, "profile": PROFILE,
            "manifest_file_sha256": pin, "source_manifest_sha256": source["source_manifest_sha256"],
            "docker_identity": list(shared_identity) if shared_identity else None, "roles": summaries, "warnings": WARNINGS}


def check_build(evidence, expected_manifest_sha256):
    evidence = absolute_directory(evidence)
    require(sorted(os.listdir(absolute_directory(evidence / "build"))) == sorted(ROLES), "four roles required")
    result = verify_roles(evidence, expected_manifest_sha256, ROLES)
    require(read_json(evidence, "build-provenance.json") == result, "provenance material digest mismatch")
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    freeze_parser = sub.add_parser("freeze", help="capture a paused dirty candidate into new private evidence")
    freeze_parser.add_argument("--worktree", required=True)
    freeze_parser.add_argument("--evidence", required=True)
    freeze_parser.add_argument("--base-commit", required=True)
    for command in ("check-source", "build", "check-build"):
        child = sub.add_parser(command)
        child.add_argument("--evidence", required=True)
        child.add_argument("--manifest-sha256", required=True)
        if command == "build":
            child.add_argument("--role", choices=ROLES, required=True)
            child.add_argument("--context", required=True)
            child.add_argument("--builder", required=True)
            child.add_argument("--platform", choices=("linux/amd64", "linux/arm64"), required=True)
    args = parser.parse_args()
    try:
        if args.command == "freeze":
            result = freeze(args.worktree, args.evidence, args.base_commit)
        elif args.command == "check-source":
            result = verify_source(args.evidence, args.manifest_sha256)
        elif args.command == "build":
            result = build_role(args.evidence, args.manifest_sha256, args.role, args.context, args.builder, args.platform)
        else:
            result = check_build(args.evidence, args.manifest_sha256)
        print(compact(result).decode("utf-8"))
        return 0
    except Exception as error:
        print(compact({"verified": False, "error_type": type(error).__name__, "error": str(error)}).decode("utf-8"))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
