# Satellite Data（多源卫星科学数据中心）

[English](README.md) | **简体中文**

[`nuist-fy-lab`](https://github.com/nuist-fy-lab) 统一的多源卫星与再分析科学数据发现、订单调度、归档索引与同步服务。

## 项目概览

`satellite-data` 提供统一的 Web 门户、HTTP API 与轻量级命令行工具（`satellite`），用于多源地球观测卫星及再分析资料的目录检索、持久订单提交与本地归档可用性追踪。

系统采用统一的 Monorepo 仓库管理，并在运行时将**目录发现与订单管理**和**底层数据传输**解耦为协同进程：

- **`satellited`**：负责维护官方产品目录、年度/日内可用性日历、本地归档索引（支持 SQLite 与无损 PostgreSQL）、数据源凭据与代理路由，以及持久化下载订单调度。
- **`satellite-data-downloader`**：通过本机私有 Unix Socket（`$XDG_RUNTIME_DIR`）接收经中心批准的资产下载任务，按数据源隔离执行断点续传、完整性校验、暂存与原子归档。
- **`satellite-checksum-sidecar`**：以低 I/O 优先级独立巡检历史已归档文件的官方校验和，不干扰实时下载队列。
- **`satellite`（轻量 CLI）**：仅通过 API Key 提交任务或查询状态，不在客户端本地保存上游数据源凭据，也不直接承担文件下载。

```text
浏览器 / satellite CLI
          │
          │ HTTPS（网页会话 / CLI API Key）
          ▼
     satellited ◄───────────► SQLite（状态与归档索引）
          │
          │ 本机 Unix Socket（任务下发 / 路由授权 / 完成事件）
          ▼
satellite-data-downloader ──► 经校验的正式科研归档
```

## 支持的数据源

所有数据源均对接官方云存储对象接口、STAC/CMR 目录或标准认证 API，不依赖已废弃的旧版 FTP 或网页爬虫：

| 数据源 | 覆盖产品 / 载荷 | 目录发现与传输机制 |
| :--- | :--- | :--- |
| **GOES-16 / 17 / 18 / 19** | ABI L1b / L2 | NOAA NODD 对象存储（优先使用 Google Cloud Storage） |
| **Himawari-8 / 9** | AHI 全圆盘 / 区域 | NOAA NODD 匿名 S3 |
| **MODIS (Terra / Aqua)** | Collection 6.1 / 7 L1B & L2 | LAADS API v2 目录 + Token 认证断点 HTTPS |
| **VIIRS (SNPP / NOAA-20 / 21)** | L1B & L2 科学产品 | NASA CMR / LAADS + Earthdata Cloud HTTPS |
| **MTG (Meteosat-12)** | FCI | EUMETSAT Data Store OpenSearch + OAuth2 |
| **EarthCARE** | L1 / L2 轨道产品 | ESA MAAP STAC + OIDC 认证 HTTPS |
| **CALIPSO** | CALIOP L1 / L2 | NASA CMR + Earthdata Login ASDC HTTPS |
| **CloudSat** | 2B-GEOPROF / CPR | CIRA DPC THREDDS 目录 + 认证 HTTPServer |
| **ERA5 / ERA5-Land** | 逐时 / 月平均单层与等压面 | Copernicus CDS STAC 目录 + 异步子集定制提取 |

## 核心特性

- **下载任务与三层去重**：一次提交最多 32 个产品，各产品可分别设置观测区间、日历周期、扫描时次和波段，由中心自动调度。系统对同一用户重复提交、跨任务同文件请求和已核验归档文件分别去重。见[产品时间选择](docs/download-tasks.md)。
- **自动可用性日历与增量归档索引**：支持静止卫星固定时次网格与极轨卫星过境事件时间轴的年度/日内通道级覆盖展示。后台索引器按数据源自适应限速，自动同步本地归档变更与上游官方目录。
- **集中式凭据与路由管控**：所有上游账号、Token、CDS 密钥及代理策略均在 `satellited` 中心加密存储，由管理员统一维护，不对普通用户或下载器暴露明文凭据。
- **统一的 ERA5 按需定制与归档**：既支持通过统一订单接口自动按天/按月拆分、轮询、校验并归档 ERA5 数据，也支持管理员直接调用底层 CDS 任务接口。

## CLI 使用指南

### 1. 首次配置

登录 Web 门户并在 CLI 页面创建 API Key（前缀为 `satellite_v1_`），随后运行：

```bash
satellite config set
```

配置文件默认保存在 `~/.config/satellite/config.json`（权限 `0600`），仅记录中心地址与 CLI API Key。自动化环境中也可通过 `SATELLITE_SERVER`、`SATELLITE_API_KEY` 或 `SATELLITE_CONFIG` 环境变量指定。

### 2. 浏览目录与查询可用性

```bash
# 查看支持的数据源并搜索产品
satellite source list
satellite product list --search temperature --json
satellite product list --source earthcare --level L2
satellite product show --product goes18:ABI-L1b-RadF

# 查询单日或指定时间范围（最长 31 天）的官方数据颗粒
satellite query list --product goes18:ABI-L1b-RadF --day 2024-01-01 --band 6
satellite query list \
  --source goes18 \
  --collection goes18:ABI-L1b-RadF \
  --start 2026-09-22T09:00:00Z \
  --end 2026-09-22T10:00:00Z
```

### 3. 提交与查看下载订单

订单提交后由服务端持久化调度执行，CLI 可立即断开连接：

```bash
# 按通道和整点时次提交单产品下载
satellite download submit \
  --product goes18:ABI-L1b-RadF \
  --start 2023-06-01 --end 2023-06-30 \
  --band 6 --hourly

# 跨数据源多产品下载任务
satellite download submit \
  --product modis:C1443536017-LAADS \
  --product goes18:ABI-L1b-RadF \
  --start 2023-05-03 --end 2023-05-10

# 统一 ERA5 子集下载订单（中心自动按日/月拆分并归档）
satellite download submit \
  --product era5:reanalysis-era5-single-levels \
  --start 2024-01-01 --end 2024-01-02 \
  --variable 2m_temperature \
  --area 32.25,118.5,32,118.75

# 查看订单状态
satellite download status --id 1 --json
satellite download batch-status --id 1
```

> **说明**：管理员还可使用 `satellite era5 schema|submit|status|result` 直接操作底层 CDS 原始任务。

## HTTP API 概览

所有页面与 API 均需网页登录会话（`GET /login`）或有效的 `Bearer` CLI API Key：

| 接口 | 说明 |
| :--- | :--- |
| `GET /healthz` | 服务健康检查 |
| `GET /api/v1/sources` | 获取已配置的数据源列表 |
| `GET /api/v1/catalog?source=<id>` | 浏览指定数据源的官方产品目录 |
| `GET /api/v1/search?...` | 交互式数据检索（最长 7 天窗口） |
| `POST /api/v1/downloads` | 创建单产品或多产品持久化下载订单 |
| `GET /api/v1/archive/index-status` | 查询本地归档扫描状态、年份覆盖与索引队列 |
| `GET /api/v1/monitor/downloads?window=1h` | 获取按数据源隔离的吞吐率与活跃并发监控（`realtime` 至 `1w`） |
| `GET /api/v1/era5/schema` | 获取 ERA5 数据集的官方 CDS 参数结构 |

## 仓库结构

```text
cmd/
  satellite/                   轻量级用户 CLI
  satellited/                  中心 API 与内嵌 Web 守护进程
  satellite-data-downloader/   按数据源隔离的断点续传下载守护进程
  satellite-checksum-sidecar/  低优先级历史归档官方校验和审计进程
internal/
  app/                         服务编排与数据源装配
  archiveevent/                归档完成事件的统一本机 IPC 契约
  central/                     加密凭据库与本机授权网关
  dataplane/                   下载队列、IPC 服务、流式校验传输与并发工作池
  downloadcoord/               持久化订单调度、三层去重与 ERA5 编排
  httpapi/                     REST 与 SSE 接口实现
  indexer/                     本地归档扫描与官方可用性日历索引器
  model/                       Source / Collection / Granule / Asset 领域模型
  objectstore/                 匿名 GCS 与 S3 列表客户端
  provider/                    各数据源适配器（calipso, cloudsat, earthcare, era5, eumetsat, goes, himawari, laads）
  state/                       SQLite 与无损 PostgreSQL 持久化状态层
web/                           内嵌零外部依赖前端界面
deploy/                        用户态 systemd 单元、c2ln1 主机绑定守卫与运维脚本
docs/                          架构与运维说明文档
```

## 部署与配置

项目通过 GitHub Actions 按改动涉及的模块测试并构建静态二进制（`CGO_ENABLED=0`，`GOAMD64=v1`）。`ci-latest` 始终包含全部四个二进制及 `deployment.tar.gz`；未受影响的产物经过摘要与依赖检查后复用，实际构建来源记录在 `build-manifest.json`。模块触发规则、发布校验与手动全量构建见 [模块化 CI/CD](docs/ci-cd.md)。旧 [satellite-data-downloader 仓库](https://github.com/nuist-fy-lab/satellite-data-downloader) 已归档，后续开发与发布统一使用本单仓。

在共享 GPFS `$HOME` 的集群环境中，生产服务通过 `systemd --user` 运行在 `c2ln1` 节点，并同时由 `ConditionHost=c2ln1`（`deploy/systemd/c2ln1/*.conf`）与进程级 `SATELLITE_REQUIRE_HOST=c2ln1` 双重限制仅允许在 `c2ln1` 启动。

`satellited` 核心环境变量：

| 变量名 | 用途 | 默认值 / 示例 |
| :--- | :--- | :--- |
| `SATELLITED_STATE_DB` | 中心状态与归档索引 SQLite 数据库 | `~/.local/state/satellite-data/state.sqlite3` |
| `SATELLITE_CENTRAL_SOCKET` | 与下载器通信的私有 Unix Socket | `$XDG_RUNTIME_DIR/satellite-data/central.sock` |
| `SATELLITED_ARCHIVE_ROOT` | 通用卫星数据归档根目录 | `/data/archive/satellite` |
| `SATELLITED_EARTHCARE_ARCHIVE` | EarthCARE 专用归档根目录 | `/data/archive/EarthCare` |
| `SATELLITED_ERA5_ARCHIVE` | ERA5 专用归档根目录 | `/data/archive/ERA5` |
| `SATELLITED_HIMAWARI_ARCHIVE` | Himawari 历史归档根目录 | `/data/archive/Himawari` |
