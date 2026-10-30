# 开源下载器与时间选择研究

核验日期：2026-10-31。仅查公开仓库、发布记录和官方文档；未调用产品接口、下单、安装或修改项目。维护程度以可见发布为证据，不保证持续维护或当前任意产品可用。参考2026-10-30目录清单，以下不是通用排名。

| 项目及适用性 | 开源与维护证据 | 可借鉴的逻辑与限制 |
|---|---|---|
| [earthaccess](https://github.com/earthaccess-dev/earthaccess)；NASA Earthdata 集合及 granule，适用于研究 MODIS、VIIRS、CALIPSO、CloudSat 中实际由 CMR 提供的集合，非所有版本和服务均已验证 | [MIT LICENSE](https://github.com/earthaccess-dev/earthaccess/blob/main/LICENSE.txt)；[v0.19.0](https://github.com/earthaccess-dev/earthaccess/releases) 2026-09-04，v0.17.0 2026-04-02 | [search_data 的 temporal](https://earthaccess.readthedocs.io/en/latest/user/explanation/search/) 是开始/结束，先集合、后文件搜索，下载另一步；显式 collection/version，列文件实际覆盖。v0.17 下载先临时文件，完成再定名，支持 force；不能据此声称支持断点续传或跨所有产品的重叠边界。 |
| [goes2go](https://github.com/blaylockbk/goes2go)；GOES AWS 下载，不是 Himawari 下载器 | [MIT](https://github.com/blaylockbk/goes2go/blob/main/LICENSE)；[2025.10.0](https://github.com/blaylockbk/goes2go/releases) 2025-10-17，约一年未有新发布，近期维护证据较弱 | 支持 latest、nearest（含容差）、range 和文件列表；[bands 仅适用独立通道产品，MCMIP 为多通道](https://goes2go.readthedocs.io/en/stable/reference_guide/index.html)。但 [data.py `_goes_file_df`](https://goes2go.readthedocs.io/en/stable/_modules/goes2go/data.html) 以 `start >= 请求开始 && end <= 请求结束` 筛完整包含，排除跨界文件；不能作为相交搜索范本。默认不覆盖仅凭存在并不足以证明文件完整或可续传。 |
| [EUMDAC](https://gitlab.eumetsat.int/eumetlab/data-services/eumdac/-/tree/public)；EUMETSAT Data Store / Data Tailor，MTG | 仓库 README 声明 MIT，含 LICENSE；[3.1.1](https://pypi.org/project/eumdac/) 2025-12-11，公开分支可见 3.1.0 更新，未验证 2026 提交 | [cli.py `search`](https://gitlab.eumetsat.int/eumetlab/data-services/eumdac/-/blob/958bb1ea0753fd3f3af50317fbda4a7073a95027/eumdac/cli.py) 先选 collection，再按 UTC 起止查询，显示总数并提示结果截断；[官方 MTG 教程](https://gitlab.eumetsat.int/eumetlab/data-services/eumdac_data_store/-/tree/master) 包含 LI 示例。不能推断所有 MTG 通道或分块规律。 |
| [EODAG](https://github.com/CS-SI/eodag)；多提供商 EO 检索与下载，可研究 ESA/EarthCARE 接入，但未核实本地每个 EarthCARE 集合存在可用映射 | 仓库声明 Apache-2.0；[发布记录](https://github.com/CS-SI/eodag/releases) 可见 v4.7.1 和后续发布；[镜像 v4.9.2](https://github.com/CS-SI/eodag/pkgs/container/eodag) 为近期发布 | 分集合列表、搜索、下载；[queryables](https://eodag.readthedocs.io/en/latest/api_user_guide.html) 与提供商插件适应不同约束，[搜索](https://eodag.readthedocs.io/en/latest/notebooks/api_user_guide/3_search.html) 支持起止。适合借鉴能力驱动界面；统一 API 不意味着各上游时间端点、认证及订单语义一致。 |
| [cdsapi](https://github.com/ecmwf/cdsapi)；CDS/ERA5，不是卫星影像下载器 | Apache-2.0；[0.7.7](https://pypi.org/project/cdsapi/) 2025-09-30，近期发布证据偏弱；[CDS 官方](https://cds.climate.copernicus.eu/how-to-api) 仍支持并介绍较新的 datastores client | 示例将 dataset、product_type、date、time、variable 分开；日期与再分析有效时刻不可当拍摄扫描。不同产品参数受数据集约束，许可证同意及上游异步处理独立于查询。 |

相邻工具：[Satpy](https://github.com/pytroll/satpy) 是读取、组合及处理库，不应列为通用下载器；[v0.60.0](https://github.com/pytroll/satpy/releases) 2026-03-04 涉 FCI reader。其主体 Apache-2.0，仓库说明特定第三方 EPS XML 为 GPL-2-or-later。其通道与文件组织可供调查，但不能从 reader 支持推断下载支持。未找到经过同等验证的 Himawari GCS/AWS 专用下载器，本次不作维护或覆盖断言。

## 对任务界面的具体建议

1. 基于服务端产品能力选择控件：已核验扫描轴提供日期与时刻；轨道/稀疏事件以时间范围或实际文件列表选择；月、年等覆盖选择对应周期。选择步长与名义 cadence 分离，未知规则不造固定网格。秒级及历史扫描模式保留精度。
2. 查询文件预览和提交任务分步：呈现集合、版本、平台、真实文件起止、通道/组合、数量、可得大小和截断/缺失状态；查询失败区别于零结果。不要声称预览即保证可下载；提交固定已确认选择并再次校验。
3. 显式区分范围相交和完整包含；覆盖型任务若采用半开 `[start,end)`，相交判断是 `file_start < end && file_end > start`，精确端点按各上游转换和测试。单时刻、最近时刻（明确容差）与范围不是同一操作；周期以真实文件覆盖为准，不把 8 日合成改称自然周。
4. 仅在文件级能力证明通道独立时显示通道筛选，多通道打包文件不要假装可按通道减少下载；仅在产品时间轴、通道定义与下载能力都兼容时共享选择。切换产品或版本时重新验证选择，不静默继承不兼容的时刻。
5. 任务身份规范化 UTC、产品/集合/版本、选择和权限归属；一致请求可识别重复但不跨用户泄露。文件临时状态、完整性验证、完成后原子定名及显式重试/续传分开；不能由文件存在、慢速或临时文件推断完成，也不应重置正在前进的连接。以上为设计建议，不代表所列项目都具备全部能力。

许可证是软件代码许可，数据集访问条件另行核对；此处建议独立实现概念。如复制代码，需按对应 MIT/Apache 等保留许可、版权及适用 NOTICE，具体复用文件需重新核查。未对代码兼容性作法律结论。
