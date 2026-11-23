# P10 资源超配分析与可观测大盘（2026-11-19 规划）

状态：**A1（数据采集与持久化）、A2（超配分析引擎）、A3（资源大盘）、A4（Grafana 看板与深链）、A6（成本从产品面移出）已做（A2、A3 的第一部分 2026-11-19，A1、A3、A4 的其余 2026-11-20，见末尾“A1、A3、A4 实际做了什么”）；A5 并入 P11 的 C6；本文其余是计划，不是实现。** 用户已答复三项：E2（operator 只读：节点；Pod 只统计已配置的模块）、E3（工作负载只统计已配置的模块，不自动发现全部）、E10（成本代码从产品面拿掉，代码保留）；其余 E 项还没答复。来源：用户给出的“资源超配分析与可观测性系统升级”需求和一组参考大盘截图（KubeCostOptimizer）。截图里的金额、成本列不在本阶段范围（见 [成本模块需求表](cost-module-requirements.md)）。动手前先读本文的“待你确认”表，没确认的项不进开发；页面规则见用户记忆：不用页签（含分段切换）、不拆成多页、字段带“?”说明。

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

## A5 LLM 周期建议（已并入 P11 的 C6，2026-11-19）

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
| A3 系统内大盘与详情 | **已做**（在真浏览器里看过，临时库、假 Prometheus）：汇总卡、排行图、模块页，加上集群卡片、节点表、Ready 节点趋势、HPA 与副本趋势、配置变更历史、“在 Grafana 打开”、BIZ 账号走查（2026-11-20） | `web/src/views/forecast-results/overprovision`、`components/business/overprovision-module-panel.vue`、`inventory-{cluster,workload}-panel.vue` |
| A4 Grafana Dashboard 与深链 | **已做**（2026-11-20）：看板由 `pkg/grafana` 用与页面同一套查询生成，JSON 在 `deploy/grafana` 和 Helm；**没在真实 Grafana 里加载过** | `pkg/grafana`、`deploy/grafana/kube-res-foresight-resources.json`、`pkg/app/server/api/handle_grafana.go` |
| A5 LLM 周期建议 | 未开始；**并入 P11 的 C6**（见 ai-and-performance-p11.md） | |
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

### A3：资源大盘（“预测结果 > 资源大盘”，菜单码 `forecast-results_overprovision`）

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
- **Grafana 打开本页**：看板里的链接 `/forecast-results/overprovision?namespace=…&workload=…&source=grafana`；页面认 `namespace`、`workload`、`module`、`window`，Grafana 的“全部”（`.*`）和多选（`a|b`）不指向一个对象，**忽略**。
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
| 1 分析窗口 | 去掉 7 个并排的选项，改成和全站一样的时间选择框（最近 N 分钟/小时/天或固定范围）；正好是 15m、30m、1h、1d、7d、15d、30d 之一就走已存的窗口，其他长度按范围现算；总览读“能盖住这个范围的最短已存窗口”并说明 | `utils/overprovision-window.ts`（有测试）、`time-range-field.vue`（加了 `size`）、`views/forecast-results/overprovision/index.vue` |
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
| 1 可回收资源排行 | 删除（总览的排行图、可回收卡片、预留合计、待优化／欠配／隐患计数都删了） | `views/forecast-results/overprovision/index.vue` |
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
