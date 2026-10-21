# 快速开始

## 启动

需要 Docker Engine 和 Docker Compose。克隆私有仓库需要已获访问权限的 GitHub 账号。

```sh
git clone https://github.com/yyhoworks/YoLink.git
cd YoLink
cp .env.example .env
mkdir -p ./data ./strm-test
sudo chown -R 10001:10001 ./data ./strm-test
docker compose build
docker compose run --rm --no-deps yolink check-permissions
docker compose up -d
```

这些命令使用默认的专属目录和 UID/GID；root 终端可省略 `sudo`。修改 `.env` 的 `STRMLINK_OUTPUT_DIR` 或 `YOLINK_UID`/`YOLINK_GID` 后，需要先建立实际挂载目录，按所选运行用户授予权限，再执行检查与启动。Compose 不会自动创建宿主机目录，避免意外建立 root 所有且服务不可写的目录。

`check-permissions` 以实际容器用户验证数据/输出目录及已有数据库、锁文件和密钥的访问权限，不启动服务或改写数据库。检查和正常启动都会在权限不足时给出具体路径与处理提示。

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

目录选择器和规则输出使用容器内路径 `/media-output`。该主机目录需要提前存在，并通过属主、组权限或 NAS ACL 授予运行用户遍历目录及创建文件的权限。镜像默认用户为 `yolink`（UID/GID `10001:10001`）。不要对共享媒体库直接递归改属主。

NAS 使用其他用户时，在 `.env` 设置 `YOLINK_UID`、`YOLINK_GID`，或在 override 配置 `user: "UID:GID"`。专属数据目录的已有数据库、锁文件和密钥也必须允许该用户访问；不要复制他人的 UID/GID。`check-permissions` 覆盖默认数据和输出挂载；额外挂载需另外验证写入权限。

## 字幕、资料与通知

规则中的字幕和非媒体复制为独立选项，默认关闭。字幕支持 SRT/ASS/SSA/VTT，最大 8 MiB；非媒体支持 NFO、图片等常用格式，最大 32 MiB。

媒体库刷新、Webhook/Telegram 通知需要在服务设置保存连接，并在规则中启用对应操作。HTTP 请求被接收不等于客户端已读或媒体库扫描完成。

## 升级与备份

```sh
git pull --ff-only
docker compose stop yolink
# 默认专属目录；自定义路径/UID/GID 时，改用实际设置。
sudo chown -R 10001:10001 ./data ./strm-test
docker compose build
docker compose run --rm --no-deps yolink check-permissions
docker compose up -d
docker compose ps
docker compose logs --tail=80 yolink
```

保留 `data/`、输出目录、`.env`、Compose override。数据库与密钥应一起备份；缺少密钥会影响凭据解密和已有播放签名。更新前备份数据，更新后使用原管理员密码重新登录。

从以 root 运行的旧镜像升级时，停止服务后再调整已有文件权限。仅修改挂载目录本身的属主不足以修复 root 所有的 `0600` 密钥、数据库和锁文件；专属目录按上面的递归命令处理，共享输出目录使用现有用户/ACL。保留数据文件内容，不要删除数据库或重新生成密钥。

修改管理员密码现在也会轮换兼容事件令牌；改密后请同步更新 Hermes 或旧脚本的接入凭据。

更改服务地址不会自动改写已有 STRM。需要使用迁移维护工具预览、核对并备份后更新，避免手工批量替换。

## 外观

皮肤管理支持本地 JPG/PNG/WebP 壁纸及 HTTPS 图片直链。外观只保存在当前浏览器；在线壁纸由浏览器直接访问图片站点，防盗链或网络故障可能使其不可用。
