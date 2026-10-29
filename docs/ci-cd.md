# Module CI/CD

The active repository is `nuist-fy-lab/satellite-data`. The legacy
`nuist-fy-lab/satellite-data-downloader` repository is archived.

## Selection / 模块触发

Every PR and push to `main` runs a small selection job and the final `test`
check. Expensive jobs run only when selected. Go modules are selected using the
transitive local import graph, including tests and platform-specific files.
Shared packages automatically select all consumers; newly added standalone
libraries also receive tests. Non-Go package resources, including embedded SQL,
JSON profiles and Web assets, invalidate their consumers.

每个 PR 和 main 推送均运行轻量选路与最终 `test` 检查，按需运行耗时任务。
共享包按传递依赖触发所有使用方，避免只根据目录名称漏掉跨模块影响。

| Example change / 改动 | Go tests, race checks and builds / Go 检查与构建 | Other checks / 其他检查 |
| --- | --- | --- |
| `cmd/satellite/` | CLI | — |
| `cmd/satellited/` | Control plane / 控制面 | PostgreSQL migration and state integration |
| `cmd/satellite-data-downloader/` | Downloader / 下载器 | — |
| `cmd/satellite-checksum-sidecar/` | Sidecar | — |
| `internal/state/` | Control plane | PostgreSQL |
| `internal/dataplane/transfer/` | Downloader + Sidecar | — |
| `internal/dataplane/model/` | Control plane + Downloader + Sidecar | PostgreSQL (state consumes this contract) |
| `web/static/` | Control plane (embedded assets) | JavaScript, styles, HTML, desktop/mobile Firefox |
| `web/*.test.mjs`, `web/visual-check.mjs` | — | Web checks only |
| `deploy/systemd/` | — | Deployment tests, host guards, deployment archive |
| Migration scripts / 数据库迁移脚本 | — | PostgreSQL + deployment checks and archive |
| `README*`, `docs/` | — | Selection and final status only |
| `go.mod`, `go.sum`, CI infrastructure, unknown root inputs | All / 全量 | All / 全量 |

Related modules test their command and transitive dependency packages; race
checks retain the existing state, dispatch, HTTP and dataplane coverage. Tests
can introduce additional consumers, so the table describes the current graph.
The workflow summary lists the actual selection for each run. Configure branch
protection against the always-running `test` check.

## Releases / 发布

PRs compare their head against the merge base with the target branch. Pushes
compare against the **last published `ci-latest` target**, so failed, superseded
or skipped runs cannot hide unpublished changes. A missing or unrelated base
triggers a complete bootstrap build. Documentation-only pushes skip publishing
when there are no outstanding runtime changes.

main 推送以“上一次成功发布”为基线累计差异。即使前一次运行失败或因新提交而
跳过发布，后续推送也会补上所有尚未发布的模块。

After every selected check passes, same-repository PRs publish only rebuilt
assets to `ci-pr-<number>-<sha12>`. These prereleases are **partial bundles**.
Fork PRs run checks and retain short-lived artifacts without publishing releases.

On `main`, the publisher downloads and checks the existing bundle's GitHub
SHA-256 digests, then combines rebuilt artifacts with verified unchanged assets.
It independently checks the accumulated diff before allowing reuse. `ci-latest`
contains:

- `satellite`, `satellited`, `satellite-data-downloader`, `satellite-checksum-sidecar`;
- `deployment.tar.gz`, containing `deploy/systemd/` and the maintenance Python/MJS tools;
- `build-manifest.json`, with the bundle target and actual source SHA/digest per current asset;
- `SHA256SUMS`, covering every other asset, plus up to two prior binary bundle slots;
- layout screenshots when the Web checks ran.

An inherited binary's `source_sha` can be older than the bundle target. History
suffixes identify earlier **bundle targets**. A docs-only commit may be newer than
`ci-latest`; it does not need a new binary release. The first modular release
rebuilds everything and upgrades the old binary-only bundle automatically.

Publication is serialized. Stale runs skip publishing; failed creation or
read-back verification attempts to restore the previous release. Every new
release is downloaded again to check its target, asset digests, manifest,
checksums and unchanged inputs. Release publication does not install artifacts
or restart production services.

发布完成后会重新下载验证；若创建或读回校验失败，尝试恢复上一套发布。
此流水线发布可部署产物，不会自动重启生产服务。

## Operations / 操作

Run **CI → Run workflow → full: true** for a complete rebuild. Run
**Release verification** to verify the published rolling bundle; optionally
provide its expected SHA. An empty expected SHA accepts the published target,
allowing newer documentation commits on `main`.

Local checks for the CI implementation require only Python 3.9+ and Bash:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s deploy/ci/tests -v
bash -n deploy/publish-ci-release.sh
python3 deploy/ci/plan.py --base <published-or-merge-base-sha> --head HEAD
```

Builds and service integration tests run in GitHub Actions. Production nodes
consume the verified cloud artifacts; do not rebuild on `c2ln1`.
