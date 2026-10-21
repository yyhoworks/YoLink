# 开发与验证

后端使用 Go（最低版本见根 `go.mod`）和 SQLite，运行网页位于 `web/`，通过 Go embed 打入程序。Docker 默认从源码构建。

## CI

`.github/workflows/ci.yml` 在每次 push 与 pull request 时自动运行：Linux 作业执行 `go build ./...`、`go vet ./...`、`go test ./...`（含仅 Linux 编译的恢复/备份/修复测试），前端作业执行 `npm ci` 与 `npm run check`。需要专用环境的测试不在默认 CI 中开启，仍按本文档手工运行：ENOSPC 磁盘满测试（需 `YOLINK_TEST_DISK_FULL=1` 与专用 tmpfs）与浏览器 E2E（需 `YOLINK_BROWSER_E2E`/`YOLINK_CHANGES_BROWSER_E2E` 与本机 Edge）。

```sh
go test ./...
go vet ./...
go build .
```

部分测试是按环境变量启用、启动后持续运行的本地浏览器 fixture；普通测试默认跳过。输出挂载测试需要 Linux Docker 环境。

## 前端

Node.js 版本要求见 `frontend/package.json`。使用固定 npm 锁文件：

```sh
npm ci --prefix frontend
npm run --prefix frontend check
npm run --prefix frontend test:browser
```

`frontend/src/legacy/app.js` 是主 app 的维护入口；`web/app.js` 是生成物，不应手工修改。修改源码后先执行 `npm run --prefix frontend build`，再运行检查。其他 `web/*.js`、HTML、CSS 继续原地维护。

前端为共享词法作用域的经典脚本链，构建保持单个无 import/export 的 app.js。详见 `frontend/vite.config.ts` 与兼容性测试；不能直接改为 IIFE 而不处理跨脚本绑定。

`frontend/go.mod` 仅隔离 node_modules 中的 Go 示例。Go/Docker 构建只使用已生成的 `web/`，运行时不需要 Node.js。

## 浏览器测试

```sh
npm run --prefix frontend preview
```

预览服务默认监听 `127.0.0.1:18797`，读取真实静态代码并提供明确标注的合成只读 API。它不连接真实网盘。运行自动浏览器测试前关闭手动预览，避免端口冲突。

Playwright 默认使用 Microsoft Edge。其他环境可调整 `frontend/playwright.config.ts` 的 channel，并安装相应浏览器。截图/trace 位于已忽略的 `frontend/test-results/`，不应提交。

前端 `check` 包含 typecheck、Vitest、lint 和内存重建生成物比对。旧 JavaScript 未全部启用类型检查，检查通过不代表所有历史脚本均通过完整类型审查。

## 仓库内容

提交应用源码、测试、静态运行资产、依赖锁文件和许可证。运行数据、账号、密钥、`.env`、机器相关 override、构建归档及私人部署记录应始终留在仓库之外。
