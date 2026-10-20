# 快速开始

## 启动

需要 Docker Engine 和 Docker Compose。克隆私有仓库需要已获访问权限的 GitHub 账号。

```sh
git clone https://github.com/yyhoworks/YoLink.git
cd YoLink
cp .env.example .env
docker compose up -d --build
```

默认随机生成首次管理员密码，位于 `data/initial-admin-password.txt`。也可以在首次启动前通过 `.env` 的 `STRMLINK_ADMIN_PASSWORD` 设置初始密码。在页面修改密码后，持久化的新密码优先，重启不会恢复旧初始密码。

浏览器访问 `http://你的NAS-IP:8796`。不要把管理员密码或 `.env` 提交到 Git。

## 生成第一批文件

1. 在服务设置填写媒体服务器和播放器能够访问的 YoLink 地址，例如 `http://nas.example:8796`。
2. 添加自己的网盘账号，导入已有凭据并测试连接。
3. 浏览一个较小的媒体目录，选择作为规则来源。
4. 选择独立输出目录，保存规则；先执行只读预览或试生成少量文件。
5. 将输出目录加入媒体服务器，并在实际播放器试播和拖动。

默认输出目录 `./strm-test` 映射为容器内 `/strm`。输出卷需要支持同卷硬链接。生成只新增或跳过相同内容，遇到不同内容保留原文件并报告错误。

## 端口与挂载

在 `.env` 设置 `STRMLINK_PORT` 可修改主机端口；`STRMLINK_OUTPUT_DIR` 可修改默认输出位置。服务地址应同步使用播放器可访问的新端口。

额外挂载可在本机创建 `compose.override.yaml`，例如：

```yaml
services:
  yolink:
    volumes:
      - type: bind
        source: /srv/media-output
        target: /media-output
```

目录选择器和规则输出使用容器内路径 `/media-output`。该主机目录需要提前存在，并授予容器运行用户必要的权限。若 NAS 需要指定用户，可在 override 中配置适合自己环境的 `user`；不要复制他人的 UID/GID。

## 字幕、资料与通知

规则中的字幕和非媒体复制为独立选项，默认关闭。字幕支持 SRT/ASS/SSA/VTT，最大 8 MiB；非媒体支持 NFO、图片等常用格式，最大 32 MiB。

媒体库刷新、Webhook/Telegram 通知需要在服务设置保存连接，并在规则中启用对应操作。HTTP 请求被接收不等于客户端已读或媒体库扫描完成。

## 升级与备份

```sh
git pull --ff-only
docker compose up -d --build
docker compose ps
docker compose logs --tail=80 yolink
```

保留 `data/`、输出目录、`.env`、Compose override。数据库与密钥应一起备份；缺少密钥会影响凭据解密和已有播放签名。更新前备份数据，更新后使用原管理员密码重新登录。

更改服务地址不会自动改写已有 STRM。需要使用迁移维护工具预览、核对并备份后更新，避免手工批量替换。

## 外观

皮肤管理支持本地 JPG/PNG/WebP 壁纸及 HTTPS 图片直链。外观只保存在当前浏览器；在线壁纸由浏览器直接访问图片站点，防盗链或网络故障可能使其不可用。
