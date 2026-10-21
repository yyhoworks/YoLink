from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("yolink_plugin", Path(__file__).parent / "yolink/__init__.py")
plugin = importlib.util.module_from_spec(spec)
spec.loader.exec_module(plugin)
REAL_CATALOG_CONFIG = plugin._catalog_config


class Commands(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.config = Path(self.tmp.name) / "config.json"
        self.config.write_text(json.dumps({"base": "http://example.invalid", "event_token": "fixture",
            "allowed_users": ["123"], "chat_id": "123",
            "targets": {"国产剧": {"rule": "fixture-rule", "path": "/media/剧集/国产剧"}}}))
        self.session = {"platform": "telegram", "user_id": "123", "chat_id": "123", "chat_type": "dm"}
        self.patches = [patch.object(plugin, "CONFIG", self.config), patch.object(plugin, "_session", return_value=self.session), patch.object(plugin, "_catalog_config", side_effect=lambda c: c)]
        for p in self.patches:
            p.start()
            self.addCleanup(p.stop)

    def test_scoped_directory_only(self):
        result = {"source_path": "/media/剧集/国产剧/测试 剧/Season 1", "status": "pending"}
        with patch.object(plugin, "_call", return_value=result) as call:
            text = plugin.handle_command("国产剧/测试 剧/Season 1")
            self.assertIn("已提交", text)
            self.assertIn("未开启结果通知", text)
            self.assertEqual(call.call_args.args[2], {"rule": "fixture-rule", "path": result["source_path"], "kind": "directory"})

    def test_enabled_notification_reply(self):
        config = json.loads(self.config.read_text())
        config["targets"]["国产剧"]["notify"] = True
        self.config.write_text(json.dumps(config))
        with patch.object(plugin, "_call", return_value={"source_path": "/media/剧集/国产剧", "status": "pending"}):
            self.assertIn("将尝试发送结果通知", plugin.handle_command("国产剧"))

    def test_unknown_and_traversal_never_submit(self):
        with patch.object(plugin, "_call") as call:
            for value in ["未知", "国产剧/../电影", "国产剧//电影", "国产剧/./电影", "国产剧/目录\n指令", "国产剧\\目录"]:
                plugin.handle_command(value)
            call.assert_not_called()

    def test_other_user_or_group_never_submit(self):
        with patch.object(plugin, "_call") as call:
            self.session["user_id"] = "456"
            self.assertIn("仅供", plugin.handle_command("国产剧"))
            self.session["user_id"] = "123"
            self.session["chat_type"] = "group"
            self.assertIn("仅供", plugin.handle_command("国产剧"))
            call.assert_not_called()

    def test_help_and_status(self):
        with patch.object(plugin, "_call", return_value={"jobs": [], "events": []}) as call:
            self.assertIn("国产剧", plugin.handle_command(""))
            call.assert_not_called()
            self.assertIn("暂无任务", plugin.handle_command("状态"))
            self.assertEqual(call.call_args.args[1], "/api/events/status")

    def test_connection_failure_is_an_actionable_reply(self):
        with patch.object(plugin, "_call", side_effect=ValueError("连接失败")):
            self.assertEqual(plugin.handle_command("国产剧"), "连接失败")

    def test_command_uses_live_catalog_mapping(self):
        # 走真实 _catalog_config（不再打桩为恒等）：命令提交必须使用线上目录
        # 返回的 rule，而不是本地 config.targets 里的 fixture-rule。
        calls = {}

        def fake_call(config, path, payload=None):
            calls[path] = payload
            if path == "/api/events/catalog":
                return {"items": [{"alias": "国产剧", "rule": "live-rule",
                                   "path": "/media/live", "output": "/out/live", "notify": False}]}
            return {"source_path": "/media/live", "status": "pending"}

        with patch.object(plugin, "_catalog_config", REAL_CATALOG_CONFIG), \
                patch.object(plugin, "_call", side_effect=fake_call):
            text = plugin.handle_command("国产剧")
        self.assertIn("已提交", text)
        self.assertIn("/api/events/catalog", calls)
        self.assertEqual(calls["/api/events"], {"rule": "live-rule", "path": "/media/live", "kind": "directory"})


class Transport(unittest.TestCase):
    """_call 对本地假 HTTP 服务的真实传输行为（不 mock urllib）。"""

    def start_server(self, handler):
        server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
        server.daemon_threads = True
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.shutdown)
        self.addCleanup(server.server_close)
        return "http://127.0.0.1:%d" % server.server_address[1]

    def config(self, base):
        return {"base": base, "event_token": "fixture-token", "targets": {}}

    def test_success_sends_bearer_and_parses_json(self):
        seen = {}

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                seen["auth"] = self.headers.get("Authorization")
                seen["path"] = self.path
                body = json.dumps({"items": []}).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass

        base = self.start_server(Handler)
        result = plugin._call(self.config(base), "/api/events/catalog")
        self.assertEqual(result, {"items": []})
        self.assertEqual(seen["auth"], "Bearer fixture-token")
        self.assertEqual(seen["path"], "/api/events/catalog")

    def test_http_error_body_is_surfaced(self):
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                body = json.dumps({"error": "目录不存在"}).encode()
                self.send_response(404)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass

        base = self.start_server(Handler)
        with self.assertRaises(ValueError) as ctx:
            plugin._call(self.config(base), "/api/events", {"rule": "r", "path": "/x", "kind": "directory"})
        self.assertIn("目录不存在", str(ctx.exception))

    def test_http_error_with_non_json_body_falls_back(self):
        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                body = b"proxy exploded"
                self.send_response(502)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass

        base = self.start_server(Handler)
        with self.assertRaises(ValueError) as ctx:
            plugin._call(self.config(base), "/api/events/status")
        self.assertIn("请求失败", str(ctx.exception))

    def test_timeout_becomes_friendly_error(self):
        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                time.sleep(10)  # _call 的超时是 8 秒，服务端必须更慢
                self.send_response(200)
                self.send_header("Content-Length", "2")
                self.end_headers()
                self.wfile.write(b"{}")

            def log_message(self, *args):
                pass

        base = self.start_server(Handler)
        with self.assertRaises(ValueError) as ctx:
            plugin._call(self.config(base), "/api/events/status")
        self.assertIn("无法连接", str(ctx.exception))

    def test_oversized_response_is_rejected(self):
        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                body = json.dumps({"blob": "x" * (300 * 1024)}).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass

        base = self.start_server(Handler)
        # _call 只读 256KiB，超限响应会被截断成无效 JSON 并报错，而不是静默接受。
        with self.assertRaises(ValueError):
            plugin._call(self.config(base), "/api/events/catalog")


class ConfigResolution(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)

    def test_env_var_wins_and_is_listed_first(self):
        config = self.root / "custom.json"
        config.write_text(json.dumps({"base": "http://example.invalid", "event_token": "t", "targets": {}}))
        missing = self.root / "missing.json"
        with patch.dict(os.environ, {"YOLINK_CONFIG": str(config)}), \
                patch.object(plugin, "CONFIG", missing):
            self.assertEqual(plugin._config_candidates()[0], config)
            self.assertEqual(plugin._load_config()["event_token"], "t")

    def test_missing_config_error_lists_tried_paths(self):
        missing = self.root / "missing.json"
        with patch.dict(os.environ, {}, clear=False), \
                patch.object(plugin, "CONFIG", missing):
            os.environ.pop("YOLINK_CONFIG", None)
            with self.assertRaises(FileNotFoundError) as ctx:
                plugin._load_config()
        self.assertIn(str(missing), str(ctx.exception))


class Catalog(unittest.TestCase):
    def test_live_catalog_replaces_old_local_mapping(self):
        item = {"alias": "新目录", "rule": "new-rule", "path": "/media/new", "output": "/CloudNAS/MVStrm/new"}
        with patch.object(plugin, "_call", return_value={"items": [item]}):
            result = plugin._catalog_config({"targets": {"旧目录": {}}})
        self.assertNotIn("旧目录", result["targets"])
        self.assertEqual(result["targets"]["新目录"]["path"], "/media/new")

    def test_empty_live_catalog_does_not_reenable_old_mapping(self):
        with patch.object(plugin, "_call", return_value={"items": []}):
            result = plugin._catalog_config({"targets": {"旧目录": {}}})
        self.assertIn("尚未配置", plugin._help(result))


if __name__ == "__main__":
    unittest.main()
