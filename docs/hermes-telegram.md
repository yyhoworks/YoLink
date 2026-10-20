# Hermes Telegram 接入

`integrations/hermes/yolink/` 提供原生 `/strm` 命令插件，可查询已配置目录、提交指定子目录生成并查询任务状态。插件不调用大模型。

1. 将插件放入自己 Hermes 环境的插件目录，按该环境的插件管理方式启用。
2. 在 YoLink「服务设置」为该工具创建独立接入应用，选择允许使用的规则并妥善保存首次显示的令牌。
3. 按插件源码中的配置字段，在 Hermes 私有配置文件 `~/.hermes/yolink.json` 填写服务地址、`event_token`、允许的 Telegram 用户及私聊 ID。配置文件权限建议 0600，禁止提交到仓库。
4. 在 YoLink 的目录命令设置里配置别名、规则与云端路径。插件每次读取当前命令目录。
5. 在已授权私聊中发送 `/strm`、`/strm 别名` 或 `/strm 别名/子目录`，通过 `/strm 状态` 查询结果。

插件同时核对 Telegram 用户与私聊会话；未知目录、越界路径和未授权用户不会提交任务。输出位置由已有规则控制，命令不接收任意本地输出路径。

结果通知需要单独配置通知通道，并在目录规则启用通知。YoLink Webhook 使用时间戳和原始 JSON 字节的 HMAC-SHA256 签名；接收方需要校验签名、时效并根据稳定的 X-Request-ID 去重。

POST /api/events 返回已排队不代表生成已完成；读取目录、生成、通知投递的失败应分别处理和重试。测试用例位于 `integrations/hermes/test_yolink.py`，不使用真实 Telegram 或网盘账号。
