# 项目工作规范

## 项目定位

`satellite-data`（`nuist-fy-lab/satellite-data`）是统一的多源卫星与再分析科学数据发现、持久化订单调度、归档索引与下载同步单仓（旧 `satellite-data-downloader` 仓库已归档）。覆盖 GOES-16/17/18/19、Himawari-8/9、MODIS、VIIRS、MTG (FCI)、IASI / IASI-NG、EarthCARE、CALIPSO、CloudSat 与 ERA5 / ERA5-Land，全部对接官方对象存储、STAC/CMR 或标准认证 API，不依赖已废弃 FTP 或网页爬虫。

系统运行时由四个静态二进制协同工作：

- `satellited`：控制面（HTTP API、内嵌前端 `web/`、官方目录、可用性日历、状态与归档索引、加密凭据 Vault 与本机授权网关、订单调度）。
- `satellite-data-downloader`：数据面（按数据源隔离的下载队列、断点续传、流式校验、暂存与原子归档），仅通过本机私有 Unix Socket 与 `satellited` 通信。
- `satellite-checksum-sidecar`：低 I/O 优先级历史归档校验和巡检进程，与实时下载解耦。
- `satellite`：轻量用户 CLI，仅通过 API Key 与中心通信，不接触上游凭据，也不直接下载文件。

## 开发、测试与 CI/CD

- **在本机工作区编辑源码与跑本地测试，严禁在 `c2ln1` 上从源码编译或直接修改生产节点源码。** 生产节点只消费 GitHub Actions 发布并经校验的静态产物（`CGO_ENABLED=0`、`GOOS=linux`、`GOARCH=amd64`、`GOAMD64=v1`）。
- **本仓库为公开仓库，CI/CD 固定使用 GitHub 托管的 `ubuntu-latest` Runner**（不要切到仅用于私有仓库的 `c2ln1-org` ghpool）。流水线按 `deploy/ci/plan.py` 的局部依赖图增量触发模块测试、构建与 `ci-latest` / `ci-pr-<num>-<sha12>` 发布，详见 `docs/ci-cd.md`。
- 提交前按改动范围执行本地验证：
  - Go 改动：运行 `gofmt`、`go vet`、`go test`；涉及 `internal/state`、`internal/downloadcoord`、`internal/httpapi`、`internal/central`、`internal/dataplane/{access,store,server,transfer,pipeline}` 时补充 `go test -race`。
  - 前端改动（`web/`）：保持零外部运行时构建依赖，运行 `node --check web/static/*.js web/*.mjs` 及 `node --test web/era5.test.mjs web/download-timezones.test.mjs web/download-order-audit.test.mjs web/calendar-index.test.mjs web/catalog-inventory.test.mjs`（样式与 Firefox 桌面/移动端布局校验由 CI 执行）。
  - 部署与 CI 脚本改动（`deploy/`）：运行 `PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s deploy/ci/tests -v`、`python3 -m unittest discover -s deploy/tests -p 'test_*.py'` 与 `bash -n deploy/publish-ci-release.sh`；维护与迁移脚本（`deploy/*.py`）需保持兼容生产环境 Python 3.6 语法。
- 保持 Go 纯静态可编译（无 CGO 依赖）与目录职责清晰，不引入与目标无关的框架或外部构建链。

## `c2ln1` 生产部署与运维

- **严格单主绑定 `c2ln1`**：集群 `$HOME` 共享 GPFS，所有用户态单元必须同时保留 `deploy/systemd/c2ln1/*.conf` 中的 `ConditionHost=c2ln1` 与进程级 `SATELLITE_REQUIRE_HOST=c2ln1`，避免其他节点误启实例破坏状态库或清理 IPC Socket。
- **私有 Unix Socket 必须在节点本地 `$XDG_RUNTIME_DIR`**：`SATELLITE_CENTRAL_SOCKET`、`SATDOWN_SOCKET`、`SATELLITE_ARCHIVE_EVENT_SOCKET` 等绝不可放在共享 GPFS `$HOME` 下。
- **生产目录与服务布局**：
  - 发行版本与软链：`~/.local/opt/satellite-data/releases/<tag>`，通过 `current` / `previous` 软链切换与回滚（`~/.local/bin/satellite*` 指向 `current/`），目录内保留 `SHA256SUMS` 与 `build-manifest.json`。
  - 运行时配置：`~/.config/satellite-data/runtime.env`（`satellited`，监听 `127.0.0.1:8787`，生产状态库使用 PostgreSQL `SATELLITED_POSTGRES_DSN`）与 `~/.config/satellite-data-downloader/runtime.env`（`satellite-data-downloader` 与 `satellite-checksum-sidecar`）。
  - 正式归档与暂存路径：通用归档 `/misc/nuist_data1/public_sharing/satellite`、EarthCARE `/misc/nuist_data1/public_sharing/EarthCare`、ERA5 `/misc/nuist_data1/public_sharing/ERA5`、Himawari `/misc/nuist_data1/public_sharing/Himawari`；下载暂存 `~/.local/state/satellite-data-downloader/delivery`。
  - `systemd --user` 服务：`satellited.service`、`satellite-data-downloader.service`、`satellite-checksum-sidecar.service`、`earthcare-verification.timer/.service`（以及配套出口监听 `satellite-direct-socks.service`）。
- **发布、升级与回滚**：
  - GitHub Actions 只发布经读回校验的构建产物，**不会自动安装或重启生产服务**。
  - 上线前保留 `previous` 版本与原配置用于回滚；涉及控制面/数据面协议变更或六路 SSH 出口配置（`SATELLITE_DOWNLOAD_EGRESS_FILE`，见 `docs/download-egress-ssh.md`）时，需同步部署匹配的 `satellited` 与 `satellite-data-downloader`，并按先 `satellited`、后 `satellite-data-downloader` 与 `satellite-checksum-sidecar` 的顺序重启。
  - 涉及数据库迁移、凭据迁移或归档目录调整时，必须先停服务并做好本地备份，不自动删除历史数据、主密钥或队列状态。

## 安全与领域不变量

- **集中式凭据归属**（见 `docs/central-access.md`）：所有上游账号、Token、CDS 密钥、代理密码与 AES-GCM Vault 仅由 `satellited` 持有；下载器与 CLI 不接触明文上游凭据。严禁在日志、命令参数、文档或提交中输出、复制或写入真实凭据、数据库密码、API Key 或生产出口公网 IP（仓库示例统一使用 RFC 5737 文档地址）。
- **观测时间与归档一致性**：可用性日历、筛选与下载订单均以经核验的文件观测时间为准，不以文件落盘时间替代；订单仅在文件经校验并原子晋升至正式归档后才视为完成。
