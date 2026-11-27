# P10 资源超配分析与可观测大盘（2026-11-19 规划）

状态：**A1（数据采集与持久化）、A2（超配分析引擎）、A3（资源大盘）、A4（Grafana 看板与深链）、A6（成本从产品面移出）已做（A2、A3 的第一部分 2026-11-19，A1、A3、A4 的其余 2026-11-20，见末尾“A1、A3、A4 实际做了什么”）；A5 已做（2026-11-25，和 P11 的 C6 一起，见 ai-and-performance-p11.md 第十二部分）；本文其余是计划，不是实现。** 用户已答复三项：E2（operator 只读：节点；Pod 只统计已配置的模块）、E3（工作负载只统计已配置的模块，不自动发现全部）、E10（成本代码从产品面拿掉，代码保留）；其余 E 项还没答复。来源：用户给出的“资源超配分析与可观测性系统升级”需求和一组参考大盘截图（KubeCostOptimizer）。截图里的金额、成本列不在本阶段范围（见 [成本模块需求表](cost-module-requirements.md)）。动手前先读本文的“待你确认”表，没确认的项不进开发；页面规则见用户记忆：不用页签（含分段切换）、不拆成多页、字段带“?”说明。

## 阶段目标与边界

**目标**：先建一个以真实历史数据为依据、可解释、可追溯的 Kubernetes 资源超配分析系统；再考虑预测、自动优化和成本。

| 边界 | 说明 |
|---|---|
| 不用预测 | 本阶段超配判断**不引入预测模型**，现有预测算法保留，但不参与判断。P9 的 C2、C3 用平台推荐值当“安全值”，与此冲突，要重做（见“与 P9 的关系”） |
| 不算成本 | 机器成本、实例价格、OpenCost、成本预测、费用优化全部移出本阶段；也不把 Requests 降幅当成本节省 |
| 模式 | 保留 Observe / Recommend / Auto，本阶段以 Observe / Recommend 为主，**不根据超配判断自动改资源** |
| 数据 | 使用量来自 Prometheus；配置与元数据来自 Controller 上报；分析结果和历史存在平台数据库，不重复写全量时序 |
| Controller | 不连数据库，只经平台 API 与 Kubernetes API；新增的是**只读**权限 |

## 现状对照（以代码为准）

| 需求 | 现有 | 缺口 |
|---|---|---|
| 系统内全局大盘 | 首页 `views/dashboard`：模块计数、需要关注的模块、扩缩容累计；效果报告 | 集群级总量、可回收量、待优化应用、稳定性风险；Top N 排行；工作负载表（配额、实际使用、建议、状态诊断）；同屏对比 |
| Grafana Dashboard | `deploy/grafana/kube-res-foresight-dashboard.json`，只覆盖平台与 operator 自己的指标（45 个查询） | 集群、节点、工作负载、Pod、Container、JVM 的资源看板；与系统的关联跳转 |
| 集群与节点信息 | 没有持久化。P9 的 C4 能从 kube-state-metrics 读一次，但没存，也属于要移出的成本模块 | 采集（云厂商、Region、可用区、实例规格、容量与可分配、Requests/Limits 汇总、使用率）与持久化 |
| 工作负载配置 | 模块里手填的 Pod 请求；有 `k8s_cpu_requests/limits`、`k8s_mem_requests/limits` 的 Prometheus 来源 | **只对已配置的模块**（用户决定，E3）：它的工作负载、Pod、Container 的副本数、Requests/Limits 及其变化历史；不自动发现全部工作负载 |
| Controller 上报元数据 | operator 只上报扩缩容执行；RBAC 只有 deployments、statefulsets、HPA、KEDA、leases | 新增只读 RBAC：**nodes**（集群级）；**模块的 Pod 与工作负载**只统计已配置的模块（用户决定，E2）；快照上报接口 |
| JVM 指标 | 指标种类有 `jvm_heap_used/max`、`jvm_nonheap_used`、`jvm_gc_pause`、`jvm_threads`，预设均未验证；堆压力已并入副本求解 | Committed、Non-Heap Max/Committed、GC 次数；超配分析里的 JVM 口径 |
| 本地存储与保留 | 样本只存模块的来源；各类记录有保留设置 | 集群级快照、配置变更历史、分析结果的表与保留、降采样 |
| 多周期超配分析 | 效果报告的“浪费”只量副本；P9 C2 用预测推荐当安全值 | 全新：多窗口分位数、移动平均、节流、OOM、可信度、判定公式 |
| 关联跳转 | 无 | Grafana 地址与 dashboard uid（系统参数），深链携带集群、Namespace、工作负载、Pod、时间范围 |
| LLM 周期建议 | AI 助手（只读工具）、AI 定时任务（提示词加计划）、LLM 管理 | 超配分析的只读工具；任务结果落库并在大盘上展示；只给建议，不执行 |

## 参考截图 → 我们的做法

| 截图元素 | 做法 |
|---|---|
| 顶部“负载分析 / 资源大屏”切换 | 不用页签。只做一个页面；大屏的内容并进同一个视图 |
| 卡片：当前资源预留量（全副本总和，CPU 与内存） | 保留：全部工作负载 Requests 总量；“优化后预留量”改为“按真实历史的建议值合计” |
| 卡片：可节省的预留量 -60.16% | 改名“可回收的预留量”，只写资源量；**去掉金额口径** |
| 卡片：待优化超配应用 76 / 185，欠配 / 扩容应用 3 | 保留（超配、欠配按下面的判定） |
| 卡片：稳定性隐患 / OOM 告警，健康良好 | 保留，口径见“风险与可信度” |
| 降本排行榜（Top 10 与命名空间占比切换） | 改成一个图 + 下拉选“Top N / 按命名空间”，不用分段切换；去掉“降本”二字，改“可回收资源排行” |
| 筛选：搜索、命名空间、健康状态；计算口径（单 Pod / 全副本总和） | 保留，计算口径改成下拉 |
| 工作负载表：配额、实际使用 P95/峰值、建议、预留量变动、状态诊断、操作 | 保留列；“预留量变动”去掉金额；“HPA 副本、伸缩行为”只显示读到的外部 HPA 信息，不显示平台的预测副本（本阶段不用预测） |
| 同屏对比（需求第六节 4） | 点一行开弹窗，竖向堆叠：配置、使用分布、JVM、分析结果、历史记录；不用页签 |

## 工作包与顺序

| 包 | 名称 | 依赖 | 复用 |
|---|---|---|---|
| A0 | 设计收敛 | 下面“待你确认”的答复 | 本文、`integration-decisions.md` |
| A1 | 数据采集与持久化 | A0 | operator（只读上报）、平台 API、GORM 迁移、`pkg/collector`、指标预设 |
| A2 | 超配分析引擎 | A1 | 指标映射与模块、`pkg/cost` 里的快照读取和分位数（拆出来复用）、`RoundCPUMilli`/`RoundMemoryBytes` |
| A3 | 系统内大盘与详情 | A2 | 首页、列表与卡片组件、变更预览弹窗的做法 |
| A4 | Grafana Dashboard 与深链 | A1（指标名）、A3（路由） | `deploy/grafana`、Helm chart 的 `files/dashboard.json` |
| A5 | LLM 周期建议模块 | A2 | AI 助手的只读工具、AI 定时任务、LLM 管理 |
| A6 | 移出成本 | 无 | 见“与 P9 的关系” |

建议顺序：A6（先把成本从产品面拿掉）→ A0 → A1 → A2 → A3，A4 在 A1 之后可并行，A5 最后。

## A1 数据采集与持久化

| 项 | 内容 |
|---|---|
| Controller 上报 | 用户决定（E2）：operator 只读 **Node**（集群级的节点信息）；**Pod 与工作负载只统计已配置的模块**，operator 只取模块对应的 Deployment、StatefulSet、DaemonSet 及其 Pod，不上报其他 Pod；按周期和变化上报快照到平台 API（带唯一 ID，离线时进发件箱补报，沿用 R21）。不读 Namespace。**限制**：Kubernetes 的 RBAC 不能把 Pod 的读取限制到“某些模块的 Pod”，只能限制到命名空间；模块以外 Pod 不被统计靠 operator 的代码过滤，权限本身比实际读取的多 |
| 集群与节点 | 集群名称（系统参数）、节点数、总 CPU/内存、可分配 CPU/内存；云厂商（取自 `providerID`）、Region、可用区、实例规格（取自节点标签 `topology.kubernetes.io/*`、`node.kubernetes.io/instance-type`）；节点实际使用率来自 Prometheus；集群与节点的 Requests、Limits 汇总来自上报 |
| 工作负载配置 | Deployment、StatefulSet、DaemonSet、Pod、Container：副本数、每个 Container 的 Requests、Limits；**配置变化时记一条历史**，平时只更新“当前” |
| 使用量 | 由 Prometheus 查询得到（cAdvisor、kube-state-metrics、JVM 指标），按分析窗口聚合；不把全量时序复制进平台库 |
| JVM | Heap 和 Non-Heap 的 Used、Committed、Max；GC 次数与耗时（有指标才有）。GC 缺失时仍用 Used、Committed、Max 分析，**不把 Committed 当物理内存使用量** |
| 存储 | 表（名称暂定）：`cluster_node_snapshot`、`workload_config`（当前）、`workload_config_change`（历史）、`overprovision_analysis`（分析结果）；重启不丢；三库兼容 |
| 采样与保留 | 配置快照：变化时记，另外每小时一次；分析结果：按窗口定期算；保留期和降采样是系统参数，默认值见“待你确认” |

## A2 超配分析引擎

**纯函数 + 定时任务 + 存储 + API，只看真实历史。** 下表的口径和默认值都是**我的提议，没有经过你的确认和真实数据验证**。

| 项 | 提议 |
|---|---|
| 窗口 | 15 分钟、30 分钟、1 小时、1 天、7 天、15 天、30 天，另加自定义范围（自定义只在请求时算，不落定期任务） |
| 单 Pod 与总和 | 单 Pod：把窗口内所有 Pod、所有时刻的样本放在一起取分位数，最大值是其中最大；全副本总和：每个时刻把所有 Pod 加起来，再对时间取分位数。两个口径分别算、分别标明 |
| 分位数与最大值 | P50、P95、P99、Max |
| 移动平均 | 写成 `MA(n, 步长)`：最近 n 个采样点的平均，步长是采样间隔。例如 `MA5@1m` 是最近 5 分钟的平均，`MA15@1m` 是最近 15 分钟；**页面一律把步长写在后面**，不让 MA15 和“15 分钟”混为一谈。默认三条线 MA5、MA15、MA25，步长随窗口取 1m、5m、15m |
| 超配比例 | `(当前 Request − 建议 Request) / 当前 Request`，同时给分子分母的数值 |
| CPU 建议值 | `max(P95 × (1 + 余量), P99)`，余量默认 20%，基准窗口默认 7 天；取整沿用 `RoundCPUMilli`。**节流保护**：窗口内节流占比超过 5%（默认）时，使用量低估了需求，不判超配，状态写“受限” |
| 内存建议值 | `max(Working Set P99 × (1 + 余量), Working Set Max × 1.05)`，余量默认 15%；取整沿用 `RoundMemoryBytes`。**OOM 保护**：窗口内出现 OOMKilled，建议值不低于出事前的 Working Set × 1.2，也**不低于当前 Request（不建议降）**，风险写“高” |
| JVM 的用法 | 并排显示 Used、Committed、Max 和 Container 的 Working Set、RSS，不拿 Heap Used 代表 Container，不拿 Max 当消耗；只做提示（例如 Heap Max 远大于 Working Set 提示 `-Xmx` 可能偏大）；JVM 应用缺这些指标时降低可信度 |
| Limits | 只显示 Limit 与 Request 的比例和 Working Set 离 Limit 的距离；本阶段不给 Limit 建议值（沿用“不自动调低内存 Limit”） |
| 分级 | 超配比例 ≥ 50% 严重，30% 到 50% 中度，15% 到 30% 轻度，小于 15% 合理；Request 低于 P95 是欠配 |
| 风险与可信度 | 可信度看数据覆盖率（窗口内有数据的比例，≥ 80% 高，50% 到 80% 中，小于 50% 低且不给建议值）、窗口长度（不到基准窗口降一级）、JVM 指标缺失降一级；OOM、节流、峰值接近 Limit 抬高风险；这些原因都写在结果里 |
| 可解释性 | 每个结论带：为什么超配、依据哪些指标、分析了多久、真实用了多少、建议值怎么算（公式与代入的数）、公式版本 |
| 历史 | 每次分析存一条（窗口、时间、各分位数、建议、判定、可信度、公式版本），可查看、对比、追溯 |

## A3 系统内大盘

| 项 | 内容 |
|---|---|
| 位置 | 复用并扩展首页，或在预测结果下新增一个页面（待你确认）；一个主视图，不用页签 |
| 内容 | 四张汇总卡、可回收资源排行图、筛选栏、工作负载表、详情弹窗 |
| 关联 | 详情弹窗和 Deployment、Pod、Node 详情都有“在 Grafana 打开”，参数自动带上 |
| 数据口径 | 与 Grafana 看板用同一套 PromQL（同一份文档里写明），避免两边数字不同 |

## A4 Grafana Dashboard

| 项 | 内容 |
|---|---|
| 覆盖 | 集群、节点、工作负载、Pod、Container、JVM；变量：数据源、集群、Namespace、工作负载、Pod、时间范围 |
| 数据源 | cAdvisor、kube-state-metrics、JVM 指标（Micrometer 或 JMX exporter，指标名要按你们实际使用的核实） |
| 关联 | 系统参数里配置 Grafana 地址和 dashboard uid；系统跳转携带 `var-*` 和 `from`/`to`；Grafana 跳回系统用 dashboard 链接 |
| 交付 | 一个 JSON 文件，放 `deploy/grafana`，Helm chart 里同步一份 |
| 限制 | 我无法在真实 Grafana 里加载它；只能校验 JSON 结构，**不能保证在你的 Grafana 版本里能导入** |

## A5 LLM 周期建议（已并入 P11 的 C6，2026-11-19；已做，2026-11-25）

下面的内容保留作为 C6 的起点；AI 的会话、权限与审计、工具注册表、异步任务见 [ai-and-performance-p11.md](ai-and-performance-p11.md)。

| 项 | 内容 |
|---|---|
| 做法 | 给 AI 助手加只读工具：读超配分析结果；用现有 AI 定时任务周期运行，把回答（Markdown）保存下来，在大盘上有一个“AI 建议”区块显示最近一次 |
| 约束 | 只给建议，**不执行任何改动**；提示词里写明数据口径和可信度，低可信度的结论不能被写成确定的；任务运行失败要在大盘上可见 |
| 复用 | 沿用 AI 定时任务的计划、重试、邮件；回答落库是新增 |

## 与 P9 的关系

| 项 | 处理 |
|---|---|
| 菜单“成本与浪费”（`forecast-results_cost`）、页面、`/api/v1/cost/*`、`cost_price_rule` 表、四个 `cost.*` 系统参数 | 移出产品面（A6）。代码怎么处理要你定（删除、保留但不暴露，或其他） |
| `pkg/cost` 里的价格、分摊、装箱模拟 | 属于成本，随之暂缓 |
| `pkg/cost` 里的 kube-state-metrics 快照读取、分位数 `UsageOf` | 与成本无关，A2 复用；要拆出成本包 |
| C2、C3 的“安全值 = 平台推荐”和三档候选 | 违背“不用预测”，不再使用；A2 用上面的公式替代 |
| 四个财务口径、基线等设计 | 写进 [成本模块需求表](cost-module-requirements.md)，待确认后再做 |

## 待你确认（A0 的输入；没确认的不进开发）

| # | 决定 | 我的建议 | 理由 |
|---|---|---|---|
| E1 | 集群维度 | 仍是单集群，加一个“集群名称”系统参数，数据里带这个字段，页面和 Grafana 变量里有 `cluster` | AGENTS 规定单集群；留字段，将来多集群不用改表。若你要真正的多集群，是另一个大改动 |
| E2 | operator 新增只读 RBAC | **已答复**：只读；节点相关信息（nodes）上报，Pod 信息只统计已配置的模块。我的落实：nodes 集群级只读；模块的 Deployment、StatefulSet、DaemonSet 与 Pod 只读，operator 代码只取模块的；不读 namespaces | 用户决定；RBAC 对 Pod 只能按命名空间限制，见 A1 的限制 |
| E3 | 工作负载范围 | **已答复**：只统计已配置的模块，不自动发现全部工作负载 | 用户决定；所以大盘里的“应用”就是模块，数量取决于建了多少模块（模块有批量创建，要不要加“从集群选工作负载批量建模块”可以以后再说） |
| E4 | 公式与阈值 | 接受“A2”表里的默认值，全部做成系统参数 | 它们没有经过真实数据验证，必须可调 |
| E5 | 使用量怎么来 | 用 Prometheus 的 `quantile_over_time` 和子查询在查询时聚合，只存聚合结果 | 需求要求避免重复写全量时序；代价是查询慢，要限并发和超时 |
| E6 | 存储保留 | 配置快照历史保留 30 天，分析结果保留 90 天，配置变化历史保留 180 天，都可调 | 没有依据，只是起点 |
| E7 | 大盘放哪 | 新增页面“资源大盘”放在预测结果下，首页不动 | 首页已有模块状态，混在一起太杂；以后你想换再换 |
| E8 | Grafana 地址与 uid | 系统参数 `grafana.base_url`、`grafana.dashboard_uid` | 不写死 |
| E9 | LLM 周期 | 每天一次，分析可回收量最大的前 20 个工作负载 | 控制成本和噪声 |
| E10 | P9 成本代码 | **已答复并已做**：从产品面拿掉（菜单、页面、接口、系统参数、数据表），代码保留，文档标“暂缓” | 代码没提交过，删了就找不回；拆出可复用的部分也需要它 |

## 验收要求（来自需求，附我的验证方式）

| 需求 | 验证方式 | 能验证到什么程度 |
|---|---|---|
| 系统内大盘与 Grafana Dashboard | 合成数据加浏览器；JSON 结构校验 | 页面可以看；Grafana 无法在真实实例里加载 |
| 集群、Node、Pod、Container、JVM 采集与持久化 | fake client、假 Prometheus、重启后读库 | 不是真实集群 |
| 多周期 CPU/内存真实超配分析 | 公式的单测（含代入的数）、边界（OOM、节流、缺数据、短历史） | 公式正确；对真实负载是否合理要你的数据 |
| 同屏对比、双向关联 | 浏览器 | 链接参数可看；Grafana 端要你来点 |
| 判定公式、阈值、数据来源、可信度说明 | 写进页面“?”和文档 | 可以 |
| 独立成本模块需求表 | 本次已出 | 已完成 |
| 数据完整性、持久化、指标准确性、异常与缺失 | 单测加合成数据 | 指标准确性只能对照 PromQL 逻辑，不能对照真实 Prometheus |

## 状态

| 包 | 状态 | 证据 |
|---|---|---|
| A0 设计收敛 | E2、E3、E10 已答复；E1、E4～E9 还没答复：**A2、A3 先按我的建议值做了（E4 默认值全部做成系统参数、E7 放在“预测结果”下的“资源大盘”、E6 分析结果保留 90 天），你一答复不同就改** | |
| A1 数据采集与持久化 | **已做**（2026-11-20，只用假客户端、合成数据验证，没在真集群上跑）：operator 只读上报节点、模块的工作负载、Pod、容器、HPA/KEDA；平台表、保留期、JVM 的 committed/非堆/GC 指标种类（见“A1、A3、A4 实际做了什么”） | `pkg/inventory`、`pkg/inventorysvc`、`pkg/operator/controller/inventory.go`、`pkg/dao/rdb/inventory.go`、`pkg/model/inventory.go` |
| A2 超配分析引擎 | **已做**（2026-11-19，只用合成数据验证；公式和默认值没有真实数据验证） | `pkg/overprovision`（纯函数）、`pkg/forecastsvc/overprovision.go`、表 `overprovision_analysis`、系统参数 12 个、定时任务 `forecast.overprovision`（每 6 小时） |
| A3 系统内大盘与详情 | **已做**（在真浏览器里看过，临时库、假 Prometheus）：汇总卡、排行图、模块页，加上集群卡片、节点表、Ready 节点趋势、HPA 与副本趋势、配置变更历史、“在 Grafana 打开”、BIZ 账号走查（2026-11-20） | `web/src/views/resource-analysis/overprovision`、`components/business/overprovision-module-panel.vue`、`inventory-{cluster,workload}-panel.vue` |
| A4 Grafana Dashboard 与深链 | **已做**（2026-11-20）：看板由 `pkg/grafana` 用与页面同一套查询生成，JSON 在 `deploy/grafana` 和 Helm；**没在真实 Grafana 里加载过** | `pkg/grafana`、`deploy/grafana/kube-res-foresight-resources.json`、`pkg/app/server/api/handle_grafana.go` |
| A5 LLM 周期建议 | **已做（2026-11-25，和 P11 的 C6 一起）**：每日 AI 建议，见 ai-and-performance-p11.md 第十二部分 | |
| A6 移出成本 | **已完成**（2026-11-19） | 见下面“A6 实际做了什么” |
| 独立成本需求表 | 已出（[cost-module-requirements.md](cost-module-requirements.md)） | |

## A6 实际做了什么（2026-11-19）

| 项 | 做法 | 结果 |
|---|---|---|
| 菜单 | 种子里去掉 `forecast-results_cost`，并把它放进 `retiredMenuCodes`：已经种过它的库在升级时会把它连授权一起删掉，它也不再路由 | 产品里没有“成本与浪费”入口 |
| 接口 | `router.go` 不再注册 `/api/v1/cost/*`；`handle_cost.go` 留着，没人挂它 | 请求这些路径得到 404 |
| 数据表 | `CostPriceRule` 不再注册进 `model.Models`，普通迁移不会建 `cost_price_rule` 表；自己的测试里手动建 | 新库没有这张表 |
| 系统参数 | 四个 `cost.*` 不再进 `timing.All()`，不会播种到系统参数页；定义留在 `pkg/timing/cost.go` | 参数页看不到 |
| 页面 | `web/src/views/forecast-results/cost/index.vue` 移到 `web/src/parked/cost/index.vue`（不在 `views` 下，路由生成器不会再扫到它），去掉路由条目和菜单标题 | 没有路由；仍参与类型检查 |
| 代码 | `pkg/cost`、`pkg/forecastsvc/cost.go`、DAO、模型、API 客户端和类型都保留，它们的测试照常运行 | 没删 |
| 验证 | `go test` 通过 `pkg/migration`、`pkg/timing`、`pkg/model`、`pkg/forecastsvc`、`pkg/app/...`、`pkg/cost`；前端 `vue-tsc`、eslint、locale 测试、`pnpm run build` 通过；重新构建没有把路由生成回来 | 没有在浏览器里确认菜单确实消失（没跑实例） |
| 注意 | 如果某个库已经跑过带成本菜单的迁移，要再跑一次 `--migration`（或 `--upgrade`）才会删掉菜单行；成本代码想重新启用时，要从 `retiredMenuCodes` 去掉这个码、恢复种子和路由 | |

## A2、A3 实际做了什么（2026-11-19）

用户要求完成 A2、A3。A2 的依赖 A1 没有做（要 operator 新增只读上报，改动大，且 E1、E4～E9 还没答复），所以**只补了 A2 真正需要的那一小部分**：用平台已经存着的样本和模块上记录的请求，不读集群。E4、E6、E7 按我的建议值做（做成系统参数，随时可调）。

### A2：超配分析（`pkg/overprovision`，纯函数）

| 项 | 做法 |
|---|---|
| 窗口 | 15 分钟、30 分钟、1 小时、1 天、7 天、15 天、30 天；定时任务一次算全部；**自定义范围**只在请求时算，不落库 |
| 数据 | 每个模块的 `cpu`、`memory` 来源（工作负载总量除以同一时刻的就绪副本数，得到“平均每个 Pod”；本来就是每 Pod 的来源照用）；限流用 `k8s_cpu_throttle`；OOM 用 `oom`、`oom_working_set`；上限用 `k8s_*_limits`；请求优先取模块上记录的 Pod 请求，没有再取 `k8s_*_requests` 的最新值；JVM 用 `jvm_heap_used`、`jvm_heap_max`、`jvm_nonheap_used` |
| 两个口径 | **单 Pod**：每个时刻的“平均 Pod”取分位数；**全副本总和**：每个时刻所有 Pod 加起来再取分位数（不是单 Pod 乘副本数）。**限制**：存下来的序列是工作负载的汇总，看不到单个 Pod，所以“单 Pod 的最大值”是平均 Pod 的最大值，某一个特别忙的 Pod 会被平均掉；要看到它需要来源的查询本身按 Pod 取最大 |
| 分位数 | P50、P95、P99、Max（最近排名法） |
| CPU 建议值 | `max(P95 × (1 + 余量), P99)`，余量默认 20%；取整沿用 `RoundCPUMilli` |
| 内存建议值 | `max(P99 工作集 × (1 + 余量), 最高工作集 × 峰值系数)`，余量默认 15%，峰值系数默认 1.05；取整沿用 `RoundMemoryBytes` |
| 限流保护 | 窗口内被限流的时间占比（`k8s_cpu_throttle` 的均值，单位是每秒被限流的秒数）超过 5%：使用量低估了需求，状态写“受限”，建议值不低于现在的请求 |
| OOM 保护 | 窗口内出过 OOM：建议值不低于出事时的工作集 × 1.2，也不低于现在的请求；风险写“高”，状态写“受限” |
| 比例与分级 | 超配比例 = （请求 − 建议）÷ 请求，同时给分子分母；≥50% 严重、30% 到 50% 中度、15% 到 30% 轻度、小于 15% 合理；请求低于 P95 是“欠配”；三个界限都是系统参数 |
| 可信度 | 看窗口内有数据的比例（≥80% 高，50% 到 80% 中，小于 50% 低且不给建议）；窗口短于基准窗口（默认 7 天）降一级；模块配置了 JVM 指标但窗口内没测到再降一级 |
| 风险 | OOM 为高；限流、请求低于 P95、峰值达到上限的 90% 各抬高一级；没读到上限时不判断离上限多远（写在依据里） |
| JVM | 堆已用、堆上限、非堆已用的分位数和容器工作集 P99 并排显示，只给提示（堆上限远大于工作集、堆已用接近堆上限、没测到），**不把堆当成容器使用量，不把上限当消耗** |
| 移动平均 | MA5、MA15、MA25，**标签一律写步长**：`MA5@5m` 是 5 个点、每点 5 分钟；步长随窗口取 1 分钟（≤1 小时）、5 分钟（≤7 天）、15 分钟（更长），存的序列更粗时用它自己的步长。先按步长算，再为图抽稀，所以标签总是真的 |
| 可解释性 | 每个结果带：公式写出来并代入数、依据的原因代码（页面翻成句子）、窗口、覆盖的点数、公式版本 `op-1` |
| 历史 | 每次分析每个窗口每个资源存一行（`overprovision_analysis`），详情里看同一窗口的历史；保留期 `analysis.result_retention_days`（默认 90 天），定时任务顺带清理 |
| 系统参数 | `analysis.cpu_margin_percent`、`memory_margin_percent`、`memory_peak_percent`、`oom_uplift_percent`、`throttle_percent`、`baseline_window_days`、`grade_severe_percent`、`grade_moderate_percent`、`grade_mild_percent`、`coverage_high_percent`、`coverage_medium_percent`、`result_retention_days`；定时任务 `forecast.overprovision`（迁移种子，`@every 6h`，管理员可改） |
| 接口 | `GET /overprovision?window=`（大盘，读存下来的结果）、`POST /overprovision/run?module_id=`（立即分析，受手动步骤的预算限制，没来得及的留给定时任务）、`GET /modules/:id/overprovision?window=` 或 `from=&to=`（详情，现算，带图的序列、JVM 和历史） |

### A3：资源大盘（“预测结果 > 资源大盘”，菜单码 `resource-analysis_overprovision`）

一个页面、一个主视图，不用页签：搜索条（窗口、命名空间、状态、计算口径、关键字）、汇总卡（当前预留 CPU 与内存、可回收 CPU 与内存及占比、待优化超配应用、欠配应用、稳定性隐患、受限、最近分析）、可回收资源排行图（下拉选 Top 10 模块或按命名空间，CPU 或内存）、工作负载表（列可在列设置里选）、详情窗口（配置、CPU 与内存各一段：图、分位数、建议值与公式、依据；JVM；历史结果；窗口可选七个之一或自定义范围）。表的“计算口径”下拉切换单 Pod 与全副本总和。**所有数字来自存下来的分析结果，不用预测，不算金额。**业务角色 `BIZ` 的种子带上了这个菜单（已有的 BIZ 角色不会被改，要管理员自己加）。

### 验证
- Go：`pkg/overprovision` 单测（分位数、两个公式代入数、取整、各分级边界、限流、OOM、数据不足、可信度降级、靠近上限、总和口径、移动平均、重采样、窗口）；`pkg/forecastsvc` 集成测试（用成本测试里的合成数据：过配的 Pod 判超配、大盘合计、立即分析与历史、详情的图与自定义范围不入库、限流／OOM／JVM、基准窗口随参数）；接口测试；迁移与 BIZ 角色测试。
- 前端：`vue-tsc`、oxlint、eslint、30 个单元测试；**在真浏览器里**（Chrome，临时库里 5 个合成模块：超配、欠配加限流、OOM 加 JVM、合理、数据不足）打开页面、点“立即分析”、看汇总卡与排行图与表、打开详情窗口并滚动；这一轮发现并修了：国际化文字里的 `@` 会被当成链接语法，整个详情窗口什么都不显示；图的移动平均标签被抽稀改成了 `MA5@35m`（现在先按窗口步长算再抽稀）；图上的请求线和建议线画不出来（图表钩子没有注册标线组件）。

### 没做，也没验证（2026-11-19 当时；A1、A3、A4 的其余已在 2026-11-20 做完，见下一节）
- **公式和阈值没有经过真实数据验证**，E4～E9 没有答复；不同工作负载的合适余量差别可能很大。
- 限流占比依赖 `k8s_cpu_throttle` 来源，它的单位按“每秒被限流的秒数”理解；来源没配时限流保护不起作用（页面上看不出来，只是没有限流这一项）。
- 没在 PostgreSQL、MySQL 上跑；大盘读最新结果用 `MAX(id) … GROUP BY` 的子查询，数据量大时没量过。
- 立即分析对所有模块是同步的，模块很多时会被预算截断（已分析的留着，其余等定时任务）。
- 没用 `BIZ` 账号在浏览器里走过这个页面（接口权限由测试核对）；**2026-11-20 已走过**。

## 资源大盘改版（2026-11-19）

用户给了“资源大盘与模块分析页面优化需求”（紧凑小卡片、四项配置、动态指标、窗口修复、选模块后只看该模块、P90/P95/P99、历史分析、一键 AI 分析）和一张 Grafana 参考图。做了什么、没做什么见 [platform-progress.md](../../../../docs/platform-progress.md) 末尾同日条目；要点：模块页只显示该模块；其他指标只显示配置了的（Container、JVM、限流、OOM）；P90 已加；上限无指标时用模块的上限系数并标明来源；集群向模块数据源（Prometheus）现查（未对真实实例试过）；一键 AI 分析走助手的后台任务；**分位数向模块数据源（Prometheus）现查（quantile_over_time），判定仍用平台存下的样本；见 platform-progress 同日“数据来源是 Prometheus”条目**。参考图里的节点明细表、HPA 扩容趋势、Ready 节点趋势没有做（要 A1 的节点数据或另外写 PromQL）。

## A1、A3、A4 实际做了什么（2026-11-20）

用户要求“A1 A3 A4 完成下，不要部分完成”。E1、E4～E9 仍没答复，按下面的默认值做，**全部是系统参数或编辑得到的查询，随时可改**。

### A1：数据采集与持久化
| 项 | 做了什么 |
|---|---|
| operator 只读上报 | `InventoryReader` 用 `mgr.GetAPIReader()`（不建缓存、不监听全集群）读：Node；模块的 Deployment、StatefulSet、DaemonSet；按选择器取这些工作负载的 Pod；HPA 与 KEDA ScaledObject（KEDA 没装时跳过）；每个容器的 Requests、Limits（节点上的模块 Pod 请求用 Pod 的有效值：容器之和，每种资源再与最大的 init 容器取较大者；**没有**处理“可重启的 init 容器（sidecar）”的特殊规则，用到时请核对）。只 get 和 list，**不写任何东西**；Pod 与工作负载只取平台告诉它的模块（`GET /operator/inventory-targets`，所有启用的模块，不管模式）。RBAC：nodes（集群级只读）、pods 只读、daemonsets 只读；Pod 的读取在 Kubernetes 里只能限到命名空间，所以权限比实际读取的多，过滤靠代码 |
| 上报节奏 | `--inventory-interval`（默认 5 分钟，0 关闭）读一次；配置指纹（只含配置：副本、容器请求与上限、HPA 范围、节点的 Ready 与容量……，不含 HPA 当前值、Pod 重启）变了就报，没变也至少每小时报一次；只有 leader 报；平台读不到目标时用上次知道的目标继续读 |
| 不丢、不重 | 沿用发件箱：记录有唯一 `record_id`，先落盘（假脱机文件）再发，至多留 48 份，平台按 `record_id` 幂等（重发返回 `duplicate:true`）；发不出去的下次补报；平台永久拒收（400、404、409、413、422）的丢弃，其他错误（凭据、网关、断网）留着等下次 |
| operator 到不了数据库 | `pkg/inventory` 是纯类型与函数，`isolation_test.go` 的白名单加了它；operator 只经平台 API |
| 平台的表 | `inventory_report`（收到的报告，幂等键）、`cluster_node_snapshot`（每次报告每个节点一行）、`workload_config`（模块的当前配置，一模块一行）、`workload_config_change`（配置变化历史，第一次读到也记一条）、`workload_snapshot`（每次报告一行：期望、就绪、HPA 当前与上下界、重启数，画趋势用）；三库兼容的 GORM 模型 |
| 迟到的报告 | 比库里更新的配置不会被旧报告覆盖；先到后到都进快照与历史；早于保留期的报告拒收（400） |
| 缺失与出错 | 工作负载在集群里找不到：`missing`；读取失败：`error`（页面写原因）；上次报告超过 2 小时：`stale` |
| 保留 | 系统参数 `inventory.snapshot_retention_days`（默认 30）、`inventory.change_retention_days`（默认 180）；定时任务 `inventory.prune`（`@every 24h`，迁移种子，管理员可改） |
| 集群名称 | 系统参数 `cluster.name`（默认 `default`） |
| 节点的实际使用、集群合计 | **向 Prometheus 数据源现查**，不存库。查询是系统参数 `query.<名字>`（`pkg/resourcequery` 是唯一来源：页面和 Grafana 用同一份），默认名只是可编辑查询的默认值，**你的指标名不同就改这些参数**；按节点的查询必须返回带 `node` 标签的序列；数据源取 `cluster.datasource_id`（0 = 第一个可用的） |
| JVM | 新增指标种类 `jvm_heap_committed`、`jvm_nonheap_committed`、`jvm_nonheap_max`、`jvm_gc_count`（原有 `jvm_heap_used`、`jvm_heap_max`、`jvm_nonheap_used`、`jvm_gc_pause`），四个预设、目录、示例数据都加了。分析里 committed **不当作容器的内存使用量**；提示 `committed_is_not_use`、`heap_near_committed`、`gc_missing`（GC 没测到时仍用已用、已申请、上限分析） |

### A3：页面
同一页（“预测结果 > 资源大盘”，不用页签）：
- **总览**：最上面是可折叠的“集群与节点”（默认展开）：集群名、就绪节点（x / y）、不可调度、可分配 CPU 与内存、模块的 Pod 数与请求 CPU 与内存（占可分配的比例）、云厂商、可用区、实例类型；**Ready 节点趋势**（每次报告一个点）；**节点表**（状态、压力、实例类型、可用区、可分配、模块 Pod 与请求、正在用的 CPU 与内存及占比——后两列向 Prometheus 现查，查不到的写原因，不弹窗；每行“Grafana”链接）。其下是原有的汇总卡、排行图、模块列表；整页在一个滚动区里。
- **模块页**：在原有内容下增加“工作负载（operator 上报）”：副本（就绪/期望、可用、已更新）、HPA 或 KEDA（名称、范围、当前→期望）、Pod 数、重启数；每个容器的 CPU/内存 Requests 与 Limits；**副本与 HPA 趋势**（期望、就绪、HPA 当前，上下界虚线）；Pod 列表（节点、阶段、重启、启动时间、每个 Pod 的 Grafana 链接）；**配置变更历史**（时间、摘要、可展开每一项 `路径: 旧 → 新`）；operator 没上报时写明怎么办；`missing`、`stale`、`error` 各有提示。再下面是可折叠的“集群与节点”（点开才加载）。
- **“在 Grafana 打开”**：工具栏（集群或所选模块）、集群、节点、工作负载、Pod、容器，URL 带 `var-cluster`、`var-namespace`、`var-workload`、`var-pod`、`var-node`、`var-container` 和 `from`/`to`（页面的窗口：`7d` → `now-7d`，自定义范围 → 毫秒时间戳）。**没配 Grafana 地址时一个链接都不画。**
- **Grafana 打开本页**：看板里的链接 `/resource-analysis/overprovision?namespace=…&workload=…&source=grafana`；页面认 `namespace`、`workload`、`module`、`window`，Grafana 的“全部”（`.*`）和多选（`a|b`）不指向一个对象，**忽略**。
- **接口**：`GET /inventory/nodes`、`/inventory/nodes/trend?hours=`、`/inventory/nodes/usage`（502 = 数据源读不到，页面在原处写原因）、`/inventory/workloads`、`/modules/:id/inventory?hours=`、`/grafana/settings`、`/grafana/dashboard`（下载看板 JSON）；operator 用 `GET /operator/inventory-targets`、`POST /operator/inventory`（权限 `forecast:operator:use`）。BIZ 角色：资源大盘菜单带上前七个读接口，**不带 operator 接口和系统参数的写**；已经存在的 BIZ 角色不会被迁移改（管理员改过的权限不收回、也不补），要在“授权管理”里给它加上这个菜单。

### A4：Grafana 看板
- `deploy/grafana/kube-res-foresight-resources.json`（uid `kube-res-foresight-resources`，schemaVersion 39），同一份在 `deploy/helm/kube-res-foresight/files/resources-dashboard.json`（Chart 的 ConfigMap 多一个键，用同一个标签让 sidecar 载入）。**由代码生成**：改查询或面板后运行 `go run ./pkg/grafana/gen`，`go test ./pkg/grafana` 保证两份文件与生成器输出一致。
- 行：Cluster、Nodes、Workload、Pods、Containers、JVM；变量：数据源、cluster（自定义，默认值是 `cluster.name`）、namespace、workload、pod、node、container（“全部”= `.*`，和页面填的一样）。面板的查询就是 `resourcequery` 的那一套（测试保证每个查询都出现在看板里），`GET /grafana/dashboard` 按**当前系统参数里改过的查询**生成，所以改了查询再下载就是你的版本。
- 系统参数：`grafana.base_url`、`grafana.dashboard_uid`（默认 `kube-res-foresight-resources`）、`platform.public_url`（看板里“在平台打开”的链接，空则不画）。**这些参数有 10 秒的读取缓存**，改了之后最多等 10 秒页面才变。
- 步骤与限制见 `deploy/grafana/README.md`。

### 验证
- Go：`go test -p 1 ./pkg/...`、`go vet`、`gofmt` 通过。新增的测试：`pkg/inventory`（校验、指纹、差异、云厂商）、`pkg/inventorysvc`（幂等、迟到、首次历史、保留）、operator 的读取与上报（假客户端：Deployment、StatefulSet、DaemonSet、HPA、KEDA、Pod 的有效资源、发件箱补报、断开时用已知目标）、`pkg/resourcequery`、集群与节点的 Prometheus 查询（假服务）、JVM 新种类、接口（报告 200/重复/400/413、读取、502）、Grafana 设置与下载、看板结构（格子不重叠、变量、每个查询在面板里、与发布文件一致）、迁移与 BIZ 的允许与禁止清单。
- 前端：`vue-tsc`、oxlint、locales 测试、47 个单元测试（含链接构造与路由参数）。
- **真浏览器**（Chrome，临时库、假 Prometheus、静态服务加代理；用 API 发了 4 份合成报告）：总览（集群卡片、趋势、节点表含实时用量、Grafana 链接；没配地址时没有链接）、模块页（容器、HPA 趋势、Pod、变更历史展开）、从 `?namespace=prod&workload=shop-api&window=7d&source=grafana` 进入自动选中模块、`.*` 与 `a|b` 被忽略、BIZ 账号（新建的 BIZ 角色）能看到页面、读接口 200、operator 接口与系统参数写 403。这一轮发现并修了：节点行的字段是 `node` 不是 `name`（表里名字和实时用量一栏是空的）、总览被集群卡片挤得列表看不见（整页改成一个滚动区）。
- **没验证**：真实集群（operator 只用 controller-runtime 的假客户端测过，没有真的列过 Node/Pod，没验证 RBAC 够不够）；PostgreSQL、MySQL；**真实 Grafana（看板 JSON 只校验了结构，没有导入过，不能保证你的版本里能用）**；真实 Prometheus（默认查询里的指标名是通用默认值，你的不同就改 `query.*`）；多副本 operator 的选主；大集群（上限：5000 个节点、2000 个工作负载、每个工作负载 500 个 Pod、64 个容器，超出的报告被平台拒收）。部署后要跑 `--migration`。

## 资源大盘优化（2026-11-20，15 条）

用户给了“资源大盘优化需求”（筛选与查询、图表与指标、集群大盘、AI 分析交互、界面视觉）。需求文字是语音转写，有几处我按上下文理解，**请纠正**：第 3 条“查询 Circle、From Microsoft Circle”理解为“Cluster 的查询和 From Prometheus Cluster 的来源标注”；第 6、7 条的“OM”理解为 OOM。

| 条 | 做了什么 | 在哪里 |
|---|---|---|
| 1 分析窗口 | 去掉 7 个并排的选项，改成和全站一样的时间选择框（最近 N 分钟/小时/天或固定范围）；正好是 15m、30m、1h、1d、7d、15d、30d 之一就走已存的窗口，其他长度按范围现算；总览读“能盖住这个范围的最短已存窗口”并说明 | `utils/overprovision-window.ts`（有测试）、`time-range-field.vue`（加了 `size`）、`views/resource-analysis/overprovision/index.vue` |
| 2 命名空间与实例 | 两个独立的下拉：先选命名空间，实例只列它的实例，名字里不再带命名空间（两个命名空间同名时才在括号里注明）；选了实例，命名空间自动对上；清掉命名空间，实例也放开 | 同上 |
| 3 查询与来源 | 所有 PromQL 用同一个列表显示：名字、来源标签（指标模板：名字／系统参数／默认查询）、Prometheus 风格的代码块、得到的值或失败原因；来源标签统一写 `Prometheus · 数据源名` | `components/business/promql-list.vue` |
| 4 504 | 见下“504” | `pkg/forecastsvc/promstats.go`、`cached.go`、`handle_overprovision.go` |
| 5 单 Pod 图 | 每个图的绘图区高 250（原来 170），宽度不变；请求、建议、上限、P90/P95/P99 的数字写在每个图上方的一行里，不再画成压在线上的标签，所以不遮字 | `overprovision-usage-chart.vue` |
| 6 OOM 卡片 | 模块页最上面（名称行之下、配置卡片之前）一排 OOM 卡片：次数、被杀时的工作集、内存 P99／上限、距上限还剩、最近一次发现 OOM、模块自己的 OOM 指标；总览最前面两张：发生 OOM 的模块数（带名字）、OOM 总次数 | `overprovision-oom-cards.vue`、总览的 `oomCells` |
| 7 历史并入图 | 删掉独立的历史图（`overprovision-chart.vue`、`overprovision-history-chart.vue`）；同一张图里画已存分析结果的“历史请求”“历史建议”（阶梯线）和“历史 P95”（点）；CPU 和内存是同一张图的左右两个面板（窄屏上下排） | `overprovision-usage-chart.vue` |
| 8 全副本合计 | 新增“全副本使用总和”图：窗口内所有副本使用量加起来的时间序列，请求、建议、上限按当前副本数相乘；历史点用已存的总和分位数和当时的副本数 | 后端 `ResourceChart.total_ts/total/replicas`（`opTotal`），同一个图组件的 `total` 模式 |
| 9～11 集群大盘 | 集群指标改成“指标模板”页（数据源指标）里的普通模板：新增 9 个集群指标种类（节点数、Ready 节点数、Pod 数、可分配／请求／使用的 CPU 与内存）和 7 个节点指标种类，层级是 `cluster`／新增的 `node`；有 16 个预设，一键建；页面读的查询顺序是：指标模板 > 系统参数 `query.*`（改过的）> 默认；每个数都带着它用的查询、来源、答案；读不到时 200 加原因（没有数据源／不能访问）和去哪里设置，不再是一条“读不到集群的信息” | `pkg/model/metric_mapping.go`（`ScopedKinds`）、`pkg/preset/scoped.go`、`pkg/forecastsvc/cluster.go`（`QueriesFor`）、`pkg/resourcequery`（`MappingKind`）、`overprovision-cluster-card.vue` |
| 12 AI 入口 | 去掉工具栏里的“一键 AI 分析”和铺在大盘上的答案；右下角悬浮按钮，点开是一个窗口（可放大、还原、收起），后台跑，窗口收起时按钮转圈，答案好了按钮上出一个红点，再点开看；答案按模块存在浏览器里 | `overprovision-ai-assistant.vue` |
| 13 搜索区布局 | 所有筛选控件同高（小号）、同一排、都从自己左边向下展开，窄屏自动换行；右边只有“在 Grafana 打开”和“立即分析” | 页面的 `.filter-bar` |
| 14、15 视觉与交互 | 总览按“OOM 与总计 → 集群 → 排行 → 列表”排；集群、操作员的节点、查询列表都用同一种可收起的折叠；计算口径从单选按钮改成下拉，和其他筛选一样 | 页面与组件 |

### 504
- 原因：模块页的“Prometheus 分位数”对每个指标发 5～10 个查询（子查询，窗口越长越慢），一个一个指标地问；指标一多或数据源一慢就超过网关的时间，网关用一个没有原因的 504 切断。
- 做法：指标最多 8 个一起问；整个请求只花“读请求时限（`http.list_query_seconds`，没有时限的请求用网关时间）减去 2 秒”，到点停止，**已经答完的指标照常给，没答的标 `skipped` 并写原因，整份标 `partial`**，不再有 504；部分答案不进缓存，下一次只补没答的；`?scope=core`（CPU 和内存）和 `?scope=more`（其余）让页面先拿主要的，再拿其余的；页面写“N 个指标限时内没答完”和重试。
- 没做：详情接口（`/modules/:id/overprovision`，读已存样本）没有加分段，它只读库，没见到慢的证据。

### 验证
- Go：`go test -p 1 ./pkg/...`、`go vet`、`gofmt` 通过。新增或改的测试：指标模板的新种类和层级规则（集群种类不能当模块的指标来源）、预设、集群与节点的查询顺序（指标模板 > 系统参数 > 默认）、没有数据源时的回答、`QueriesFor`、Prometheus 分位数的范围、限时内的部分答案、部分答案不进缓存、全副本序列。
- 前端：`vue-tsc`、oxlint、oxfmt、51 个单元测试（新增窗口换算 4 个）。
- **真浏览器**（Chrome，临时库、假 Prometheus 其中 JVM 的查询故意 15 秒、假语言模型）：筛选栏（同高、窄屏换行）、命名空间到实例、时间选择框改成 3 小时后的请求（`from`/`to`）、OOM 卡片、合并后的 CPU／内存图与全副本图、集群卡片（没有指标模板时是默认查询，用预设建了 9 个以后来源标签变成“指标模板”，没答的数显示“-”）、读限时 3 秒时 2.3 秒内给出部分答案（原来会挂到网关）、AI 悬浮按钮的整个流程、指标模板页列出新种类。
- **没验证**：真实 Prometheus（默认查询里的指标名是编辑得到的默认，不同就改指标模板）、真实网关、PostgreSQL、MySQL；全副本图在没有“副本数”来源的模块上没有数据（图里写没有数据）；暗色主题只看了配色没逐页看。

## 资源大盘调整（2026-11-23，12 条，只读 Prometheus）

用户给了“资源大盘需求调整与优化”。需求文字是语音转写：“OM”理解为 OOM；第 2 条“评估是否需要保留独立模块列表”我的评估是**保留**——它正好是“进入大盘先看模块列表并选实例”的入口，所以保留，只留标识列。**请纠正。**第 3 条“集群大盘保留现状”与第 4 条“删除集群与节点、模块详情移除集群大盘”：我理解为总览页的“集群大盘”卡片（向 Prometheus 查的那张）不动，operator 上报的“集群与节点”面板删除，模块详情里的集群卡片删除。

| 条 | 做了什么 | 在哪里 |
|---|---|---|
| 1 可回收资源排行 | 删除（总览的排行图、可回收卡片、预留合计、待优化／欠配／隐患计数都删了） | `views/resource-analysis/overprovision/index.vue` |
| 2 模块列表 | 保留为总览的默认内容：模块、命名空间、工作负载三列加“分析”按钮，选命名空间和实例后进入模块大盘；列表来自模块接口，不含任何存下的分析结果 | 同上 |
| 3 集群大盘 | 不动：总览页下方仍是 `overprovision-cluster-card.vue` | 同上 |
| 4 功能移除 | 删除 `inventory-cluster-panel.vue`（集群与节点）、`inventory-workload-panel.vue`（工作负载）、模块详情里的集群卡片、“立即分析”按钮和回收资源卡片；operator 上报与 `/inventory` 接口原样保留（operator 和 Grafana 还要用） | web components |
| 5 OOM 数量 | 模块大盘最上面一行“OOM 数量：N”（有 OOM 红色、0 绿色）；没配 OOM 指标写“模块没有配置 OOM 指标”；OOM 指标不再出现在指标卡片里 | `overprovision-module-panel.vue` |
| 6 移动平均 | MA5／MA15／MA25 从图和接口（`ma5`、`ma15`、`ma25`、`ma_labels`）都删了 | `overprovision-usage-chart.vue`、`ResourceChart` |
| 7 点数 | 分位数表的“点数”列写成“样本数 / 窗口应有样本数”，表头加问号说明（步长、缺数据的含义）；P50、P90、P95、P99、Max 的表头也各加问号 | module-panel、locales |
| 8 AI 触发 | 右下角悬浮按钮只在选了模块时出现；总览不出现；打开页面、选模块、切窗口都不会发问，只有点悬浮按钮（或窗口里的“分析”）才发；按钮点开且没有答案时就是这次点击发问 | `overprovision-ai-assistant.vue`、页面 `v-if="sel.moduleId"` |
| 9 AI 提示词 | `utils/overprovision-ai.ts` 重写：只输出 HTML、用表格（配置与使用、判定与优化、限流／OOM／JVM）、状态单元格用 lv-bad／lv-warn／lv-ok／lv-info 类、直接写当前情况和优化后的数值、不写引导语、含糊词和额外推荐；答案经 `utils/safe-html.ts` 清洗（只留表格、列表、强调和这四个等级类，没有脚本、样式、链接、图片和事件） | 同左 |
| 10、11 卡片墙 | 新组件 `overprovision-card-wall.vue`：一个尺寸、一个间距；列数按宽度和卡片数算（`utils/card-wall.ts`，有测试），一列就一行一张，两列就一行两张，最后一行不满时用更宽的卡片补满，没有空位；等级用鲜明的颜色：红（危险）、橙（超配或需关注）、绿（正常）、蓝（信息）、灰（中性），由判定状态、使用率、可用量决定 | 同左 |
| 12 只读 Prometheus | 模块大盘的所有数字（请求、上限、副本、P50 到 Max、图、OOM、JVM、限流、各指标）都由 `GET /modules/:id/overprovision` 向模块指标映射的数据源现查（每个指标一个 query_range，`{{range}}` 取查询步长），不再读存下的样本，也不读存下的分析结果（历史图、历史请求、最近 OOM 时间都删了）；每个指标发的 PromQL、步长、点数随答案返回，页面的“查询”折叠里列出 | `pkg/forecastsvc/oplive.go`、`overprovision.go`（`OverprovisionDetail`）、`cached.go` |

### 读法与限时
- 步长：映射的步长，窗口长了放大到不超过约 1500 个点；第一个点在窗口起点后一个步长，所以一天是 288 个点（5 分钟步长），不是 289。
- 副本数：模块的“副本数”指标最后一个值；工作负载级的指标除以同一时刻的副本数得到单 Pod，Pod 级的指标乘以它得到总和。
- 限时：最多 8 个指标一起问，到读请求时限（`http.list_query_seconds`，没有就用网关时间）减 2 秒就停；答完的照常给，没答完的标 `skipped` 写原因，整份标 `partial`，页面提示“N 个指标限时内没答完”并给重试；部分答案不进缓存，答案缓存 5 分钟（以模块修改时间为版本）。
- 没有的：详情接口里不再有 `history`；`GET /overprovision`（大盘）、`POST /overprovision/run`、`/modules/:id/overprovision/prometheus` 仍在后端，页面不再用；定时任务 `forecast.overprovision` 照常存分析结果，现在只有 AI 助手的 `analyze_resources` 工具读它。

### 验证
- Go：`go test -p 1 ./pkg/...`、`go vet`、`gofmt`。新增或改的测试：详情向 Prometheus 现查（删光存下的样本也一样、不写样本）、慢 Prometheus 时给出答完的指标并说明没答完的、图没有移动平均、长窗口放大步长。
- 前端：`vue-tsc`、oxlint、oxfmt、单元测试（新增卡片列数与跨度、答案清洗、提示词）。
- 真浏览器：见 `docs/platform-progress.md` 末尾；**没验证**：真实 Prometheus（映射里的 PromQL 要对着真实指标名）、真实网关、PostgreSQL、MySQL。

## 资源大盘需求修正（2026-11-23 第二次，两级页面、集群大盘独立、指标类型、Requests／Limits 只读 Prometheus）

需求文字是语音转写，我的理解（**请纠正**）：“资源表格”理解为两处——总览的模块列表和模块详情里的分位数表，两处都加了 CPU／内存的 Requests 和 Limits；“集群数量”理解为平台管理的集群数，一个“集群”就是一个有集群指标模板的 Prometheus 数据源（再加系统参数 `cluster.datasource_id` 指的那个）。

### 页面边界与导航（两级页面）
| 层级 | 页面 | 路由 | 里面有什么 | 没有什么 |
|---|---|---|---|---|
| 一级 | 资源大盘 | `/resource-analysis/overprovision`（菜单“资源大盘”） | 模块列表（模块、CPU 请求／限制、内存请求／限制，命名空间与关键字筛选），每行一个“分析”按钮 | 集群、图、AI 按钮 |
| 二级 | 模块资源分析 | `/resource-analysis/overprovision-detail?module=ID&window=&basis=`（隐藏菜单，菜单码 `resource-analysis_overprovision-detail`，高亮一级菜单） | “返回模块列表”、窗口、口径、OOM 数量、卡片墙、分位数表（含 Requests／Limits）、图、查询、AI 悬浮按钮 | 集群大盘、列表 |
| 独立 | 集群大盘 | `/resource-analysis/cluster-dashboard`（菜单“集群大盘”，在“预测结果”下） | 管理的集群数、集群列表（已声明几项 / 总项数、没声明的是哪些）、所选集群的节点／Pod／CPU／内存 | 模块 |
- **入口**：模块只能从一级列表进二级页；集群只在集群大盘里；两个大盘互不嵌入。
- **返回与状态保留**：二级页的“返回模块列表”回到一级并关掉二级页的标签；一级页的命名空间和关键字存在页面外的状态里（`hooks/business/use-overprovision-list-state.ts`），回来时原样还在；二级页的模块、窗口（取标准标签时）和口径写在地址里，刷新、浏览器前进后退都落在同一页；集群大盘所选的集群写在 `?cluster=` 里。窗口选了固定范围时不写进地址（回到默认 7 天）。
- **权限**：二级页和集群大盘各是一个菜单权限（迁移种子里，二级页不可见），BIZ 角色都有；旧库要跑一次 `--migration` 才有这两个菜单。

### 指标模板的“指标类型”
- 指标模板新增“指标类型”：**模块指标**（给模块资源大盘用，模块的指标来源从这里选）和**集群指标**（给集群大盘用，`cluster_*` 和 `node_*` 的 16 个种类）。由指标种类决定（`model.MetricScope`），所以两种不会混：创建／修改时选的类型与指标种类不符会被拒绝；列表可按类型筛选，列表有“指标类型”列，目录接口 `/metrics/catalog` 每个指标带 `scope`。表单里先选类型，再选这个类型下的指标，集群指标的层级自动定（集群或节点）。
- **集群大盘只读已声明的模板**：集群的每个数都必须在指标模板里声明；没声明的不查，也不用系统参数或默认查询代替，页面写“没有在「指标模板」里声明（指标类型选“集群指标”）”并列出没声明的种类。（Grafana 看板生成仍用原来的“模板 > 系统参数 > 默认”。）

### Requests／Limits 只读 Prometheus
- 模块详情和模块列表里的 CPU／内存 Requests／Limits 只来自模块的 `k8s_cpu_requests`、`k8s_cpu_limits`、`k8s_mem_requests`、`k8s_mem_limits` 这四种指标（模块指标来源里的 Prometheus 查询）；**不再用模块自己设的 Pod 请求和 Limit 系数补**。模块没配某个指标时，那一格是“-”，提示写“模块没有配置 xxx 指标：在模块的指标来源里加上它”；没有请求时判定是“未知”，不给建议值。
- 模块列表：`GET /overprovision/resources`，对每个启用的模块并行（最多 8 个）问 Prometheus 现在的值（工作负载级的指标除以副本数指标得单 Pod），读请求时限内没问完的模块标“限时内没答完”并可重试，部分答案不进缓存；缓存 5 分钟，模块有变动立即重问。

### AI 分析
- 悬浮按钮只在二级页出现。进页面、点悬浮按钮都**不**发问：点按钮只打开窗口，窗口里点“分析”才发问。
- 提示词（`utils/overprovision-ai.ts`）要求：只输出 HTML；第一张表必须是“当前配置与 AI 推荐配置对比”，固定四行 CPU Requests、CPU Limits、Memory Requests、Memory Limits，四列（资源项、当前配置〔抄 Prometheus 的值，没有写“Prometheus 无数据”〕、AI 推荐配置〔具体数值和单位〕、依据）；其后是使用情况和判定；推荐依据（Requests 以平台公式建议值为底，有 OOM 或限流只升不降；Limits 不低于 P99，内存 Limits 不低于推荐 Requests）；不写引导语、含糊话和额外推荐。
- 答案里没有四行对比表时窗口顶上提示“这次的答案里没有……对比表”，让用户点“重新分析”（`hasComparisonTable` 有测试）。

### 验证与没验证
- Go：`go test -p 1 ./pkg/...`、`go vet`、`gofmt`；新增／改的测试：指标类型（创建、修改、列表筛选、目录）、集群只读已声明的模板、集群列表、模块列表的 Requests／Limits、详情里 Requests／Limits 不取模块设置、菜单与 BIZ 权限。
- 前端：`vue-tsc`、oxlint、oxfmt、单元测试（新增 AI 提示词、对比表检查）。
- 真浏览器（临时库、假 Prometheus、假语言模型）：模块列表的四列、命名空间与关键字筛选、进二级页、点 AI 按钮不发问、点“分析”才发问且答案是四行对比表、返回后筛选还在、集群大盘（1 个集群，14 / 16 已声明、没声明的两项列出）、指标模板的类型列／筛选／表单。
- **没验证**：真实 Prometheus（模块的 Requests／Limits 查询要自己写对：workload 级用 sum，并配副本数指标）、真实网关、PostgreSQL、MySQL；多个集群（只有一个数据源试过）；固定窗口范围不进地址。

## 资源大盘问题修复与需求调整（2026-11-23 第三次，大盘指标与历史指标分开、模块列表、推荐值）

用户这次的话是语音转写的需求清单（外加一份无关的考勤脚本，我没有处理它）。我的理解（**请纠正**）：“OM 指标”＝OOM；“查询区域（Program Circle）”＝大盘里的 PromQL 查询区；“全副本总和”＝所有副本加起来；“资源表格”＝详情里的 Requests／Limits 对照表。

### 没读到数据的原因（能从代码里查到的）
- **推荐值缺失**：`overprovision.Analyze` 在没有 Requests 时直接返回，所以模块没配 Requests 指标就既没有建议值也没有可用量；现在没有 Requests 时照样按使用量给出建议 Requests 和建议 Limits，只是状态为“数据不足”、不给超配比例。
- **模块列表少了模块**：页面请求 `page_size=500`，服务端上限 200 超了就改成 20，所以只列出 20 个；改成每页 200 逐页取到取完，并且不再只列启用的模块，模块管理里有的都列出来（禁用的标“已禁用”）。资源接口 `GET /overprovision/resources` 也改成所有模块。
- **Requests／Limits 和历史混用**：它们是模块的指标来源，同时会被当成历史指标采集；现在分开（下一节）。
- 真实环境里 Prometheus 读不到的其他原因（指标名、标签、网关超时）我没法在这里复现；每条查询的错误原因、步长、点数在“查询”折叠区里逐条列出。

### 大盘指标与历史指标（`MetricSource.Purpose`、`MetricSource.Per`）
- `purpose`：`dashboard`（只给资源大盘，向 Prometheus 实时读）、`history`（只给历史采集、预测、评分、保护）、`both`（两边都用，一个指标只写一次，例如 CPU／内存使用量）。旧数据的列默认 `both`，不动现有采集。新建时按指标种类取默认：`k8s_cpu_requests`、`k8s_cpu_limits`、`k8s_mem_requests` 是 `dashboard`；`k8s_mem_limits` 默认 `both`（OOM 防护从历史里读内存上限），可以改成只给大盘。三个只给大盘的种类不能选 `history`。
- `AlgorithmRun.ActiveSources()`：历史侧只取给历史的来源，大盘侧（`AlgorithmRun.Dashboard`）只取给大盘的来源。大盘的实时读取（`oplive.go`、`opresources.go`、`promstats.go`）用大盘侧。
- `per`：这条查询返回的是单 Pod（`pod`）还是全副本总和（`total`），空表示由指标模板的层级决定（workload 层级＝总和）。大盘用副本数在两者之间换算：总和除以副本数是单 Pod，单 Pod 乘以副本数是总和。模块列表的 Requests／Limits 和详情的序列都按它。
- 模块表单（`module-metrics.vue` 的 `scope="dashboard"`）多一栏“大盘指标（Requests / Limits）”：每种选一个指标模板、“查询结果为”（按模板层级／单 Pod／全副本总和）、“用途”（内存 Limits 可选“大盘和历史”，其他固定“仅大盘”）；每种只能有一条。保存时旧的 `both` 的 Requests／Limits 会改成 `dashboard`，即不再进历史采集。内存 Limits 从“OOM 监控指标”里挪到这一栏。
- **没做**：把 CPU／内存使用量、副本数、OOM 也拆成两套互不相干的配置——它们是大盘和历史都要的同一个指标，拆开只会让用户写两遍；用 `both` 表示一份配置两边用。

### 模块列表（资源大盘首页）
- 一行一个模块，只有名称（命名空间／工作负载在提示里和列里），四列 CPU Requests、CPU Limits、Memory Requests、Memory Limits；名称统一用原始指标名，不再翻成“请求上限”等。分页有上一页、下一页和页码，页码和每页条数留在列表状态里，从详情返回落在同一页；筛选条件一变回到第一页。
- 数据只来自模块管理里的模块（`GET /modules` 分页取全、禁用的也列）。

### 模块详情
- 最上面是“Requests / Limits：当前与推荐”表：资源项、当前值（单 Pod，旁边是总和）、单 Pod 推荐值（带相对当前值的涨跌）、全副本总和推荐值（单 Pod 推荐值乘以副本数）；没有的格子写“-”并在提示里说原因。当前值只来自 Prometheus。
- 推荐值：Requests 取 P95＋余量与 P99 的较大者（内存取 P99＋余量与最大值×系数的较大者），有 OOM 或限流时只升不降；Limits＝max(推荐 Requests, 最大使用量)×(1＋`overprovision.LimitHeadroom` 20%)，所以不低于 P99，内存 Limits 不低于推荐 Requests。
- CPU／内存使用率超过 200% 显示“> 200%”；表格放在自己的滚动框里，不会把页面撑出横向滚动条。
- OOM：数量卡片（0 绿、大于 0 红）、有 OOM 时的“OOM 时工作集”卡片；没配 OOM 指标时卡片上写到哪里加（模块管理的“OOM 监控指标”）。
- 查询区：每条查询的名称带指标种类，并标“单 Pod”或“全副本总和”，出错的写明原因。
- AI 提示词：表格列改成资源项、当前值、单 Pod 推荐值、全副本总和推荐值、依据；数据里带平台算的建议 Requests 和建议 Limits；`hasComparisonTable` 不变（四行名字）。

### 验证与没验证
- Go：`go test -p 1 ./pkg/...`、`go vet`、`gofmt`；新增：没有 Requests 也给建议和 Limits、来源的用途与单 Pod／总和、`per` 改变换算、所有模块（含禁用）都在资源接口里。
- 前端：`vue-tsc`、oxlint、oxfmt、单元测试（提示词的新列）。
- 真浏览器（临时库、假 Prometheus）：31 个模块分 3 页、行高都一样、上一页下一页和页码、禁用的模块在列表里；详情的当前与推荐表（有 Requests 的 shop-api 和没有的 gateway）；模块表单的大盘指标栏，改“单 Pod”和“仅大盘”并保存，库里和接口里都对，列表上 CPU Requests 不再除以副本数。
- **没验证**：真实 Prometheus（PromQL 要自己写对）、真实网关、PostgreSQL、MySQL；AI 窗口没重新点过（只改了提示词，单元测试覆盖）；多集群。

## 资源大盘问题修复与调整（2026-11-23 第四次，监控展示指标与算法计算指标、按模板动态读取、推荐表取整）

**这一节取代上一节里 `purpose` 的三个值（dashboard／history／both）和“Requests／Limits 不进历史采集”的做法**：用户这次明确要的是展示指标（Requests、Limits）也在历史数据和图表里，只是不进算法。

用户这次给了真实环境的截图（数据源与模块名从略），从截图里看到的原因：
- 用户建的“xx-cpu-requests / xx-cpu-limits / xx-memory-requests / xx-memory-limits”四个指标模板，指标种类选的是 **CPU / 内存**，不是 `k8s_*`；绑到模块上就成了同一种类（cpu、memory）的第二、第三个来源。大盘每种类只读第一条（`FirstOfKind`），所以查询区永远是原来的四条，Requests／Limits 读不到，使用率和可用量跟着为空。
- 这些来源还带着权重 1 混进了 CPU／内存的算法指标里。
- Limits 模板的 PromQL 是从 Grafana 贴来的：`cluster="$cluster"`、`namespace="$namespace"` 没有被替换，又同时有 `namespace="{{namespace}}"`，Prometheus 里什么也匹配不到。**这条需要用户自己改查询**（删掉 `$cluster`、`$namespace` 的匹配，用 `{{namespace}}`、`{{workload}}`）；平台现在在“没有数据”的错误里点出残留的 Grafana 变量。

### 监控展示指标与算法计算指标（`MetricSource.Purpose`：`display` / `algorithm`）
| 配置项 | 监控展示指标（display） | 算法计算指标（algorithm） |
|---|---|---|
| 资源大盘展示 | 支持 | 支持 |
| 历史数据展示、图表 | 支持（采集进历史） | 支持 |
| 指标查询与映射 | 需要 | 需要 |
| 算法权重 | 没有 | 有 |
| 算法启用／关闭 | 没有 | 有 |
| 参与预测计算 | 否 | 是 |
- `AlgorithmRun.ActiveSources()` 默认只给算法来源；大盘、历史采集（`Collect`）、OOM／JVM／Requests／Limits 这类周边指标的读取（`boundSet.sourcesOfKind`）用 `AllSources`，所以展示指标进历史、不进任何算法（`forecastOfResource`、算法的数据集、推荐说明 `sourceDetails` 都不含它）。
- Requests 与 Limits 四种（`DisplayKinds`）新建时默认 `display`；旧版本的值 `dashboard` 读作 `display`，`both`／`history`／空读作 `algorithm`（`NormalPurpose`）。
- 展示指标可以用**任何单位对的 CPU／内存模板**（`AcceptsMapping`：CPU 类 cores、内存类 bytes，不管模板建的时候选的种类），所以用户已有的四个模板不用重建。
- 模块表单：「算法计算指标」（有权重、启用开关，只给算法）和「监控展示指标（Requests / Limits）」（固定四行：CPU Requests／CPU Limits／Memory Requests／Memory Limits，各选模板和“查询结果为”单 Pod／全副本总和，没有权重、没有启用开关，不绑定就不显示）分开。算法计算指标里如果有模板名带 request／limit 且种类是 CPU／内存的行，会标黄提示“应放到监控展示指标里”，要用户手动删掉（不自动搬）。

### 大盘按模板动态读取
- `readLive` 读模块**每一个**启用的来源（不是每种一条）：查询区列出所有来源，写明指标模板名、种类、是否仅展示、单 Pod／全副本总和；同种类的第二条以后进“其他指标”卡片，带 P90／P95／P99。图和表用的“主”来源仍是每种的第一条。
- 一个查询没有数据而又带 `$变量` 时，错误里写明 Grafana 变量（`grafanaHint`）。

### 当前值与推荐值表
- 列：资源项（Requests（预留）、Limits（上限））、当前值（单 Pod）、当前值（全副本总和）、推荐值（单 Pod）、推荐值（全副本总和）；一个推荐值都没有（数据不足）时只剩前三列。
- “推荐值”的含义写在表头提示里：建议写进 Pod 的配置，不是已生效的；Requests 是调度预留（Reserved）的量，Limits 是上限。
- 取整（`utils/amount.ts`）：不到 1 核按整数 mCPU，1 核以上整数核；不到 1 GiB 按整数 MiB，1 GiB 以上整数 GiB。当前值四舍五入，推荐值向上取整（不会低于使用量要的），推荐的全副本总和 = 取整后的单 Pod 值 × 副本数。
- Requests 没查到时：使用率和可用量显示“-”（原来误判“请求低于使用量”）。

### AI 的推荐值
- 原来让模型自己换算，容易算错。现在推荐值由平台算好并取整，写进提示词的“平台推荐值”一节（每行：当前单 Pod／总和、推荐单 Pod／总和），要求表里**逐字使用**；表有六列（资源项、当前值两列、推荐值两列、依据）。
- 答案出来后 `missingRecommendations` 核对每一行的推荐数值是否原样出现，不一致时窗口顶上提示并让用户重新分析（和“缺四行对比表”的提示分开）。

### 其他
- 分析按钮用统一的实心主按钮（`type: primary`，和其他列表的编辑按钮一致）。
- P90／P95／P99 三条线各用一个颜色（紫、青、棕），不与使用、请求、建议、上限的颜色重复。

### 验证与没验证
- Go：`go test -p 1 ./pkg/...`、`go vet`、`gofmt`；新增：用途与读取范围、展示指标能用同单位的任意模板、模板绑定的 Requests 能读出、同种类的两个 CPU 来源都被查询并列出、展示指标进历史采集但不进算法、Grafana 变量提示、没有 Requests 也给推荐 Requests／Limits。
- 前端：`vue-tsc`、oxlint、oxfmt、单元测试（取整、AI 推荐值与核对）。
- 真浏览器（临时库、假 Prometheus、假语言模型，库里按用户的做法造了一个模块：四个 Requests／Limits 模板绑成 CPU／内存来源）：改前表里当前值“-”、推荐值有；模块表单里四行被标黄、删掉后在监控展示指标里绑定、保存；之后当前值出来（单 Pod 和总和），查询区列出七条（含四条“仅展示”），AI 答案的数值与平台不符时出提示。
- **没验证**：真实 Prometheus（用户 Limits 模板里的 `$cluster` 要自己改）、真实网关、PostgreSQL、MySQL；历史数据页在浏览器里没点过（展示指标会被采集进去，只有后端测试）；AI 答案和平台数值一致时的“无提示”路径只有单元测试。

## 资源大盘与模块管理优化（2026-11-24 第五次，推荐规则第二版、独立分析周期、动态图表、AI 发邮件）

**这一节取代上面“推荐值：Requests 取 P95＋余量与 P99 的较大者…”和“超配比例分级（≥50% / 30% / 15%）”的旧规则**（`FormulaVersion` 由 `op-1` 变成 `op-2`）。

### 用户这次给的真实数据，和原因
- 真实配置：Requests CPU 4 核、内存 6 GiB，Limits CPU 8 核、内存 16 GiB；大盘当前值却显示 4、9 核、7 GiB、17 GiB，全副本总和 48、96、72、192。
- 原因：Requests／Limits 的查询是全副本总和（12 个 Pod 的 48 核），平台用**就绪副本数 11** 去除，得到 4.36、8.73、6.55、16.0，再被上一版的“取整”变成 4、9、7、17。就绪副本比汇总进去的 Pod 少一个（还在启动或没就绪的 Pod 已经有 Requests）。
- 改法：①当前值不再取整，按 Prometheus 给的写（最多两位小数）；②`podsOfTotal` 在就绪副本附近（+0、+1、+2、−1、+3）取第一个除出来是“规格能写的数”的 Pod 数（CPU 不到 1 核按 10 mCPU、1 核起按 50 mCPU；内存不到 1 GiB 按 16 MiB、1 GiB 起按 64 MiB），都不是就退回就绪副本数；用在 Requests／Limits 的来源（按种类，不管用途）和资源列表页；③全副本总和直接用 Prometheus 汇总的值（`request_total`、`limit_total`），推荐的总和＝单 Pod 推荐 × 汇总的 Pod 数（总和 ÷ 单 Pod）。
- **这是按“数是不是规格能写的”猜的**：就绪副本数就是真实 Pod 数时不受影响；要让数完全可信，Requests／Limits 的查询最好按单 Pod 写（`max by (...)`）并在模块里选“查询结果为：单 Pod”。

### 推荐规则（第二版，`pkg/overprovision`）
- 三个分位数各有一个**带**（`Band`）：目标比例（推荐后该分位数占 Requests 的比例）、合理区间的下限和上限。默认：P90 目标 80%、区间 70%–85%；P95 目标 85%、区间 80%–90%；P99 目标 110%、区间 80%–120%（CPU 可以超出 Requests 到 Limits）。全部是系统参数（`analysis.rec_p90_target_percent`、`rec_p90_low_percent`、`rec_p90_high_percent`，P95、P99 同），页面“系统管理 > 系统设置 > 资源分析与推荐”里改。
- **推荐 Requests** ＝ P90、P95、P99 各自（使用量 ÷ 目标比例）的最大者（再按规格取整：CPU 1 核起 50 mCPU，内存 1 GiB 起 64 MiB）。**最大值（Max）不参与**，一次性的高峰不决定推荐。有 OOM、被限流时只升不降（地板：OOM 工作集×系数、现在的 Requests）。
- **状态**看三个分位数落在各自区间的哪里（`Shares` 是使用量占 Requests 的比例）：P95 或 P90 高于上限，或 P99 高于上限且 P95 不低于下限 → **欠配**；P95、P90 或 P99 低于下限 → **超配**；但平时低、P99 高于上限（偶发高峰）→ **合理**（原因 `burst_tail`），Requests 不下调；其余 → **合理**。例：Requests 4 核，P90 2.92、P95 3.22、P99 4.02（73%、80.5%、100.5%）→ 合理，推荐 3.79 核，写“合理，可优化 0.21 核（5.3%）”；内存 Requests 6 GiB、P95 8.18 GiB（136%）→ 欠配，推荐 9.94 GiB，欠配 3.94 GiB（65.6%）。
- 超配分级：超配的 Requests 里比推荐多出的占比 ≥50% 严重、≥30% 中度，其余轻度（`grade_severe_percent`、`grade_moderate_percent`；原来的轻度界限 `grade_mild_percent` 和 `cpu_margin_percent`、`memory_margin_percent`、`memory_peak_percent` 删掉了，迁移时删除这四行）。
- **推荐 Limits** ＝ max(推荐 Requests, P99 × `analysis.rec_limit_factor_percent` 120%（×1.2，第十一次由“再加的余量”改为系数））。
- 合理区间内：推荐 ≥ 现在时保持现在的值；推荐比现在低时给出推荐，写“可优化”的量。

### 独立的分析周期
- `analysis.baseline_window_days`（改名“推荐分析周期”，默认 7 天，取 1／7／15／30）是判定和推荐用的周期，**不随大盘选的图表窗口变化**：30 天里只有一天的高峰不会决定推荐。
- 详情接口 `GET /modules/:id/overprovision` 多一个 `recommend`（和 `windows[0]` 同形，周期是上面这个），`windows[0]` 仍是页面选的窗口（用量卡片、分位数表、图表、指标）；推荐周期比窗口长时另读一次 Prometheus（只读推荐用到的种类，和主读并行），否则从窗口的数据里切。详情缓存的版本里带参数和周期。`params` 带出各带的数。

### 页面
- “Requests / Limits：当前与推荐”表加“调整”列：Requests 行是状态加数量与比例（超配 0.5 cores (12.5%)、欠配 …、合理，可优化 …、合理，无需调整、受限（OOM 或限流）不下调）；Limits 行写“可降低／需提高 …”。表下一行写每个资源的 P90／P95／P99 使用量占 Requests 的比例和各自的合理区间；顶上写“推荐分析周期 7d（不随图表窗口变化）”。去掉“（预留）／（上限）”括号，意思写在表头提示里。
- 图表按模块绑的指标**动态生成**：CPU 和内存各一张单 Pod 图和一张全副本总和图（线：使用；横线：Requests、推荐 Requests、Limits、P90／P95／P99），JVM 的堆（used／committed／max）一张、非堆一张，GC、线程、限流、OOM 和其他指标各一张，没绑的不出现；一行两张，最后落单的一张占满整行（`.chart-grid`）。指标的序列由 `MetricStat.ts／value` 带出（≤300 点）。图例和表一律写 Requests、Limits、推荐 Requests，不再翻成“请求”“上限”“建议”。
- AI：点右下角按钮直接以放大窗口打开并开始分析（原来要再点“分析”）；有这个模块保存的答案时直接显示它。问题用推荐分析周期，提示词里“平台推荐值”一节每行带当前、推荐、**调整（超配／欠配／合理，带数量和比例）**，答案表七列。窗口里加“发送到邮箱”：`POST /assistant/mail-me {title, content}`，发给当前登录用户的账号邮箱和邮箱联系方式（收件人不从请求里取），走系统参数里的 SMTP；HTML 答案按 HTML 发，没有邮箱或没配 SMTP 时页面提示原因。接口权限在 `chatAPIs()`。
- 模块管理：表单里去掉“服务”（库里已有的值保留，不显示不改）；运行模式、启用、状态三列的标签统一成同一个大小、前面一个图标、一种颜色表一种意思（灰：观察／停用，蓝：仅推荐，橙：准入，绿：自动／启用）。
- 系统设置新增分类“资源分析与推荐”（`analysis.*`），带中文名和说明。

### 验证与没验证
- Go：`go test -p 1 ./pkg/...`、`go vet`、`gofmt` 全过；新增：推荐规则（目标、带、偶发高峰、可配置的带、无请求）、独立周期、Pod 数的取法、12 个 Pod 就绪 11 个仍是 4 核、邮件内容。前端：`vue-tsc`、单元测试 65 个全过（含取整改写、调整量、AI 提示词）。
- 真浏览器（临时库、假 Prometheus：12 个 Pod 的 Requests、11 个就绪；假语言模型）：当前值 4 核／8 核／6 GiB／16 GiB，总和 48／96／72／192；调整列和 P90／P95／P99 比例行；4 张图两列、9 张图最后一张占满整行；AI 按钮直接放大并开始分析；“发送到邮箱”在没有邮箱时给出原因；模块表单没有“服务”；运行模式／启用标签统一。
- **没验证**：真实 Prometheus 和真实 Pod 数（上面“猜”的取法）；真实 SMTP 发出去（只验证了没有邮箱时的错误路径和邮件内容的单元测试）；PostgreSQL、MySQL；推荐规则的默认带没有真实数据验证，都是系统参数；历史数据页。

## 副本数推荐、独立 AI 分析、邮件表格（2026-11-24 第六次）

**这一节取代上一节里“AI 的当前值、推荐值、调整都逐字抄平台推荐值”的做法**：AI 要独立分析，平台推荐只是对照。

### 副本数推荐（`pkg/overprovision/replicas.go`，纯函数 `PlanReplicas`）
- 同一份使用量有两种调整办法，分开算、分开列：①**调整副本数**（每个 Pod 的 Requests 不变）；②**调整单 Pod 配置**（副本数不变，就是原来的推荐 Requests／Limits）。**优先推荐副本数**，副本数调不动才推荐改 Pod 配置。
- 方案①的算法：每个资源取 P90、P95、P99 各自的（全副本使用量总和 ÷（每 Pod Requests × 该分位数的目标比例））的最大者，再向上取整；有 Limits 时另加保护：全副本 P99 ÷（Limits × 90%）；CPU 和内存取最大者，决定的资源写在 `binding`。它假设各 Pod 平均分担负载、单 Pod 使用量随副本数反比变化；每个 Pod 都要付一份的部分（JVM 堆）不按这个规律变，页面和 AI 提示词里都写明。
- **约束只来自模块管理里这个模块自己配置的，平台不另设系统参数**（上一版加过 `analysis.rec_min_replicas`、`analysis.rec_max_scale_in_percent`，用户指出模块里已经配了，已删除，迁移时也删这两行）：最少副本数（`min_replicas`）、最多副本数（`max_replicas`）、扩容和缩容每次最多增减的副本数（`scale_up_max_step`、`scale_down_max_step`）；固定副本数（`fixed_replicas`）的模块没有调整空间。没配的约束不套用任何默认数。唯一平台自己的规则：CPU 被限流或内存出过 OOM 时不减副本（`replicas_limited`）。原因码：`replicas_floor`、`replicas_ceiling`、`replicas_step`（都是模块的约束）、`replicas_limited`、`replicas_limit`、`replicas_no_current`、`replicas_no_total`。结果的 `limits` 带出算时用的约束。
- 结果 `replica_plan`（在 `recommend` 里）：`current`、`needed`、`raw`、`direction`（out／in／same／unknown）、`delta`、`share`、`binding`、`resources`（各资源要几个）、`primary`（replicas／config／none）、`reasons`、`formula`。没有全副本使用量时不给计划（`replicas_no_total`）；没有当前副本数时只给数量不判增减。

### 当前副本数（继承 Kubernetes 实际值）
- **用户指出的问题**：大盘上的副本数和实际副本数不一样（他的 Deployment 是 12 个副本，大盘写 11）。原因：大盘的副本数是模块里“副本数”指标的值，常见模板是 `kube_deployment_status_replicas_available`（就绪的），一个还在启动的 Pod 不算，而 Requests 的总和里有它。
- 现在的取法（`OverprovisionDetail`）：①优先取 operator 最近从 Kubernetes API 读到的 `spec.replicas`（`workload_config.desired`，operator 每 5 分钟读一次，超过 15 分钟的报告不算“现在”，没报告、找不到工作负载或读取失败也不用），`replicas_from: kubernetes`，同时带出就绪数 `replicas_ready`；②没有则取模块副本数指标的 Prometheus 最新值（`prometheus`，通常是就绪的，页面写明可能比 Kubernetes 设定的少）；③再没有取平台保存的最近一次实测采样（`stored`，有年龄上限，页面标橙）；都没有就是没有。**不用预测、推荐值或过期结果代替。** 同时带出 `replicas_at`、`replicas_min`／`replicas_max`（推荐周期内最少与最多）。
- Requests／Limits 总和除以的 Pod 数：有 Kubernetes 的副本数时就用它做起点（12 个 Pod 的 48 核＝4 核），不再靠就绪副本数猜（`opLoader.podsHint`）。
- **没有真实环境验证**：operator 没部署或没有报告时仍是 Prometheus 的就绪副本数，此时和实际的差异依旧存在，页面的来源一行会写明。
- 页面“副本数”卡片写来源、测量时间和周期内的范围：一处写 8、另一处写 22 这类差异，用这个范围就能看出是不是副本数在周期内真的变过（HPA 或手动扩缩）。**没有复现过“卡片墙 8 个、别处 22 个”**，没有找到另一个用不同口径算副本的地方；原因没查实，只是把口径统一成一个并写明来源、时间和范围。
- 大盘（板）上的合计用存下的分析行里的副本数（定时任务读存下的实测样本，不用预测）。

### 页面
- “副本数与推荐方案”表（第二版，用户指出第一版有横向滚动条、第一列被挤成竖排）：**一行一个项目**，列是方案、项目、当前值、推荐值、调整，共 5 列，方案列占多行（rowspan）；每个方案三行：副本数、CPU Requests（单 Pod）、Memory Requests（单 Pod）。①调整副本数（Pod 配置不变，优先标签）的 Requests 两行写“Pod 配置不变”，②调整单 Pod 配置（副本数不变）的副本数行写“副本数不变”。表用固定列宽，标签在格子里换行，**页面里这几张表不出现横向滚动条**（1280 和 1650 宽度下都量过）。
- **推荐副本数的依据**（不再是一行英文算式）：一张表，每个资源一行，列是每个 Pod 的 Requests、P90、P95、P99（各写“全副本使用 ÷（Requests × 目标比例）＝ 需要几个”）、Limits 保护（P99 ÷（Limits × 90%））、需要副本数（决定的那个资源带“决定”标签）；下面写：取最大的规则、模块管理里的副本数约束（没配就写没配）、推荐周期内实际副本数的最少与最多、触发的原因（最少、最多、每次最多增减、OOM／限流）。后端 `ReplicaNeed` 带 `request`、`limit`、`parts`（各分位数的使用量、目标比例、需要数）、`limit_needed`。
- **颜色统一**：绿＝合理无需调整；橙＝多于需要（减少副本、降低 Requests、可降低的 Limits）；红＝少于需要（增加副本、提高 Requests）；蓝＝仅提示（OOM、限流、数据不足）；Limits 行也按这套（原来是灰）。
- **“超配 300CM”**：原话是语音转写，读作“超配 300 mCPU”（CPU 不到 1 核按 mCPU 写）这类带单位的超配量，**没有在代码里找到“CM”这个单位**。每个“调整”后面加了“?”：写含义、算式（差 = |当前 − 推荐|，占比 = 差 ÷ 当前，带上这一行的实际数）、P90／P95／P99 占 Requests 的比例与合理区间、推荐值的算式、单位（CPU 核，1 core = 1000 mCPU；内存二进制，1 GiB = 1024 MiB；都是单 Pod 的量）和数据来源（Prometheus、推荐分析周期，当前值不取整）。

### AI 助手
- 提示词（`utils/overprovision-ai.ts`）改为：独立分析，用“实测数据”（分位数、峰值、OOM、限流、JVM、副本变化、当前副本数）自己得出结论并写算式；平台推荐放在最后一节“仅供对照，不要照抄”；两种方案都给，优先副本数；安全边界（副本不低于 2、一次最多减一半、有 OOM 或限流不减不降、缩容后单 Pod P99 不接近 Limits、最大值不作依据、内存比 CPU 保守）；第一张表五行（副本数、CPU Requests／Limits、Memory Requests／Limits）七列，第二张表“AI 与平台推荐的差异”（项、平台推荐、AI 推荐、差异、原因，一致写“一致”，不一致写原因）；目标比例作为参考给出，允许依据数据另作判断并说明。后端助手的系统提示词（`pkg/assistant/service.go`）也加了同样的几条通用规则。
- **第二版（用户指出 AI 报告“项”的语义不对）**：第一版的差异表把两种方案混在一起（“主方案 11 个，备选 12 个”）。现在提示词规定：第一张表五行，“副本数（方案①）”写推荐几个副本（Pod 配置不变），“CPU／Memory Requests／Limits（方案②）”写副本数不变时每个 Pod 的推荐值，一个格子只写一个数，依据每行从不同角度写（分位数、趋势或波动、约束、风险）；第二张表的行和第一张一一对应，项写同样的名称，平台推荐写平台同一方案同一项的数，AI 推荐写第一张表同一行的数，不写“主方案”“备选”，不拿不同方案的数相比。最后一节“平台推荐”也按方案①、方案②分行写。AI 窗口里的表格用固定布局，窄窗口里也不超出窗口。
- 页面不再要求答案里写出平台的数（去掉“答案里的推荐值和平台算出的不一致”的提示）；仍检查答案里有五行对比表（含副本数）。**没验证**：真实语言模型是否照着要求答（只用假模型验证了页面、提示词内容和长度不超过 3900 字）。

### 邮件
- `POST /assistant/mail-me` 多一个 `tables`（`pkg/notify/mailstyle.go` 的 `MailTable`）：页面把平台自己的两张表（副本数与推荐方案、当前与推荐配置，带颜色）一起发给后端，邮件里一定有表格，不依赖模型有没有写表；HTML 答案里的表格、表头、单元格、标题和 lv-* 颜色都改成写在标签上的样式（邮件客户端不读页面的样式表，原来表格会变成没有边框的文字）；Markdown 或文本答案也包进 HTML；文字版同样有表格的行。收件人仍然只取当前登录用户的邮箱，不从请求里取；表格最多 6 张、每张 40 行、12 列，单元格 300 字。
- **邮件的版式只是按页面的样式做的，用户说“具体版式以随后的截图为准”，截图到了再调。**

### 验证与没验证
- Go：`go test -p 1 ./pkg/...`、`go vet`、`gofmt` 全过；新增：副本数推荐（P95 决定、内存决定、缩容的下限与步长、OOM 不减、Limits 保护、没有全副本使用量、没有当前副本数）、详情带出当前副本数与计划、邮件表格与内联样式。前端：`vue-tsc`、`oxlint`、单元测试 83 个全过。
- 真浏览器（临时库、假 Prometheus、假语言模型）：方案表两行、优先标签、卡片（当前副本数的来源、时间、范围，推荐副本数）、颜色、“?”的内容、AI 直接放大并分析、答案里的两张表；邮件 HTML 单独渲染成图确认有边框和颜色。**没有真发邮件**（没有邮箱时的错误路径验证过）。
- **没验证**：真实 Prometheus 和真实的副本数指标；真实模型的答案；副本数方案对每个 Pod 都要付一份的内存（JVM）不成立，页面只是写明，没有按 JVM 数据自动修正；PostgreSQL、MySQL。

## 系统功能调整与优化（2026-11-24 第七次：AI 交互、模块管理分区与计算系数、卡片）

### AI 助手
- 点窗口外的任意位置收起窗口（`.ai-backdrop`，窗口和按钮在它上面）；分析不会自动开始：进入页面不问，点右下角按钮只打开放大的窗口、也不问，**只有窗口里的“AI 分析”按钮才问**（有保存的答案时直接显示，按钮写“重新分析”）。去掉了第四次里的“按钮打开即分析”。
- 公式排版：页面里的公式用 `formula-view.vue`（`utils/formula.ts`）一步一行，名称、数字、运算符、系数（橙色，悬停写来源）、结果（绿色）各有样式，不再是一段文字（“?”里的说明、“依据”折叠里都用它）。AI 的提示词要求答案里的公式写成 `<div class="fx">` 一步一行、系数 `<span class="fx-c">`、结果 `<span class="fx-r">`（`safe-html` 放行这四个类，邮件里 `InlineMailStyles` 把它们写成标签样式）。提示词里给出“模块的计算系数”（容量保留系数、推荐值系数、OOM 保护系数、看板目标占比、Limits 余量），并写明推荐值是**除以**系数。英文提示词缩短到不会被 3900 字截断；超长时先丢“其他指标”，规则和平台对照数保持完整。

### 模块管理
- 表单按“一节一个主题”分成带边框的区块，每节有标题、说明和（需要的）“?”，一节里的字段按宽度并排（不再是一根很长的单列）：基本信息、数据与预测周期、算法计算指标、监控绑定（大盘展示指标）、统计周期与时间段、容量与副本、推荐的应用、降级（Fallback）默认值恢复与副本数边界、副本调整节奏、计算系数、OOM 保护与 JVM 压力、推荐值的计算与边界。没有用页签。
- **容量与副本**：副本数算法、**单 Pod 安全容量（RPS = Requests Per Second，每秒请求数，说明里写明）**、动态模式的 Pod 请求量；下面四张卡片按填写的值现算：单 Pod 请求利用率（＝容量保留系数）、单 Pod 安全容量、最小安全总容量（最小副本数 × 单 Pod 安全容量）、最大安全总容量。
- **降级（Fallback）**：降级默认值和恢复原样保留，并且把最小副本数、最大副本数、最大副本数方式、硬上限、扩展比例也放进这一节（字段、校验、逻辑都没变，只是挪了位置）。
- **副本调整节奏**：字段和逻辑不变，说明里写明它作用于完整的运行周期、不分时间段（“应用于完整运行周期”读作：节奏限制在整个运行周期内都生效，没有按时间段区分，不是另有一套新逻辑）。
- 算法计算指标、监控绑定、统计周期与时间段、推荐值与计算边界：配置与逻辑原样保留，只是分区。

### 计算系数（新的独立区域，按模块设置，公式读这里的值）
| 系数 | 字段 | 用在哪里 | 默认 |
|---|---|---|---|
| 容量保留系数 | `target_utilization` | CPU、内存推荐值 ＝ 预测峰值 × 推荐值系数 **÷** 它（`decision.Policy.CapacityReserve`，打分用的结果 `outcomeOf` 同）；动态副本的单 Pod 承载 ＝ Pod 请求量 × 它（原来的用法） | 0（按 1，不保留） |
| 推荐值系数 | `headroom_ratio` | 预测峰值要乘的倍数（原“余量系数”） | 1 |
| 看板 P90／P95／P99 目标占比 | `rec_p90_target`、`rec_p95_target`、`rec_p99_target` | 资源大盘推荐 Requests ＝ 三个分位数使用量各除以自己的占比取最大；覆盖系统设置 `analysis.rec_*_target_percent` | 0（跟随系统） |
| 看板 Limits 余量 | `rec_limit_headroom` | 推荐 Limits ＝ max(推荐 Requests，P99 × (1+余量)) | 0（跟随系统） |
| OOM 保护系数 | `oom_memory_uplift`（原有） | OOM 后内存地板 ＝ 底数 × 它；也覆盖大盘分析里的 OOM 系数 | 1.2 |
| OOM 计算系数（三种底数） | `oom_calc_working_set_ratio`、`oom_calc_limit_ratio`、`oom_calc_history_ratio` | 地板的底数 ＝ max(工作集 × 系数, 内存上限折回 request × 系数)，两者都没有时用内存历史 × 系数 | 1、1、1（行为不变） |
| 保底系数 | `floor_ratio` | CPU、内存推荐不低于“模块声明的 Pod 请求量 × 它”（并入最小值，取较大者） | 0（不启用） |
| 预测偏差上限 | `max_shortfall` | 原来写死的 0.5：实测峰值偏低比例超过它就按它算，预测最多放大 s/(1−s) 倍（`decision.SolverConfig.MaxUncertainty`） | 0.5（行为不变） |
- 表单里“公式”预览随填写的值实时变化（`moduleFormulaLines`）。所有新字段范围在 `ForecastModule.Validate` 里校验，创建和更新接口都带。
- **没有写死的数还有**：评估目标（及时率 0.9、紧急占比 0.2、不足峰值 0.15）、`DefaultPercentile` 0.9 和提前量参数（已是系统参数，管理员可调）、置信度权重 0.8、一致度 0.75／0.9 等：它们不是“推荐值 ＝ 计算结果 ×／÷ 系数”里的系数，没有搬到模块里。
- **0.7 的迁移**：旧表单把 0.7 写进了每个保存过的模块的 `target_utilization`，但这个字段只在“动态副本”下显示。新的推荐值要除以它，所以迁移把“副本数模式不是动态、值正好 0.7”的清成 0（没有人看过、选过这个数）；动态副本模块、其他数值的原样保留。新建模块默认 0。

### 卡片墙
- 资源大盘的卡片墙新增“容量保留系数”卡片：值就是模块管理里这个模块的值（接口 `coefficients.capacity_reserve`，没配写“未配置（按 1）”）；模块保存会让详情缓存失效（缓存版本含模块的更新时间），所以改了系数后再看大盘就是新值。
- **“卡片墙下方只有 4 个卡片”读作资源大盘的卡片墙**（不是别的页面）；如果指的是别处，请告诉我是哪一页。

### 验证与没验证
- Go：`go test` 新增：推荐值＝需要除以容量保留系数（含默认与非法值）、OOM 底数系数、保底系数、预测偏差上限、模块系数覆盖系统设置、详情带出模块系数、字段范围、0.7 迁移、公式样式写在标签上。前端：新增 `formula`、AI 提示词系数与公式要求的测试。
- 真浏览器（临时库、假 Prometheus、假语言模型）：见 `platform-progress.md` 这一条。
- **没验证**：真实环境里 CPU、内存推荐值除以容量保留系数后的数（动态副本的模块如果设了 0.8，它们的推荐值会变大约 25%，这是你说的“÷ 系数”的结果）；真实模型是否按提示词排版公式。

## 系统功能调整与优化（2026-11-24 第八次：参数解释、配置分组、OOM 与 JVM 拆分、模块策略、副本数依据、曲线筛选）

### 模块管理：四组配置，十六个区块（没有页签）
- 表单按用户给的四个区域分组，每组一条标题栏，组内是带说明的区块：**基础配置**（基本信息、数据与预测周期、算法计算指标、监控展示指标）、**计算参数**（容量与副本、计算系数、推荐值的计算与边界、OOM 保护、JVM 压力）、**安全边界**（副本边界、副本调整节奏、降级 Fallback、推荐的有效期与评分）、**模块策略**（预测模式、时间周期、指标触发策略、固定副本数与多策略合并）。
- 预测模式（固定算法、动态算法、不预测）从“基本信息”挪到“模块策略”，与运行模式（观察、推荐、准入、自动）分开说明；字段和逻辑不变。

### 容量与副本卡片：含义、来源、公式、统计周期、状态
- 四张卡片（单 Pod 请求利用率、单 Pod 安全容量、最小／最大安全总容量）各有“?”：含义、数据来源、公式、统计周期。状态分五种：**已计算**、**未设置**、**无历史数据**、**数据不足**、**计算异常**（另有“保存后计算”：新模块没有历史，不显示没有依据的数字，也不显示默认的 90% 之类）。
- 新接口 `GET /modules/:id/capacity`（`forecastsvc.CapacityReportOf`）：填写的值、历史估计的值（`state`、`note`）、估计用的样本数和有效样本数；估计规则的数都有出处：回看 7 天、忙碌时刻＝Pod CPU 达到“容量保留系数 × 请求值”的 70%（`decision.BusyShare`）、至少 30 个忙碌时刻（`MinCapacitySamples`）、取 P50；不够就写缺什么，不编数。
- “90%”的来源：单 Pod 请求利用率卡片显示的就是**容量保留系数**（计算系数里填的），没填就显示“未设置，按 100%”，不再有默认值。

### 副本边界与降级
- “副本边界”独立成节（正常扩缩容和降级**共同**遵守）：最少副本数、最多副本数、方式（固定／动态扩展）、硬上限（只在动态扩展下显示和使用）、上限扩展速度。表单下面一句话随填写的值变化：例如“平时最多 15 个；预测需要更多时，上限每个周期最多抬高 4 个，最高到硬上限 25”。
- **15 和 25 的关系**：最多副本数 15 是日常上限；方式为“动态扩展”时，预测需要的副本超过 15，上限才逐周期抬高（每周期最多当前上限的“上限扩展速度”），最高到硬上限 25；方式为“固定”时硬上限不起作用，也不显示。
- **“每周期最大调整比例”**（现在叫“上限扩展速度”）抬高的是**允许的最大副本数**，“副本调整节奏”限制的是**一次动作增减几个副本、隔多久**：两者不是同一件事，说明里写明，没有合并。
- 降级（Fallback）只留降级默认值（CPU、内存、GPU、副本数）和恢复需要的连续周期数；降级给出的副本数同样受副本边界约束（`decideField` 的 clamp）。固定副本数挪到“模块策略”。
- 副本调整节奏：字段不变，说明里写明正常扩缩容和降级都适用、不分时间段。

### 计算系数：每个都有定义、作用、公式、示例、适用范围
- 每个系数旁的“?”（`param-help.vue`）五个部分都有：定义、作用、计算公式、示例（输入是样例 4、3 等，系数是表单里**现在**的值，改了就变）、适用范围。文本在 `page.forecast.params.*`，公式在 `utils/param-docs.ts`。例：推荐值系数 `预测峰值 4 × 1.2 = 4.8`，再 `÷ 容量保留系数`。
- 新增 **Requests 余量**（CPU、内存各一个：`rec_cpu_request_headroom`、`rec_memory_request_headroom`）：推荐 Requests ＝ 三个分位数的最大需求 × (1 + 余量)，默认 0（不加）。
- **Limits 余量改成 CPU、内存各一个**（`rec_cpu_limit_headroom`、`rec_memory_limit_headroom`，原 `rec_limit_headroom`）：0 跟随系统；迁移把原来的值复制到两个新字段并清空旧列，只做一次。去掉了“看板余量”这类说法，叫“推荐 Requests／Limits 余量（CPU／内存）”。
- 新增 **Limits 峰值占比上限**（`rec_limit_guard`，系统参数 `analysis.rec_limit_guard_percent` 默认 90，模块 0 跟随系统）：原来代码里写死的 0.9，用在“所需副本数 ≥ ⌈P99 总用量 ÷ (Pod Limits × 占比)⌉”和风险判断里。JVM 的“接近堆上限”提示也不再用 0.9，用模块的堆压力阈值。
- OOM 保护系数、OOM 计算系数（三种底数）搬到“OOM 保护”里；预测偏差上限（原来说的“OM 计算系数”里的预测偏差上限）和保底系数留在计算系数里并各自解释。
- **一次性迁移的修正**：清掉隐藏的 0.7 原来每次启动都跑，会把有意设成 0.7 的值清掉；现在用系统参数行 `migration.capacity_reserve_cleared` 做标记，只跑一次。

### OOM 保护与 JVM 压力拆成两个区块
- **OOM 保护**：追溯天数、OOM 保护系数、OOM 计算系数（工作集、内存上限、内存历史）、OOM 计入评分的开关与权重、OOM 指标。资源大盘只展示**发生次数、发生时间点、评分**（新接口字段 `oom`：`kills`、`events`、`score`），不展示计算系数。评分＝100 × (1 − 次数 × 权重)，未启用权重时写“未启用”。
- **JVM 压力**：堆压力阈值（0 关闭）、JVM 指标、计算方式（堆占用率＝堆已用 ÷ 堆上限；压力评分＝100 × (1 − 占用率 ÷ 阈值)）。大盘 JVM 分组多了“堆占用率（P99）”和“JVM 压力评分”两张卡片（`jvm.ratio`、`jvm.score`）。
- 监控指标有了**类型**：算法计算指标、OOM 指标、JVM 指标三个列表各自只读自己类型的指标（OOM：`oom`、`oom_working_set`；JVM：`jvm_*`），每一行有类型标签（OOM／JVM／资源／K8s／GPU／网络／其他）。

### 推荐值与计算边界
- “保底值”就是最低资源保障：计算结果低于它就按它（例：需求 2000、保底值 3000，推荐 3000）。单位与推荐值一致（CPU：毫核 m，内存：MiB），**按每个 Pod 计**。生效顺序写在表单里：计算结果（已除以容量保留系数）→ 不低于“保底值”与“保底系数 × 声明的 Pod 请求量”中较大的一个 → 内存发生过 OOM 时不低于 OOM 地板 → 不高于封顶值（封顶值有最后的决定权）→ Limits ＝ 最终 Requests × limit 系数（只在准入时）。这个顺序就是 `decision.Decide` 的实际顺序。

### 资源大盘
- **副本数**：只取 Kubernetes 里设置的期望副本数（operator 读 `spec.replicas`），不按 Prometheus 历史时间序列的条数算；期望副本数和**就绪副本数**是两张卡片（就绪低于期望时写还有几个没就绪）。没有 operator 的报告时才退到 Prometheus 的副本数指标，再退到平台存的最近读数，来源写在卡片上。
- **计算口径保留**：单 Pod 和全副本两种口径的卡片、表和图都还在。
- **推荐方案**：两个方案各自一个块，并排；每块有标题、一行说明（“每个 Pod 的 Requests 不变，只改副本数”／“副本数保持现在的，只改每个 Pod 的 Requests”）、“?”里的详细说明，表只有当前值、推荐值、调整结果三列，颜色规则不变。原来占大面积的“推荐副本数的依据”表去掉了，依据在方案一的“计算依据”按钮里。
- **计算依据弹窗**（`replica-basis-modal.vue`）：当前与推荐副本数（当前的来源与就绪数）；每种资源的 Pod 规格、P90／P95／P99 全副本使用量、各自的目标占比（系数）、公式一步一行（`utils/replica-basis.ts`）、Limits 保护一行、取最大并向上取整的结果；用到的系数及设置位置；安全边界每一条（最少、最多、单次最多增减、OOM 与限流保护、Limits 保护）是**已生效**、**未触发**还是**没有限制**。
- **曲线筛选**：图上方的分组勾选（实际使用量、配置值 Requests／Limits、推荐值、分位数 P90／P95／P99），一次选择对单 Pod 和全副本的所有图生效，保存在浏览器里；默认不画分位数。Requests（红色虚线）、Limits（橙色点划线）、推荐 Requests（绿色实线加菱形标记，画在最上面）、推荐 Limits（浅绿点划线）画法各不相同；Requests 与推荐 Requests 相差不到 1% 时图的说明里写“重合”。

### 模块策略（新增的指标触发策略与多策略合并）
- **结构**：不拆成另一个业务系统：在模块管理里编辑，数据单独存（表 `module_trigger`，模型 `model.ModuleTrigger`，接口 `/modules/:id/triggers`），不往模块基本信息里堆参数。
- **指标触发策略**：每条＝“当［指标］的［最新／平均／最大／最小］在最近［N］分钟［大于／大于等于／小于／小于等于］［阈值］时，就［扩容／缩容／设定为］［数量，个数或百分比］”，可以限定星期和时段，有名称、启用、优先级。指标可以是模块里任何有历史的指标，或 `jvm_heap_pressure`（堆占用率）。例如“CPU 使用率达到 80% 扩容”（需要模块有 `cpu_util` 指标）、“JVM 压力达到 90% 扩容”。
- **多策略怎么合并**（`decision.ApplyTriggers`，其前后各步的顺序固定）：①旁路、时间周期里“固定”的副本数、模块的固定副本数是管理员的决定，触发策略不改；②其余的先由预测（预测模式“不预测”或预测不可用时用降级默认值）得出；③触发策略：设定副本数的取优先级最高的（同级取较大的）；扩容的取增量最大的，它不会被设定、预测或缩容压低；缩容只在没有扩容或设定、没有 OOM 和 JVM 压力、预测不要求比现在更多的副本时才生效，多条缩容取减得最少的；④时间周期里“至少”抬高、“至多”压低；⑤副本边界（最少、最多）对最终结果生效，永远在最后，然后是副本调整节奏。结果的来源写 `trigger`，推荐的依据里有每条触发策略的状态（`triggers`）和合并过程（`trigger_outcome`）。
- 触发策略只改副本数，不改 CPU、内存的 Requests 和 Limits（那些只在准入时设）。
- 已有的“时间周期”（每周重复或绝对时间、固定／至少／至多、优先级）就是时间周期策略，原样保留并搬进“模块策略”。

### 验证与没验证
- Go：新增 `decision.ApplyTriggers` 的单元测试（八个场景）、`forecastsvc` 指标触发的集成测试（JVM 堆触发设定副本、被模块最大值限制、关闭后不触发）、容量报告接口测试、触发策略接口校验测试、Requests／Limits 余量按资源测试、Limits 余量迁移测试；`go test -p 1 ./pkg/...` 全过。前端：`param-docs`、`replica-basis`、`formula` 的单元测试。
- **没验证**：真实 Prometheus 和真实集群里的触发策略（只用假数据和 fake client）；operator 一侧对 `trigger` 来源的副本数没有改动（它读推荐里的副本数，不区分来源）；真实环境里除以容量保留系数后的推荐值；PostgreSQL、MySQL。

## 系统功能调整与优化（2026-11-24 第九次：当前副本数、两个方案上下排、三个计算口径、依据按方案独立、曲线重合）

### 当前副本数 22 与实际 12 不一致
- **原因（按截图读）**：K8s 里这个 Deployment 在“Updating”（11/12 就绪）；资源大盘上 Requests 的全副本总和是 96 核＝24 个 Pod × 4 核，副本数指标是 22。发布期间新旧两个 ReplicaSet 的 Pod 同时存在，Prometheus（kube-state-metrics 一类的指标）数的是**存在的 Pod**，不是工作负载设置的副本数；而使用量的总和是 12 个 Pod 的（28.5 核 ÷ 每 Pod 2.37 核）。所以只看监控时序数量得到的 22，是新旧 Pod 并存的数。
- **现在**：当前副本数只取 Kubernetes 里设置的期望副本数（operator 上报，`WorkloadReplicas`）；上报里的就绪数、不就绪的 Pod（异常副本数）、已存在的 Pod 数、是否在发布中（`updated` 小于期望，或 Pod 数多于期望）都单独显示。大盘多了“期望副本数、就绪副本数、异常副本数”三张卡片，发布中会写“Pod 共 N 个，新旧并存”。
- **每 Pod 的 Requests**：Requests 的总和是按**存在的 Pod 数**（上报的 Pod 数）除，所以发布期间仍是 4 核，不是 96 ÷ 12 ＝ 8 核。
- **没有 operator 的上报时**：Prometheus 的最新值比这个周期平时的副本数（中位数）高出 2 个且超过 25% 时，不当作副本数，改用平时的数并标成“估算”，写出 Prometheus 最新值和“operator 没有上报／上报太旧／找不到工作负载／读取出错”的原因；其他情况（读到的值正常）仍用 Prometheus 的值。这只是兜底，Kubernetes 的读数才是准的。
- 推荐副本数的计算（`decide`）、成本口径（`costReplicas`）也先取 Kubernetes 的期望副本数。

### 推荐方案：上下两个块，每个块有三个口径
- “调整副本数”和“调整单 Pod 配置”上下排成两块（不再左右两列），原来重复的“Requests／Limits 当前与推荐”表并入第二块（Requests、Limits 各一行，按页面上的单 Pod／全副本口径显示）。
- 每块的表：当前值｜P90｜P95｜P99｜综合｜调整。P90、P95、P99 各自独立算（后端 `ReplicaPlan.by_caliber`、`Result.candidates`），“综合”是三者里需求最大的一个（平台的推荐值，再经过模块的边界、OOM／限流／Limits 保护），表头上方的“计算口径”单选（综合、P90、P95、P99）决定“调整”一列和“计算依据”用哪个口径，选中的一列有底色。决定综合结果的百分位（Requests 行）、决定副本数的资源（CPU 或内存）有“决定”标记。
- 调整副本数只改副本数（不改 Pod 的 Requests、Limits），调整单 Pod 配置只改 Requests、Limits（副本数保持），两块的依据各算各的，不混用。

### 计算依据：按方案独立、单位分开
- 依据弹窗按所点的方案分开：**输入数据**表（CPU、内存各一行单 Pod 和一行全副本：P90、P95、P99 使用量，Requests、Limits）、**副本数**（当前、就绪、异常、来源）、**系数**表（值、单位与含义、来源）、**计算过程**（公式一步一行）、**限制**（副本方案：最小、最大、单次增减、OOM／限流保护、Limits 保护是否生效；单 Pod 方案：为什么调或不调）。
- 单位不混：全副本 CPU 使用量（核）÷（单 Pod Requests（核）× 目标占比（%））＝所需副本数（个副本）；使用量、Requests、Limits 带各自单位，系数写成百分比，副本数写成“N 个副本”。
- 单 Pod 方案：每个百分位的使用量 ÷ 目标占比、取最大再乘（1＋Requests 余量）、OOM 和限流的下限、向上取整、Limits＝max(推荐 Requests，P99 × (1＋Limits 余量))；选了某个口径就只列它。
- 为什么 CPU 4 核“合理”：P90、P95、P99 各自要的值在表里并排，某个口径的值低于现在也不等于要调，综合结果在“合理”区间里的 Requests 不为小差别改动（弹窗里有说明）。

### 曲线重合
- Requests 与推荐 Requests（以及其他数值相差不到 1% 的线）画成条纹：第一条最宽在最下面，后面的更细、在上面，每一条的边都能看到；Requests 红色虚线、Limits 橙色点划线、推荐 Requests 绿色实线加菱形、推荐 Limits 浅绿点划线。分组筛选和图例的单独开关保留。

### JVM 的叫法
- 需求里的“GVM”是笔误，统一叫 JVM（页面、文档里都是 JVM）。

### 验证与没验证
- Go：`go test -p 1 ./pkg/...`；新增：每个口径的副本方案、每个百分位的 Requests、发布期间的副本数（有上报时是期望数，没有上报时按平时的数估算，Requests 按存在的 Pod 数除）。前端：`config-basis`、`replica-basis` 的单元测试。
- **没验证**：真实 operator 的上报（只有写进库的上报）、真实 Prometheus；发布期间的 22 只是按截图推出来的原因。

## 系统功能调整与优化（2026-11-25 第十次：当前配置的来源、推荐副本数与计算一致、持续使用量、OOM 与 Limits 依据）

### 当前 Requests／Limits：5.82 核、23 GiB 是怎么来的
- **原因（按截图读）**：Pod 的 Requests 原来是“最新一个总和 ÷ 猜出来的 Pod 数”。发布期间总和是 32 个 Pod 的（128 核、192 GiB），副本数指标是 22，猜的范围是 22 附近 ±3，找不到整数 Pod 数让 CPU 是规格里常见的数，就退回 22：128 ÷ 22 ＝ 5.82；内存 192 ÷ 24 ＝ 8 GiB（24 恰好“像”规格），Limits 同理 11.64、23.27。CPU 的 5.82、11.64 和内存 Limits 的 23.27 都是真值的 1.4545 倍（32 ÷ 22）；内存 Requests 的 8 GiB 是 192 ÷ 24，24 恰好得到一个像规格的数，真值是 6 GiB（192 ÷ 32）。
- **现在**：
  1. 有 operator 上报时，单 Pod 的 Requests／Limits 直接用上报的 Pod 模板（各容器相加，Limits 只在每个容器都设了才算），来源写“Kubernetes”；全副本的 Requests／Limits＝单 Pod × 工作负载设置的副本数。
  2. 没有上报时，用 Prometheus 总和推算，但不再用“最新总和 ÷ 猜的 Pod 数”：取最近 6 小时总和的中位数 ÷ 同期副本数的中位数（再在附近找规格能写的数），发布期间的新旧 Pod 不会进中位数；来源写“推算”，并说明不是 Pod Spec。
- 详情新增 `pod_spec_from`（kubernetes／estimated）；计算依据弹窗的“参数来源”表写明。

### 推荐副本数与计算结果一致
- 原来的“推荐副本数 11”是计算出的 9 再被“副本调整节奏：单次最多减少 1 个”限制成 11（12 → 11），这一步没有写出来，所以看着前后不一致。现在：`推荐副本数 ＝ max(CPU 所需副本数, 内存所需副本数)`（各自向上取整，再经过模块的最小、最大副本数和 OOM、限流、Limits 的保护），**就是 9**；节奏另起一行“本次动作到 11（单次最多 1 个，要 3 次到位）”，依据弹窗里有一段说明。后端 `ReplicaPlan.needed` 是推荐值，`this_round` 是一次动作到的数。
- 副本方案的表改成 CPU（单 Pod 当前 Requests）、内存（同）、推荐副本数＝max(CPU, 内存)、本次动作到；当前值列不再是空的；口径标注为“单 Pod 规格不变”。

### P90／P95／P99：持续使用量
- 原来的做法：Requests ＝ max(P90 ÷ 目标占比, P95 ÷ …, P99 ÷ …)，百分位取的是原始点，几个点的峰值就能抬高 P99。
- 现在：百分位按“持续使用量”取：每个点是它之前 N 分钟（系统参数 `analysis.rec_sustain_minutes`，默认 30，0＝原始点；模块可在“计算系数”里设自己的值 `rec_sustain_minutes`）使用量的平均值（`overprovision.Sustained`）。比 N 分钟短的峰值被摊开，持续超过 N 分钟的高负载照常算数；副本方案里全副本的总和同样处理。Limits 仍按原始 P99 峰值。你的例子（1000 个点：800 个 2 核，100 个 3 核，100 个 4 核）：4 核若是 100 个零散的点，持续使用量的 P99 约 2.3 核；若是连续 100 个点（约 8 小时），P99 仍是 4 核（测试 `TestAShortPeakDoesNotRaiseWhatARequestIsSizedFor`）。
- 同时给出推荐值“还要回答什么”：`exceed_suggested`、`exceed_now`：使用量超过推荐 Requests（和现在的 Requests）的时间占比、分几段、最长连续多少分钟，显示在依据弹窗里。
- 依据弹窗说明百分位怎么影响推荐：每个百分位各算一个 Requests 取最大、占比越小推荐越大、P99 的占比可以大于 100%（短时超出由 Limits 承担）、持续时间体现在持续使用量里；输入表里持续使用量是大字，原始百分位是小字。

### OOM
- **评分**＝100 × (1 − OOM 次数 × 权重)，最低 0，一次 OOM 扣一个权重；它是预测算法的内存评分（OOM 会扣算法的评分、影响平台跟随哪个算法），**不改**大盘上的推荐 Requests 和副本数；追溯天数是预测流水线自己的 OOM 下限看的周期。
- **大盘上改推荐值的是 OOM 保护下限**：推荐分析周期内有 OOM 时，内存 Requests 不低于 max(被杀时的工作集 × OOM 保护系数, 现在的 Requests)，并且不减副本。新增“OOM 保护下限”卡片（次数、工作集、系数、下限，是否抬高了推荐值）和依据弹窗里的说明（`oom.protection`）。

### Limits 保护与依据里的每个数
- “P99 全副本使用量 ÷ (单 Pod Limits × 90%) ＝ 副本数”：依据弹窗新增“参数来源”表：全副本使用量（指标名、窗口、是否持续处理）、单 Pod Requests／Limits（Kubernetes 或推算）、副本数（来源）、取整规则（每个资源向上取整，再取较大者，至少 1 个，再经过模块的最小、最大副本数）；系数表写明 90% 是“Limits 峰值占比上限”（模块管理 > 计算系数，没设取系统设置 `analysis.rec_limit_guard_percent`）。
- Requests 与 Limits 的对应：单 Pod 方案里写出现在的 Limits ÷ Requests 倍数和推荐的倍数（推荐 Limits ＝ max(推荐 Requests, P99 × (1 + Limits 余量))，所以不会低于推荐 Requests）。

### 验证与没验证
- Go：新增“Pod 模板与中位数估算”“发布期间的副本数与 Requests”“持续使用量”“超过多久”“推荐副本数与本次动作”“OOM 保护数据”的测试；`go test -p 1 ./pkg/...` 全过。前端：`config-basis`、`replica-basis`、`param-docs` 的单元测试。
- **没验证**：真实 operator 的 Pod 模板上报（只写进库的上报）；真实 Prometheus；“持续使用量”的窗口默认 30 分钟是按常见的发布、峰值时长取的起点，要按各模块的负载调；PostgreSQL、MySQL。

## 系统功能调整与优化（2026-11-25 第十一次：Limits 系数、全副本总量、AI 分析不预生成、术语与线宽）

### Limits：系数是 P99 的倍数，不再是“再加的余量”
- **原因**：推荐 Limits 原来是 max(推荐 Requests, P99 × (1 + Limits 余量))，系统参数 `analysis.rec_limit_headroom_percent` 填 110 时就成了 P99 × 2.1（7.75 核）。填 110 的人想要的是 P99 × 1.1。
- **现在**：推荐 Limits ＝ max(推荐 Requests, P99 × Limits 系数)，系数 120 即 ×1.2，和“OOM 保护系数”同一种写法。系统参数改名 `analysis.rec_limit_factor_percent`（默认 120，范围 100–500）；模块字段 `rec_cpu_limit_headroom`、`rec_memory_limit_headroom` 改为 `rec_cpu_limit_factor`、`rec_memory_limit_factor`（0 跟随系统，否则 1–5）。Requests 的余量 `rec_*_request_headroom` 不变，它是真正“再加的”比例。
- **迁移**：旧模块字段 ＝ 1 ＋ 旧余量（0.3 → 1.3），旧列清零；旧系统参数行 ＝ 100 ＋ 旧值（20 → 120），旧行删除；标记行 `migration.limit_margin_to_factor` 保证只转一次，之后管理员改的值不会被覆盖。
- 依据里每个 Limits 公式写成 `Limits = max(推荐 Requests, P99 × 系数) = …`，两个候选值（Requests 与 P99 × 系数）各自有数，取较大者。模块管理里“Limits 系数”的“?”给出定义、公式、带当前值的例子。

### 全副本总量 ＝ 单 Pod × 副本数
- 原来全副本的 Requests／Limits 用 Prometheus 求和的最新值：求和里有 24 个 Pod 的量（发布时新旧 Pod 并存，或同一个 Pod 的序列被数了两次），图上就成了 24 × 16 GiB ＝ 384 GiB，推荐 Limits 也乘了 24。
- 现在 `RequestTotal`／`LimitTotal` ＝ 单 Pod 的值 × 工作负载现在的副本数（Kubernetes 的设置值，没有上报时用 Prometheus 的副本数）：12 × 16 GiB ＝ 192 GiB；折线图、表格、依据、AI 的问题用同一个数。没有副本数时才用 Prometheus 的求和。

### P90／P95／P99 的叫法
- 副本方案、配置方案的列头改为“按 P90 推荐”“按 P95 推荐”“按 P99 推荐”“综合”，图例里写明：列里是按该百分位推荐的结果，不是实测使用量；实测 P90、P95、P99 是 Prometheus 的原始值（使用量表、折线图），不会因 Requests、Limits、系数改变；Limits 一律按 P99 × Limits 系数，所以各列相同。

### AI 分析
- 页面（和助手窗口）打开时不请求；之前版本把上一次的分析结果存在浏览器里，进入页面就显示出来，看着像“预先生成”。现在不再存，进入页面是空的，只有点“AI 分析”才请求，结果只在这个页面里显示。

### 折线图
- Requests 和 Limits 同宽（3.9，是原来 Requests 压在推荐线下时 5.6 的 70%），与它们重合的线更窄（2），边缘仍看得见。

### 没改
- 内存推荐副本数（12 个）：内存总量不随副本数缩小，单 Pod 5 GiB 左右、Requests 6 GiB、目标占比 85% 时，需要 ⌈总量 ÷ (6 GiB × 85%)⌉ ＝ 12 个，这是计算结果而不是由“当前 6 GiB”直接推出；依据里有各步数字。若要更少副本，需要提高单 Pod 内存 Requests 或降低目标占比。
- **没验证**：真浏览器里的新线宽和列头；真实环境的迁移（用临时库的测试验证了旧列、旧参数行、重复运行）；真实 operator 的上报。

## 系统功能调整与优化（2026-11-25 第十二次：高负载时段、依据补全、参数查看、页面传输）

### 低使用量为什么还是 12 个副本
- 内存 P95：全副本 60.71 GiB ÷ (6 GiB × 85%) ＝ 11.9 → 12。每个 Pod 的 P95 是 5.11 GiB，占 Requests 的 85.2%，已经在目标上；总内存使用量不随副本数缩小，副本少了每个 Pod 就超过目标。CPU 单独算只要 9～11 个。这是计算结果，不是继承现在的副本数。依据弹窗新增每个资源的“现在 N 个副本时每个 Pod 的使用量占 Requests 的 X%（目标 Y%）”一行，说明为什么这个资源需要这么多副本；要更少副本，要提高单 Pod 内存 Requests 或降低目标占比（方案②）。
- 单 Pod 配置来自 Kubernetes（operator 上报的 Pod 模板）；没有上报时是推算，页面标“推算”。使用量（Prometheus）、现在的配置、推荐值在表里各是各的。
- 依据里“全副本维度”和“单 Pod 维度”都有（使用量、Requests、Limits、副本数及来源）；全副本的 Requests、Limits ＝ 单 Pod × 现在的副本数。

### 方案②的数值口径
- 85% 是目标占比（推荐后这个百分位的使用量占 Requests 的比例），推荐 Requests ＝ 使用量 ÷ 目标占比：5.11 GiB ÷ 85% ≈ 6.01 GiB，不是在使用量上加 85%。推荐值再按规格向上取整（内存 1 GiB 以上取 64 MiB 的整数倍：6.01 GiB → 6.06 GiB），表里和公式里的 Requests 都是取整后的数，取整前的数在公式上一行；取整后和现在只差一个规格以内（CPU 50 mCPU、内存 64 MiB）时按“没有调整”算，不再出现“欠配 64 MiB”。
- 推荐 Limits ＝ max(推荐 Requests, P99 原始峰值 × Limits 系数)；“可降低”的量就是现在的 Limits 减去这个推荐值；窗口内有 OOM 时内存推荐还不低于 OOM 保护下限。依据里写明。
- 百分位的来源：平台对 Prometheus 范围查询返回的原始点计算（不是 PromQL 的 quantile），依据的来源表写出采样步长；窗口是推荐分析周期（默认 7 天，系统参数）。

### 常态与高负载时段（`overprovision.BuildLoadProfile`）
- 把全副本的持续使用量按一天里的小时分开（模块的时区），每个小时取所有天的 P95 ÷ (单 Pod Requests × P95 目标占比) 向上取整，就是这个小时需要的副本数（CPU、内存取较大者）：**常态**＝24 个小时居中的那个，**最忙**＝最大的，高于常态的是高峰时段。还给出使用量超过常态副本能承载的部分占多少时间、分几段、最长多久，以及按时段调整比全天保持最忙的副本数少多少副本小时。
- 资源大盘多一块“常态与高负载时段”：24 个小时的柱，高峰时段着色，一行结论；各小时相近或省得少于 5% 时明说“没有按时段调整的空间”（比如内存常驻时，内存决定全天的副本数）。按时段调整在模块管理的“时间周期”里设置，扩缩容有提前量和冷却，实际省的比估算少。
- 窗口不足 2 天、使用量没测到时没有这块。

### 模块管理：参数查看
- 操作栏新增“参数”：只读窗口，按模块管理的分组（当前配置与副本、副本与资源边界、容量与副本计算、推荐系数、OOM/JVM/限流、调整节奏与冷却、降级与安全保护、预测与时间）列出每项参数的当前生效值、单位、来源（本模块／系统设置／未设置）和含义；模块没填的显示系统设置的值。只有系统设置的（三个百分位的合理区间、限流界限、分析周期、超配分级）也列出并标“系统设置”。
- 这些只有系统设置的参数模块里还不能单独改（要在系统管理 > 系统设置 > 资源分析与推荐里改）。

### 固定副本数与策略合并
- 固定副本数＝把副本数写死成常数，填了以后预测模式只管 CPU、内存；它不是和预测、时间周期、指标触发重复的配置，而是合并顺序里“最优先、不再被后面的策略改动”的一档（只有时间周期里的“固定”和旁路更高）。模块策略里的小节改名“固定副本数与策略的合并顺序”，说明写清这一点。三种策略模式没改。

### 页面性能
- 原因之一：内嵌的页面文件由 Go 原样发送，没有压缩也没有缓存头，3 MB 的脚本每次打开都整个下载。现在：脚本、样式、页面按 gzip 发送（读一次、压一次、放在内存里），`assets/` 下的文件带内容哈希，缓存一年；页面本身每次校验、没变化返回 304。构建把 naive-ui 和 vue 全家拆成独立文件，发版时浏览器不必重新下载库。
- 实测（构建产物）：主脚本 3.08 MB → 2.54 MB（naive-ui 1.0 MB、vue 0.18 MB 拆出），gzip 后首屏脚本合计约 1.1 MB。
- **没验证**：页面交互卡顿的根因（没有在真浏览器里做性能分析）；图表、卡片墙、模块列表的渲染耗时没有测。

## 系统功能调整与优化（2026-11-25 第十三次：实际负载的 24 小时分布、标签精简、邮件带模块身份）

### 常态与高负载时段：增加实际资源负载
- 主标签“常态与高负载时段”不变，下面分两个区域：**副本数时段分布**（原来的 24 根柱和结论）、**实际资源负载时段分布**（新增）。
- 实际负载：最近 24 小时（整点到整点，最后一个小时到现在为止，小时用模块的时区）分成 24 个大方块；每个大方块里按 5 或 10 分钟一格（`overprovision.LoadProfile.AddRecent`：序列步长 ≤5 分钟用 5 分钟，≤10 分钟用 10 分钟，更粗的取步长向上凑整到 5 分钟的整数倍），格里是这段时间全副本使用量的平均值（原始点，不是持续使用量）。CPU、内存各一行。
- 颜色：格里的值 ÷ **常态副本数能承载的量**（常态副本数 × 单 Pod Requests × P95 目标占比）：不超过为常态负载（蓝色，越深越接近上限），超过为高负载（橙到红），没有数据为灰；大方块的底色是这个小时各格的平均，有高负载格的小时边框为橙色；鼠标放上去有时间、使用量、占常态承载的比例和“常态／高负载”。
- 7 天窗口的查询步长若粗于 10 分钟，格就是那个步长（凑整到 5 分钟的倍数）；要稳定的 5～10 分钟需要这个模块的使用量指标按 5～10 分钟的步长查询。

### 推荐副本数的标签
- 方案表和口径选择里“综合（取最大）”改为“综合”，保留 P90、P95、P99，计算不变；副本方案里的行名改为 “CPU Requests”“Memory Requests”（不再写“单 Pod 当前”），表头“项目”旁加“?”：这个区域的 CPU、Memory Requests 都是单 Pod 口径。

### 邮件：模块身份和近期运行情况
- 邮件主题是“集群 · 命名空间/模块 · 分析周期”；表格最前面新增三张：①模块与工作负载（集群、命名空间、模块、工作负载 Kind/名称、Pod 数及未就绪数、数据源）；②Pod 明细（Pod 名称、节点、阶段、就绪、重启，最多 40 个，operator 上报的）；③模块近期运行情况（分析周期、副本数现在／就绪／异常／发布中／周期内最少最多、CPU 和内存单 Pod 使用 P50/P95/P99/最高与 Requests、判断、OOM、CPU 限流）。其后才是副本数与推荐方案、当前与推荐配置。AI 的问题里也写集群，回答对应到这个模块。
- 数据来源：详情新增 `identity`（`WorkloadIdentity`：operator 上报的集群、命名空间、Kind、名称和 Pod 列表）。**没有 operator 上报时没有集群名和 Pod 名**：集群写数据源名并注明“operator 没有上报集群名”，Pod 写“没有 Pod 信息”，不编造。
