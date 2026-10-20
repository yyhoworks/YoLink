# Third-party code and assets

## OpenList

- Source: https://github.com/OpenListTeam/OpenList
- Reviewed snapshot: `3b28270a4066ea52943a69ecddd850d83f7e8bdc`
- Copyright: OpenList and original AList contributors, as recorded in that repository.
- License: GNU Affero General Public License v3.0 (included in `LICENSE`).
- `providers.go` adapts cloud API request formats, pagination, 139 request signing and token renewal, Quark Web/TV request conventions, and Aliyun OpenAPI request conventions from the corresponding drivers.
- `accounts.go` adapts read-only quota request paths and byte/MB response fields from the 139, Quark Web and Aliyun OpenAPI drivers. Capacity remains unavailable when the current authorization does not return the required fields.
- This project is distributed under AGPL-3.0. All modified source is supplied in the distribution. When deploying a modified service, make its corresponding source available to its users as required by the license.
- No OpenList server, online public application, or external token service is included. Quark TV token renewal is disabled unless the operator configures a trusted HTTPS endpoint.

## Lucide

- Source: https://unpkg.com/lucide@0.468.0/dist/umd/lucide.min.js
- Copyright (c) Lucide contributors 2022. ISC License.
- Copyright (c) 2013-2022 Cole Bemis (Feather icons), MIT License.
- The vendored asset retains its license header. Additional license texts are in `web/assets/LICENSE.txt`.

## Go dependencies

SQLite driver: `modernc.org/sqlite`, BSD-3-Clause, including SQLite (public domain) and the dependencies pinned in `go.mod`/`go.sum`. Dependency license texts are distributed in the source archive under `licenses/`.

Encrypted backups use `filippo.io/age v1.3.2` (BSD-3-Clause), including its
`filippo.io/hpke v0.4.0` and `golang.org/x/crypto v0.55.0` dependencies
(BSD-3-Clause). Their upstream license notices are included under `licenses/`.
No external encryption process or hosted backup service is used. Indirect
`golang.org/x/sys v0.47.0` retains the Go BSD-3-Clause notice already distributed.

The existing MoviePilot plugin and MediaWarp informed the architecture; their implementation files are not copied into this service.

## Fonts

- IBM Plex Sans, IBM Corp. Source: https://github.com/IBM/plex . Vendored Latin WOFF2 subset at `web/assets/ibm-plex-sans-latin.woff2`.
- Noto Sans SC, The Noto Project Authors. Source: https://github.com/notofonts/noto-cjk . Vendored UI WOFF2 subset at `web/assets/noto-sans-sc-ui.woff2`.
- SIL Open Font License 1.1 texts accompany the fonts as `web/assets/ibmplexsans-OFL.txt` and `web/assets/notosanssc-OFL.txt`. Fonts are self-hosted; no external font request is needed.

## LocalDNS visual reference

The user requested the LocalDNS cosmos-glass design. Its locally supplied UI source informed the dark/translucent palette, floating header, inset sidebar, and mobile navigation. YoLink's interface is authored for its own STRM/302 flows; DNS content, logo and application logic are not included. The self-hosted OFL font files above were obtained from the user's LocalDNS UI assets and retain their accompanying licenses.

## YoLink 品牌素材

`web/assets/yolink-logo.png` 来自用户在项目中提供的 `yolink-horizontal-dark-ui-cropped.png`，仅增加来源元数据，保留像素。`web/assets/favicon.ico` 是用户提供的 `favicon.ico` 原样副本。来源与原文件 SHA256 见同目录 `yolink-provenance.json`。品牌素材的权利由原提供者保留，不据此授予新的第三方许可。

## 115 Cookie 协议

`provider115.go` 的只读目录、容量、pickcode 及下载取链接口参考 SheltonZhu/115driver 的 `pkg/driver`；`provider115_crypto.go` 的 m115 兼容编码从该项目 `pkg/crypto/m115` 改编，使用 Go 标准 RSA 加密并补充长度、分块及填充校验。

- Source: https://github.com/SheltonZhu/115driver
- Reviewed revision: `542720cb0034954750454e89f32b4852add6e2a9`
- Copyright (c) 2022-2024 SheltonZhu, MIT License；原许可及附带声明保存于 `licenses/115driver-LICENSE.txt`。
- 只实现读取与取链，不导入该库的登录、上传、删除、分享或离线下载功能；没有增加第三方运行时依赖。
- Cookie + pickcode + 302 的产品流程参考 Muvyo 官方文档 https://doc.mediavault.qzz.io/docs#config-strm；未取得其后端源码，不声称复用了 Muvyo 的内部实现。
