"""Mechanical filesystem fixtures; Docker is an explicit external boundary."""
import hashlib
import importlib.util
import os
import json
from pathlib import Path
import tempfile
import unittest
import subprocess
from unittest import mock

ROOT = Path(__file__).resolve().parents[3]
spec = importlib.util.spec_from_file_location("experience_dirty_build", ROOT / "scripts/experience-dirty-build.py")
tool = importlib.util.module_from_spec(spec)
spec.loader.exec_module(tool)
BASE = "fbebfe754045bdbf1af40871a5018eed33743011"
PINNED = (".dockerignore", "images/backend/Dockerfile", "images/web/Dockerfile",
          "images/worker/Dockerfile", "images/assistant-litellm/Dockerfile")
BUILDER_ARGV = ["docker", "--context", "fixture-context", "buildx", "ls", "--format",
                '{{if eq .Builder.Name "fixture-builder"}}{"Name":{{json .Builder.Name}},"Driver":{{json .Builder.Driver}}}{{end}}']
BUILDER_STDOUT = (b'\n{"Name":"fixture-builder","Driver":"docker-container"}\n \t\n'
                  b'{"Name":"fixture-builder","Driver":"docker-container"}\n')


class FakeDocker:
    """Only the Docker system boundary is fake; all evidence files are real."""
    def __init__(self, effect=None, failure=None, iid=None, inspect_id=None, arch="arm64", builder_stdout=None):
        self.effect, self.failure = effect, failure
        self.iid = iid or "sha256:" + "a" * 64
        self.inspect_id, self.arch = inspect_id or self.iid, arch
        self.builder_stdout = BUILDER_STDOUT if builder_stdout is None else builder_stdout
        self.calls = []

    def __call__(self, argv):
        self.calls.append(argv)
        if "info" in argv:
            data = {"ID": "engine-fixture", "Name": "fixture", "ServerVersion": "fixture-v1", "OSType": "linux", "Architecture": "aarch64"}
        elif "build" in argv:
            if self.effect:
                self.effect()
            if isinstance(self.failure, Exception):
                raise self.failure
            if self.failure:
                return subprocess.CompletedProcess(argv, self.failure, b"partial stdout\n", b"actual failure stderr\n")
            Path(argv[argv.index("--iidfile") + 1]).write_text(self.iid + "\n")
            return subprocess.CompletedProcess(argv, 0, b"full build stdout\x00\n", b"full build stderr\n")
        elif "buildx" in argv:
            # Independent CLI oracle: inspect has no --format in installed Buildx.
            if "inspect" in argv and "--format" in argv:
                return subprocess.CompletedProcess(argv, 125, b"", b"unknown flag: --format\n")
            if argv != BUILDER_ARGV:
                return subprocess.CompletedProcess(argv, 125, b"", b"unsupported builder command\n")
            return subprocess.CompletedProcess(argv, 0, self.builder_stdout, b"")
        else:
            data = {"Id": self.inspect_id, "Os": "linux", "Architecture": self.arch}
        return subprocess.CompletedProcess(argv, 0, json.dumps(data).encode(), b"")


class SourceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="experience-mechanical-")
        self.addCleanup(self.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.wt = self.root / "dummy-worktree"
        self.wt.mkdir()
        self.ev = self.root / "evidence"
        for name in PINNED:
            # The fixed historical v1 profile predates qualification binaries.
            # Test its mechanical contract without repinning production inputs.
            source = (ROOT / "test/acceptance/experiencefreeze/testdata/worker-profile-v1.Dockerfile"
                      if name == "images/worker/Dockerfile" else ROOT / name)
            self.put(name, source.read_bytes())
        for name in ("go.mod", "go.sum", "cmd/main.go", "internal/auth.go", "web/token.ts",
                     "web/package.json", "web/package-lock.json", "images/web/nginx.conf",
                     "images/worker/package.json", "images/worker/package-lock.json",
                     "images/worker/litellm/config.yaml", "images/assistant-litellm/config.yaml"):
            self.put(name, b"dummy dirty/untracked input\n")
        self.put("images/assistant-litellm/entrypoint.sh", b"#!/bin/sh\nexit 0\n", 0o751)
        (self.wt / "internal/empty").mkdir()

    def cleanup(self):
        # Only the fresh tmpdir this test owns; sealed snapshots need writable dirs.
        for parent, dirs, files in os.walk(self.root):
            os.chmod(parent, 0o700)
            for name in files:
                path = Path(parent) / name
                if not path.is_symlink():
                    os.chmod(path, 0o600)
        self.temp.cleanup()

    def put(self, name, data, mode=0o644):
        path = self.wt / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        path.chmod(mode)
        return path

    def test_current_qualification_dockerfile_is_not_the_historical_v1_profile(self):
        # Current qualification additions need their own reviewed build profile.
        # The fixture above must not silently make the real source acceptable.
        with self.assertRaisesRegex(tool.EvidenceError,
                                    'fixed profile changed: images/worker/Dockerfile'):
            tool.profile_check(ROOT, original=True)

    def test_freeze_preserves_dirty_bytes_empty_dirs_and_executable_semantics(self):
        result = tool.freeze(self.wt, self.ev, BASE)
        self.assertTrue((self.ev / "source/internal/auth.go").is_file())
        self.assertEqual((self.ev / "source/web/token.ts").read_bytes(), b"dummy dirty/untracked input\n")
        self.assertTrue((self.ev / "source/internal/empty").is_dir())
        self.assertEqual((self.ev / "source/images/assistant-litellm/entrypoint.sh").stat().st_mode & 0o7777, 0o555)
        self.assertEqual((self.ev / "source").stat().st_mode & 0o7777, 0o500)
        self.assertEqual((self.ev).stat().st_mode & 0o7777, 0o700)
        self.assertEqual(len(result["manifest_file_sha256"]), 64)

    def test_build_four_roles_binds_real_filesystem_to_four_actual_ids(self):
        pin = tool.freeze(self.wt, self.ev, BASE)["manifest_file_sha256"]
        for index, role in enumerate(("backend", "web", "worker", "assistant")):
            tool.build_role(self.ev, pin, role, "fixture-context", "fixture-builder", "linux/arm64",
                            FakeDocker(iid="sha256:" + str(index + 1) * 64))
        result = tool.check_build(self.ev, pin)
        self.assertEqual(len(result["roles"]), 4)
        self.assertEqual(result["roles"]["assistant"]["image_id"], "sha256:" + "4" * 64)
        self.assertTrue((self.ev / "build/backend/source-before.json").is_file())
        self.assertTrue((self.ev / "build/backend/source-after.json").is_file())
        self.assertEqual((self.ev / "build/backend/build.stdout").read_bytes(), b"full build stdout\x00\n")
        for role in ("backend", "web", "worker", "assistant"):
            root = self.ev / "build" / role
            self.assertEqual((root / "builder.stdout").read_bytes(), BUILDER_STDOUT)
            self.assertEqual(json.loads((root / "builder.json").read_bytes())["argv"], BUILDER_ARGV)
            self.assertEqual(json.loads((root / "role.json").read_bytes())["builder_identity"],
                             {"Name": "fixture-builder", "Driver": "docker-container"})

    def capture(self):
        return tool.freeze(self.wt, self.ev, BASE)["manifest_file_sha256"]

    def build_all(self, pin):
        for index, role in enumerate(("backend", "web", "worker", "assistant")):
            tool.build_role(self.ev, pin, role, "fixture-context", "fixture-builder", "linux/arm64",
                            FakeDocker(iid="sha256:" + str(index + 1) * 64))

    def test_invalid_builder_metadata_blocks_build_and_keeps_failure_materials(self):
        valid = b'{"Name":"fixture-builder","Driver":"docker-container"}\n'
        cases = {
            "empty": b"", "whitespace": b"\n \t\r\n", "malformed": b"not-json\n",
            "unknown-field": b'{"Name":"fixture-builder","Driver":"docker-container","Endpoint":"extra"}\n',
            "name-number": b'{"Name":1,"Driver":"docker-container"}\n',
            "name-empty": b'{"Name":"","Driver":"docker-container"}\n',
            "driver-number": b'{"Name":"fixture-builder","Driver":1}\n',
            "driver-empty": b'{"Name":"fixture-builder","Driver":""}\n',
            "missing-name": b'{"Driver":"docker-container"}\n',
            "missing-driver": b'{"Name":"fixture-builder"}\n',
            "wrong-name": b'{"Name":"another-builder","Driver":"docker-container"}\n',
            "conflict": valid + b'{"Name":"fixture-builder","Driver":"docker"}\n',
            "later-malformed": valid + b"not-json\n",
            "later-wrong-name": valid + b'{"Name":"another-builder","Driver":"docker-container"}\n',
            "null": b"null\n", "array": b'["fixture-builder","docker-container"]\n',
            "duplicate-field": b'{"Name":"fixture-builder","Name":"fixture-builder","Driver":"docker-container"}\n',
        }
        for label, raw in cases.items():
            with self.subTest(case=label):
                self.ev = self.root / ("builder-invalid-" + label)
                pin = self.capture()
                runner = FakeDocker(builder_stdout=raw)
                with self.assertRaises(tool.EvidenceError):
                    tool.build_role(self.ev, pin, "backend", "fixture-context", "fixture-builder", "linux/arm64", runner)
                role = self.ev / "build/backend"
                self.assertEqual(runner.calls[1], BUILDER_ARGV)
                self.assertEqual(len(runner.calls), 2)  # engine + builder; build never starts
                self.assertEqual((role / "builder.stdout").read_bytes(), raw)
                self.assertEqual(json.loads((role / "builder.json").read_bytes())["exit_code"], 0)
                self.assertTrue(json.loads((role / "source-after.json").read_bytes())["verified"])
                self.assertIsNotNone(json.loads((role / "role.json").read_bytes())["error"])
                self.assertFalse((role / "build.json").exists())
                self.assertFalse((self.ev / "build-provenance.json").exists())

    def test_check_build_reparses_conflicting_raw_builder_materials(self):
        pin = self.capture()
        self.build_all(pin)
        self.assertEqual(len(tool.check_build(self.ev, pin)["roles"]), 4)
        role = self.ev / "build/backend"
        raw = BUILDER_STDOUT + b'{"Name":"fixture-builder","Driver":"docker"}\n'
        (role / "builder.stdout").write_bytes(raw)
        call_path = role / "builder.json"
        call = json.loads(call_path.read_bytes())
        # Update the raw-log receipt so rejection must come from parsing identity,
        # before the final provenance digest check, not a stale stdout digest.
        call["stdout"] = {"path": "builder.stdout", "bytes": len(raw),
                          "sha256": hashlib.sha256(raw).hexdigest()}
        call_path.write_bytes(json.dumps(call).encode())
        self.assertEqual(json.loads((role / "role.json").read_bytes())["builder_identity"],
                         {"Name": "fixture-builder", "Driver": "docker-container"})
        with self.assertRaisesRegex(tool.EvidenceError, "conflicting builder metadata"):
            tool.check_build(self.ev, pin)

    def test_evidence_in_excluded_worktree_scratch_is_outside_context(self):
        (self.wt / ".scratch").mkdir()
        self.ev = self.wt / ".scratch/unique-evidence"
        pin = self.capture()
        self.assertTrue(tool.verify_source(self.ev, pin)["verified"])
        self.assertFalse((self.ev / "source/source-manifest.json").exists())

    def test_new_evidence_is_exclusive_and_cannot_be_inside_allowed_input(self):
        pin = self.capture()
        with self.assertRaises(FileExistsError):
            self.capture()
        self.assertTrue(tool.verify_source(self.ev, pin)["verified"])
        with self.assertRaises(tool.EvidenceError):
            tool.freeze(self.wt, self.wt / "internal/evidence", BASE)

    def test_deep_denied_symlinks_are_not_followed_or_read(self):
        names = (".git", ".scratch", "node_modules", ".aws", ".ssh", ".codex", ".docker",
                 ".env", ".env.production", ".npmrc", ".pypirc", "auth.json", "credentials",
                 "secret.pem", "secret.key", "id_rsa", "id_ed25519", "credential-mounts", "config-mounts")
        deep = self.wt / "internal/deep/path"
        deep.mkdir(parents=True)
        for name in names:
            (deep / name).symlink_to(self.root / "nonexistent-secret")
        for name in ("dist", "playwright-report", "test-results"):
            (self.wt / "web" / name).symlink_to(self.root / "nonexistent-secret")
        pin = self.capture()
        manifest = json.loads((self.ev / "source-manifest.json").read_bytes())
        excluded = {x["path"] for x in manifest["excluded"]}
        self.assertEqual(len(excluded), len(names) + 3)
        self.assertTrue(tool.verify_source(self.ev, pin)["verified"])
        self.assertFalse(any(os.path.lexists(self.ev / "source/internal/deep/path" / x) for x in names))

    def test_hardlinks_become_independent_regular_files(self):
        os.link(self.wt / "internal/auth.go", self.wt / "internal/hard.go")
        self.capture()
        self.assertNotEqual((self.ev / "source/internal/auth.go").stat().st_ino,
                            (self.ev / "source/internal/hard.go").stat().st_ino)

    def test_symlink_special_permission_and_fifo_fail_without_manifest(self):
        for kind in ("symlink", "fifo", "setuid", "sticky_directory", "root_symlink"):
            with self.subTest(kind=kind):
                path = self.wt / "internal/unsafe"
                if kind == "symlink":
                    path.symlink_to(self.wt / "internal/auth.go")
                elif kind == "fifo":
                    os.mkfifo(path)
                elif kind == "setuid":
                    self.put("internal/unsafe", b"x", 0o4644)
                    if not path.stat().st_mode & 0o4000:
                        # This macOS sandbox strips setuid; real sticky-dir case
                        # below still exercises the special-permission rejection.
                        path.unlink()
                        continue
                elif kind == "sticky_directory":
                    path.mkdir()
                    path.chmod(0o1755)
                else:
                    path = self.root / "symlink-worktree"
                    path.symlink_to(self.wt, target_is_directory=True)
                self.ev = self.root / ("unsafe-" + kind)
                with self.assertRaises((tool.EvidenceError, OSError)):
                    tool.freeze(path if kind == "root_symlink" else self.wt, self.ev, BASE)
                self.assertFalse((self.ev / "source-manifest.json").exists())
                path.rmdir() if path.is_dir() and not path.is_symlink() else path.unlink()

    def test_fixed_profile_changes_required_missing_and_specific_ignore_fail(self):
        for name in PINNED + ("images/backend/Dockerfile.dockerignore", "web/package-lock.json"):
            with self.subTest(path=name):
                path = self.wt / name
                old = path.read_bytes() if path.exists() else None
                if name == "web/package-lock.json":
                    path.unlink()
                else:
                    self.put(name, (old or b"") + b"COPY unexpected /extra\n")
                self.ev = self.root / ("profile-" + name.replace("/", "_"))
                with self.assertRaises((tool.EvidenceError, OSError)):
                    self.capture()
                self.assertFalse((self.ev / "source-manifest.json").exists())
                path.unlink() if old is None else path.write_bytes(old)

    def test_copy_time_bytes_modes_and_file_directory_sets_are_checked(self):
        real_open = os.open
        for change in ("bytes", "mode", "added", "removed", "empty_directory"):
            with self.subTest(change=change):
                self.ev = self.root / ("race-" + change)
                opens = [0]

                def boundary_open(name, flags, *args, **kwargs):
                    if name == "auth.go" and flags & os.O_NOFOLLOW and not flags & os.O_DIRECTORY:
                        opens[0] += 1
                        if opens[0] == 2:  # real filesystem changes when copy opens its source
                            if change == "bytes":
                                (self.wt / "internal/auth.go").write_bytes(b"changed")
                            elif change == "mode":
                                (self.wt / "internal/auth.go").chmod(0o755)
                            elif change == "added":
                                self.put("internal/new.go", b"new")
                            elif change == "removed":
                                (self.wt / "web/token.ts").unlink()
                            else:
                                (self.wt / "internal/empty").rmdir()
                    return real_open(name, flags, *args, **kwargs)

                with mock.patch.object(os, "open", boundary_open):
                    with self.assertRaises((tool.EvidenceError, OSError)):
                        self.capture()
                self.assertFalse((self.ev / "source-manifest.json").exists())
                self.put("internal/auth.go", b"dummy dirty/untracked input\n")
                self.put("web/token.ts", b"dummy dirty/untracked input\n")
                (self.wt / "internal/empty").mkdir(exist_ok=True)
                if (self.wt / "internal/new.go").exists():
                    (self.wt / "internal/new.go").unlink()

    def test_snapshot_extra_missing_empty_directory_bytes_modes_and_manifest_pin(self):
        for change in ("extra", "missing", "extra_empty", "missing_empty", "bytes", "write", "execute", "rootmode", "manifest", "pin", "original"):
            with self.subTest(change=change):
                self.ev = self.root / ("tamper-" + change)
                pin = self.capture()
                parent = self.ev / "source/internal"
                path = parent / "auth.go"
                parent.chmod(0o755)
                if change == "extra":
                    (parent / "extra.go").write_bytes(b"extra")
                elif change == "missing":
                    path.unlink()
                elif change == "extra_empty":
                    (parent / "extra-empty").mkdir()
                elif change == "missing_empty":
                    (parent / "empty").rmdir()
                elif change == "bytes":
                    path.chmod(0o644)
                    path.write_bytes(b"tampered")
                    path.chmod(0o444)
                elif change in ("write", "execute"):
                    path.chmod(0o644 if change == "write" else 0o555)
                elif change == "rootmode":
                    (self.ev / "source").chmod(0o555)
                elif change == "manifest":
                    (self.ev / "source-manifest.json").write_bytes(b"{}")
                elif change == "original":
                    (self.ev / "dockerignore.original").write_bytes(b"different ignore")
                else:
                    pin = "0" * 64
                parent.chmod(0o555)
                with self.assertRaises((tool.EvidenceError, OSError)):
                    tool.verify_source(self.ev, pin)

    def test_build_failure_exception_signal_and_snapshot_change_keep_after_receipt(self):
        for failure in (2, -9, RuntimeError("external failure"), "snapshot"):
            with self.subTest(failure=str(failure)):
                self.ev = self.root / ("failure-" + str(type(failure).__name__) + str(failure).replace("/", "_"))
                pin = self.capture()

                def mutate():
                    path = self.ev / "source/internal/auth.go"
                    path.chmod(0o644)
                    path.write_bytes(b"build mutated snapshot")
                    path.chmod(0o444)

                runner = FakeDocker(effect=mutate if failure == "snapshot" else None,
                                    failure=None if failure == "snapshot" else failure)
                with self.assertRaises(tool.EvidenceError):
                    tool.build_role(self.ev, pin, "backend", "fixture-context", "fixture-builder", "linux/arm64", runner)
                before = json.loads((self.ev / "build/backend/source-before.json").read_bytes())
                after = json.loads((self.ev / "build/backend/source-after.json").read_bytes())
                self.assertTrue(before["verified"])
                self.assertEqual(after["verified"], failure != "snapshot")
                self.assertFalse((self.ev / "build-provenance.json").exists())
                if isinstance(failure, int):
                    call = json.loads((self.ev / "build/backend/build.json").read_bytes())
                    self.assertEqual(call["exit_code"], failure)
                    self.assertEqual(call["signal"], 9 if failure == -9 else None)
                    self.assertEqual((self.ev / "build/backend/build.stderr").read_bytes(), b"actual failure stderr\n")
                with self.assertRaises(tool.EvidenceError):
                    tool.build_role(self.ev, pin, "backend", "fixture-context", "fixture-builder", "linux/arm64", FakeDocker())

    def test_bad_iid_inspect_id_and_platform_cannot_succeed(self):
        for index, runner in enumerate((FakeDocker(iid="bad"), FakeDocker(inspect_id="sha256:" + "b" * 64), FakeDocker(arch="amd64"))):
            with self.subTest(case=index):
                self.ev = self.root / ("bad-image-" + str(index))
                pin = self.capture()
                with self.assertRaises(tool.EvidenceError):
                    tool.build_role(self.ev, pin, "backend", "fixture-context", "fixture-builder", "linux/arm64", runner)
                self.assertTrue((self.ev / "build/backend/source-after.json").is_file())
                self.assertFalse((self.ev / "build-provenance.json").exists())

    def test_check_build_rechecks_logs_missing_materials_argv_roles_and_receipts(self):
        for change in ("stdout", "stderr", "missinglog", "argv", "before", "after", "iid", "inspect", "missingrole", "sequence", "provenance"):
            with self.subTest(change=change):
                self.ev = self.root / ("build-tamper-" + change)
                pin = self.capture()
                self.build_all(pin)
                role = self.ev / "build/backend"
                if change in ("stdout", "stderr"):
                    (role / ("build." + change)).write_bytes(b"tampered")
                elif change == "missinglog":
                    (role / "build.stdout").unlink()
                elif change == "missingrole":
                    (self.ev / "build/assistant").rename(self.ev / "build/missing-assistant")
                elif change == "iid":
                    (role / "image.iid").write_bytes(("sha256:" + "b" * 64).encode())
                elif change == "inspect":
                    (role / "inspect.stdout").write_bytes(b'{}')
                elif change == "provenance":
                    (self.ev / "build-provenance.json").write_bytes(b'{"result":true}')
                else:
                    name = "source-" + change + ".json" if change in ("before", "after") else "role.json"
                    record = json.loads((role / name).read_bytes())
                    if change == "argv":
                        record["commands"]["build"][-1] = str(self.wt)
                    elif change == "sequence":
                        record["sequence"] = 3
                    else:
                        record["verified"] = False
                    (role / name).write_bytes(json.dumps(record).encode())
                with self.assertRaises((tool.EvidenceError, OSError, KeyError)):
                    tool.check_build(self.ev, pin)

    def test_safe_identity_fixed_role_platform_and_sequence_reject_without_runner(self):
        pin = self.capture()
        cases = (("web", "ctx", "builder", "linux/arm64"), ("unknown", "ctx", "builder", "linux/arm64"),
                 ("backend", "--host=x", "builder", "linux/arm64"), ("backend", "ctx", "--load", "linux/arm64"),
                 ("backend", "tcp://remote", "builder", "linux/arm64"), ("backend", "ctx", "builder", "linux/x86"))
        for args in cases:
            with self.subTest(args=args):
                with self.assertRaises(tool.EvidenceError):
                    tool.build_role(self.ev, pin, *args, docker_runner=lambda argv: self.fail("runner must not be called"))

    def test_source_after_before_last_docker_call_blocks_next_role_without_runner(self):
        pin = self.capture()
        tool.build_role(self.ev, pin, "backend", "fixture-context", "fixture-builder", "linux/arm64", FakeDocker())
        backend = self.ev / "build/backend"
        record = json.loads((backend / "role.json").read_bytes())
        inspect = json.loads((backend / "inspect.json").read_bytes())
        after_path = backend / "source-after.json"
        after = json.loads(after_path.read_bytes())
        after["checked_at"] = record["started_at"]
        self.assertLess(after["checked_at"], inspect["finished_at"])
        after_path.write_bytes(json.dumps(after).encode())
        calls = []
        fake = FakeDocker()

        def runner(argv):
            calls.append(argv)
            return fake(argv)

        with self.assertRaisesRegex(tool.EvidenceError, "order mismatch"):
            tool.build_role(self.ev, pin, "web", "fixture-context", "fixture-builder", "linux/arm64", runner)
        self.assertEqual(calls, [])
        self.assertFalse((self.ev / "build/web").exists())

    def test_source_operations_never_run_git_docker_or_other_subprocesses(self):
        with mock.patch.object(subprocess, "run", side_effect=AssertionError("no subprocess authority")):
            pin = self.capture()
            self.assertTrue(tool.verify_source(self.ev, pin)["verified"])

    def test_special_permission_on_source_root_is_rejected(self):
        self.wt.chmod(0o1755)
        with self.assertRaises(tool.EvidenceError):
            self.capture()
        self.assertFalse((self.ev / "source-manifest.json").exists())

    def test_copy_time_source_root_permission_changes_fail_without_manifest(self):
        real_open = os.open
        self.wt.chmod(0o755)
        for changed_mode in (0o700, 0o1755):
            with self.subTest(mode=oct(changed_mode)):
                self.ev = self.root / ("root-mode-race-" + oct(changed_mode))
                opens = [0]

                def boundary_open(name, flags, *args, **kwargs):
                    if name == "auth.go" and flags & os.O_NOFOLLOW and not flags & os.O_DIRECTORY:
                        opens[0] += 1
                        if opens[0] == 2:
                            self.wt.chmod(changed_mode)
                            self.assertEqual(self.wt.lstat().st_mode & 0o7777, changed_mode)
                    return real_open(name, flags, *args, **kwargs)

                try:
                    with mock.patch.object(os, "open", boundary_open):
                        with self.assertRaises(tool.EvidenceError):
                            self.capture()
                    self.assertGreaterEqual(opens[0], 2)
                    self.assertFalse((self.ev / "source-manifest.json").exists())
                finally:
                    self.wt.chmod(0o755)

    def test_missing_iid_and_source_before_failure_still_save_after(self):
        for case in ("missing-iid", "source-before"):
            with self.subTest(case=case):
                self.ev = self.root / case
                pin = self.capture()
                runner = FakeDocker()

                def missing_iid(argv):
                    result = runner(argv)
                    if "build" in argv:
                        Path(argv[argv.index("--iidfile") + 1]).unlink()
                    return result

                with self.assertRaises(tool.EvidenceError):
                    tool.build_role(self.ev, "0" * 64 if case == "source-before" else pin, "backend",
                                    "fixture-context", "fixture-builder", "linux/arm64", missing_iid)
                self.assertTrue((self.ev / "build/backend/source-before.json").exists())
                self.assertTrue((self.ev / "build/backend/source-after.json").exists())
                self.assertFalse((self.ev / "build-provenance.json").exists())

    def test_cli_help_parser_and_pinned_check_source_on_mechanical_fixture(self):
        pin = self.capture()
        executable = str(ROOT / "scripts/experience-dirty-build.py")
        for args, expected in ((["--help"], 0), (["check-source", "--evidence", str(self.ev), "--manifest-sha256", pin], 0),
                               (["check-source", "--evidence", str(self.ev)], 2),
                               (["build", "--evidence", str(self.ev), "--manifest-sha256", pin, "--role", "invalid"], 2)):
            result = subprocess.run([os.sys.executable, "-B", executable] + args, capture_output=True)
            self.assertEqual(result.returncode, expected, result.stderr)


if __name__ == "__main__":
    unittest.main()
