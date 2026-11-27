# 项目工作规范

## 项目定位

`satellite-data`（`nuist-fy-lab/satellite-data`）是统一的多源卫星与再分析科学数据发现、持久化订单调度、归档索引与下载同步单仓（旧 `satellite-data-downloader` 仓库已归档）。覆盖 GOES-16/17/18/19、Himawari-8/9、MODIS、VIIRS、MTG (FCI)、IASI / IASI-NG、EarthCARE、CALIPSO、CloudSat 与 ERA5 / ERA5-Land，全部对接官方对象存储、STAC/CMR 或标准认证 API，不依赖已废弃 FTP 或网页爬虫。

项目提供四个静态二进制。

- `satellited`：控制面（HTTP API、内嵌前端 `web/`、官方目录、可用性日历、状态与归档索引、加密凭据 Vault 与本机授权网关、订单调度）。
- `satellite-data-downloader`：数据面（按数据源隔离的下载队列、断点续传、流式校验、暂存与原子归档），仅通过本机私有 Unix Socket 与 `satellited` 通信。
- `satellite-checksum-sidecar`：低 I/O 优先级历史归档校验和巡检进程，与实时下载解耦。
- `satellite`：轻量用户 CLI，仅通过 API Key 与中心通信，不接触上游凭据，也不直接下载文件。

## 开发、测试与 CI/CD

- **在本机工作区编辑源码并运行测试。** `c2ln6` 生产节点只安装 GitHub Actions 发布并经校验的静态产物（`CGO_ENABLED=0`、`GOOS=linux`、`GOARCH=amd64`、`GOAMD64=v1`）。
- **本仓库为公开仓库，CI/CD 固定使用 GitHub 托管的 `ubuntu-latest` Runner**（不要切到仅用于私有仓库的 `c2ln1-org` ghpool）。流水线按 `deploy/ci/plan.py` 的局部依赖图增量触发模块测试、构建与 `ci-latest` / `ci-pr-<num>-<sha12>` 发布，详见 `docs/ci-cd.md`。
- 提交前按改动范围执行本地验证：
  - 修改 Go 代码后运行 `gofmt`、`go vet`、`go test`。涉及 `internal/state`、`internal/downloadcoord`、`internal/httpapi`、`internal/central`、`internal/egress`、`internal/dataplane/{access,store,server,transfer,pipeline}` 时补充 `go test -race`。
  - 前端保持零外部运行时构建依赖。修改 `web/` 后，对 `web/static/*.js` 和 `web/*.mjs` 逐文件运行 `node --check`，再运行 `node --test web/era5.test.mjs web/download-timezones.test.mjs web/download-order-audit.test.mjs web/calendar-index.test.mjs web/catalog-inventory.test.mjs web/modern-portal.test.mjs`。更新共享 CSS 版本时同步入口页与登录页，并按 CI 命令运行 `web/style.test.mjs`。Firefox 桌面和移动端布局由 CI 校验。
  - 部署与 CI 脚本改动（`deploy/`）：运行 `PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s deploy/ci/tests -v`、`python3 -m unittest discover -s deploy/tests -p 'test_*.py'` 与 `bash -n deploy/publish-ci-release.sh`；维护与迁移脚本（`deploy/*.py`）需保持兼容生产环境 Python 3.6 语法。
- 保持 Go 纯静态可编译（无 CGO 依赖）与目录职责清晰，不引入与目标无关的框架或外部构建链。

## `c2ln6` 生产部署与运维

- **单主与进程管理。** 集群家目录共享 GPFS，生产应用仅在 `c2ln6` 运行，并保留 `SATELLITE_REQUIRE_HOST=c2ln6`。Process Compose 的站点配置为 `~/.config/c2ln6-satellite/apps.yaml`，使用 `~/.config/c2ln6-satellite/ctl process list -o json` 查看状态，通过同一 `ctl` 的 `process restart <name>` 重启指定进程。三个应用进程为 `satellited`、`satellite-data-downloader` 和 `satellite-checksum-sidecar`，站点另有入口、后端及 PostgreSQL SSH 隧道。`c2ln5` 的 Process Compose 实例和 `c2ln1` 的 systemd 配置属于旧部署。
- **运行目录与状态。** 私有 Unix Socket 统一位于节点本地 `/tmp/c2ln6-satellite-3012/`，该目录用作 `$XDG_RUNTIME_DIR`，权限为 `0700`。控制面使用 `satellite-data/central.sock` 和 `satellite-data/archive-events.sock`，下载器使用 `satellite-data-downloader/downloader.sock`，Process Compose 使用 `control.sock`。`satellited` 在 `127.0.0.1:8787` 提供 HTTP 服务，控制面状态库使用 PostgreSQL，下载器状态库位于共享 GPFS 上的 `~/.local/state/satellite-data-downloader/downloader.sqlite3`。
- **归档与暂存。** 通用归档为 `/misc/nuist_data1/public_sharing/satellite`，同一 `public_sharing` 下设有 `EarthCare`、`ERA5`、`Himawari` 归档目录。下载默认暂存于 `~/.local/state/satellite-data-downloader/delivery`，另配置 `SATDOWN_DATA1_DOWNLOAD_ROOT` 用于 `nuist_data1` 上的暂存。
- **内存目标。** `satellited`、下载器与校验进程的 Go `GOMEMLIMIT` 分别为 `256MiB`、`96MiB` 和 `32MiB`。这是运行时内存软目标。核查内存时以节点实测为准。

### 发布与回滚

- GitHub Actions 发布并读回校验产物。生产安装与重启单独执行。安装前核对发布目标提交、`build-manifest.json` 和 `SHA256SUMS`。
- 生产版本安装到 `~/.local/opt/satellite-data/releases/` 下的独立目录，通过 `~/.config/c2ln6-satellite/release` 原子切换，保留原链接目标和配置以供回滚。以节点主机名和实际链接为准，迁移前的产物目录可能仍带 `satellite-ln5-` 前缀。共享的 `~/.local/opt/satellite-data/current` 属于旧部署。
- 按变更范围重启进程。出口策略和监控由 `satellited` 加载，可单独重启控制面。控制面与数据面协议有变更时，部署匹配版本，按 `satellited`、下载器、校验进程的顺序重启。切换后核对 Process Compose 状态、控制面 `/healthz`、下载器私有 Socket 及相关功能读回。
- 涉及数据库、凭据或归档目录迁移时，先停受影响的进程并备份。保留历史数据、主密钥和队列状态。

### 下载出口

- 控制面的 `SATELLITE_DOWNLOAD_EGRESS_FILE` 指向 `~/.config/c2ln6-satellite/download-egress.json`。生产出口包括节点直连、`vwan1` 至 `vwan9` 直连，以及经 iStoreOS 的东方超算云代理 `east-hpc`。代理通过节点本地 `127.0.0.1:17980` 接入，与节点直连和九条 VWAN 直连共同参与全部 12 个下载源的主线路调度。选路按源比较文件下载实测速率，仅影响新传输。明显偏慢的线路每源保留 1 条下载额度，快线可按实测总速率试加 1 条。目录查询和上游认证仍由数据源的访问配置管理。
- 下载器单独读取 `~/.config/satellite-data/download-egress.ssh.json` 计算工作并发。修改主线路数量或公网 IP 时，核对控制面与下载器的两份配置，并读回并发。共享公网 IP 的线路共用每源并发额度。当前两份配置各有 11 条主线路和 11 个独立公网 IP。MODIS 每公网 IP 基础上限为 3，下载器传输上限为 33，校验并发为 10。
- 在 **数据访问 → 下载出口** 查看逐条 VWAN、代理按源及按协议的活动连接、实时速率和平滑速率。展开 **VWAN → 代理路径** 可查各 WAN 与协议组合的健康状态和新连接分配份额。控制面私有 Socket 的 `GET /v1/egress/status` 提供按源和线路的统计。每条活动连接按实际出口计一次，平滑速率采用约 30 秒的指数平滑。
- iStoreOS 的 `east-hpc-proxy` 将 VLESS、Hysteria2 分别绑定 `vwan1` 至 `vwan9`。`east-hpc-metrics --adaptive` 每 15 秒根据健康和近期吞吐调整 6 个轮询份额，原代理组负责兜底。该服务从路由器本地 Mihomo 控制 Socket 采集代理入口的全部流量，经独立 SSH 转发向 `satellited` 提供只读汇总。c2ln6 上的 `SATELLITE_PROXY_EGRESS_METRICS_SOCKET` 指向节点本地 `east-hpc-metrics.sock`。代理连接数可能包含复用连接，和下载任务数口径不同。

## 安全与领域不变量

- **集中式凭据归属**（见 `docs/central-access.md`）：所有上游账号、Token、CDS 密钥、代理密码与 AES-GCM Vault 仅由 `satellited` 持有；下载器与 CLI 不接触明文上游凭据。严禁在日志、命令参数、文档或提交中输出、复制或写入真实凭据、数据库密码、API Key 或生产出口公网 IP（仓库示例统一使用 RFC 5737 文档地址）。
- **观测时间与归档一致性**：可用性日历、筛选与下载订单均以经核验的文件观测时间为准，不以文件落盘时间替代；订单仅在文件经校验并原子晋升至正式归档后才视为完成。
