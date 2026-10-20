import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("yolink_plugin", Path(__file__).parent / "yolink/__init__.py")
plugin = importlib.util.module_from_spec(spec)
spec.loader.exec_module(plugin)


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
