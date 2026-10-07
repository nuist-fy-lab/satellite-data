# Satellite Data（卫星数据平台）

[English](README.md) | **简体中文**

面向 `nuist-fy-lab` 的统一科学数据发现、获取、归档索引与运行监控平台。

`satellite-data` 为不同卫星与再分析数据源提供统一控制面，同时明确分离
凭据管理、数据发现、文件传输和归档职责。

## 核心能力

- **统一数据目录**：覆盖 GOES、Himawari、MODIS、VIIRS、MTG、EarthCARE、
  CALIPSO、CloudSat，以及 ERA5 / ERA5-Land。
- **持久化下载订单**：客户端只需提交一次，中心服务负责发现、分段、重试和完成状态追踪。
- **数据源隔离**：不同提供方拥有独立的发现、传输和校验资源，单个上游变慢不会占满其他数据源的槽位。
- **断点续传与可靠归档**：文件传输、完整性校验和归档提升由独立的
  `satellite-data-downloader`（卫星数据下载器）负责。
- **共享归档识别**：能够索引现有 EarthCARE、ERA5、Himawari 和 GOES
  历史数据，而不会静默移动或覆盖其他用户的数据。
- **实时运行界面**：Web UI（网页界面）提供目录、日历、归档覆盖率、下载状态、
  提供方实时速度和队列监控。
- **集中式 Secret（敏感凭据）管理**：账号、Token（令牌）和代理认证保留在中心服务，
  CLI（命令行客户端）既不是凭据管理器，也不是下载进程。

## 架构

```text
                        Web 门户 / CLI
                             │
                             ▼
                    ┌─────────────────┐
                    │   satellited    │
                    │─────────────────│
                    │ 登录与 API      │
                    │ 官方目录        │
                    │ 数据发现        │
                    │ 订单调度        │
                    │ 归档索引        │
                    │ 运行监控        │
                    └────────┬────────┘
                             │ 私有 Unix Socket（套接字）
                             ▼
                 ┌───────────────────────────┐
                 │ satellite-data-downloader │
                 │───────────────────────────│
                 │ 断点续传                  │
                 │ 完整性校验                │
                 │ 暂存                      │
                 │ 归档提升                  │
                 └─────────────┬─────────────┘
                               │
                               ▼
                           共享归档
```

### 组件职责

| 组件 | 负责 | 不负责 |
| --- | --- | --- |
| `satellited` | API（应用程序接口）、门户、官方目录、数据发现、持久订单、凭据、代理路由、归档索引、监控 | 文件传输 Worker（工作进程） |
| `satellite-data-downloader` | 传输队列、断点续传、校验、暂存、归档提升 | 提供方凭据、用户登录 |
| `satellite` CLI | 控制请求、目录/查询/状态输出、订单提交 | 提供方登录、Secret、本地下载 |
| Web UI | 目录、日历、访问管理、下载和监控 | 直接访问上游提供方 |

中心服务与下载器通过本地 Unix Socket（Unix 套接字）通信。
下载发现采用分块流水线：每发现一批资产就立即提交给下载器，而不是等待整个请求全部发现完成。

不同数据源的目录/搜索调用独立限速，并根据连续错误动态调整。
持续失败采用带随机抖动的有界退避，因此单个异常上游不会拖住整个系统。

## 支持的数据源

| 数据源 | 官方发现路径 | 当前状态 |
| --- | --- | --- |
| GOES-16/17/18/19 | NOAA NODD 对象层级；优先 Google Cloud Storage（谷歌云存储） | 已实现发现与传输 |
| Himawari-8/9 | NOAA NODD 匿名 S3（对象存储） | 已实现发现与传输 |
| MODIS Terra/Aqua | LAADS API v2（接口）目录与 `content/details` | 已实现发现、认证与断点续传 |
| VIIRS Suomi-NPP / NOAA-20 / NOAA-21 | NASA CMR + LAADS Earthdata Cloud | 已实现发现与传输 |
| MTG / Meteosat-12 FCI | EUMETSAT Data Store OpenSearch + OAuth2；低延迟路径支持 MQTT / EUMETCast | 已实现发现；认证传输继续完善 |
| EarthCARE | ESA MAAP STAC + OIDC（开放身份连接）保护的科学数据资产 | 已实现发现 |
| CALIPSO | NASA CMR + Earthdata Login / ASDC | 已实现发现 |
| CloudSat | CIRA DPC THREDDS + 认证 HTTPServer；子集可使用 OPeNDAP | 已实现发现 |
| ERA5 / ERA5-Land | CDS STAC 目录 + 异步子集请求 | 已实现目录、请求 UI/API 和持久订单；归档集成继续完善 |

如果存在官方机器可读接口，项目会优先使用官方接口，而不是退役 FTP（文件传输协议）、
HTML（网页）抓取或旧版 NASA API。

> **ERA5 不是卫星观测产品。** 它被纳入本项目，是因为卫星研究工作流通常需要统一获取、
> 管理和归档再分析资料。

## 典型工作流

1. `satellited` 刷新或查询官方提供方目录。
2. 用户通过 Web 门户或 CLI 提交持久化下载订单。
3. 中心服务切分订单，执行数据源特定的发现逻辑并持久记录进度。
4. 已发现资产按批次提交到 `satellite-data-downloader`。
5. 下载器执行断点续传、文件校验和归档提升。
6. 下载器把归档完成事件返回中心服务。
7. 中心更新覆盖率和日历状态，并通过 SSE（服务器发送事件）向前端增量推送变化。

长订单会在分段之间主动让出调度机会，使同一提供方上的其他订单也能继续推进。

## CLI 快速使用

在门户登录后创建 Satellite CLI API Key（命令行 API 密钥），然后配置客户端：

```bash
satellite config set
```

客户端仅保存中心地址和 CLI Key：

```text
~/.config/satellite/config.json
```

配置文件权限为 `0600`。

### 浏览产品

```bash
satellite source list
satellite product list --search temperature --json
satellite product list --source earthcare --level L2
satellite product show --product goes18:ABI-L1b-RadF
satellite catalog list --source goes18
```

### 查询已发布颗粒

```bash
satellite query list \
  --source goes18 \
  --collection goes18:ABI-L1b-RadF \
  --start 2026-09-22T09:00:00Z \
  --end 2026-09-22T10:00:00Z

satellite query list \
  --product goes18:ABI-L1b-RadF \
  --day 2024-01-01 \
  --band 6
```

交互查询会限制时间范围。大规模历史数据获取应提交持久下载订单，而不是构造巨大的同步查询。

### 提交持久下载订单

```bash
satellite download submit \
  --source goes18 \
  --collection goes18:ABI-L1b-RadF \
  --start 2023-06-01 \
  --end 2023-06-30 \
  --band 6 \
  --hourly

satellite download status --id 1 --json
```

`download submit` 使用包含首尾日期的 UTC（协调世界时）范围，并支持重复
`--time HH:MM` 或 `--hourly`。

客户端提交成功后即可断开。只有中心已经观察到文件完成校验并正式进入归档，
订单才算真正完成；仅完成上游发现或生成任务并不等于归档完成。

## ERA5 获取

ERA5 / ERA5-Land 属于 CDS（哥白尼气候数据存储）的按需生成数据集，
因此本项目不会把它伪装成类似 GOES 或 MODIS 的静态已发布颗粒。

- 目录时间范围表示数据集元数据范围，不代表每小时都一定存在可直接下载的文件。
- 普通颗粒查询和日历索引不会隐式提交 CDS 作业。
- 自动获取和归档优先使用统一持久下载订单。
- 管理员仍可通过底层接口显式操作 CDS 作业。

### 持久 ERA5 订单

```bash
satellite download submit \
  --product era5:reanalysis-era5-single-levels \
  --start 2024-01-01 \
  --end 2024-01-02 \
  --variable 2m_temperature \
  --area 32.25,118.5,32,118.75
```

中心服务会自动生成日期参数：小时产品按 UTC 日拆分，月产品按完整月份拆分，
持久化 CDS 提交检查点，并把成功结果交给下载器。

如果一次 CDS POST（提交请求）的结果不确定，系统**不会自动重复提交**。
运维人员必须先核对上游任务状态，避免静默创建重复作业。

### 显式 CDS 作业操作

```bash
satellite era5 schema --collection era5:reanalysis-era5-single-levels

satellite era5 submit \
  --collection era5:reanalysis-era5-single-levels \
  --variable 2m_temperature \
  --product-type reanalysis \
  --year 2024 --month 01 --day 01 --time 00:00 \
  --area 32.25,-118.75,32,-118.5 \
  --data-format netcdf \
  --download-format unarchived

satellite era5 status --id JOB_ID
satellite era5 result --id JOB_ID
```

这些底层命令要求管理员拥有的 CLI Key，而且不会创建中心归档订单。

## HTTP API

生产门户默认需要登录。`/login` 是 Web 入口，同一 HttpOnly（仅 HTTP 可访问）
Session（会话）根据角色授权目录、日历、监控和管理页面。

普通运行 API 支持门户 Session 或网页创建的 Satellite CLI API Key。

```text
GET /healthz
GET /api/v1/sources
GET /api/v1/catalog?source=goes18
GET /api/v1/search?source=goes18&collection=goes18:ABI-L1b-RadF&start=...&end=...
GET /api/v1/monitor/downloads?window=1h

GET  /api/v1/era5/schema?collection=era5:reanalysis-era5-single-levels
POST /api/v1/era5/requests
GET  /api/v1/era5/requests/{jobID}
GET  /api/v1/era5/requests/{jobID}/result
```

Satellite CLI Key 使用 `satellite_v1_` 命名空间，与 NSMC Key 相互独立。
完整 Key 只在创建时返回一次，中心仅保存摘要，并立即拒绝已撤销 Key 或已禁用用户的 Key。

## 权限与敏感信息边界

所有提供方账号、凭据、代理路由与代理认证都归中心服务所有。
Secret 在中心加密保存，并通过受保护的管理界面进行只写管理。

下载器只能获得访问已批准资产所需的最小中心授权：

- 不打开中心 Vault（凭据保险库）；
- 不读取主密钥；
- 不向外暴露提供方凭据。

CLI 同样遵守严格边界：

- 不把提供方密码或 Token 当作普通命令参数；
- 不保存 Vault 主密钥；
- 不直接执行提供方文件下载；
- 携带中心 Bearer（持有者）授权时不跟随重定向；
- CLI Key 不能创建或管理提供方凭据。

完整权限、迁移与回滚说明见
[`docs/central-access.md`](docs/central-access.md)。

## 归档索引与校验

中心既索引新下载文件，也索引已有共享历史数据。
“本地存在”与“已经得到官方身份验证”是两个不同状态。

### EarthCARE

- c2ln1 共享归档根目录：`/misc/nuist_data1/public_sharing/EarthCare`。
- 支持 `<product>/<YYYYMMDD>/<filename>` 与
  `<product>/<YYYY>/<MM>/<filename>` 两类历史布局。
- 历史文件只有在能够匹配精确官方元数据并完成哈希后才会变为 `verified`（已验证）。
- HDF5 / NetCDF 文件头有效只代表格式正常，**不能证明官方身份**。
- 无权限读取的目录会明确报告，而不是当成空目录。

历史修复与证据规则见
[`docs/earthcare-historical-verification.md`](docs/earthcare-historical-verification.md)。

### ERA5

- 共享根目录：`/misc/nuist_data1/public_sharing/ERA5`。
- CDS 结果归档到
  `CDS/<dataset>/<YYYY>/<MM>/<DD>/<job-id>/<result-name>`。
- 无法追溯到可靠 CDS Job（作业）/Result（结果）身份的历史文件不会被自动标记为已验证。

### Himawari

`SATELLITED_HIMAWARI_ARCHIVE` 可启用已有 Himawari 共享归档索引。
历史 `.bz2` 文件只进行格式级检查，除非能够建立精确提供方身份。

### GOES

GOES 归档扫描从 NOAA 文件名推导产品与日期，并可以自动补排缺失的官方目录日期。
每日指纹在服务重启后仍保留。只下载部分波段时会显示为部分覆盖，不会伪装为整日完整。

所有新归档写入都拒绝静默覆盖路径相同但内容不同的现有文件。

## 日历与监控

Web UI 通过增量索引更新维持日历和归档状态：

- 固定时间间隔产品保留真实缺口，不把缺测伪装成零值；
- 事件型产品使用真实时间戳，不强行吸附到固定时次；
- 每日波段视图区分完整、部分、缺失和未验证状态；
- 年历支持闰年，小屏幕下允许横向滚动；
- 下载输入和列表默认 UTC，传输图表和采样时间可使用 BJT（北京时间，UTC+8）。

中心每 15 秒采样下载器遥测，保存 8 天历史。
监控提供按数据源隔离的实时速度、活动传输 Worker、自适应并发上限与队列深度，
支持 `realtime`、`1h`、`3h`、`6h`、`12h`、`1d`、`2d`、`1w`。

缺失采样会保留为空档，不会被错误改写成“速度为 0”。

## 状态与配置

中心应用状态和索引默认使用同一个 SQLite（嵌入式数据库）：

```text
~/.local/state/satellite-data/state.sqlite3
```

它与以下数据库相互独立：

- NSMC 的 `index.sqlite3`；
- 下载器数据库；
- 显式配置生产路径时的访问/凭据数据库。

重要生产配置包括：

```text
SATELLITE_ACCESS_DB
SATELLITE_ACCESS_MASTER_KEY
SATELLITE_CENTRAL_SOCKET
SATELLITED_STATE_DB
SATELLITED_EARTHCARE_ARCHIVE
SATELLITED_ERA5_ARCHIVE
SATELLITED_HIMAWARI_ARCHIVE
SATDOWN_CENTRAL_SOCKET
SATDOWN_EARTHCARE_ARCHIVE_ROOT
SATDOWN_ERA5_ARCHIVE_ROOT
```

Unix Socket 应放在节点本地运行时目录，不要放到 GPFS / NFS 等共享 HOME（主目录）文件系统。

## 仓库结构

```text
cmd/
  satellite/              轻量 CLI
  satellited/             API + 内嵌 Web 服务

internal/
  app/                    提供方装配
  authstore/              门户 / CLI Key 持久化
  central/                凭据、提供方认证、私有抓取网关
  downloadcoord/          持久下载协调
  downloadfilter/         产品 / 波段过滤规则
  downloadmon/            下载器遥测采样
  httpapi/                已认证 HTTP API + SSE
  indexer/                共享 / 本地归档索引
  linkmon/                提供方与链路可达性监控
  model/                  Source / Collection / Granule / Asset 数据模型
  objectstore/            匿名 GCS + S3 列表
  provider/               数据源适配器
  slotaxis/               固定时次 / 事件时间轴策略
  state/                  SQLite 状态与调度
  transfer/               中心侧 HTTP 传输辅助

web/                      内嵌、无外部运行时依赖的 Web UI
deploy/                   部署单元与归档维护工具
docs/                     专项设计与运维文档
```

## 文档

| 文档 | 用途 |
| --- | --- |
| [`docs/central-access.md`](docs/central-access.md) | 凭据归属、中心/下载器边界、迁移与回滚 |
| [`docs/earthcare-historical-verification.md`](docs/earthcare-historical-verification.md) | EarthCARE 历史归档验证与修复证据 |
| [`docs/product-availability-2026-09-27.md`](docs/product-availability-2026-09-27.md) | 提供方 / 产品可用性快照 |
| [`docs/product-catalog-2026-09-27.csv`](docs/product-catalog-2026-09-27.csv) | 产品目录快照 |
| [`docs/satellite-timeline.md`](docs/satellite-timeline.md) | UI 使用的卫星任务时间轴语义 |
| [`docs/era5-network-2026-09-27.md`](docs/era5-network-2026-09-27.md) | c2ln1 上 ERA5 / CDS 网络快照 |

## 开发与验证原则

项目构建和测试只通过 GitHub Actions（云端自动化工作流）执行。
开发机和生产数据节点只用于源码编辑、配置、部署和部署后的运行状态检查，
不作为本地构建或测试环境。

Linux（操作系统）发布产物使用 `CGO_ENABLED=0` 和 `GOAMD64=v1`，
以避免二进制继承 GitHub Runner（云端执行器）的 glibc（GNU C 库）版本要求，
并提高在较旧 HPC（高性能计算）用户空间上的兼容性。

## 生产服务名

数据节点上的用户级服务为：

```text
satellited.service
satellite-data-downloader.service
```

检查实际单元名称：

```bash
systemctl --user list-unit-files '*satellite*'
```

不存在 `satellite-downloader.service`。
