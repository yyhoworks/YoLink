# 115 Cookie 通道

在网盘管理中添加「115 网盘 · Cookie 302」账号，导入自己的完整 Cookie（至少包含 UID、CID、SEID），根目录留空使用 0。保存后运行连接测试和容量查询，再配置目录规则。

Cookie 加密保存，API 不回显，失效时重新导入。此通道不提供扫码或短信登录。

目录和容量使用 115 HTTPS 接口，下载通过 Cookie + pickcode 获取原文件地址并返回 302。索引保留 pickcode，旧索引可通过文件 ID 补查。取链按播放器 User-Agent 隔离缓存，请求按账号串行并限速。

若播放器无法直接访问，可在账号中显式允许服务器中转；媒体流量此时经过服务器。取链返回的临时 Cookie 不写回账号，也不随 302 发给播放器。

账号权益、授权状态、风控和接口变化都会影响结果。模拟接口测试不代表真实账号取流或实际播放器拖动成功；请自行验证。

协议参考：

- [115driver](https://github.com/SheltonZhu/115driver/tree/542720cb0034954750454e89f32b4852add6e2a9/pkg)，MIT 许可，改编说明见根 THIRD_PARTY.md。
- [Muvyo 官方文档](https://doc.mediavault.qzz.io/docs#config-strm)，用于 Cookie、pickcode、302 产品流程参考。
