# README 界面截图

这些图片用于项目首页，来自 `9e91e10`（0.14.1）中的实际网页代码。

- 使用 `frontend/scripts/preview.mjs`，场景为 `YOLINK_PREVIEW_SCENARIO=butter`。
- 演示数据来自 `frontend/tests/fixtures/butter.mjs`；账号、目录、文件名、容量与统计均为合成数据。
- 预览仅监听本机，拒绝写请求，不访问 NAS、真实网盘或媒体服务器。
- 截图保留顶部的合成数据说明，仅裁去外侧空白、部分页尾或下方内容；未修改界面数据。
- JPEG 图片移除额外元数据，点击首页图片可查看原尺寸。

| 图片 | 页面 |
| --- | --- |
| `dashboard.jpg` | 仪表盘 |
| `generation.jpg` | STRM 生成 → 运行状态 |
| `rules.jpg` | STRM 生成 → 目录规则 |
| `accounts.jpg` | 网盘管理 |
| `records.jpg` | 运行记录 → 生成记录 |

更新截图时继续使用合成数据，避免采集真实凭据、个人路径、内网地址或私人媒体记录。品牌 Logo 复用 `web/assets/yolink-mark.svg`，来源说明见根目录 `THIRD_PARTY.md`。
