# P12 Cluster Agent 与集群大盘（2026-11-25 规划，E1～E8 已答复）

状态：**E1～E4 已做（2026-11-25，见末尾“已做的第一部分”）；D2 只做了事件与 RBAC 的一部分，D5 只做了事件与解释器，其余未开始。** 来源：用户 2026-11-25 给的“Kubernetes Operator / Agent 权限与功能改造需求（增补版）”，新增：Controller 扩缩容过程上报、Node/Pod/工作负载/HPA 的状态变化与追因（**不特指 Karpenter**）、Pod 生命周期异常、集群资源大盘、节点标签日报（**暂缓**）；外加启动时绑定集群。**保留现有权限原则：除授权工作负载的 scale 子资源外，Kubernetes 的一切只读。**

## 目标与边界

| 边界 | 说明 |
|---|---|
| 权限 | 唯一的业务写是授权工作负载的副本数（`deployments/scale`、`statefulsets/scale`）。其余全是 `get/list/watch`，Metrics API 是 `get/list`。不加 Node、Pod、Namespace、ConfigMap、Secret 的写权限；不 Cordon、Drain、删除节点。选主用的 `leases` 是和其他 controller 一样的做法（E6） |
| **不影响现有的副本调整（E1）** | 副本执行（`Applier`、`ModuleSync`、`MakePlan`、Fallback、降级与恢复、outbox 补传、审计）的行为**不变**：新增的采集在独立的协程和错误域里，失败、超时、内存紧张都不能拖慢或打断扩缩容；新增的上报字段只增不改，旧 Operator 对新平台、新 Operator 对旧平台都能继续工作；采集有总开关，关掉就和现在一样；现有测试全部保持通过 |
| 数据 | Requests/Limits/Allocatable/Pod 状态来自 Kubernetes 对象；当前使用量来自 Metrics API；**变化当时与之前的使用量、历史趋势来自 Prometheus（E7）**；平台只存**事件、快照和聚合**，不重复存全量时序 |
| 保留 | 默认 **3 天**，系统配置里可改，过期自动清理（E4） |
| 计算 | Agent 不跑预测；聚合、判定都在平台 |
| 一个页面一个主视图 | 集群大盘仍是**一个页面**，不用标签页；各区块自上而下，明细用抽屉或弹窗 |

## 已确认的决定（用户 2026-11-25 答复）

| 编号 | 你的答复 | 我的理解与对计划的影响 |
|---|---|---|
| E1 | 集群标识的做法同意；**现有的 Pod 副本调整功能不要受影响** | 平台登记集群，访问密钥绑定一个集群，报告里的集群与绑定不一致就拒绝；模块增加所属集群，旧数据归到现在的 `cluster.name`。**没有 `--cluster` 的旧 Operator 照常工作（归默认集群）**。上面“不影响现有副本调整”作为硬性约束写进每个工作包的完成条件 |
| E2 | 连接之后，用户可以配置过滤哪些 Namespace | 过滤规则（包含/排除）是**平台上每个集群的设置**，Agent 连上后经现有的 `operator/settings` 拉取并随刷新生效，不用重启。**没配置之前读全部 Namespace**（这是我的读法）。过滤作用于 Pod、工作负载、HPA、事件；Node 不属于 Namespace，不过滤。Agent 用全集群 Informer 加客户端过滤，还是按 Namespace 分开建 Informer，D2 量过 API 负载再选 |
| E3 | 要支持多云扩展，原因**从 Event 获取就好**；既有手动，也有自动（如 Karpenter） | **原因的来源就是 Kubernetes Events**（kubelet、调度器、Node controller、云控制器、Karpenter、Cluster Autoscaler 等谁发就记谁，原样保留 `reason`、`message`、来源组件、次数、涉及对象）。**不再读 Karpenter 的 NodePool/NodeClaim**，目标 RBAC 里也去掉。多云扩展靠一个**事件解释器注册表**：按来源组件认出“自动”（已知的伸缩组件）并标注，新增一朵云或一个组件只加一条解释规则，不改采集。**“手动”只在有证据时才标**（事件或对象信息里能看出是人操作）；认不出来的写“原因未知”或“来源未归类”，**不把没有自动事件就当成手动**。状态本身（对象现在是什么状态）仍读对象的 `status`，因为 OOMKilled 的退出原因只在容器状态里，不在事件里 |
| E4 | 默认 3 天，系统配置可设，过期清理 | 一个系统参数管事件、变化记录、快照的保留天数，默认 3；清理走现有任务引擎（与 `inventory.prune` 同一做法）。日报的天汇总因日报暂缓，不需要 |
| E5 | 日报之类暂时先保留，不做 | **D7 暂缓**：需求原文保留在本文末尾，不实现；节点标签仍然读（大盘按标签筛选要用），只是不做日报、不发邮件 |
| E6 | 用 k8s 自己的 lease，和其他 controller 一致，主 Operator 操作，其他不操作 | 现有的选主（`--leader-elect`，默认开）保留。**新增的采集和上报和现有的 `InventoryRunner`、`ModuleSync` 一样只在当选副本上跑**（`NeedLeaderElection` 为真），备用副本不建 Informer、不上报、不扩缩容。`leases` 的权限限定在 Operator 自己的命名空间（Role），在文档里作为选主所需的写，和业务写分开写明 |
| E7 | 还是之前的：发生了对应的事件，可以从 Prometheus 里获取 | **变化当时与之前的使用量从 Prometheus 查**，Agent 不再滚动保留每个节点和 Pod 的采样（省内存）。分工：Requests/Limits/Allocatable、节点上的 Pod 清单取自 Agent 缓存里**事件发生时**的对象（删除事件带最后一次看到的对象），由 Agent 随记录上报；使用量由**平台在收到记录后**按事件时间查 Prometheus（变化前、变化时、变化后三个时刻，各带取到的样本时间），写进记录。查不到（没有数据源、对象在 Prometheus 里没有序列、已超出 Prometheus 的保留）就写“未取到”并说原因，**不写 0**。每个集群要绑定一个 Prometheus 数据源（登记集群时选） |
| E8 | 目前 Pod / Node / 工作负载 / HPA 就可以了；HPA 指 Kubernetes API 提供的 HorizontalPodAutoscaler，不是自己的扩缩 | 追因的对象是这四类。HPA 的变化记录：min/max、当前与期望副本、条件（`AbleToScale`、`ScalingActive`、`ScalingLimited` 的 reason/message）、它的事件（如重新扩缩的事件，原样保留）。HPA 的记录同时是 D3 判断“这次副本变化是不是 HPA 做的”的证据。KEDA 建出来的 HPA 一样读；KEDA 自己的对象不单独追 |

## 现状对照（以代码为准）

| 需求 | 现有 | 缺口 |
|---|---|---|
| 启动时绑定集群 | 集群名是平台的**一个**系统参数 `cluster.name`；快照表有 `cluster` 字符串列；operator 用访问密钥与平台通信，**没有集群参数**，模块也没有集群 | 集群登记（含 Prometheus 数据源与 Namespace 过滤）、`--cluster`、密钥绑定、模块所属集群、多集群隔离与迁移 |
| 读取范围 | `pkg/operator/controller/inventory.go`：节点 + **已配置模块**的工作负载与 Pod；用 API reader，不起全集群 Pod 的 Informer（P10 E2、E3 的决定） | 全集群 Pod、Namespace、Event（E2 改了这个决定），带 Namespace 过滤 |
| RBAC 与选主 | `rbac.go`：deployments、statefulsets、daemonsets、nodes、pods 读；deployments/statefulsets 的 scale 写；HPA、KEDA ScaledObject 读；leases 读写；选主 `--leader-elect` 默认开，读取与同步只在当选副本上 | 加 namespaces、services、replicasets、events、`metrics.k8s.io` 只读；leases 限定命名空间 |
| 设置下发 | `SettingsStore` 从平台拉取时间设置，运行中生效 | 加 Namespace 过滤、采集开关 |
| 扩缩容过程 | `Report`：模块、前后副本、动作、结果、原因、决策版本、`RecordID`、发生时间、决策来源、状态；outbox 补传；平台 `ScaleOutcome`（只记前瞻扩容的评估） | 触发来源（本 operator / HPA / 其他）、执行耗时、执行后实际副本、失败信息、当时的资源状态；非本 operator 的副本变化 |
| 节点与 Pod | `inventory.Node`：Ready、压力、云厂商、Region、Zone、规格、Capacity、Allocatable、Pod 数、Requests、Limits；`inventory.Pod`：名、节点、阶段、Ready、重启次数、启动时间（只有已配置模块的 Pod）；`ClusterNodeSnapshot` | 节点标签、实际使用量、全集群 Pod 的 Requests/Limits、最近重启与终止原因、Pending/NotReady 起点 |
| 状态变化与追因 | 没有任何代码；每次上报是一个时间点，**不记变化与原因，也不把一个对象的变化和另一个对象的变化连起来** | 全部：变化记录、原因（来自 Event）、有证据的因果关联、变化当时的配置快照与 Prometheus 使用量、删除后的保留 |
| 集群大盘 | 页面上的集群卡片是**只读 Prometheus 的现查**（`forecastsvc/cluster.go`，`cluster_*`、`node_*` 指标种类），不存 | Requests/Limits/Used 三类对比、六个层级、事件叠加、Namespace 过滤的生效 |

## 工作包与顺序

大小只是相对：S 一两天内、M 几天、L 一周以上。**不写日期。**

| 编号 | 工作包 | 大小 | 依赖 | 主要交付 |
|---|---|---|---|---|
| D0 | 设计收敛与样本 | S | 样本（见末尾，**有就给，没有也能开始**，没有的话 D5 认不出你环境里的原因字符串） | 线上格式 v2 的字段表；各表的主键与保留；API 调用量预算；事件解释器的接口 |
| D1 | 集群绑定与隔离 | M | D0 | 集群登记表（名称、Prometheus 数据源、Namespace 过滤）、`--cluster`、密钥绑定、模块的所属集群、迁移（现有数据归到默认集群）、所有读写按集群过滤；**旧 Operator 不带集群照常工作**；隔离测试（两个集群的同名 Namespace、Pod、Node 不混）；集群设置页（连接后配置 Namespace 过滤） |
| D2 | 采集底座 | L | D1 | Informer（Node、Pod、Namespace、ReplicaSet 属主、工作负载、HPA、Events），只在当选副本上；Namespace 过滤随设置下发；Metrics API 读取（没有时如实上报“没有指标来源”）；上报格式 v2：**全量快照 + 增量变化**、按类别分队的 outbox（沿用 `MaxInventoryReports` 的做法：新增类别不挤掉扩缩容记录）、压缩；采集总开关；RBAC 改动同步到 `rbac.go` 标记、`deploy/operator/rbac/role.yaml`、Helm chart；**RBAC 测试：除 scale 与 leases 外没有写动词**；**回归：现有副本调整的测试全部通过，采集故障注入下扩缩容不受影响** |
| D5 | Node、Pod、工作负载、HPA 的状态变化与追因 | L | D2 | 变化记录（从→到、时间、原因列表、来源、原始事件）；事件解释器注册表（自动/手动/未归类）；Node、Pod、工作负载、HPA 各自的状态跃迁；有证据才连的因果关联；变化时的配置快照（Agent）与 Prometheus 使用量（平台，变化前/时/后）；节点生命周期时间线；按保留期清理 |
| D3 | 扩缩容过程上报 | M | D2、D5（HPA 记录作证据） | `Report` 扩展（只增字段）：触发来源、耗时、执行后实际副本、失败信息；**非本 operator 的副本变化**：监听工作负载的 `spec.replicas` 变化，按 `managedFields` 的管理者与 HPA 记录、事件识别来源，只上报不干预；每次变化关联当时的使用量、Requests/Limits、Ready、Pod 异常、节点可分配；平台的时间线表 |
| D4 | Pod 生命周期与异常（分类与展示） | M | D2、D5 | 平台侧的纯函数判定（输入是上报的 Pod 事实）：长期未重启（只是状态，不算故障）、Pending 过久、NotReady 过久、频繁重启、反复退出、OOMKilled、其他；阈值是系统参数；大盘区块与筛选，点开看详情、重启记录，**原因与关联来自 D5** |
| D6 | 集群资源大盘 | L | D2（事件叠加在 D5、D3、D4 之后） | 一个页面：容量、Requests、Limits、Used 对比；层级 集群 → 节点分组（节点标签）→ Node → Namespace → Workload → Pod；节点标签筛选；趋势（Prometheus 历史 + 平台快照）；状态变化与扩缩容事件叠加；去重与删除节点的规则 |
| D7 | 节点标签日报 | — | — | **暂缓（E5）**：不做；需求原文保留在末尾 |
| D8 | 验证、压测与文档 | M | 以上 | 假集群的数据量测试、API 调用计数、断线补传、选主切换时只有一个副本在报、文档、官网、进度 |

**顺序：D0 → D1 → D2 → D5 → D3 → D4 → D6 → D8（D7 暂缓）。** Node、Pod 的变化追因与当时的真实负载是这轮的重点，所以 D5 紧跟采集底座；D6 的第一个版本（容量、Requests、Limits、Used）在 D2 之后就可以先出，D5、D3、D4 的结果再叠上去。

## 设计要点

**采集与 API Server 负载**
- 读用 Informer 的 List+Watch，不轮询全量；对象进缓存前去掉不用的字段（`managedFields`、环境变量、卷、注解等）。Agent 内存与全集群 Pod 数成正比，D8 用合成的 N 个 Pod 量一次，**量之前不承诺上限**。
- 只在当选副本上建 Informer；备用副本不占内存、不占 API Server 连接。
- Metrics API 一次 List 一批（节点、Pod 各一次），间隔为系统参数，默认值待 D0 按 metrics-server 的刷新间隔定。
- 事件按 `(对象 UID, 原因, 消息的指纹)` 去重，计数合并，只上报新的和计数变化。
- 上报：变化触发 + 周期全量兜底；批量、压缩；失联时进 outbox（沿用已有做法，唯一的 `record_id`，平台幂等），分队设上限，丢弃要计数并告警。

**变化记录与追因（D5）**
- **变化记录**（平台一张表，Agent 上报，`record_id` 幂等）：集群、对象类别（Node、Pod、Workload、HPA）、命名空间、名称、UID、变化类型、从→到、发生时间（对象自己的 `lastTransitionTime`、事件的时间）与观察时间（分开存）、原因列表、关联记录、资源快照。只记**状态跃迁和去重后的事件**，不是每次上报都记一行。
- **原因列表**：每条写 `source`（`event`、`pod_status`、`node_condition`、`unknown`）、来源组件、`reason`、`message`、原始事件。同一次变化可以有多个，全部保留；认不出来的写“原因未知”，**不猜**，不翻译、不归并成自己编的类别。`origin` 字段写 `auto`（事件解释器认出的伸缩或控制组件）、`manual`（有证据）、`unclassified`。
- **事件解释器**：按来源组件（事件的 `source.component` 或 `reportingController`）匹配一张可扩展的表，每条规则说“这个组件是自动的/属于哪一朵云或哪个伸缩组件”，可能还附几个已知原因的分类标签。**规则表的初始内容只放我能从官方来源确认的组件名**；Karpenter、Cluster Autoscaler、各云控制器的事件原因与消息格式**我没有从官方来源确认到，D0 用你的样本定，没样本就只放组件名、不放原因解析**。
- **对象之间的关联（追因的核心）**：只有**有证据**才写 `caused_by`——事件的 `involvedObject` 指向、`ownerReferences` 属主链、Pod 的 `nodeName` 与节点变化吻合且 Pod 的原因或事件文字里指向该节点或其污点；证据写进记录。只有时间相邻、同一个节点而没有指向的，写 `related`，页面标成“相关，不一定是原因”；对不上就不连。例：节点 NotReady → 上面的 Pod 被驱逐（有对应事件）= `caused_by`；一个 Pod 被删而同时间工作负载副本在减少 = `related`，属主 ReplicaSet 与副本变化吻合才写 `caused_by`；HPA 重新扩缩的事件与工作负载副本变化吻合 = 副本变化 `caused_by` 该 HPA 记录。
- **变化当时的资源**：Agent 取缓存里事件发生时的对象（节点：Capacity、Allocatable、Requests、Limits、状态、标签、节点上的 Pod 清单及各 Pod 的 Requests/Limits；Pod：Requests、Limits、重启次数；工作负载与 HPA：副本与范围）。使用量由平台按事件时间查 Prometheus（变化前、变化时、变化后），标采样时间，查不到写“未取到”和原因。**对象在变化迹象出现前的使用量不会被事后数据覆盖**。
- **数据量**：集群大了，Pod 的重启与事件会很多。记录只留跃迁和去重事件，每个对象有上限、全表有保留期（默认 3 天）；超过上限丢最旧的并计数，页面写明“已截断”。
- **节点生命周期时间线**：创建、注册 Ready、状态与压力的每次变化、不可调度、污点、终止开始、Pod 迁移、最终删除，由变化记录和关联生成；对象删除后记录保留到保留期结束。

**扩缩容过程（D3）**
- 本 operator 的动作：在 `Applier` 执行处记前后副本、起止时间；执行后再读一次工作负载的 `status`，写入“执行后实际副本”和 Ready。上报失败不影响扩缩容（现有行为）。
- 其他来源：工作负载 `spec.replicas` 变化 → 查 `managedFields` 里最近改 `scale` 子资源的管理者，并与 HPA 记录对照；对得上本 operator 的是自己的，对得上 HPA 的标 HPA，对不上的写“来源未知”。**具体的管理者名称因版本而异，以样本为准，不猜。**
- 关联的资源状态取**变化时刻最近的一次**，带采样时间；超过一个周期没有采样标明“采样已过时”。

**集群资源大盘（D6）**
- 层级汇总由平台从快照算，统计只含**当前存在**的节点和 Pod；节点删除后不计入当前总量；历史趋势按当时的快照。Namespace 过滤生效后，被过滤的 Namespace 不计入。
- 三类对比的每个数字写来源：Requests/Limits 来自 Kubernetes 对象，Used 来自 Metrics API，趋势来自 Prometheus；即时、历史、缓存各自标注；Requests、Limits 按 Pod 配置**原值**显示，不取整。
- Requests 不等于使用量、Limits 不等于实际分配的物理资源：页面各写一句话说明。

## 权限（目标 RBAC，D2 落地）

| 组 / 资源 | 动词 |
|---|---|
| `apps`：deployments、statefulsets、daemonsets、replicasets | get、list、watch |
| `apps`：deployments/scale、statefulsets/scale | get、update、patch（**现有，唯一的业务写**） |
| core：nodes、pods、namespaces、services、events | get、list、watch |
| `autoscaling`：horizontalpodautoscalers；`keda.sh`：scaledobjects | get、list、watch |
| `metrics.k8s.io`：nodes、pods | get、list |
| `coordination.k8s.io`：leases（只在 operator 的命名空间，Role） | 选主所需的读写，与其他 controller 一致 |

测试：把 `rbac.go` 的标记解析成规则，断言除 scale 与 leases 外没有 `create/update/patch/delete/deletecollection`；Helm 渲染的 ClusterRole 与 `role.yaml` 与标记一致（本机没装 helm，渲染这一项目前只能读文件，不能算验证）。

## 验收（对应需求第十一部分）

| 需求 | 由哪个包满足 | 怎么验证 |
|---|---|---|
| 1 启动指定集群并绑定平台 | D1 | 单测 + 假平台；两个集群并发上报互不混；旧 Operator 不带集群照常工作 |
| 2 上报集群、Node、Pod、工作负载状态 | D2 | fake client 的数据 → 平台入库的逐项比对 |
| 3 记录 HPA/Controller 的副本过程与结果 | D3 | fake client 模拟本 operator、HPA、其他三种来源 |
| 4 Node、Pod、工作负载、HPA 状态变化与真实原因 | D5 | 回放你给的真实样本；没有样本，只能证明：事件与状态的原因被原样记下、认不出来的写“原因未知”、没有证据不写 `caused_by` |
| 5 变化前的 Requests、Limits、Used、Pod | D5 | 回放：配置取事件时的对象，使用量取 Prometheus 变化前的样本且标时间；查不到写“未取到” |
| 6 大盘看变更历史与快照 | D5、D6 | 浏览器里点事件看详情 |
| 7 长期 Pending、NotReady、频繁重启，并说明原因 | D4、D5 | 纯函数表驱动测试 + 页面 |
| 8 集群、节点、工作负载的 Requests/Limits/Used | D6 | 与 Prometheus 对账；去重、删除节点、Namespace 过滤测试 |
| 9 按节点标签统计并发日报 | D7 | **暂缓** |
| 10 采集保持只读 | D2 | RBAC 测试；fake client 断言除 scale 外没有写调用 |
| 11 保留副本执行、Fallback、安全边界、审计 | 全程 | 现有测试全部通过；采集故障注入（Informer 起不来、事件风暴、Metrics API 不可用）下扩缩容不变 |
| 12 不拖垮 API Server、不重复统计 | D2、D8 | 记录 fake client 的调用次数与 Watch 数量；选主切换只有一个副本在报；去重测试 |

## 这份计划无法验证的

- Agent 在真实集群上的内存与 API 调用量；metrics-server 的刷新间隔与数据缺口。
- 真实集群里 Node、Pod、HPA 的事件原因与消息（各 Kubernetes 版本、云厂商、伸缩组件不同）：**没有你的样本之前，D5 只证明“原样保留、不猜”，没证明认得出你环境里的原因**。
- 真实 Prometheus 里按事件时间查到的使用量（节点删除后的序列、保留期、数据源与集群的对应）。
- 多个副本选主切换的真实表现；PostgreSQL、MySQL 上的新表。

## 样本（D0 用，有就给，没有也能开始）

1. 一个异常过的节点、一个被缩掉的节点：`kubectl get node <名> -o yaml`、`kubectl get events --field-selector involvedObject.name=<名>`。
2. 几个出过问题的 Pod（重启、OOMKilled、被驱逐、Pending 不调度各一个更好）：`kubectl get pod <名> -o yaml` 与它的 Events。
3. 一个有 HPA 的工作负载：`kubectl get hpa <名> -o yaml` 与它的 Events。
4. 一个能量内存和调用量的集群，或者节点数和 Pod 数的描述。

名字去掉也行。

## 暂缓：节点标签日报（D7，用户 2026-11-25 决定先保留不做）

原需求保留：按节点标签（NodePool、Zone、实例规格、容量类型、自定义）分类汇总；日报含集群名称、日期、标签分组、节点总数与状态、Requests、Limits、实际使用量、节点增减次数与原因、异常、Pod 异常与重启、主要资源变化；支持按标签筛选；每日定时发邮件到配置的邮箱，表格展示，含摘要和集群大盘深链，不混集群与分组。由平台聚合与发信，Agent 只上报。**现在不做，也不为它预留表和接口；节点标签仍随节点上报，大盘筛选用。**

## 已做的第一部分（2026-11-25：E1～E4）

用户要求把 E1～E4 先做完。做的是这四项决定本身，不是整个 D1／D2／D5：

- **E1 集群标识与隔离（D1 的全部）**：`model.Cluster` 登记集群（名称创建后不能改）；访问密钥绑定一个集群（`secret_key.cluster`），**一个密钥只能绑一个集群，没绑的密钥只能用于默认集群**；Operator 加 `--cluster`（或 `KUBE_RES_FORESIGHT_CLUSTER`），每个请求带头 `X-kube-res-foresight-Cluster`，平台按“密钥绑定的集群”核对，不一致 403、没登记 404、被关 403；模块增加所属集群（空 = 默认集群），`/operator/modules`、`/operator/inventory-targets`、`/operator/decisions`、`/operator/reports`、`/operator/inventory` 都只认本集群的模块；节点快照与趋势按集群读（`/inventory/nodes?cluster=`，不带就是默认集群）；同一个工作负载可以在两个集群各有一个模块（模块唯一索引加了集群，旧库由迁移重建一次，用标记行保证只做一次）；已有数据与模块、没有 `--cluster` 的旧 Operator **都归默认集群，照常工作**（名字取迁移时的系统参数 `cluster.name`）。**现有的副本调整没有改动**：`Applier`、`MakePlan`、Fallback、降级、outbox、审计的代码没碰，Operator 只在取模块和上报时多带一个头；采集是独立的协程，有 `--collect-events=false` 的本地开关。
- **E2 命名空间过滤**：每个集群的“只读取／不读取”两个列表（结尾 `*` 是前缀，排除优先，节点不受限）在平台的“数据源 → 集群”页面里设置，Operator 连上后经新接口 `GET /operator/cluster-settings` 每分钟（`operator.settings_refresh_seconds`）拉取，**运行中生效**；改了过滤后，缓冲区里按旧规则收的事件会丢掉。**没配置之前读全部命名空间。现在过滤只作用于事件**（全集群的 Pod、工作负载还没读，是 D2）；模块的副本调整、已配置模块的工作负载上报**不受过滤限制**。
- **E3 原因从 Event 取，支持多云扩展**：Operator 在**当选副本**上用 informer 读 core/v1 Event（`events` 只读，`NeedLeaderElection`），按 `(uid)` 去重、有上限的缓冲（5000，满了丢最旧的并计数）、每 10 秒一批（≤500）发 `POST /operator/events`，平台不可达时保留并退避重发，被平台 400 拒绝的批丢弃；平台按 `(集群, uid)` 存一行，重复事件更新次数与时间，**晚到的旧批不会把次数改小**，保存原始字段（原因、消息、发出方、次数、涉及对象、时间）和原始 JSON。**事件解释器**（`pkg/clusteragent/rules.go`）按发出方（`reportingController`，没有就用 `source.component`）读：内置规则认 cluster-autoscaler、horizontal-pod-autoscaler、karpenter、keda、云控制器、kubelet、调度器、controller-manager 与 `*-controller`，结果是 `auto`（类别：伸缩组件／控制器／kubelet／调度器／云）；**认不出的是 `unclassified`，不猜；“手动”只由规则明说，没有内置的手动规则**。新增一朵云或一个组件只加规则：系统参数 `cluster.event_rules`（JSON 列表，先于内置规则，写错了会提示并用内置规则）。默认不发的噪音原因 `Pulling,Pulled,Created,Started` 是系统参数 `cluster.event_skip_reasons`（这四个是我定的，可改）；平台可用 `cluster.collect_events` 关掉所有 Operator 的采集。页面：集群页的“事件”抽屉，按来源、类型、搜索筛选。
- **E4 保留期**：系统参数 `cluster.retention_days`（默认 3，范围 1～365），任务 `cluster.prune`（每小时）按事件**最后一次发生的时间**清理；已经过期的事件到达时不入库。**现在只有事件一类数据**：变化记录和节点／Pod 快照还不存在（D5、D2），它们建好时用同一个参数；已有的节点快照仍按各自的 `inventory.*` 保留参数。
- **权限（D2 的一部分）**：RBAC 加 `events` 的 get／list／watch（标记、生成的 role.yaml、Helm 的 ClusterRole，Helm 用 `clusterAgent.collectEvents` 开关，`clusterAgent.cluster` 对应 `--cluster`）。**“除 scale 外只读”实际有两个例外，都是原来就有的选主所需：`leases` 的读写，和 Helm 角色里给选主记事件用的 `events` create／patch**；新增的 RBAC 测试读标记、role.yaml、Helm 角色三处，除这两个和 scale 外有任何写动词就失败。leases 没有收窄到命名空间（仍是 ClusterRole），改动会影响已部署的 Operator，没做。

**验证了什么**：Go：`pkg/clusteragent`（命名空间规则、批的校验、事件解释器与自定义规则）、`pkg/clustersvc`（解析集群、密钥与集群的绑定、事件入库与读取与去重、保留与清理）、API（集群页增删改、密钥绑定、Operator 取设置与发事件、跨集群隔离：模块、目标、决策、上报、节点、事件、同一个工作负载在两个集群）、Operator（事件转换、过滤、缓冲、失败与重发、批的大小、informer 在平台说采集后才建、请求头）、迁移（默认集群、种子、任务、权限、旧库的索引升级）、RBAC；`go test -p 1 ./pkg/...` 全部通过；前端 `vue-tsc`、110 个单元测试、中英文文案一致性。**真浏览器**（临时库、真实的服务器与中间件、真实创建的访问密钥并绑定到集群，用脚本模拟 Operator 发了 8 条事件）：集群页、编辑（命名空间保存后接口立即返回新的过滤）、事件抽屉与筛选、模块表单出现集群选择（两个集群时）、中英文；**这一轮在浏览器里查出一个真问题**：同一个工作负载在第二个集群建模块被“已有模块管理这个工作负载”挡住（DAO 的检查没带集群），已修并加测试。

**没验证**：Operator 对着**真实的 Kubernetes**（informer 能不能读到 core/v1 Event、`events.k8s.io` 的新格式事件、RBAC 在真集群里够不够）；真实的 Karpenter、Cluster Autoscaler、各云的事件格式（内置规则里 karpenter、keda、云控制器是按名字片段匹配的，没对过真实事件）；PostgreSQL、MySQL 上的新表与索引重建；Helm 渲染（本机没有 helm，只读了文件）；多个副本选主切换时只有当选副本在发（靠 `NeedLeaderElection`，没起两个副本试）；`cluster.prune` 的定时运行（任务行在、清理逻辑有单测，没等它到点）；集群自己的 Prometheus 数据源（`datasource_id`）只接到了 `/inventory/nodes/usage`，没有单独的测试。

**没做、和 E1～E4 有关的**：资源大盘、集群大盘的页面还没有集群选择，仍然看默认集群（接口已经能按 `?cluster=` 取）；准入（Kyverno）按工作负载找模块的 `ForWorkload` 不分集群，多集群时要等准入的集群标识（准入链路没有这个信息）；全集群的 Pod、Namespace、Metrics API 采集（D2）；Node、Pod、工作负载、HPA 的状态变化与因果（D5）；日报暂缓。官网的功能说明还没写这一页。
