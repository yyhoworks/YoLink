"""Hermes-native /strm command. No Telegram update consumer or menu replacement."""

import json
import logging
import os
from pathlib import Path
from urllib import error, request


CONFIG = Path(__file__).resolve().parents[2] / "yolink.json"


def _config_candidates():
    """Config lookup order: $YOLINK_CONFIG, the classic location next to the
    Hermes home (parents[2] of this file, i.e. ~/.hermes/yolink.json when the
    plugin lives in ~/.hermes/<plugin-dir>/yolink/), beside the plugin package,
    and ~/.hermes/yolink.json as an absolute fallback."""
    candidates = []
    env_path = os.environ.get("YOLINK_CONFIG", "").strip()
    if env_path:
        candidates.append(Path(env_path).expanduser())
    candidates.append(CONFIG)
    candidates.append(Path(__file__).resolve().parent / "yolink.json")
    candidates.append(Path.home() / ".hermes" / "yolink.json")
    unique = []
    for path in candidates:
        if path not in unique:
            unique.append(path)
    return unique


def _load_config():
    tried = _config_candidates()
    for path in tried:
        if path.is_file():
            return json.loads(path.read_text())
    raise FileNotFoundError("；".join(str(path) for path in tried))
STATUS = {
    "pending": "等待开始", "queued": "排队中", "running": "生成中",
    "completed": "已完成", "partial": "部分失败", "failed": "失败",
    "cancelled": "已取消", "interrupted": "已中断", "submitted": "已提交",
}


def _session():
    from gateway.session_context import get_session_env

    return {k: get_session_env("HERMES_SESSION_" + k.upper(), "")
            for k in ["platform", "user_id", "chat_id", "chat_type"]}


def _call(config, path, payload=None):
    raw = None if payload is None else json.dumps(payload, ensure_ascii=False).encode()
    req = request.Request(config["base"].rstrip("/") + path, data=raw, headers={
        "Authorization": "Bearer " + config["event_token"],
        "Content-Type": "application/json",
    })
    try:
        # Both services are on the LAN; never send this credential through a proxy.
        with request.build_opener(request.ProxyHandler({})).open(req, timeout=8) as response:
            return json.loads(response.read(256 * 1024))
    except error.HTTPError as exc:
        try:
            detail = json.loads(exc.read(4096)).get("error", "请求失败")
        except (ValueError, AttributeError):
            detail = "请求失败"
        raise ValueError("YoLink：" + str(detail)[:240]) from None
    except (error.URLError, TimeoutError, OSError, ValueError):
        raise ValueError("无法连接 YoLink，请稍后重试或检查服务状态。") from None


def _help(config):
    if not config["targets"]:
        return "尚未配置可用目录命令。请在 YoLink 的服务设置 → Telegram 定向生成中新增或启用目录。"
    targets = "、".join(config["targets"])
    return ("STRM 定向生成\n可用目录：" + targets +
            "\n\n/strm 国产剧\n/strm 国产剧/具体剧名\n/strm 状态\n\n"
            "按对应规则的输出目录生成；相同内容跳过，保留已有不同内容的文件。")


def _catalog_config(config):
    data = _call(config, "/api/events/catalog")
    targets = {item["alias"]: {"rule": item["rule"], "path": item["path"], "output": item["output"], "notify": item.get("notify", False)}
               for item in data["items"]}
    return dict(config, targets=targets)


def _status(config):
    data = _call(config, "/api/events/status")
    rules = {v["rule"] for v in config["targets"].values()}
    pending = [v for v in data["events"] if v["rule"] in rules and v["status"] in ["pending", "failed"]][:3]
    jobs = [v for v in data["jobs"] if v["rule"] in rules][:5]
    lines = ["YoLink 最近任务"]
    for event in pending:
        lines.append(f"{STATUS.get(event['status'], event['status'])} · {event['path']}" +
                     ("\n原因：" + event["error"] if event.get("error") else ""))
    for job in jobs:
        lines.append(f"{STATUS.get(job['status'], job['status'])} · {job['source_path']}\n"
                     f"新增 {job['created_files']}，跳过 {job['skipped']}，失败 {job['errors']}" +
                     ("\n" + job["message"] if job.get("message") else ""))
    if not pending and not jobs:
        lines.append("暂无任务。")
    return "\n\n".join(lines)[:3800]


def handle_command(raw_args):
    try:
        config = _load_config()
        session = _session()
        if (session["platform"] != "telegram" or
                session["user_id"] not in config["allowed_users"] or
                session["chat_id"] != config["chat_id"] or
                session["chat_type"] not in ["private", "dm"]):
            return "此命令仅供已配置的 Telegram 私聊账号使用。"
        config = _catalog_config(config)
        args = raw_args.strip()
        if not args or args in ["help", "帮助", "列表", "list"]:
            return _help(config)
        if args in ["状态", "status"]:
            return _status(config)
        if len(args) > 300 or any(ord(c) < 32 for c in args) or "\\" in args:
            return "目录参数无效，请使用 /strm 国产剧 或 /strm 国产剧/具体剧名。"
        alias, _, relative = args.partition("/")
        target = config["targets"].get(alias)
        if not target:
            return "未找到这个目录名称。\n\n" + _help(config)
        if relative and any(p in ["", ".", ".."] for p in relative.split("/")):
            return "子目录必须在已配置的目录内，不能包含空路径、. 或 ..。"
        path = target["path"].rstrip("/") + ("/" + relative if relative else "")
        response = _call(config, "/api/events", {"rule": target["rule"], "path": path, "kind": "directory"})
        return (f"已提交 STRM 定向生成\n目录：{response['source_path']}\n"
                + ("输出：" + target["output"].rstrip("/") + ("/" + relative if relative else "") + "\n" if target.get("output") else "") +
                f"状态：{STATUS.get(response['status'], response['status'])}\n"
                + ("任务结束后将尝试发送结果通知；使用 /strm 状态 查看进度。" if target.get("notify") else
                 "此目录规则未开启结果通知；使用 /strm 状态 查看进度。"))
    except FileNotFoundError as exc:
        return ("YoLink 尚未配置，请先完成连接设置。已按顺序查找以下配置文件路径，均不存在："
                + str(exc)
                + "。也可设置环境变量 YOLINK_CONFIG 指定配置文件路径。")
    except (KeyError, TypeError, json.JSONDecodeError):
        return "YoLink 插件配置或返回数据无效，请检查连接设置。"
    except ValueError as exc:
        return str(exc)
    except Exception as exc:
        logging.getLogger(__name__).warning("strm command failed (%s)", type(exc).__name__)
        return "YoLink 命令未完成，请稍后重试，或在服务设置中检查连接与目录配置。"


def register(ctx):
    ctx.register_command("strm", handler=handle_command,
                         description="按目录生成 STRM 或查看任务状态",
                         args_hint="[目录|状态]")
