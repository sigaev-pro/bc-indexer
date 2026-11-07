"""Opt-in daemon cleanup fixture; no native client or network service is started."""

import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import sys
import tempfile
import time
from types import SimpleNamespace
import unittest

import build_resources
import evidence
import session_runner
from controller import Blocked
from controller_backend import RealBackend


SOURCE = Path(__file__).resolve().parents[2]


class DockerSupervisionTests(unittest.TestCase):
    @unittest.skipUnless(os.environ.get("EXTRACTOR_DOCKER_FIXTURE") == "1", "explicit disposable Docker fixture only")
    def test_timeout_stops_and_removes_the_owned_daemon_container(self):
        directory = Path(tempfile.mkdtemp(prefix="extractor-docker-cleanup-"))
        lock_fd = os.open(directory / "lock", os.O_RDWR | os.O_CREAT, 0o600)
        selected = json.loads((SOURCE / ".project/bootstrap/components/candidate-lock.json").read_text())
        image = selected["components"]["debian-runtime-image"]["pull_reference"]
        spec = {"schema_version": 1, "run_id": "fixture-owned-docker", "task_id": "E01-01.builds",
                "operation_id": "fixture-timeout", "nonce": secrets.token_hex(32), "images": [image]}
        inventory = directory / "docker-resources.json"
        request_path = directory / "request.json"
        worker = directory / "worker.py"
        worker.write_text(
            "import json,sys,time\nfrom pathlib import Path\n"
            + "sys.path.insert(0," + repr(str(SOURCE / ".project/automation")) + ")\n"
            + "import build_resources\n"
            + "request_path=Path(sys.argv[1]); request=json.loads(request_path.read_text())\n"
            + "spec=request['owned_docker']; inventory=request_path.parent/'docker-resources.json'\n"
            + "cid=build_resources.create(spec,inventory,'short-fixture',spec['images'][0],"
              "['--network','none','--cpus','1','--memory','128m','--pids-limit','16'],['sleep','20'])\n"
            + "result=build_resources.command(['container','start',cid]); assert result.returncode==0\n"
            + "print(cid,flush=True); time.sleep(60)\n")
        (directory / "stdin.txt").write_text("")
        request = {"operation_id": "fixture-timeout", "command": [sys.executable, "-I", str(worker), str(request_path)],
                   "cwd": str(directory), "deadline": time.time() + 25, "timeout_seconds": 6,
                   "stop_file": str(directory / "stop.json"), "log_limit": 8192, "env": {},
                   "stdin_sha256": hashlib.sha256(b"").hexdigest(), "owned_docker": spec}
        evidence.atomic_json(request_path, request)
        try:
            result = session_runner.supervise(request_path, lock_fd)
            self.assertEqual(result["reason"], "timeout")
            self.assertTrue(result["cleanup_complete"])
            cleanup = build_resources.validate_cleanup(spec, result["docker_cleanup"])
            self.assertEqual(len(cleanup["resources"]), 1)
            self.assertTrue(cleanup["resources"][0]["running"])
            self.assertEqual(build_resources.members(spec), [])
            self.assertEqual(result["request_sha256"], evidence.digest(request))
            print(json.dumps({"fixture": "owned-docker-timeout", "status": "PASS", "nonce": spec["nonce"],
                              "container_ids": [row["id"] for row in cleanup["resources"]],
                              "image": image, "cleanup_complete": True, "native_runtime": "NOT_RUN"}))
        finally:
            os.close(lock_fd)
            if inventory.exists():
                final = build_resources.cleanup(spec, inventory)
                if final["complete"]:
                    shutil.rmtree(directory)
                else:
                    print(json.dumps({"retained_fixture": str(directory), "cleanup": final}))
            else:
                shutil.rmtree(directory)

    @unittest.skipUnless(os.environ.get("EXTRACTOR_DOCKER_FIXTURE") == "1", "explicit disposable Docker fixture only")
    def test_parent_watchdog_reconciles_the_daemon_after_killing_the_supervisor(self):
        directory = Path(tempfile.mkdtemp(prefix="extractor-docker-watchdog-"))
        lock_fd = os.open(directory / "lock", os.O_RDWR | os.O_CREAT, 0o600)
        selected = json.loads((SOURCE / ".project/bootstrap/components/candidate-lock.json").read_text())
        image = selected["components"]["debian-runtime-image"]["pull_reference"]
        operation = directory / ".project/automation/runs/fixture-owned-docker/watchdog"
        worker = directory / "worker.py"
        worker.write_text(
            "import json,sys,time\nfrom pathlib import Path\n"
            + "sys.path.insert(0," + repr(str(SOURCE / ".project/automation")) + ")\n"
            + "import build_resources\n"
            + "request_path=Path(sys.argv[-1]); request=json.loads(request_path.read_text())\n"
            + "spec=request['owned_docker']; inventory=request_path.parent/'docker-resources.json'\n"
            + "cid=build_resources.create(spec,inventory,'watchdog-fixture',spec['images'][0],"
              "['--network','none','--cpus','1','--memory','128m','--pids-limit','16'],['sleep','20'])\n"
            + "assert build_resources.command(['container','start',cid]).returncode==0\n"
            + "(request_path.parent/'ready').write_text(cid); print(cid,flush=True); time.sleep(60)\n")
        backend = object.__new__(RealBackend)
        backend.root, backend.source = directory, SOURCE
        backend.launch_bound = {"hard_deadline": time.time() + 25}
        backend.remaining = lambda: 25
        backend.check_stop = lambda run: None
        backend.store = SimpleNamespace(directory=directory / "state", lock_fd=lock_fd,
            stopped=lambda run: (operation / "ready").exists())
        spec = None
        try:
            with self.assertRaises(Blocked):
                backend.supervised({"run_id": "fixture-owned-docker", "deadline": time.time() + 25},
                    "watchdog", [sys.executable, "-I", str(worker)], directory, timeout=25, owned_images=[image])
            request = json.loads((operation / "request.json").read_text())
            spec = request["owned_docker"]
            watchdog = json.loads((operation / "watchdog.json").read_text())
            self.assertEqual(watchdog["reason"], "operator stop")
            cleanup = build_resources.validate_cleanup(spec, watchdog["cleanup"]["docker"])
            self.assertEqual(len(cleanup["resources"]), 1)
            self.assertTrue(cleanup["resources"][0]["running"])
            self.assertEqual(build_resources.members(spec), [])
            print(json.dumps({"fixture": "owned-docker-watchdog", "status": "PASS", "nonce": spec["nonce"],
                              "container_ids": [row["id"] for row in cleanup["resources"]],
                              "cleanup_complete": True, "native_runtime": "NOT_RUN"}))
        finally:
            os.close(lock_fd)
            if spec is None and (operation / "request.json").exists():
                spec = json.loads((operation / "request.json").read_text()).get("owned_docker")
            if spec is not None and (operation / "docker-resources.json").exists():
                final = build_resources.cleanup(spec, operation / "docker-resources.json")
                if final["complete"]:
                    shutil.rmtree(directory)
                else:
                    print(json.dumps({"retained_fixture": str(directory), "cleanup": final}))
            else:
                shutil.rmtree(directory)


if __name__ == "__main__":
    unittest.main()
