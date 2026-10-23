# Satellite Data Downloader（卫星数据下载器）

[English](README.md) | **简体中文**

[`nuist-fy-lab/satellite-data`](https://github.com/nuist-fy-lab/satellite-data) 配套的持久化、按数据源隔离的传输、完整性校验与归档提升服务。

## 项目概览

`satellite-data-downloader` 是与 `satellite-data` 中心服务协同工作的数据面守护进程。`satellite-data` 负责官方目录、数据发现、持久订单、凭据管控与用户接口；本服务则专注于将已批准的下载任务可靠地转化为经过校验的正式归档文件：

- 在传输开始前将任务持久化至 SQLite 队列，支持跨进程重启恢复；
- 为每个数据源维护独立的传输池与校验池，互不抢占并发槽位；
- 支持 HTTP 断点续传（`Range` / `If-Range` / `ETag`），并主动回收异常慢连接而不丢失已下载分片；
- 将网络下载与完整性校验解耦为两个独立流水线阶段；
- 在单次文件读取中同时完成本地 SHA-256 与上游官方校验码（MD5、CRC32C、SHA-256 等）核对，通过后原子提升至正式归档；
- 拒绝静默覆盖目标路径下内容不同的已有文件，并通过持久化发件箱向中心可靠回报归档完成事件。

```text
 satellite-data（目录发现 + 订单 + 凭据管控）
          │
          │ 私有 Unix Socket
          ▼
 ┌─────────────────────────────┐
 │ satellite-data-downloader   │
 │                             │
 │ 持久化 SQLite 任务队列      │
 │          │                  │
 │          ▼                  │
 │ 按源隔离的传输 Worker 池    │
 │          │                  │
 │          ▼                  │
 │ 按源隔离的校验 Worker 池    │
 │          │                  │
 │          ▼                  │
 │ 原子归档提升                │
 └──────────┬──────────────────┘
            │
            ├──► 正式科研归档文件
            └──► 持久化完成事件 ──► satellite-data
```

## 数据源隔离与自适应流控

每个数据源拥有独立的传输池、校验池、自适应并发与退避计时器，单一上游源拥塞或故障不会拖慢其他数据源。

| 数据源 | 初始 / 最大传输并发 | 校验并发 | 上游官方文件校验方式 | 中心路由标识 |
| :--- | :---: | :---: | :--- | :--- |
| **GOES** | 4 / 8 | 2 | MD5 + CRC32C（GCS 对象元数据） | 匿名 / 中心路由 |
| **Himawari-8** | 1 / 1 | 1 | 本地 SHA-256 | 匿名 / 中心路由 |
| **Himawari-9** | 1 / 1 | 1 | S3 完整对象校验码 | 匿名 / 中心路由 |
| **MODIS** | 4 / 8 | 2 | MD5（NASA CMR UMM-G） | `laads` |
| **VIIRS** | 4 / 4 | 2 | MD5（NASA CMR UMM-G） | `laads` |
| **MTG** | 2 / 2 | 1 | MD5（EUMETSAT Browse） | `eumetsat` |
| **EarthCARE** | 2 / 2 | 1 | 本地 SHA-256 | `earthcare` |
| **CALIPSO** | 3 / 3 | 2 | MD5（NASA CMR UMM-G） | `earthdata` |
| **CloudSat** | 2 / 2 | 2 | 本地 SHA-256 | `cloudsat` |
| **ERA5** | 2 / 2 | 2 | 本地 SHA-256 | `cds` |

### 拥塞控制与重试机制

- **自适应请求节奏**：各数据源初始启动间隔为 200 ms。出现超时或 HTTP `408`/`429`/`5xx` 时自动下调并发并将间隔拉长至最多 5 秒；持续成功后逐步恢复并发并将间隔缩短至最低 100 ms。
- **随机抖动指数退避**：临时错误按 5 秒基准、最高 10 分钟上限进行等幅随机抖动退避重试，并严格尊重上游 `Retry-After` 响应头（最长 1 小时）。HTTP `404`/`410` 最多确认 6 次下载尝试后标记为永久失败。
- **慢连接回收**：在 30 秒启动宽限期后按移动窗口评估吞吐率，主动断开持续异常低速的连接，同时保留已验证的 `.part` 暂存文件与 `ETag` 以便续传。

## 访问与凭据模型

所有上游凭据、账号与代理配置均由 `satellite-data` 中心统一管理。下载器自身不存储任何密钥或凭据环境变量，每次传输前通过 `SATDOWN_CENTRAL_SOCKET` 向中心请求授权路由。当中心暂无可用路由时，任务自动进入 `waiting_auth` 状态，待路由恢复后继续执行。

## 任务生命周期与归档安全

```text
queued（排队）──► downloading（下载中）──► verifying（校验中）──► complete（完成）──► 归档事件发件箱
                       │                        │
                       ├─► retry（退避重试）     ├─► redownload（校验不匹配重下）
                       └─► waiting_auth         └─► verification retry（路径冲突重试）
```

1. **暂存与单遍哈希校验**：下载文件首先写入 `SATDOWN_DOWNLOAD_ROOT`。传输完成后立即释放下载槽位，由独立校验 Worker 在单次文件扫描中计算本地 SHA-256 与官方摘要。
2. **原子归档与本地复用**：校验通过后原子移动至目标归档目录。若本地已存在通过中心身份核验与 SHA-256 校验的同名归档文件，可直接跨订单复用而无需重复下载。
3. **历史归档旁路复核（Sidecar）**：可选的 `satellite-checksum-sidecar` 守护进程以只读和低 I/O 优先级（限速 `8 MiB/s`）后台巡检历史归档并补齐官方校验记录，不影响实时下载队列。

## 部署与配置

完整配置模板见 [`deploy/systemd/satellite-data-downloader.env.example`](deploy/systemd/satellite-data-downloader.env.example)。请将实时 Unix Socket 置于节点本地运行时目录（如 `$XDG_RUNTIME_DIR`），避免放在 NFS/GPFS 等共享文件系统上。

### 核心环境变量

| 变量名 | 用途 | 默认值 / 示例 |
| :--- | :--- | :--- |
| `SATDOWN_SOCKET` | 下载器任务 API Unix Socket | `$XDG_RUNTIME_DIR/satellite-data-downloader/downloader.sock` |
| `SATDOWN_DB` | 持久化任务队列 SQLite 数据库 | `~/.local/state/satellite-data-downloader/downloader.sqlite3` |
| `SATDOWN_DOWNLOAD_ROOT` | 下载暂存目录 | `~/.local/state/satellite-data-downloader/delivery` |
| `SATDOWN_ARCHIVE_ROOT` | 通用卫星数据归档根目录 | `/data/archive/satellite` |
| `SATDOWN_<PROVIDER>_ARCHIVE_ROOT` | 指定数据源的独立归档根目录 | `/data/archive/EarthCare`、`/data/archive/ERA5` |
| `SATDOWN_CENTRAL_SOCKET` | 中心授权与路由 Socket | `$XDG_RUNTIME_DIR/satellite-data/central.sock` |
| `SATDOWN_ARCHIVE_EVENT_SOCKET` | 归档完成事件回报 Socket | `$XDG_RUNTIME_DIR/satellite-data/archive-events.sock` |

各数据源可通过 `SATDOWN_<PROVIDER>_{ENABLED,INITIAL_TRANSFER_CONCURRENCY,TRANSFER_CONCURRENCY,VERIFY_CONCURRENCY,REQUEST_TIMEOUT}` 独立调整并发与超时。

### systemd 服务管理

```bash
systemctl --user status satellite-data-downloader.service
journalctl --user -u satellite-data-downloader.service -n 100 --no-pager
```

## 仓库结构

```text
cmd/
  satellite-data-downloader/   下载器主守护进程
  satellite-checksum-sidecar/  低开销历史归档校验巡检进程
internal/
  access/                      中心路由与认证客户端
  archiveevent/                持久化归档完成事件发件箱
  config/                      非敏感运行时配置加载
  model/                       任务与状态领域模型
  pipeline/                    数据源工作池、流控与退避重试
  server/                      私有 Unix Socket 任务接口
  store/                       SQLite 队列持久化与故障恢复
  transfer/                    断点续传 HTTP 客户端、校验与归档提升
deploy/systemd/                用户级 systemd 单元与环境变量模板
```
