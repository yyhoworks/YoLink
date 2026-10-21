#!/usr/bin/env python3
"""Exercise the built image with disposable Linux bind mounts and no host ports."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    parser.add_argument("--docker", default="docker")
    parser.add_argument("--work-dir", help="Parent directory on the Docker host")
    args = parser.parse_args()

    def docker(*argv, check=True):
        result = subprocess.run(
            [args.docker, *argv], text=True, capture_output=True, timeout=90
        )
        if check and result.returncode:
            raise RuntimeError(f"docker {argv[0]} failed: {result.stderr.strip()}")
        return result

    with tempfile.TemporaryDirectory(
        prefix="yolink-permissions-", dir=args.work_dir
    ) as temporary:
        root = Path(temporary).resolve()
        for scenario, uid, gid in (
            ("fresh", 10001, 10001),
            ("old-root-volume", 10001, 10001),
            ("custom-user", 23456, 34567),
        ):
            data, output = root / scenario / "data", root / scenario / "output"
            data.mkdir(parents=True)
            output.mkdir()
            # Every ownership operation is confined to these newly created
            # paths. Never accept a production path or follow a substituted link.
            for directory in (data, output):
                if root not in directory.resolve().parents or directory.is_symlink():
                    raise RuntimeError("test mount escaped its temporary directory")
            mounts = [
                "--mount", f"type=bind,src={data},dst=/data",
                "--mount", f"type=bind,src={output},dst=/strm",
            ]
            common = [
                "--network", "none", "--read-only", "--cap-drop", "ALL",
                "--security-opt", "no-new-privileges:true",
                "--tmpfs", "/tmp:rw,nosuid,nodev,size=32m", *mounts,
            ]
            name = "yolink-permissions-" + uuid.uuid4().hex

            def owner(owner_uid, owner_gid):
                docker(
                    "run", "--rm", *common, "--user", "0:0",
                    "--cap-add", "CHOWN", "--cap-add", "FOWNER", "--cap-add", "DAC_OVERRIDE",
                    "--entrypoint", "/bin/sh", args.image, "-ec",
                    f"chown -R {owner_uid}:{owner_gid} /data /strm; chmod 700 /data /strm",
                )

            def start(user):
                docker("run", "-d", "--name", name, *common, "--user", user, args.image)
                healthy()

            def healthy():
                deadline = time.monotonic() + 30
                while time.monotonic() < deadline:
                    running = docker("inspect", "--format", "{{.State.Running}}", name)
                    if running.stdout.strip() != "true":
                        # The application logs paths and errors, never the
                        # generated administrator password or key contents.
                        logs = docker("logs", name, check=False)
                        raise RuntimeError("test service exited: " + logs.stderr.strip())
                    if docker("exec", name, "yolink", "healthcheck", check=False).returncode == 0:
                        return
                    time.sleep(0.25)
                raise RuntimeError("test service did not become healthy")

            def fingerprint():
                result = docker(
                    "run", "--rm", *common, "--user", "0:0",
                    "--cap-add", "DAC_OVERRIDE", "--entrypoint", "/bin/sh", args.image,
                    "-ec", "sha256sum /data/credentials.key /data/playback.key /data/events.key /strm/existing.strm",
                )
                # Keep only an aggregate of test-file checksums; no secrets
                # or individual credential hashes are written to the log.
                return hashlib.sha256(result.stdout.encode()).hexdigest()

            expected = None
            try:
                owner(0, 0)
                if scenario == "old-root-volume":
                    # Simulate a previous image's root-owned 0600 SQLite/keys
                    # and lock, with an existing STRM that must survive upgrade.
                    start("0:0")
                    docker("exec", name, "/bin/sh", "-ec", "printf 'existing test STRM\\n' > /strm/existing.strm")
                    docker("stop", "--time", "15", name)
                    docker("rm", name)
                    expected = fingerprint()

                denied = docker(
                    "run", "--rm", *common, "--user", f"{uid}:{gid}",
                    args.image, "check-permissions", check=False,
                )
                if denied.returncode == 0 or "QUICKSTART.md" not in denied.stderr:
                    raise RuntimeError("unprepared mount did not fail with a permissions hint")

                owner(uid, gid)
                docker(
                    "run", "--rm", *common, "--user", f"{uid}:{gid}",
                    args.image, "check-permissions",
                )
                if scenario == "old-root-volume":
                    # A writable directory can still contain an inaccessible
                    # 0600 file. Verify that the preflight catches that too.
                    docker(
                        "run", "--rm", *common, "--user", "0:0", "--cap-add", "CHOWN",
                        "--cap-add", "DAC_OVERRIDE", "--entrypoint", "/bin/sh", args.image,
                        "-ec", "chown 0:0 /data/.yolink.lock; chmod 600 /data/.yolink.lock",
                    )
                    denied = docker(
                        "run", "--rm", *common, "--user", f"{uid}:{gid}",
                        args.image, "check-permissions", check=False,
                    )
                    if denied.returncode == 0 or ".yolink.lock" not in denied.stderr:
                        raise RuntimeError("root-owned existing lock escaped the permission check")
                    owner(uid, gid)
                    if fingerprint() != expected:
                        raise RuntimeError("ownership migration changed existing key/STRM contents")

                start(f"{uid}:{gid}")
                actual = docker("exec", name, "/bin/sh", "-ec", "printf '%s:%s' \"$(id -u)\" \"$(id -g)\"")
                if actual.stdout != f"{uid}:{gid}":
                    raise RuntimeError("runtime user does not match the selected UID/GID")
                docker("exec", name, "/bin/sh", "-ec", "printf 'new test STRM\\n' > /strm/new.strm")
                docker("restart", "--time", "15", name)
                healthy()
                docker("exec", name, "/bin/sh", "-ec", "test -s /data/strmlink.db; test -s /strm/new.strm")
                if expected is not None and fingerprint() != expected:
                    raise RuntimeError("upgrade/restart changed existing key/STRM contents")
                print(json.dumps({"scenario": scenario, "uid": uid, "gid": gid, "result": "passed"}), flush=True)
            finally:
                docker("rm", "--force", name, check=False)
                # Let TemporaryDirectory remove even root-owned test files.
                owner(os.getuid(), os.getgid())


if __name__ == "__main__":
    main()
