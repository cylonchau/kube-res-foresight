# kube-resource-fcst 实施计划与执行进度

更新日期：2026-10-28。当前里程碑：P1～P6 的非真实环境部分与「预测系统优化」8 项需求（M1～M7）与「预测系统功能与页面优化」6 部分（R1～R10）均已实现并通过自动化测试与浏览器验收；真实环境验证仍缺。P7 算法质量：A0 基线测量、A1 回测公平、A2 低估敏感评分、A3 保形分位数、A4 Holt-Winters 补强与 Theta、A5 直接多步、A6 KNN 互斥区与时刻约束、A7 MSTL 已完成，A8 未开始。

## 1. 进度口径与当前结论

状态统一为：已完成、部分完成、待实施、待验证。设计或示例完成不等于功能完成；本文件不以文件数估算软件完成百分比。没有真实数据回测、集群演练或对应数据库验证的能力，不能标记已验收。

已实现 P1～P6 中不依赖真实集群的全部功能（数据采集、预测算法、回测、决策、实测回填与排行榜、指标、准入接口、operator、LDAP）及管理页面，并有单元/集成测试与一次浏览器验收（真实 Chromium、全新 SQLite 库、模拟 Prometheus）。以下均**未经真实环境验证**：真实 Prometheus、真实集群（operator 扩缩容、CRD/RBAC 安装）、Kyverno、MySQL/PostgreSQL、真实 LDAP 服务（尤其 StartTLS）、真实负载上的预测准确率。P0-02/03/04 仍待收敛；admission 与 auto 模式由系统设置默认关闭。

| 能力 | 状态 | 已有证据 / 余项 |
|---|---|---|
| 系统、授权、任务管理模块 | 已有实现 | 复用现有代码；工作流引擎与 Telegram 已删除（见末尾 M4 记录） |
| 系统设置、用户、角色、权限、菜单、操作日志 | 已有实现 | 沿用现有模型/DAO/API/UI/Casbin，禁止重复建设 |
| GORM 数据访问及三种驱动 | 已有实现 | SQLite/MySQL/PostgreSQL 驱动已存在；新增业务迁移三库兼容待验证 |
| 项目技能、算法筛选和参数研究 | 已完成设计整理 | 金融指标已排除；不是模型可运行证明 |
| 前后端编译修复 | 已完成 | 三个页面补充共享表格导入；Go build/test、Vue 类型检查、生产构建、2 项前端单元测试通过 |
| 优化 M1 指标预设与演示 | 已实现 | pkg/preset 12 条预设（cAdvisor/kube-state-metrics/ingress-nginx/Istio/OOM，含来源、标签、限制、中英文，全部标「未验证」）；pkg/demo 合成数据源（type=demo）；一键创建/刷新/删除演示（重放 12 个预测周期后跑 Collect→Predict→Backtest→Evaluate→Recommend）；映射保留 preset_id；单测证明每条预设通过 ValidateMetricMapping、渲染后无残留变量、演示生成器返回单序列；浏览器验收：一键演示后各页有数据。未在真实 Prometheus 上跑过任何预设 |
| 优化 M2 算法中心 | 已实现 | 目录（类别/版本/复杂度/最小样本）、参数模板（schema 校验、被引用不可删、同步或仅新绑定、同步后晋升失效提示）、使用情况与表现、绑定→任务→预测→回测→评估追溯；模块绑定可选模板；API 集成测试与浏览器验收 |
| 优化 M3 页面规范 | 已实现 | 预测 4 页、新增页（预设、算法、指标、登录日志）、LDAP 页按标准列表结构重做（搜索卡片经 list-search 插件自动提交、TableHeaderOperation、ExpandableDataTable、自适应分页、列配置入库、说明改问号提示、无常驻警告横幅）；浏览器验收在 1440/1024/390 宽度检查无横向溢出、1280×640 小窗口表格仍有行；服务端排序仅登录日志支持，其余列表沿用默认序 |
| 优化 M4 删除工作流与 Telegram，补登录日志 | 已实现 | 前后端、模型、任务、配置、菜单、文档语义已删除（git rm，可恢复）；退役表仅在 --upgrade 时先打印清单（表名与行数）再删除，已用旧库结构副本实跑；登录日志页（用户名/IP/结果/原因/方式/时间检索、服务端分页排序）、锁定阈值/窗口与保留天数可在系统设置修改并即时生效，保留任务按设置清理；首页改为 forecast_modules |
| 优化 M5 Operator 与 Metrics | 已实现 | operator 业务指标 kubeforecast_operator_*（fake client 下校对数值与标签基数）、Prometheus 决策来源（多序列/NaN/负值/陈旧/401/403 均走冻结或 fallback，httptest 覆盖）、Adapter 来源有意不做；平台指标补采集/预测/回测/评估计数与耗时、数据源状态与延迟；Service+ServiceMonitor 示例。真实集群未验证 |
| 优化 M6 历史与统一指标 | 已实现 | 指标目录（pkg/forecastsvc/catalog.go）为唯一来源，/metrics 导出、GET /forecast-metrics/query 与目录由测试对账（每个导出值等于查询最后一点）；查询按步长对齐、点数上限 5000、源行上限 20 万；指标页（预测 vs 实测、各算法 WAPE 随时间、导出 CSV、目录与保留期）；存储位置与保留见 deploy/README.md |
| 优化 M7 动态最优实例数 | 已实现 | pkg/decision/solver.go 多约束（吞吐/CPU/内存）求解、各算法按自身实测峰值低估修正、平滑防抖、边界；单 Pod 容量可手填或从历史估计（样本不足拒绝并说明）；每个算法单独给出实例数与决定性约束；推荐详情与页面可解释；fixed 模式结果不变（回归测试）。**估计容量与动态结果未与真实压测对照** |
| 重构 R1 菜单拆分与页面规范 | 已实现 | 菜单拆为 预测管理 / 预测结果 / 推荐与执行 / 监控指标（见 [规格](../.agents/skills/requirements/references/forecast-redesign-spec.md) 第 2 节）：回测、评估、排行榜、趋势、快照、执行、降级记录、审计导出各自独立页面，旧菜单码经 menuSuccessors 把授权映射到新码后退役（迁移测试覆盖）；列表页统一用 ListSearchCard + ListTablePanel + usePagedList，服务端分页、筛选与白名单排序；指标页查询区一张卡片（≤2 行控件，浏览器在 1440 下量过），预设按钮移到趋势对比页 |
| 重构 R2 算法参数与多指标输入 | 已实现 | EWMA 增加 span/bias_correction，新增 macd_trend（单变量、结果下限 0、有上限）；岭回归/GBDT 读取协变量（滞后特征，预测步沿用最后观测值）；输入指标集（目标+协变量+历史天数 1–180+缺失处理 drop/degrade）进入模型版本签名，回测在每个截止点同样切片协变量，单测断言无泄漏；绑定与回测记录保存 input_set_id |
| 重构 R3 OOM / JVM / Kubernetes 指标 | 已实现 | 指标种类 oom_working_set、jvm_heap_used/max、jvm_nonheap_used、jvm_gc_pause、jvm_threads、k8s_cpu/mem_requests/limits、k8s_cpu_throttle；11 条预设（均标未验证）、演示生成器与目录用量指标；模块 extra_mappings 绑定 |
| 重构 R4/R5 推荐计算 | 已实现 | OOM 地板（max(OOM 前工作集, 内存 Limit)×uplift，对除 bypass 外所有来源生效，受模块上限约束并标注）、JVM 堆压力约束并入动态副本求解；推荐详情记录 OOM、堆压力、各字段来源与降级原因；页面可解释 |
| 重构 R6 模块与 Fallback | 已实现 | Fallback 默认值与降级阈值（覆盖率下限、允许缺样率、恢复周期数）为模块页独立块；模块列表带关联摘要与运行状态标签；克隆（含算法绑定与固定周期） |
| 重构 R7 降级链与审计 | 已实现 | 降级按字段独立（bypass > 固定周期 > 算法 > 模块 Fallback > 保持原值），原因码 no_data / stale_data / insufficient_history / predictor_failed / no_prediction / stale_prediction / no_eligible_model / recovering；恢复需 recover_cycles 个连续健康周期（module_run_state）；降级与回退记录页含持续时长与恢复时间；审计导出 5 类，每次导出写操作日志；kubeforecast_fallback_total{reason,field} |
| 问题修复 R10 历史曲线、资源与副本职责、推荐快照、降级与回退 | 已实现 | 见本文末 2026-10-28 R10 记录与 [规格第 16 节](../.agents/skills/requirements/references/algorithm-framework-spec.md)：图表刷新/图例/断点（含视野覆盖运行周期）、数据源并发不设上限、算法运行状态、时间选择器 3h/12h 与表格列宽、Pod 规格与副本分开（取整、保持、规格版本）、资源推荐与推荐快照（状态、保留）、降级与回退两类记录 |
| 重构 R8 LDAP 并入系统参数 | 已实现 | LDAP 存为 ldap.* 系统参数（不出现在通用接口），系统参数页新增 LDAP 分类（连接、用户/组、组到角色映射、测试、同步）；旧 ldap_setting/ldap_group_mapping 在 --migration/--upgrade 迁入并退役，登录行为不变 |
| Windows 可执行产物 | 已生成 | target/bin 下主程序、server、job；不代表预测功能上线 |
| Platform 指标与探针（P5-02） | 已实现 | pkg/metrics 提供 /metrics、/healthz、/readyz（metrics.listen）；forecastsvc.Collector 在抓取时读库导出模块启用/旁路、推荐值/来源/年龄/有效性、样本与预测年龄、实测 WAPE/峰值低估、晋升状态，标签仅 module/metric/algorithm/resource/source；已在浏览器验收的服务上取得真实抓取输出 |
| 数据源与指标映射（P1-01） | 已实现 | 后端、连接测试、菜单与两个页面（含密码/Token 加密、留空保持不变、变更后重置检测状态）；类型检查、构建与浏览器验收通过；未连接真实 Prometheus |
| Prometheus 历史查询、质量与特征（P1-02～03） | 已实现 | pkg/collector、promclient.QueryRange、metric_series/metric_sample、Collect/History（长窗口服务端分桶平均，质量按全量样本计算）；浏览器验收中对模拟源采集 72 小时并显示缺口；Retry-After 解析与响应缓存未做 |
| Go 算法插件、模型与回测（P2） | 已实现 | 六个算法、注册表与 schema 驱动的参数编辑页、滚动回测与晋升；合成数据下 GBDT（5000 行×100 棵）约 0.19s、Holt-Winters（30 天、日周期）约 18ms、岭回归（30 天）约 0.13s（本机基准）；无真实负载的准确率/优劣结论；模型产物不持久化 |
| 模块绑定、推荐快照、固定周期、fallback（P3） | 已实现 | 模型/API/页面/定时任务；admission 与 auto 模式受系统设置控制，开关在每次准入/决策回答时检查 |
| Kyverno 资源准入与 kubeforecast-operator（P4A/P4B） | 部分完成 | 准入接口、operator（CRD、MakePlan、Reconciler、PlatformAPI 决策与上报、HPA/KEDA/重复 scaler 冲突检测）、`kubeforecast operator` 命令、CRD/RBAC/manager 清单已实现，用 fake client 与 httptest 测试；Kyverno 策略仅为未验证的参考草稿；无 envtest、无集群演练，CRD 组名 autoscaling.kubeforecast.io 为暂定 |
| LDAP/LDAPS（P6-01） | 已实现 | LDAPS 或 ldap+StartTLS（明文被拒绝）、CA、用户/组查询与组到角色映射、禁用同步（目录不可达不改任何账号）、本地账号永不走目录（应急管理员）、连接测试、LDAP 页面；go-ldap 客户端对本地 TLS 测试服务器验证了转义、空密码、证书校验；StartTLS 与真实 AD/OpenLDAP 未验证 |

## 2. 实施设计基线

以 [功能需求](01-功能需求.md)、[新版架构](02-系统架构.md)、[数据契约](03-数据与接口契约.md)、[验收标准](04-验收标准与实施阶段.md)、[算法目录](05-算法目录与参数.md) 为输入，实施前同步对应技能。源码入口见 [代码地图](../.agents/skills/requirements/references/repository-map.md)。

平台分 Platform、Operator 两个逻辑模块（Metrics 暴露内置于 Platform）。采集、特征、算法、回测和决策归 Platform；算法以纯 Go 在进程内实现，不依赖 Python，也不为每个算法另建独立服务。Operator 负责 Kubernetes 执行和对账；Platform 内置 Metrics，复用现有指标基础设施。

业务链路：Prometheus → 采样质量/特征 → 多模型预测 → 安全决策/固定周期/fallback → 版本化快照 → 资源准入或副本执行 → 审计/实测回填/评估。Platform Metrics 观察各环节，不参与执行。

拟定边界：按新版文档采用 Kyverno 处理创建时资源变更，Platform 供给资源推荐；kubeforecast-operator 通过 PredictiveScaler CRD 声明式完成扩缩容，替代 HPA/KEDA，独占目标工作负载的副本控制（内部经 Kubernetes scale 子资源写入，属实现细节）。Kyverno 不写目标工作负载 replicas，原生 HPA/KEDA 也不能与本 Operator 双写。实际策略/API/TLS/旁路机制须经过 P0 核实，不能将需求描述当 Kyverno 已具备的接口。

数据层统一复用现有 GORM 与部署配置，兼容 SQLite/MySQL/PostgreSQL。基础系统配置写现有 SysConfig，新领域模型按现有注册、DAO 和显式迁移接入，不能另建一套用户/权限/菜单。

## 3. 开始实现前的设计收敛（P0）

| ID | 任务 | 具体交付与完成条件 | 状态 |
|---|---|---|---|
| P0-01 | 同步两模块架构与技能 | 对齐新版 docs/02、03 与 control-plane/admission-safety 等参考；清理独立准入服务和八服务拆分的冲突表述 | 待实施 |
| P0-02 | 确认 Kyverno 接入契约 | 按目标 Kubernetes/Kyverno 版本验证推荐读取方式、调用超时、缓存、TTL、无效值/不可达旁路、命名空间选择和审计；禁止修改 replicas | 待实施 |
| P0-03 | 冻结控制参数与所有权 | 区分管理员不可突破的硬边界与模型动态建议；统一 Static/PlatformAPI/Prometheus/Adapter 的有效期、单位、版本、冲突和失败语义 | 待实施 |
| P0-04 | 固化数据与验收输入 | 确定首个目标工作负载、指标映射、历史可用区间、脱敏样本、时区、权限和测试环境；保留期/数据量预算有依据 | 待实施 |

P0 的结果应落入技能与 schema；P1 数据接入可在边界明确的部分推进，准入与 Auto 不得跳过上述收敛。没有真实集群或 30 天历史时标记待验证，不用模拟结果代替。

## 4. 执行阶段与工作包

| 阶段 | 工作包 | 主要交付 | 进入条件 | 阶段验收 / 状态 |
|---|---|---|---|---|
| P1 数据基础 | P1-01 数据源与指标映射；P1-02 分段历史查询；P1-03 质量/特征；P1-04 页面与权限 | Prometheus TLS/凭据/连接测试；CPU/Memory/RPS 粒度、采样、限流、缓存；历史查询和缺样可视化 | P0-04 输入明确 | 状态：P1-01～04 已实现并浏览器验收（含历史/缺样页）；真实 Prometheus 30 天数据未验证，阶段验收未满足 |
| P2 算法与 Observe | P2-01 插件 schema；P2-02 Go 算法接口与模型存储；P2-03 基线/岭回归/GBDT；P2-04 滚动回测/UI | Naive/SeasonalNaive、EWMA/Holt-Winters、岭回归/GBDT（纯 Go）可运行对照；版本化参数、产物和预测 | P1 有可回放数据 | 状态：P2-01～04 已实现（含参数编辑页与回测/排行榜页）；无真实负载对照，阶段验收未满足 |
| P3 决策与 Recommend | P3-01 模块多算法绑定；P3-02 安全推荐；P3-03 周期/fallback；P3-04 快照/审计 | 硬边界、资源与副本建议分离；模型选择、TTL、固定周期、UTC/IANA；可解释推荐页面 | P0-03、P2 通过 | 状态：P3-01～04 已实现（模块、绑定、固定周期、旁路、推荐与解释页）；P0-03 未收敛，副本换算只在配置了已验证单 Pod 容量时产生；阶段验收未满足 |
| P4A 创建时准入 | P4A-01 推荐 API/快照；P4A-02 Kyverno 策略与部署；P4A-03 故障演练 | 仅改变授权范围内新 Pod requests/limits，原/建议/应用资源完整审计 | P0-02、P3 通过 | 状态：P4A-01 推荐 API 已实现；P4A-02 Kyverno 策略为未验证草稿；P4A-03 故障演练未做；阶段验收未满足 |
| P4B 副本执行 | P4B-01 CRD/schema/RBAC；P4B-02 决策来源；P4B-03 reconcile/status；P4B-04 扩缩容演练 | kubeforecast-operator 按 PredictiveScaler CRD 执行扩缩容，动态参数在硬边界内执行，冷却/限幅/Ready/恢复对账 | P0-03、P3 通过 | 状态：P4B-01～03 已实现（fake client 测试）；P4B-04 扩缩容演练未做，Prometheus 来源已实现（httptest 验证），Adapter 来源有意不做；阶段验收未满足 |
| P5 闭环与运维 | P5-01 实测回填/排行榜；P5-02 Platform Metrics；P5-03 审计导出；P5-04 发布验证 | 关联预测/推荐/准入/执行/Ready；业务指标、健康诊断、部署与回滚文档 | P2 起持续建设，执行评估依赖 P4 | 状态：P5-01 实测回填与排行榜、P5-02 指标与 /readyz、P5-03 CSV 审计导出、数据保留清理已实现；P5-04 部署说明见 deploy/README.md，灰度与故障演练报告未做；执行效果评估（Pod-hours、SLO）未实现 |
| P6 完整需求与增强 | P6-01 LDAP；P6-02 增强算法；P6-03 其他决策来源 | LDAP/禁用同步/应急管理员；其他 Go 实现的 challenger 算法；其余决策适配器 | 基础闭环稳定，数据和预算具备 | 状态：P6-01 LDAP 已实现；P6-02 增强算法未做；P6-03 中 Prometheus 决策来源已实现，Adapter 有意不做 |
| P7 算法质量 | A0 基线测量；A1 回测公平；A2 低估敏感评分；A3 保形分位数；A4 Holt-Winters 补强与 Theta；A5 直接多步；A6 KNN 修正；A7 MSTL；A8 全局模型设计；A9 小型时序基础模型离线评估；A10 周期画像分位数；A11 峰值包络趋势；A12 组合预测；A13 变点后重置 | 先量化现状，再修评估，再补算法；每个包有单测与无泄漏回测；四类主力算法（季节朴素+分位数基线、ETS/Holt-Winters、Theta、GBDT 直接多步分位数）的现状与缺口逐项对应工作包，见改进计划 2.1 | P2 回测与评分已存在 | 状态：A0～A7、A10～A13 已完成（A6 KNN 互斥区与时刻约束、A7 MSTL、A10 周期画像分位数、A11 峰值包络趋势、A12 组合预测、A13 变点后重置，见 [改进计划](../.agents/skills/algorithm-library/references/improvement-plan.md) 第 4 节与 [A0 基线](../.agents/skills/algorithm-library/references/baseline-a0.md)）；A8、A9 未开始；准确率结论只来自测试环境约 2.9 小时的数据，不是生产结论 |

P4A 与 P4B 在 P3 后可独立推进，但新工作负载先 Observe，再 Recommend，分别验证 Admission/Auto 才开放相应写入。P5 的模型评估和审计从 P2/P3 起建设，不能拖到执行上线后补证据。

P6 的编号不表示 LDAP 必须最后开发：它不阻塞内部本地账号验证的预测 MVP，可在 P1 后独立接入；平台完整验收前必须完成。正式日程需基于可用人力、集群与数据排期，本计划不承诺未经估算的日期。

## 5. 最小闭环与实现顺序

首个闭环选择一个明确 Namespace/Deployment/container 和一个可用 Prometheus 数据源：先采集 RPS/CPU/Memory 并生成真实滚动回测，再展示未来需求与资源/副本建议，最后分别开启准入和 operator 扩缩容执行。不依赖全部高级模型、全部决策来源或全部看板先完成。

下一批按以下顺序实施（功能已齐，缺口是真实环境的验证）：

1. 取得 P0-04 的输入（首个目标工作负载、指标映射、30 天以上历史、测试环境），用真实数据跑 Collect→Backtest→Evaluate→Recommend，记录各算法的 WAPE、峰值低估和训练耗时，据此决定默认参数与晋升阈值。
2. 在测试集群安装 CRD/RBAC/operator，用 Observe 观察、再用 Auto 在非关键工作负载上演练扩缩容、重启恢复、陈旧决策冻结和 HPA 冲突；通过后才把 forecast.allow_auto_mode 设为 true。
3. 核实目标 Kyverno 版本对 apiCall、header、caBundle 的支持（P0-02），验证草稿策略与 LimitRange/ResourceQuota 的交互，再开放 admission。
4. 在 MySQL、PostgreSQL 上验证全部新增迁移和读写；对真实 LDAP 验证 StartTLS 与 AD 的 userAccountControl。
5. 完成 P0-01～03 的契约收敛，并把 CRD 的 API group 定为项目域名。

算法首期以统计基线与树模型比较，不先建设大模型服务。第一版 Operator 决策接入顺序建议 Static → PlatformAPI，再追加 Prometheus/Adapter；保留四来源完整需求，每种来源单独验收，不能把两个来源完成写成全部完成。

## 6. 代码接入与交付要求

| 内容 | 复用入口 | 增量约束 |
|---|---|---|
| 管理 API | pkg/apis、pkg/app/server/api、pkg/app/router/router.go | DTO/权限/错误码/分页一致，handler 注入 DAO |
| 数据模型 | pkg/model、pkg/dao/rdb、pkg/migration | 三库兼容、唯一键、索引、版本和显式迁移；保留现有用户数据 |
| 调度与通知 | pkg/job、pkg/job/tasks/registry.go、pkg/notify | 复用幂等/重试/心跳；新训练/预测/回填任务分开注册 |
| UI 与导航 | web/src/views、service/api、typings/api | 公共表格、用户列配置、宽屏 schema 弹窗、中英文；同步后端菜单白名单与种子 |
| 运行生命周期 | pkg/cmd、pkg/config | Platform 复用现有入口；Operator 入口按两模块边界增量接入，不虚构当前已有命令 |
| 指标 | pkg/metrics | 复用 Registry，统一前缀、HELP/TYPE/单位，限制标签基数 |

新业务包、Go 算法包和部署文件的具体路径在 P0 设计时确定，本表只列真实已有位置。root 与 web 是独立 Git 仓库，跨端变更分别检查；未经指令不推送或重写远端。

每个工作包交付对应代码、schema/API、界面/权限（涉及时）、迁移/部署（涉及时）、相关测试及失败回退证据。预测性能结论记录样本、时间范围、horizon、对照模型和运行预算；不能仅凭演示图表完成验收。

## 7. 验收检查点

- 数据：真实历史窗口、单位与聚合层级明确；不伪造单 Pod RPS；保留缺样/重启/OOM 标记。
- 算法：滚动切分不泄漏；未来未知协变量不能作为已知输入；未优于基线不晋升，内存峰值保护单独检查。
- 策略：全局 bypass、禁用、固定周期、有效模型、合法 fallback、原始配置顺序可测试；配置变更有操作者与版本。
- 准入：调用失败与数值校验分别覆盖；保留完整原/应用资源；不在同步链路等待模型或 Kafka。
- Operator：按 PredictiveScaler CRD 真实扩缩容、所有权冲突、动态参数失效、冷却/限幅、重启恢复和 Ready 延迟有证据。
- 数据库：新增迁移及核心读写分别在 SQLite、MySQL、PostgreSQL 验证；只测 SQLite 不算三库验收。
- 运维：预测/建议/执行/Ready/指标能关联对账；可旁路、可回滚，敏感数据不入日志。

## 8. 进度维护规则与本轮记录

2026-10-24（多指标优化）：按 [规格](../.agents/skills/requirements/references/multi-metric-forecast-spec.md) 实现「预测扩缩容系统需求优化」：模块管理独立一级菜单（模块列表、指标来源）；动态指标来源（CPU/内存/GPU/请求量/网络/自定义 Prometheus/OOM/JVM，各有优先级、权重、统计方式、阈值，同资源多来源可配合并方式）；固定算法目录 + 创建时直接填参数的算法实例（删除参数模板与输入指标集，旧数据在迁移中转换），新增 hist_percentile、random_forest、knn、naive_bayes、rnn、gru、lstm（纯 Go）；多指标加权综合计算与逐来源计算依据；回放评分（副本/CPU/内存/GPU 准确度、OOM 扣分、完整度）、排名与历史、自动选择与切换滞后；低分/历史不足/预测超时等兜底原因码；最大副本数固定或动态扩展并受独立硬上限；operator 校验硬下限与硬上限，准入回答含 GPU 并全部落库；平台指标增加分数、在用算法、有效上限。验证：go vet/test 全绿（含 pkg/multimetric、scoring、迁移、decision、forecastsvc 多来源/评分/兜底/动态上限用例）、vue-tsc、locales 测试、浏览器验收 multimetric/optimization/platform 三个脚本全部通过（SQLite、模拟 Prometheus）。**未验证**：真实 Prometheus/集群/Kyverno（GPU 准入）、MySQL/PostgreSQL（`rank` 列已改名避开 MySQL 保留字）、真实负载上各算法与评分的准确性；动态上限不做集群容量检查。

每次完成工作包，同时更新第 1 节当前基线和第 4 节对应状态，再追加实际改动/验证/余项。测试通过只针对执行时的代码和环境；未执行项明确标记，不能继承为新功能已通过。

2026-10-23：完成 P0～P6 工作包拆解、依赖与验收设计，记录 Kyverno/独立 Webhook、两模块/旧参考、动态来源/硬边界的待对齐事项。下一执行项为 P0 收敛及 P1 数据源闭环。本轮没有新增预测、准入或控制代码，没有新增模型准确率或集群演练结论。

## 9. 历史记录：平台精简与品牌调整

以下保留早期记录，含当时的目录、数据库及 Git 状态；这些描述不是当前环境复验结果，当前基线以第 1 节为准。

### Platform simplification — 2026-10-23

Scope: Workflow Engine, System Management, Authorization Management, Task Center.
Removed pages, runtime wiring, APIs, collectors, strategies, data models, SDK dependencies
and locale groups for the retired business. Shared framework and UI conventions remain.
Default landing route: workflow_pipelines. The profile route stays hidden.

Existing database cleanup is explicit via --upgrade, with an inventory of retired tables.
Custom generic tasks, settings and permissions remain. No production database is upgraded automatically.
Validation: go test ./... passed; admin typecheck, locale tests and production build
passed. A scratch database initialized from all 131 existing table schemas upgraded
successfully to 49 tables with no retired business tables remaining. Playwright
verified all ten retained pages and the four visible menu roots without page errors;
removed business API endpoints returned 404. Screenshots were inspected.

The backend and existing nested admin Git repositories retain their original
layout. Each contains one main branch and one new initial commit, with old refs
and unreachable objects removed using Git's own history management commands.
No remote rewrite.

Directory cleanup: removed retired CLI, SSL deployment/provider, plugin and browser
exchange-client empty directories, 43 obsolete design/reference/progress documents,
and four retired trading executables. The original database and backups remain.
Recursive removal was rejected by the execution policy, so the remaining nested Git
metadata under refer/coredns_mysql and four old browser-profile cache directories
could not be removed. Source/document files were removed individually and empty
directories were removed without recursion.

Command cleanup: removed both standalone messaging-agent commands and their command
implementations, tests and binary entrypoint. The CLI exposes only all, platform,
server, job, notify and version (plus Cobra help/completion). Internal workflow
delivery adapters remain. The command-list regression test checks the complete
supported command set. Workflow persistence and internal delivery assembly now live
in pkg/app; cmd and pkg/cmd contain no messaging-provider names or dependencies.

Product identity: KubeForecast, focused on Kubernetes resource forecasting,
Pod scaling orchestration and capacity planning. Replaced the product name, SVG
logo, favicon, loading screen, login copy, CLI, Go module namespace, configuration
prefix, request headers, WebSocket protocol and deployment/build identifiers.
The four management modules remain the foundation; forecasting models, cluster
metrics ingestion and automatic Pod scaling are not implemented by this change.
Runtime databases and cryptographic key values are not changed by the rename.
Validation: Go tests, admin type checking, locale tests and production build passed.
Browser checks covered Chinese/English login, a 390px viewport, favicon delivery,
all ten management pages and the shared product title. Screenshots were inspected.

2026-10-23（P1-01 数据源与指标映射）：
- 后端新增：pkg/model（Datasource、MetricMapping，表 datasource、metric_mapping，硬删除，Enabled 不用 GORM default 以免 false 被改写）、pkg/promclient（官方 client_golang 封装，Basic/Bearer、CA、可选跳过校验，不外泄凭据）、pkg/dao/rdb（DatasourceDAO、MetricMappingDAO，删除前检查引用）、pkg/apis/datasource.go、handle_datasource.go、handle_metric_mapping.go；路由 /datasources（含 /options、/:id/test、batch-delete）、/metric-mappings、/metrics/catalog；密码/Token 经现有 EncryptValue 加密保存，DTO 只返回 has_* 标记，留空保持不变，更换地址或凭据会重置检测状态；column meta 注册 datasource_manage、metric_mapping_manage。
- 菜单：新增 datasource 目录及 datasource_connections、datasource_metric-mappings，已加入 IsPlatformRoute 白名单和显式种子；既有菜单测试由 4 个根目录改为 5 个。
- 映射校验：指标与单位固定配对（cpu→cores、memory→bytes、rps→rps、latency→seconds、oom→count）；层级决定必需模板变量；只允许 namespace/workload/service/pod/container/range；步长 15～3600 秒。仅做静态校验，未对真实 Prometheus 执行模板查询。
- 前端（web 独立仓库，未提交）：src/service/api/datasource.ts、src/typings/api/datasource.d.ts、views/datasource/connections 与 metric-mappings 两个页面、中英文文案；elegant-router 因 `sa gen-route` 在当前 node_modules 无法运行，路由文件按生成格式手工补全。
- 验证：go build ./...、go vet ./pkg/...、go test ./pkg/model ./pkg/promclient ./pkg/dao/... ./pkg/app/... ./pkg/migration 通过（SQLite；Prometheus 为 httptest 模拟）。go mod tidy 将 prometheus/common 提升为直接依赖。
- 未完成/未验证：前端 vue-tsc、单元测试、构建和浏览器检查均未执行——web/node_modules 的链接损坏（pnpm 链接无法被 Node 解析，vue-tsc 找不到 @volar/typescript），修复需要重新 pnpm install；MySQL/PostgreSQL 迁移与读写；真实 Prometheus 连接；“可用时间范围”留待 P1-02 通过真实查询得出；权限仅继承 ADMIN 通配，细分读写/凭据编辑权限未做。

2026-10-23（P1～P3 后端）：
- 采集（P1-02/03）：pkg/collector——Render 对变量做白名单校验（拒绝引号、反斜杠、花括号），pod 级映射不允许用于工作负载；Segments 按步长网格分段且不重叠不留洞；Fetch 受点数/并发预算约束，瞬时错误指数退避加抖动并限次，格式错误/多序列结果为永久错误不重试；Analyze 报告缺口、NaN、错位或重复样本、跳变、最新样本延迟；ToInput 只插值不超过 max_fill_steps 的缺口，更长缺口截断训练段，不外推陈旧尾部。promclient.QueryRange 对多序列结果报错，避免混入其他工作负载。
- 存储：metric_series（模块+指标唯一，映射或步长变化时清空旧样本，避免不同查询/分辨率混存）、metric_sample（series_id+ts 主键，upsert 幂等，按 lookback 清理）。
- 算法与回测（P2）：pkg/forecast 六个算法与参数 schema（未知参数、类型、范围、枚举、必填均校验）；pkg/backtest 滚动起点，哨兵值测试证明测试窗口数据不进训练窗口；晋升规则默认最严（WAPE 严格更优且峰值低估不更差），阈值由模块策略显式配置。
- 决策（P3）：pkg/decision 为纯函数；forecastsvc 串联 collect→predict→backtest→recommend；recommendation 为不可变版本并保存各字段来源、被考虑/被拒绝算法及原因、生效固定周期；predictions 只追加。
- 接口：/algorithms、/modules（含 algorithms、overrides、history、collect、predict、backtest、backtests、recommend）、/recommendations、/predictions、/controls/:id/bypass|resume（原因必填，经操作日志中间件审计）。任务：forecast.run（@every 5m）、forecast.backtest（@every 6h）由迁移种子创建，管理员改动不会被覆盖；单个模块配置或数据问题只记录日志，不使整个任务失败重试。
- 验证：go build ./...、go vet ./pkg/...、go test ./... 通过（SQLite；Prometheus 为 httptest/合成数据；Go race 检测器因本环境无 cgo 未运行）。
- 未完成/未验证：前端——模块、绑定、固定周期、历史与缺样、回测、推荐页面均未做，web/node_modules 链接损坏导致无法做类型检查和构建，已有数据源页面同样未验证；真实 Prometheus 与 30 天历史；MySQL/PostgreSQL；GBDT 与 Holt-Winters 在真实数据量下的耗时和准确率；模型产物持久化；预测业务指标（P5）；P0-02/03/04 仍待收敛。

2026-10-24（P4～P6 与页面，及随后的缺陷修复）：
- 新增：operator（pkg/operator、pkg/cmd/operator、deploy/operator）、准入与 operator 的平台接口（handle_runtime.go）、实测回填/排行榜/指标/审计导出/数据保留、LDAP（pkg/ldapauth、handle_ldap.go、ldaptask）、前端预测四页与 LDAP 页及参数编辑组件、scripts/e2e/forecast.mjs 与模拟 Prometheus、deploy/README.md。
- 浏览器验收（真实 Chromium，全新 SQLite 库，模拟 Prometheus）：17 项检查全部通过且浏览器零错误——菜单与图标、模块与绑定参数编辑、界面创建固定周期、运行流水线、历史图含缺口、回测列表、推荐解释、operator 上报列表、CSV 导出、LDAP 页、数据源页。同一服务上还验证了：开发者凭据只能访问被授权的 operator 路由（其余 403），job 进程按 5 秒调度时 forecast.run/forecast.evaluate 连续成功，/metrics 与 /readyz 输出。
- 验收中发现并修复的缺陷：(1) 模拟 Prometheus 暴露了客户端以 POST 表单发送 query_range，验收用的假服务因此需要读取表单；(2) 前端 CSV 导出把 "/" 与 "/api" 拼成协议相对 URL（//api/...），在生产构建中指向主机 "api"，已改为去掉基础地址的尾部斜杠；(3) 历史页的 30 天窗口在 1 分钟步长下超过点数上限而报错，改为服务端按桶平均并在响应中给出 bucket_seconds，质量仍按全量样本；(4) 采集 Fetch 在上下文已取消时可能因信号量选择随机而继续请求（测试偶发失败），现在入口与每次重试前都检查上下文；(5) 预测/评估/回测/推荐表无保留策略，新增按 DefaultRetention 的清理（预测 30 天，其余 180 天，各模块最新推荐永远保留，且避开 MySQL 不允许自引用子查询的写法）；(6) admission/auto 开关原先只在创建模块时检查，已改为每次回答都检查，关闭即刻停止；(7) 发现重复出现的缺口会使训练段被截断：模块默认 max_fill_steps=3 时，每天一段 30 分钟的监控缺口会让一天周期的季节模型因历史不足被跳过——这是有意的安全规则，已在 e2e 说明和页面提示中写明，需要时调大 max_fill_steps。
- 验证：go build ./...、go vet ./pkg/...、go test ./... 通过（SQLite；LDAP 客户端对本地 TLS 测试服务器；Kubernetes 用 fake client）；前端 vue-tsc、locales 单元测试、生产构建通过；Go race 检测器因无 cgo 未运行。web 仓库的 simple-git-hooks 因依赖重装被安装（pre-commit 会执行 typecheck/lint/fmt）。
- 仍未验证/未做：见本文件开头；另有：同一模块同一指标下同一算法只能绑定一组参数；operator 的 Prometheus/Adapter 决策来源；执行效果评估；web 仓库有用户原有未提交改动，本轮没有提交。

2026-10-24（优化需求拆分）：收到 8 条优化需求（预设与 Demo、算法中心、页面规范、删除工作流与 Telegram、Operator/Metrics 补齐、算法机制说明、模块管理与动态实例数、统一指标体系）。已核对现状并写出交付范围、验收标准和删除影响范围，见 .agents/skills/requirements/references/optimization-acceptance.md。本轮没有改动任何代码；删除类变更等待该文件第 0 节三项确认。

2026-10-24（优化需求落地：M4→M7→M1→M6→M5→M2→M3）：
- 范围与验收：见 [优化验收文档](../.agents/skills/requirements/references/optimization-acceptance.md)。假设 A（Telegram 整体删除）、B（登录日志页此前不存在，现补）、C（退役表仅显式 --upgrade 清理并先打印清单）按文档执行。
- 删除（M4）：pkg/workflow、telegram*、agentauth、message、notify 模板、相关 API/DAO/模型/迁移、前端页面与路由/文案/类型全部用 git rm 删除；仓库内 grep "workflow|工作流|telegram" 只剩迁移里带注释的退役表清单与测试、AGENTS 说明、旧 CHANGELOG 中无关的 GitHub Actions 条目。TelegramBinding 等用户模型字段、用户列表与个人中心 Telegram 项一并移除。
- 新增后端：pkg/preset、pkg/demo、forecastsvc/{demo,catalog,query,replicas,telemetry}.go、pkg/decision/solver.go、pkg/operator/controller/{prometheus,metrics}.go、handle_{preset,algorithm_center,forecast_metrics,login_log}.go、model.AlgorithmTemplate、ForecastModule 的 replica_mode/capacity_mode/pod requests/target_utilization/replicas_mapping_id、MetricMapping.preset_id、LoginAuditLog 的 method/reason/user_agent；CRD 增加 Prometheus 来源，用 controller-gen v0.19.0 重新生成 deepcopy/CRD/RBAC。
- 新增前端：datasource/presets、forecast/{algorithms,metrics}、system/login-log、components/business/replica-explain.vue；重做 forecast/{history,evaluation,recommendations} 与 authorization/ldap；模块页增加副本求解配置与模板选择。
- 验证：go build、go vet、gofmt 干净，go test ./... 全部通过（SQLite；Prometheus/LDAP/Kubernetes 为 httptest、本地 TLS 测试服务器、fake client）；前端 vue-tsc、locales 单元测试、oxfmt、新增文件的 oxlint 通过，生产构建成功。浏览器验收（真实 Chromium，全新 SQLite 库）：optimization.mjs 65/65 项、platform.mjs 全部页面、forecast.mjs（修正两处因页面重做而失效的选择器后）通过且浏览器零错误；--upgrade 在旧库结构副本上实跑，打印并删除 15 张退役表。
- 过程中发现并处理：①web/node_modules 的链接被 Git Bash 改成 Node 不可解析的符号链接，改用 PowerShell 下 pnpm install --offline 重建；②测试夹具的行时间戳来自真实时钟，与夹具时钟不一致会让窗口查询漏行，夹具改为让 GORM NowFunc 跟随夹具时钟；③查询接口在按步长对齐时可能多出一个桶，最小步长按 maxPoints-1 计算。
- 仍未验证/未做：真实 Prometheus（每条预设仍标未验证，至少要有一条 cAdvisor 预设在真实环境返回数据）、真实集群（operator 扩缩容、CRD/RBAC 安装、ServiceMonitor、Prometheus 来源）、Kyverno、MySQL/PostgreSQL（新增 algorithm_template、login_audit_log 新列、查询 SQL 只在 SQLite 上测过）、真实 LDAP StartTLS、估计出的单 Pod 容量与动态实例数对照压测、预测准确率；服务端排序目前只有登录日志；web 与根仓库的 Git 状态未提交。

2026-10-24（预测系统功能与页面重构，规格见 [forecast-redesign-spec.md](../.agents/skills/requirements/references/forecast-redesign-spec.md)）：
- 范围与假设：按规格第 0 节假设 A–E 实施（MACD 以单变量 macd_trend 形态进入；多指标=目标+协变量；LDAP 并入系统参数；JVM/K8s 指标均标未验证）。与规格第 5 节的偏差见规格末尾「实施结果与偏差」。
- 新增后端：pkg/ldapauth/store.go、pkg/forecast/{macd,features}.go、pkg/model/{input_set,ldap}.go、dao/rdb/{input_set,forecast_audit}.go、forecastsvc/{inputs,protect}.go、decision 的 OOM 地板与 jvm_heap 约束、handle_input_set.go；/input-sets、/fallbacks、/backtests，/recommendations、/scaling-decisions、/evaluations 的筛选与排序，模块列表 summary，导出写操作日志。
- 新增前端：forecast/{algorithm-templates,input-sets}、forecast-results/*、forecast-actions/*、monitoring/metrics，共享件 list-search-card / list-table-panel / metric-series-chart / recommendation-explain / ldap-settings-panel；系统参数页的 LDAP 分类；模块页的辅助指标映射、Fallback 与保护块、克隆。
- 验证：go build、go vet、gofmt 干净，go test ./... 全部通过（SQLite；含 decision/floor_test、forecastsvc/protect_test、api 的 input_set / fallback 测试）；前端 vue-tsc、locales 单元测试、oxfmt、生产构建通过。浏览器验收（真实 Chromium，全新 SQLite 库，模拟 Prometheus，scripts/e2e）：redesign.mjs 99 项、forecast.mjs 17 项、optimization.mjs 64 项全部通过且浏览器零错误，platform.mjs 十个页面通过；redesign.mjs 覆盖菜单、各新页面、OOM 地板（模拟源给出 1 次 OOM、堆 95%）出现在快照与说明中、降级记录与恢复、导出下载并进入操作日志、输入指标集界面创建与被引用不可删、模块克隆、LDAP 分类、1440/1024/390 三种宽度无横向溢出。
- 验收中发现并修复：①模拟源的每日缺口吞掉了唯一一次 OOM 事件（OOM 序列不再带缺口）；②历史页对尚未采集的指标返回 400 并在控制台报错，改为 200 + not_collected 并显示空状态；③候选算法判定晋升用的是不含输入指标集的模型版本，带输入集的绑定永远不合格，改为同一版本函数；④执行记录里的推荐版本列不在默认列内，链接移到操作列。
- 仍未验证/未做：真实 Prometheus（新增的 11 条预设与 JVM/OOM/Kubernetes 查询均未在真实环境运行）、真实集群、MySQL/PostgreSQL（新增 input_set、module_run_state、forecast_module 与 recommendation 新列、模块摘要里的 SUM(CASE WHEN) 聚合均只在 SQLite 验证）；手动「生成推荐」不更新模块运行状态，恢复滞后只由定时流水线推进；预测任务超过 2×TTL 未成功按 stale_prediction 记录，未单设 prediction_system_error；前端只有浏览器脚本验收，没有组件单测。

2026-10-25（模块管理与算法管理优化：模块 / 算法目录 / 算法实例 / 绑定关系拆分——**已被下一条取代**，算法实例与绑定关系已移除，其规格文件已删除）：
- 范围：模块只是一个 Kubernetes 工作负载（命名空间 + Deployment/StatefulSet/DaemonSet + 名称，一个工作负载一个模块），不再有容器、Service、指标映射、历史窗口与预测范围；算法实例独立创建、配置、采集、预测、回测、评估、追溯，拥有自己的指标来源，不依赖模块；模块与实例通过绑定关系（优先级、权重、启用）关联。移除“统一资源合并方式”（固定为加权平均）、评分窗口/切换阈值（内部常量）、最短历史、预测超时等含义不清的参数，保留的参数在页面上有说明。
- 后端：pkg/model/{forecast,algorithm_instance,metric_source}.go、dao/rdb/algorithm_instance.go、forecastsvc/{bound,recommend,collect,predict,backtest,evaluate,score,...}.go、handle_{instance,forecast,algorithm_center,runtime}.go、迁移 pkg/migration/forecast_sources.go；预测、回测、评估、历史按 instance_id 存储；定时任务 forecast.run 先实例后模块。
- 前端：模块管理 = 模块列表 + 绑定关系；预测管理 = 算法目录（只读）、算法实例、指标来源（按实例）、算法评分与排名；预测结果页按算法实例选择；监控指标按 scope 选择所有者；旧的 forecast-modules/sources 页面删除。
- 验证：go build、go vet、gofmt 干净，go test ./... 全部通过（SQLite）；前端 vue-tsc、locales 单元测试、oxfmt、生产构建通过（oxlint 仍报 8 条既有文件的问题，非本次引入）；浏览器验收（真实 Chromium，全新 SQLite 库，模拟 Prometheus）：scripts/e2e/modules-instances.mjs 41/41、platform.mjs 全部页面通过。
- 验收中发现并修复：无绑定的实例追溯接口返回 bindings:null，前端追溯弹窗会因此报错，改为空数组并加测试；监控指标查询接口只认 module_id，实例级指标无法查询，改为按指标 scope 接受 instance_id 或 module_id。
- 删除：旧的 scripts/e2e/multimetric.mjs、optimization.mjs（依赖“来源与实例属于模块”的旧接口与页面，由 modules-instances.mjs 取代）；其余历史记录里提到它们的段落描述的是当时的验证，不代表现状。
- 迁移行为：旧库的数据按规格第 6 节转换，旧的按模块归档的历史/预测/回测数据被删除并打印清单、由新实例重新采集；同一工作负载的重复模块被停用并标 DUPLICATE。该转换只在构造的旧库结构副本上测试过，未在真实旧库、MySQL、PostgreSQL 上运行。
- 仍未验证/未做：真实 Prometheus、真实集群（Kyverno 策略已改为按 Deployment/StatefulSet/DaemonSet 推导并传 kind，仍是未在集群验证的参考草案；operator 的 DaemonSet 无副本路径未在集群验证）、MySQL/PostgreSQL。

2026-10-25（算法管理与模块优化，规格见 [module-algorithm-spec.md](../.agents/skills/requirements/references/module-algorithm-spec.md)；**取代**上一条“模块 / 算法实例 / 绑定关系”的设计）：
- 需求：算法统一配置、统一应用于所有模块，不再创建算法实例、不再手动绑定；模块创建后自动关联所有已启用算法，新增或启用算法自动应用到现有模块；算法目录改为算法管理（分页、搜索、编辑，参数带问号提示）；曲线从指标查询页拆成独立页面；授权管理为一级菜单；预测结果与推荐执行页统一模板。
- 后端：新增 `algorithm_config`（每个目录算法一行，系统创建，基线中无必填参数者默认启用）；指标来源、历史窗口、预测周期回到模块；历史序列、预测、回测、评估按 `module_id` 存储；`AlgorithmRun` 只是内存视图；`/algorithms` 分页/搜索/过滤并可 `PUT` 修改设置；移除 `/algorithm-instances`、`/bindings`；一个算法跑不起来只在自己的结果里报错，不影响其他算法与任务。
- 前端：模块管理 = 模块列表 + 指标来源；预测管理 = 算法管理 + 算法评分与排名；监控指标 = 指标目录 + 指标曲线（新独立页）；结果与动作页统一时间窗口选项（6 小时/24 小时/7 天/30 天）、模块选择可搜索、操作列与按钮尺寸；算法编辑页每个参数有含义/计算作用/配置依据提示（`paramHelp` 文案）。
- 菜单：种子每次重置父级，旧库里挂在别处的菜单（含授权管理）回到一级；`forecast_catalog`、`forecast_sources`、`forecast-modules_bindings` 退役，授权由 `menuSuccessors` 带到 `forecast_algorithms` 与 `forecast-modules_sources`，`monitoring_metrics` 的授权带到 `monitoring_curves`。
- 迁移：从实例形态升级时历史按实例归档的数据删除并打印清单，模块取第一个已启用绑定实例的来源与窗口，已启用实例的算法在平台设置里启用并带上参数；`algorithm_instance`、`module_binding` 由退役清理删除；转换只在构造的旧库副本上测过。
- 验证：go build、go vet、gofmt 干净，go test ./... 全部通过（SQLite）；前端 vue-tsc、locales 单元测试、oxfmt、生产构建通过；浏览器验收（真实 Chromium，全新 SQLite 库，模拟 Prometheus）：scripts/e2e/modules-algorithms.mjs 41/41、platform.mjs 全部页面通过。
- 验收中发现并修复：①创建时默认启用的 `seasonal_naive` 缺必填周期参数，使整个模块的回测失败——默认只启用不需要参数的基线，且一个算法回测失败不再中断其他算法；②无参数算法的列表返回缺少 `params`，前端 `row.params.length` 会崩溃，改为总是返回数组；③用 PowerShell 的 Set-Content 改文件把 en-us.ts 的非 ASCII 字符弄成乱码，按 GBK 往返恢复并核对了仅有的 6 处无法往返的行（此后改文件用 Python/Edit，不用 Set-Content）。
- 删除：旧的 scripts/e2e/modules-instances.mjs（上一版）。
- 仍未验证/未做：真实 Prometheus、真实集群（Kyverno 策略仍是参考草案）、MySQL/PostgreSQL；平台级参数按步数计，要求各模块指标映射步长一致（已在页面与规格里说明，未做自动换算）。

2026-10-25（算法框架、Prometheus 指标数据集、数据周期、评分页面，规格见 [algorithm-framework-spec.md](../.agents/skills/requirements/references/algorithm-framework-spec.md)）：
- 算法框架：算法只经 `forecast.Register` 进入注册表（内置算法逐个注册、校验与构造不再有第二处算法名）；新增**公式算法**——管理员给出参数 Schema 与公式，由平台自带的解释器真实执行（无循环/赋值，节点与运算预算，结果非负且有限），保存前在合成序列上试算，编辑公式即换实现版本；算法管理页可新增、编辑、启用/停用、删除自定义算法，编辑器带函数/变量提示、参数 Schema 编辑与试算曲线。不上传代码；新增 Go 实现走同一个注册接口。
- 训练记录：不保存模型权重（每次在历史窗口上重新拟合、固定种子），改为每次运行一条 `forecast_training_run`（窗口、样本数、参数、公式、模型版本、耗时、状态与原因，保留 30 天）和「预测与训练记录」页；同一版本、参数与窗口可复现预测。
- 数据集：指标来源增加换算系数、精度、聚合方式、缺失处理；指标分 CPU/内存/GPU/网络/其他并补充利用率、显存容量、连接数、错误率类型；创建来源时可直接写 PromQL（同时创建映射）；模块采样粒度（5m/15m/1h/1d）把查询点聚合到统一栅格（桶标在结束时刻，不看未来，只留完整桶）；形态变化会重建序列，升级前的序列不被清空。监控指标菜单新增「Prometheus 指标数据集」页（取代模块下的指标来源页），含数据周期弹窗、算法需求、立即采集。
- 周期：模块新增采样粒度、训练窗口长度、最少数据量、执行频率（调度按最近训练记录判断是否到期），保存时校验粒度、历史范围、预测跨度、执行频率与查询步长彼此自洽；`GET /modules/:id/requirements` 按算法当前参数与栅格算出需要的历史并报告是否放得下。页面有 5m/15m/1h/1d/30d 预设与自定义输入。
- 评分页面：拆为算法评分（记录与各部分的准确度/误差/权重/贡献）、当前排名（综合排名 + 指标评分：预测值对比 Prometheus 实际值的每指标误差/准确度/权重与综合）、历史排名（每次评估的排名与曲线）；回测、实测评估、准入决策补充详情弹窗。
- 菜单与迁移：新增 5 个页面；`forecast_scores`、`forecast-modules_sources` 退役并把授权带到新页面；升级测试（上一版结构的库）覆盖新增列默认值、新表与授权。
- 验证：go build、go vet、gofmt 干净，go test ./... 全部通过（SQLite）；前端 vue-tsc、locales 单元测试、oxfmt、生产构建通过；浏览器验收（真实 Chromium，全新 SQLite 库，模拟 Prometheus）：scripts/e2e/modules-algorithms.mjs 92/92、platform.mjs 全部页面通过。
- 验收中发现并修复：①新增页面的默认可见列只有 5 个，数据集页把聚合方式放在第 6 列而不可见，调整列顺序；②数据集页顶部的周期摘要用 NDescriptions 竖排标签挤在一起，且「历史窗口(小时)」下显示「2d」，改为独立标签；③locales 单元测试不允许消息里出现 `{{…}}`，PromQL 示例移到代码里、提示用 `{'{{namespace}}'}` 写法。
- 仍未验证/未做：真实 Prometheus（自定义 PromQL 与新增类型）、真实集群、MySQL/PostgreSQL（新增列与历史排名的分组查询只在 SQLite 跑过）、真实负载上的准确率；浏览器里没有可对比的实测数据（预测目标时间尚未到来），指标评分的数据路径由「预测→推进时钟→采集→评估→评分」的服务测试与接口测试验证；公式算法只读数值/布尔参数；算法需求只报告不足，不自动延长历史范围。

2026-10-25（本地 kubeadm 测试床：operator 扩缩容与 Kyverno 准入，见 deploy/testbed/README.md）：
- operator：在 Docker Desktop 集群上部署，`PredictiveScaler` Auto 模式下按平台决策真实改了 Deployment 副本（8→3、3→9、9→7，受 maxStep 与冷却限制）；压力用 `load-web`（总需求由 ConfigMap 旋钮设定，Pod 均分）模拟。
- 准入：修复两处——`auto` 模式的模块被准入接口拒绝（auto 本应包含 admission）；准入从不返回 limits。模块新增 `limit_cpu_ratio`/`limit_memory_ratio`（0＝不管 limit），接口返回 `limits`；Kyverno 策略重写（一个 Pod 只调一次平台、凭据读自 Secret、平台不可用/无模块/凭据错都放行不报错）并在 Kyverno 1.19.1 上验证：有模块的新 Pod request 与 limit 被改写，无模块的原样，平台宕机时原样。
- 发现的环境问题：本集群的 cAdvisor 序列没有 container 标签，预设里的 `container!=""` 会过滤掉全部数据（测试床映射去掉该过滤）；测试负载最初用的 `date +%s%N` 在该镜像上无效，Pod 一直满载，已改用 /proc/uptime。
- 仍未验证：只改新 Pod（运行中的 Pod 要等滚动）；limits 比例在真实负载上的 OOM 行为；多节点集群、真实 Prometheus 规模；Kyverno 其他版本。


2026-10-26（预测扩缩容系统问题整理：模块/算法/排名/结果/导出/指标/前端规范，规格见 [algorithm-framework-spec.md](../.agents/skills/requirements/references/algorithm-framework-spec.md) 第 8 节）：
- 模块与 CRD：平台本来就不生成 CRD；operator 新增 `--platform-url` 模式（`GET /operator/modules`），直接按模块配置经 scale 子资源调副本，模块新增扩/缩容步长与冷却；模块操作列收为 编辑/数据集/运行/更多（固定在右侧，不再溢出）。Helm/清单/README 补了该模式。
- 算法：新增「基于内置算法」的派生算法（参数随所选算法变化，Naive 无参数、Seasonal Naive 季节周期、EWMA 平滑系数），公式算法保留；`macd_trend` 归入技术指标。
- 排名：当前排名/指标评分/历史排名明细改为排行榜（得分条、各部分准确度、名次变化 `previous_rank`），不再用表格。
- 结果页：趋势对比与指标曲线拆成图表/摘要卡片/分页数据点；训练记录页加运行概览（`/training-runs/summary`）并改名「训练记录」；审计导出改为弹窗。
- 指标：指标目录统一列出数据映射（可新增/编辑/删除）、内置模板、平台导出；映射表单与内置映射库共用，PromQL 可由模板生成并手改。
- 前端：各列表页操作按钮统一（small、编辑 primary ghost、删除 error ghost、居中）。
- 发现并修复：审计导出的帮助文案含 `@`，vue-i18n 编译失败使该页整页空白（已存在的 bug）；locales 测试现在对每条消息跑编译。
- 验证：go build/vet/gofmt 干净，go test 通过（全量并行跑时 api 包编译被内存挤掉，单独 -p 1 通过）；vue-tsc、locales 测试通过，生产构建通过；浏览器 modules-algorithms.mjs 141/141（全新 SQLite 库，模拟 Prometheus；指标评分的排行榜用拦截的 API 应答验证，因为浏览器里没有可对比的实测值）；Docker Desktop 集群上 module 驱动的 operator 真实 2→4、改 max 后 4→3。
- 未验证：Helm chart 未渲染（本机无 helm）；MySQL/PostgreSQL（新列、`PreviousScores` 的 SQL）；运行中 operator 的重启后冷却重新计时；其余旧页（权限/系统/任务中心）只统一了操作按钮，没有迁到新的列表组件。

2026-10-26（问题修复版：列表规范、真实历史、K 线对比、指标查询测试，规格与验收见 [algorithm-framework-spec.md](../.agents/skills/requirements/references/algorithm-framework-spec.md) 第 9 节）：
- 全局：各列表统一 ListSearchCard + ListTablePanel（排名用 RankList），复选框与批量操作（模块/算法启用停用删除，记录类批量删除，排名/历史类导出所选），起止时间选择器替代“最近 N 小时”，三位小数与单位换算（`utils/format.ts`），操作列统一；模块列表删除“运行”，数据集/克隆/固定周期/旁路收进“数据集”按钮与“更多”。
- 模块：最小/最大/默认及故障回退副本数与恢复周期在同一个“副本”配置段，“安全边界”不再有副本上下限。
- 算法：列表复选框、批量与删除；详情显示实际保存的参数值，并按模块列出关联的数据集（含 PromQL）、评分与各指标表现。
- 排名：当前排名的综合结果与标准指标排名是两个独立的、可滚动分页勾选的列表，覆盖所有已启用且有结果的算法；历史排名有复选框、导出所选与起止时间。
- 历史数据：改名“历史数据”；新增 `GET /modules/:id/compare`，实际值与实际副本数在请求时从 Prometheus 查询（含模块创建前的历史），只有整个区间没有样本才判定缺失；K 线、多算法预测对比、预测副本数、逐时间点提示（9.2 数据口径与时间对齐）。趋势对比同一组件，删除预测摘要；指标曲线改为真实 Prometheus K 线，删除序列摘要。
- 回测/实测/训练：起止时间、复选框与批量删除（`POST /records/batch-delete`）；实测详情可选精度/WAPE/MAE/RMSE/偏差/峰值低估/覆盖率并说明计算；训练记录增加数据集列与命中率及状态含义，统计全部来自训练记录。
- 准入记录：补 limits、模式、算法、工作负载类型（新列），详情给出请求值的前后变化。
- Metrics 数据集：改名；新增 `POST /metric-queries/test` 与查询测试弹窗（模板校验、Prometheus 语法/执行错误、曲线预览、单位/精度/换算），新增自定义 PromQL 必须先测试；指标目录支持批量删除映射与测试查询。
- 发现并修复：SQLite 以文本存时间且带各自偏移，起止时间筛选在跨时区时比较错误（结束时间筛不出刚产生的记录）——SQLite 统一以 UTC 写入，筛选边界转 UTC。
- 验证：go build/vet/gofmt，go test -p 1 ./... 全部通过（pkg/app/server/api 新增 compare、批量、查询测试用例）；vue-tsc、lint（0 错误）、locales 编译测试；浏览器 e2e `modules-algorithms.mjs`（全新 SQLite 库 + 模拟 Prometheus）168/168。指标评分排行榜的有数据路径用拦截的 API 应答验证。
- 未验证/未做：真实 Prometheus 上的 K 线对比与查询测试（只在模拟 Prometheus 上跑）；MySQL/PostgreSQL（新增列与 SQL）；SQLite 中旧行仍是本地时区偏移，旧数据上的起止筛选在跨时区时可能不准，新写入的行是 UTC；资源推荐快照与降级回退按要求未改，仍用原来的“最近 N 小时”筛选。

2026-10-26（问题修复 R3：真实数据链路、合并排名、算法覆盖、记录链路，规格与验收见 [algorithm-framework-spec.md](../.agents/skills/requirements/references/algorithm-framework-spec.md) 第 10 节）：
- 根因（在用户真实库的副本上复现）：① SQLite 旧行带 `+08:00`，起止时间筛选按文本比较把它们全部筛掉——回测 537、扩缩容 122、实测评估 5662 行都在库里，页面是空的；新增迁移 `normalizeSQLiteTimes` 把所有 datetime 列重写为 UTC。② 评分回放没有可用窗口时仍存一行 0 分并按名称排名（库里 435 行评分有 355 行是这种）：现在不产生评分行，排名查询只读 `windows > 0`。③ random_forest 启用时没有必填参数 `lags`，715 次训练全部失败：启用（单个与批量）前按参数 schema 校验。④ 准入记录为 0：系统设置 `forecast.allow_admission_mode=false` 时旧代码在查模块之前就拒绝、不落记录；现在先查模块，拒绝也记录。⑤ 历史数据「两个列表没有数据却有大量分页」：容器 `h-460px` 在可滚动页面里被 flex 压缩到只剩表头与分页，容器改 `flex-shrink-0`，`ListTablePanel` 对空列表不显示分页。
- 全部已启用算法都出现：`compare` 的 `statuses`、排行榜的 `status/reason`、指标评分的 `unranked`、排名的 `status/reason`；状态取自训练记录（ready / skipped / failed / not_run / no_result）。真实库副本上 10 个已启用算法：排行榜 7 个有名次，3 个（holt_winters、knn 跳过：历史质量不足；random_forest 失败：缺 lags）列出原因。
- 页面：当前排名与历史排名与两个独立列表合为一页「排名」（时间范围区分当前与历史，可打开某次评估，历史评估列表服务端分页）；删除 `ranking-history` 页与菜单；历史数据 = 真实历史 + 预测记录，趋势对比 = 各算法预测对实际 + 误差汇总 + 逐点误差；训练记录概览只留四个数，成功/跳过/失败与原因在详情里；新增「算法运行情况」页（每算法运行次数/成败/命中率/平均耗时/最近一次/常见原因，来自训练记录）；删除审计导出（接口、页面、菜单、权限 `forecast:audit:export`，迁移清理已有授权，操作日志不再特判）；图表工具栏与图例分行、范围滑块 44px、图高 480–500；下拉滚动条留槽；Logo 换为 `site/assets/favicon.svg`；模块编辑页新增「指标预览」，用与 Metrics 数据集页相同的 `PromqlTester`。
- 验证：`go vet`、`gofmt`、`go test -p 1 ./...`（43 个包全部通过，新增时间迁移、状态/启用校验、准入拒绝记录用例）；`vue-tsc`、lint（0 错误）、locales 编译测试；浏览器 e2e `modules-algorithms.mjs`（全新 SQLite + 模拟 Prometheus）177/177。另在真实库副本（`target/ui-real`，端口 20141，不动 20001 的实例）上逐页核对：回测、扩缩容执行、历史排名、10 个算法的状态都有数据或明确原因；对副本发一次准入请求，确认记录落库并在准入记录页显示（该行只在副本里）。
- 未验证/未做：真实 Kyverno 调用准入接口（用户环境里没有请求到达平台，也没有部署验证）；真实 Prometheus 的 K 线对比与查询测试；MySQL/PostgreSQL（时间迁移只对 SQLite，新 SQL 只在 SQLite 跑过）；用户库里已存在的 355 行 0 窗口评分仍在「算法评分」记录页里（该页按要求不改），只是不再进入排名；`forecast-results_algorithm-runs` 菜单对已有自定义角色不会自动授权，需要在授权管理里加；用户的 20001 实例需要用新二进制并执行 `--migration` 才会有时间迁移与菜单变更。

2026-10-27（问题修复 R4：排名页分页、历史数据与趋势合并、训练数据，规格见 [algorithm-framework-spec.md](../.agents/skills/requirements/references/algorithm-framework-spec.md) 第 11 节）：
- 排名页分页不可用的根因：卡片内 `NSpin` 无高度，列表撑到 1122px，分页条被 `overflow: hidden` 裁在卡片外（浏览器探针量得分页条 y=1294，视口 900）。`RankList` 改为列布局并约束高度；排名页拆成当前排名 / 按指标排名 / 历史变化三个标签，一次一个视图。
- 历史数据与预测趋势对比是同一个组件同一个接口，合并为「历史数据」（曲线、各算法误差、预测记录、数据缺口四个视图）；删除趋势对比页、路由、菜单（迁移 `forecast-results_trends` → 历史数据）。
- 新增 `GET /training-runs/{id}/data` 与训练记录详情的「训练数据」标签：该次训练读取的窗口样本与它写出的预测。
- 验证：见下一条的实测结果。

2026-10-27（问题修复 R5：去掉所有标签页，预测领先于实际）：
- 用户多次要求不要标签页，R4 仍用了分段标签（排名页 3 个、历史数据 4 个、训练记录详情 2 个）——违反已有约束，现已全部去掉，并把规则写成硬约束进 `constraint/frontend.md`。排名页改为一个列表（「评估时间」筛选选历史评估，指标排名在详情里）；历史数据页改为一个图表 + 一个预测记录列表；训练记录详情纵向排列运行信息与训练数据。
- 图表增加「现在」竖线与「预测（尚未发生）」着色区，默认窗口最近 3 小时再向后一个预测时长，让预测领先于实际值。
- 验证见本轮最终报告。

2026-10-27（问题修复 R6：全局界面规范、模块/记录/系统/用户整理，规格见 [algorithm-framework-spec.md](../.agents/skills/requirements/references/algorithm-framework-spec.md) 第 13 节：优先级、页面名称、删除后的数据处理、验收标准）：
- 已实现：Logo 亮暗两版；统一的 Kibana 风格时间组件（相对窗口/固定范围，每页一个）；关键列独立复制图标；评分类列与表单字段的“?”说明（`help-title.ts`、`help-label.vue`，文案在 `page.formHelp.*`）；Naive 默认“请输入/请选择”全局置空并给搜索条件补字段名；模式与状态名称中文化；模块页去掉固定周期、数据集入口与底部指标块，改为按需「指标预览」；数据集页去掉周期/算法需求/采集行；删除滚动回测、运行概况、指标目录页（菜单随迁移退役）；资源推荐只列最新版（每模块保留 20 版）；「扩缩容记录」与准入记录各保留 30 天；历史数据去掉概览，图表按 Grafana 风格重写，预测在前，线条类型下拉；训练一直“跳过”的根因是 `window_steps` 截断训练历史，已修；用户有本地/LDAP 类型、联系方式 EAV（`user_contact`，可存任意类型，首个邮箱/电话镜像回旧列）、角色与用户批量启用/停用/删除；集群接入凭据有用途与复制；个人中心去掉标签页，联系方式改为同一套 EAV。
- 本轮顺带发现并修复的既有缺陷：SQLite 连接池只有 1 个连接，casbin 的 `Enforcer.SavePolicy` 在事务内再走连接池，创建/修改用户或角色会永久卡住整个服务；`SyncPolicies` 改为事务内重写 `casbin_rule`。预测列表同一秒内排序不稳（新旧两次运行的预测值顺序不定），排序末位改为 `id DESC`。
- 验证（本轮最终代码）：`go vet`、`gofmt`、`go test -p 1 -count=1 ./...` 全部通过；web `vue-tsc`、`oxlint`、`oxfmt --check`、locales 单测（4 项）、`vite build` 通过；全新 SQLite + 假 Prometheus 上 `scripts/e2e/modules-algorithms.mjs` 201/201；逐页清扫脚本（占位文案、英文单词、标签页）无遗留。e2e 需要 `fake-prometheus.mjs` 的每日空洞离当前时间远一点：在回看窗口内会让连续历史低于 80% 而被正确拒绝，用 `HOLE_MINUTE` 调整。
- 未完成或只做了一部分：中文化与搜索框宽度按清扫脚本和抽查页面为准，未做逐像素审计；性能只统计了请求次数并去掉重复请求，没有大数据量渲染实测；指标目录的有效功能只并入了数据集表单；任务定义里的两个轮询周期字段当前调度器没有读取（说明已写进“?”提示）；用户中心的密码/OTP 表单没有逐项“?”；Kyverno 策略未在集群安装；平台 20001 需重新编译并执行 `--migration` 后才生效；自定义角色不会自动获得 `forecast-results_algorithm-runs` 菜单。

2026-10-27（问题修复 R7：固定周期、统一预测/实际表述、查询单一入口、启用/禁用，规格见 [algorithm-framework-spec.md](../.agents/skills/requirements/references/algorithm-framework-spec.md) 第 14 节：优先级、固定周期冲突规则、决策可追溯、验收标准）：
- 固定周期恢复为模块表单里的分区（`period-editor.vue`）：名称、启用/禁用、起止时间、固定副本/CPU/内存、优先级，随模块保存（新增、更新、删除）；后端决策一直是“旁路 > 固定周期 > 算法 > 兜底 > 原值”，本轮加了服务级测试（窗口内固定、窗口后恢复、禁用不生效）。每周重复的周期只列出、不改动。
- 决策详情新增 `periods`（每个周期是否启用/生效）与 `sources`（数据源、查询、层级、单位、步长、统计、取值依据），解释弹窗按“字段 → 生效顺序一句话 → 固定周期 → 指标来源 → 计算过程”展示。
- 模块：行内正好四个按钮（编辑、删除、克隆、旁路/解除旁路），去掉“更多”；表单新增“参与计算的指标”分区（逐条启用/禁用、预览，查询在数据集页添加）；指标预览打开即执行。
- 数据集：只有“直接写查询”一种模式，“执行查询”在同一表单内显示结果，删除“自定义/选择映射”二选一和第二个对话框；可套用内置模板（只填充表单）；编辑时查询不变则保持原映射，改了则写新映射并在无人使用时删除旧映射（`PUT /modules/:id/sources/:sid` 新增 `custom`）；删除“指标映射”页和菜单（授权迁到 Metrics 数据集），模板页只读。
- 历史数据：算法选择与状态标签条并入图表图例（名称 · 最新预测值 · WAPE，点击显示/隐藏，无预测的灰显并提示原因），预测线画在实际线之上。
- 启用/禁用统一用 `status-select.vue`，用词统一为“启用/禁用”（原有“停用”全部替换）；集群接入凭据列名改为 Access Key（AK）/ Secret Key（SK，已隐藏），页内说明它不是 Kubernetes Secret 也不是盐值；LDAP 认证配置成为系统参数页的按钮（带启用/禁用状态）+ 抽屉；操作列宽按按钮数量收紧。
- 顺带修复：SQLite 连接池只有 1 个连接时，casbin `SavePolicy` 会让用户/角色创建永久卡住；登录不再读联系方式表（未迁移的库也能登录）。
- 验证：`go vet`、`gofmt`、`go test -p 1 -count=1 ./...` 通过；web `vue-tsc`、`oxlint`、`oxfmt`、locales 单测、`vite build` 通过；全新 SQLite + 假 Prometheus 的 e2e（含在模块表单里真实添加固定周期并用接口核对、窗口内/禁用后的推荐来源）。
- 未完成或只做了一部分：操作列里按钮已按数量收紧，但表格会把总宽度按比例拉伸，列宽仍比按钮宽约 90px（没找到不影响其它列的办法）；图例里的文字只能靠截图核对（ECharts 画在 canvas 上，e2e 读不到）；指标映射页已删但 `metric_mapping` 表、接口和模板页保留；固定周期只在界面里支持绝对时间段；新建模块时还不能在表单里直接绑定指标（保存后到数据集页添加，表单里已写明）；用户中心密码/OTP 表单没有逐项“?”；你的 20001 实例需要重新编译并执行 `--migration`。

2026-10-27（问题修复 R8：指标模板、状态标签、时间选择器、操作列，规格见 [algorithm-framework-spec.md](../.agents/skills/requirements/references/algorithm-framework-spec.md) 第 15 节）：
- 指标模板：恢复「指标模板」页（菜单 `datasource_metric-mappings`，表 `metric_mapping`，名字不再叫“映射”）：写一次 PromQL（带 `{{namespace}}`、`{{workload}}` 变量），可套用内置模板，表单里选一个模块就把变量换成该模块的参数并执行，结果在同一表单里显示；模块表单的“参与计算的指标”改成和固定周期一样的动态多行（模板 + 权重 + 启用/禁用 + 预览），保存时增删改对应的模块指标；数据集页只选模板、设作用，不再写查询。模块里不再内置任何指标。删除被模块指标使用的模板返回 409（此前会留下指向不存在模板的指标，已补 DAO `UsedBy` 与测试）。
- 列表里的启用/禁用改为状态标签（算法列表原来的选择框已去掉），状态改在编辑表单或批量菜单里。
- 时间选择器：触发按钮改为普通按钮样式（背景/边框随亮暗主题，高度与同行一致），历史数据、当前排名、曲线三页把它移到筛选区最后；e2e 检查每页它都是最后一个字段、只有一个。
- 操作列：表格多出的宽度改由最宽的文字列吸收，操作列只占按钮宽度（数据源、模块、集群凭据、指标模板实测列宽与按钮宽度之差 ≤ 60px）。模块表单里“参与计算的指标”与“固定周期”之间加了分隔线和间距。
- 另外：operator 已用新凭据重新部署并确认可用（镜像改用数字用户 `10001`，`runAsNonRoot` 才能通过；`forecast-test/kubeforecast-platform` 的 Secret 已换成新的 AK:SK，四个 PredictiveScaler 都是 Ready，决策有效）。
- 验证：`go vet`、`gofmt`、`go test -p 1 -count=1 ./...`；web `vue-tsc`、`oxlint`、`oxfmt`、locales 单测、`vite build`；全新 SQLite + 假 Prometheus 的 e2e（含模块表单里添加指标模板并核对权重、指标模板页新增并在表单内测试、时间选择器位置与主题、算法状态标签、操作列宽度）。
- 未完成或只做了一部分：历史数据页按要求没动；暗色主题下时间选择器只用截图核对（亮色由 e2e 检查）；`metric_mapping` 的 API 路径仍叫 `/metric-mappings`（只改了界面和文案名）；新建模块时可以直接在表单里添加指标模板（保存模块后一并写入），但预览要等保存后才能按该模块的参数渲染；你的 20001 平台要重新编译并 `--migration` 才会有“指标模板”菜单。

2026-10-28（问题修复 R9：LDAP 回到系统参数列表、行内按钮、时长输入）：
- 模块保存报 `cannot unmarshal number 0.1666…`：时长输入组件在“分钟”字段里仍提供秒单位，选 10 秒发出小数。现在单位只提供不小于字段单位的选项，发出的值一律取整。
- LDAP：删除抽屉和单独表单（`ldap-settings-panel.vue`），`ldap.*` 作为普通系统参数出现在列表（分类“LDAP 认证”），逐项编辑；`ldap.bind_password`、`ldap.ca_cert` 列表只显示遮盖，回传遮盖表示不改。保存时按整体校验：未启用时只检查各项格式，启用时校验完整并连通目录，失败不保存。迁移 `EnsureDefaultConfigs` 写入默认的 ldap.* 行（默认关闭）。`/ldap/settings`、`/ldap/test`、`/ldap/sync` 接口保留，页面不再使用测试与同步按钮（启用时自动检查连接，同步仍由每小时任务执行）。测试：`TestLDAPParametersAreEditedLikeAnyOtherSystemParameter`。
- 行内操作按钮统一 `tiny`，主题同任务列表：编辑实心 primary，启用/禁用/重置 warning/success ghost，删除 error ghost。技能 `constraint/frontend.md`（两侧）已更新。
- 验证：`go test ./pkg/...` 通过；eslint 对改动文件无错误（顺带清掉 compare-view、数据集页的未使用变量）；oxfmt 已格式化。`vue-tsc` 仍有 2 个旧错误（`forecast.d.ts` 的 `CombineMode` 未定义、`naive-ui.d.ts` 的 `SelectBaseOption`），不是本次改动，尚未处理，会挡住预提交的 typecheck。浏览器 e2e 未重跑（脚本里旧的 LDAP 抽屉检查已改成列表检查）。

2026-10-28（算法评审与改进计划，P7）：评审 `pkg/forecast`（Naive、SeasonalNaive、EWMA、Holt-Winters、岭回归、KNN、随机森林、MACD、百分位）、`pkg/backtest`、`forecastsvc/backtest.go`；GBDT、神经网络、朴素贝叶斯、公式算法只看了注释与参数。发现 9 项（递归多步收敛、WAPE 对低估不敏感、回测起点少、候选与基线起点可能不同、KNN 重复计数等），选型取舍与工作包 A0～A8 写入 [improvement-plan.md](../.agents/skills/algorithm-library/references/improvement-plan.md)，并在 algorithm-library、model-evaluation 技能里加了指引（两侧已同步）。**本轮没有改任何代码**，没有跑回测，所以文中的“业界依据”不是本项目结论；A0 会用测试平台数据给出第一份实测基线。下一步：A0 → A1 → A2。

2026-10-28（P7 A0/A1/A2：实测基线、回测公平、低估敏感评分）：
- **A0**：新增 `scripts/baseline`（`go run ./scripts/baseline -db <库>`，在数据库副本上跑平台自己的回测路径并输出 markdown 表，不碰原库）。在测试平台数据（load-api、load-batch 各 CPU/内存，约 2.9 小时、步长 60 秒、视野 10 步；load-web 只有 6 分钟历史多数被跳过，load-cache 快照时数据过期未计入）上用同一份快照对比旧、新代码，结果和读数注意事项见 [baseline-a0.md](../.agents/skills/algorithm-library/references/baseline-a0.md)。
- **A1**：`backtest.Origins` 与 `Config.Spread/SeasonSteps/Origins`：起点铺开在整段历史上、避开日周期整数倍，默认起点 5→10；`forecastsvc.compareWithBaselines`：候选与每个基线在共同起点上比较（候选需要时在基线的窗口上重跑），须胜过所有可用基线，共同窗口少于 3 个的基线不用；`Evaluate` 拒绝点数/起点数不同的比较；`forecast.MinSamplesFor` 修了 KNN 在铺开起点上因视野不足整体被跳过。
- **A2**：回测结果新增 `bias`、`risk_wape`、`under_weight`（低估按 3 倍计）；晋升多一条“低估加权误差不得更差”（沿用 PeakTolerance，旧结果不比较）；实测排行榜按 RiskWAPE 排名（由已存的 WAPE/MAE/偏差还原，无需迁移），WAPE 并列显示；排行榜新增「低估加权误差」列（默认可见、有说明、中英文），回测详情新增「低估加权误差」「评分窗口」卡片。模块评分（回放准确度）未改。
- **同时处理的界面项**：行内按钮 tiny 后，e2e 量出操作列过宽，已按按钮数收窄各页操作列宽，并把表格组件操作列的最小宽度 120→72。
- **验证**：`go vet ./...`、`go test ./pkg/...` 通过（新增 `pkg/backtest/shared_origins_test.go`、`pkg/forecastsvc/shared_windows_test.go`、排行榜排名测试）；浏览器 e2e `scripts/e2e/modules-algorithms.mjs` 全新 SQLite 库上 242/242 通过（新增：回测结果带低估加权误差与权重、候选与基线点数/起点数相同、排行榜出现「低估加权误差」、行内按钮 tiny）；eslint 对改动文件无错误。`vue-tsc` 仍有 2 个旧错误（`CombineMode`、`SelectBaseOption`），不是本轮改动，未处理。
- **在这份数据上的结果**：66 个结果里 12 个晋升状态变化（晋升 20→22），原因是起点与窗口变化（A1）；A2 的低估规则没有成为任何一次拒绝的原因；CPU 上新版没有非基线算法被晋升（naive 最好），内存上多数算法胜过 naive。**这是 2.9 小时合成负载上的结果，不能当算法优劣或生产准确率结论。**
- **余项/已知后果**：旧回测记录与新记录的 WAPE 因窗口不同不可直接比较，已存的晋升要重跑回测才按新规则更新；运行中的平台（20001）需用新二进制重启才有这些变化，没有动用户的实例；昨日同一时刻基线在不足一天的历史上不参与比较；下一步 A3（保形分位数）、A4（Holt-Winters 补强与 Theta）。

2026-10-28（P7 A4/A3：Holt-Winters 补强与 Theta、保形分位数）：
- **A4**：`HoltWinters` 增加 `seasonal_mode`（additive/multiplicative；乘性要求历史严格大于 0，触到 0 的序列被拒绝而不是悄悄改成加性）、`damping_phi`（0.8–1）、`non_negative`（默认开，预测不低于 0）；非季节趋势初值改用前 10 点的最小二乘斜率；`ImplVersion` 升到 2，所以旧的 Holt-Winters 晋升会失效（预期，需重跑回测）。新增 `Theta`（SES 加斜率一半的漂移，自动选 alpha，可选季节分解，预测不低于 0），注册在算法目录，默认不启用。新参数在默认值时不进规范化存储，旧设置与版本签名不变。
- **A3**：`forecast.Conformal` 包装任意模型：用历史里几个更早的截止点测它自己的误差，按预测步取 ceil((K+1)·level) 位的误差加到预测上。做成**每个非基线算法的参数** `quantile_level`（0=点预测，默认）和 `calibration_windows`（默认 8），沿用算法管理与模型版本机制（和计划里“模块参数”不同，已在改进计划里说明）；`forecast.Quantiled` 让回测按该分位算 pinball 与覆盖率、预测按 level×1000 存；分位预测的晋升改走分位规则（`backtest.evaluateQuantile`：覆盖率不低于 level−0.1、pinball 胜过同分位下的基线、峰值低估不更差），基线也按候选的分位评分。
- **测试与证据**：新增 `pkg/forecast/theta_hw_conformal_test.go`（乘性季节随水平增长、拒绝触零历史、非负、阻尼、初值、参数不影响旧存储；Theta 的常数/直线/公式/季节形状；保形覆盖率在合成噪声上约 0.9±0.06、无泄漏与确定性、历史不足、顺序统计量、基线不提供分位参数）、回测包的分位晋升测试、`TestAQuantileForecastIsStoredAndScoredAtItsLevel`（服务层端到端：存 900‰、按 0.9 评分）。`go vet ./...`、`go test ./pkg/...` 通过；浏览器 e2e 在全新 SQLite 库上 247/247 通过（新增：Theta 在目录里、Holt-Winters 新参数、每个非基线算法有分位参数而基线没有、分位低于 0.5 被拒、算法编辑页出现 quantile_level/calibration_windows 并截图）。
- **在测试平台数据上的读数**（同一份 2.9 小时快照，详见 [baseline-a0.md](../.agents/skills/algorithm-library/references/baseline-a0.md) 的“A3/A4 补充”）：Theta 的低估加权误差 6 个来源平均最低（0.260 对 random_forest 0.269、naive 0.326），差距很小，不足以排出先后；P90 线覆盖率从约 0.5–0.65 升到 0.83–0.89（略低于名义 0.9），WAPE 变成两倍多（故意偏高），6 个来源里 5～6 个按分位规则被晋升。**这是 2.9 小时合成负载上的结果，不是生产结论；要不要把某个算法设成 P90 取决于业务对“扛不住”和“浪费”的权衡，没有替用户决定，默认仍是点预测。**
- `scripts/baseline` 新增 `-enable`、`-set 算法={JSON}`，并把时钟固定在副本里最新样本之后（旧快照也能测）。
- **余项/已知后果**：乘性季节没有在真实日周期数据上验证（快照只有 2.9 小时，装不下一个日周期）；Conformal 每个窗口多跑一次预测，慢算法（GBDT、神经网络）要放宽训练时间预算；校准只用 8 个窗口时覆盖率偏低一些；运行中的平台需用新二进制重启；所有改动未提交。下一步 A6（KNN 修正）、A5（直接多步）。

2026-10-28（P7 A5：直接多步）：
- 岭回归 AR 与 GBDT 增加 `multi_step`（recursive 默认｜direct）和 `direct_anchors`（默认 8）。direct：每个锚点步长各训练一个模型，特征是起点之前的已观测值（含协变量），目标是起点之后第 h 步，日历特征取目标时刻，不把自己的预测当输入；锚点均匀取点、中间线性插值；第 1 步与递归相同。`featureSpec.trainingDirect`、`directSteps`、`interpolateSteps`；拟合抽成 `fitRidge`、`GBDT.fit`，递归路径行为不变。参数在默认值时不进规范化存储，旧设置与模型版本不变。
- 历史要求加上 horizon−1：新增 `forecast.HorizonMinimum`/`MinSamplesFor` 的实现（岭回归、GBDT），回测起点与「模块数据需求」页都按它算（顺带让 KNN、保形分位数的需求显示也包含视野）。
- 测试：`pkg/forecast/direct_test.go`（锚点与插值、h=1 的训练矩阵与原矩阵相同且 h>1 向前看、第 1 步与递归相同且确定、噪声正弦上 40 步误差直接比递归低 10% 以上、历史要求、参数不影响旧存储）；`go vet ./...`、`go test ./pkg/...` 通过；浏览器 e2e 新增两项检查（岭回归与 GBDT 有 multi_step，ewma 没有；未知模式被拒）。
- **在测试平台数据上的读数**（见 baseline-a0.md「A5 补充」）：10 步视野下直接与递归没有明显胜负（GBDT WAPE 0.143→0.144、低估加权 0.288→0.257；岭回归 0.146→0.154）；长视野（40 步）的噪声正弦合成测试里直接比递归误差低约 22%（岭回归）和 37%（GBDT）。因此直接多步是选项，默认仍递归，预测视野长时再试。**2.9 小时合成负载，不是生产结论。**
- 余项：直接多步的训练成本约为锚点数倍，慢模型要放宽训练时间预算；随机森林、朴素贝叶斯、神经网络没有直接多步；运行中的平台需用新二进制重启；所有改动未提交。下一步 A6（KNN 互斥区）、A7（MSTL）、A8（全局模型设计）。

2026-10-28（问题修复 R10：历史曲线、数据源并发、算法管理、时间选择器与表格、资源与副本职责、推荐快照、降级与回退；规格见 [algorithm-framework-spec.md](../.agents/skills/requirements/references/algorithm-framework-spec.md) 第 16 节）：
- **预测断点（先查真实库）**：在测试平台库上量出两个原因。load-web 的视野只有 1 分钟而预测任务每 5 分钟一次，每次只预测一步，目标时间每 5 分钟才有一个；其余模块的断点是平台没运行的几个小时。修复：`AlgorithmRun.CoverMinutes()`（视野不短于运行间隔，默认 5 分钟任务周期，预测/回测/数据需求都用）；`PredictionDAO.ForCompare` 在库里取每个目标时间最新的点预测（原来读全部重叠运行再截 30 万行，长窗口会从新的一端截掉）；图上断开处线也断开并标出每个算法的中断处数，稀疏的点显示圆点。
- **图表**：折线图自带刷新按钮（历史数据与指标曲线），图例只写名字并自动换行（不滚动、不分页、细节在悬浮提示），序列顺序预测在前、实际在后（`data-series` 给 e2e 核对）。
- **数据源并发**：去掉固定上限 16（前端 `:max`、后端校验都去掉，只要求 ≥ 1），采集不会同时发起比分段数更多的请求；测试覆盖 64、500、负数、更新到 40。
- **算法管理**：列表与详情新增 `runtime`（未启用/参数无效/等待首次预测/运行中/已跳过/运行失败），「运行状态」列写明；检查结果：注册、加载、启用的校验原本就有，缺的是「启用后要等下一个预测周期才第一次运行」没有说，现在页面写出来；「算法运行情况」算法列宽 170 → 130。
- **时间选择器与表格**：所有时间筛选加「最近 3 小时」「最近 12 小时」；`ExpandableDataTable` 改为内容列同比例放大（最多 1.35 倍）、余下留在右侧，不再把多出的宽度倒进一个列，操作列最小宽度 120 → 72，各页模块/算法/数据集列统一 130；`scripts/e2e/table-fit.mjs` 量出「列宽 − 内容宽」：留白超过 150px 的列从 32 个降到 6 个（剩下的是内容本来就空的列）。
- **资源与副本职责**：operator 决策接口只回副本；Pod CPU/内存规格（`forecastsvc/resourcespec.go`）取整（CPU 10m/50m，内存 10Mi/64Mi 并向上）、生效后保持（变化不超过 `forecast.resource_change_ratio`=20% 不改，降低等 `forecast.resource_hold_hours`=24 小时，增加/固定周期/旁路/OOM 立即生效）、带规格版本；这两个参数在系统参数里新增「预测与资源」分类。单位展示：420m、1.5 核、320Mi、2Gi。
- **资源推荐与推荐快照**：「资源推荐」页 = 各模块当前推荐；新菜单「推荐快照」= 历史：第一个、每个变化、至少每小时一个（`recommendation.snapshot`），状态 待执行/已执行/执行失败/已过期（以及仅建议），由 operator 执行报告与准入记录得出；保留 = 最近 20 条推荐 + 每个模块最多 `forecast.snapshot_max_per_module`（200）个快照，最新的永远保留；升级补记快照，拥有旧页面权限的角色自动获得新菜单。接口 `GET /snapshots`、`/snapshots/:id`；扩缩容记录的结果名统一。
- **降级与回退**：重新定义并分开（降级 = 改用安全配置，回退 = 变更没落地、保持此前有效配置；平台没有「撤销已应用的变更」的机制，不说成有）；`GET /fallbacks` 新增 `kind`，记录有触发原因、原配置、目标配置、实际执行结果、发生时间、是否进行中；降级按事件（从第一个降级快照到下一个正常快照）合并，不再每个周期一行；页面默认可见五列恰好是这五项。
- **验证**：`go vet ./...`、`go test ./pkg/...` 通过（新增 `for_compare_test`、`coverage_test`、`resourcespec_test`、`snapshot_test`、`handle_snapshot_test`、降级与回退测试、数据源并发测试、算法运行状态测试、菜单拆分迁移测试）；web `pnpm run typecheck`（已无报错，顺带修了 `CombineMode`）、eslint、oxfmt、单测通过；浏览器 e2e 在全新 SQLite 库上 273/273 通过（含 R10 的 21 项：数据源并发、历史页刷新并真的重读、图例预测在前、时间快捷项、算法运行状态、取整与规格版本不变、快照只含快照且有状态、快照可打开、降级与回退五项、新菜单）。
- **未验证/余项**：真实集群上规格保持与滚动更新的关系（规格更新只到新 Pod，运行中的 Pod 要等滚动）；operator 重启与租约丢失（上一轮发现，未处理）；`forecasttask` 里 `TestRunJobHonoursTheRunIntervalOfAModule` 在 HEAD 上就有约 20% 的偶发失败，未处理；所有改动未提交。

### 修复轮（历史图、列表宽度、算法评分）

- **历史页**：去掉搜索栏与图表之间的提示卡片；图表无数据时在图内说明原因（Prometheus 无数据或读取错误）；图默认只画前 6 条预测线，其余在图例点选，刷新保留选择。load-web 无数据的根因未能确认（无法实时查 Prometheus；库副本显示其历史点只有约 10 个、多次运行因历史不足被跳过）。
- **列表宽度**：`ExpandableDataTable` 去掉 1.35 倍上限，内容列同比例放大到铺满容器（此前只占约 2/3）。
- **资源推荐操作列**：`columnMinWidths: { operate: 150 }`，保存过的窄列宽不再遮按钮（按代码推断，未在浏览器看到原遮挡）。
- **降级回退**：时间列标题改「创建时间」（该列本来就在默认第一位）。
- **算法评分**：当前排名页只保留一个排名，去掉评估选择器与历史快照模式；算法详情新增「历史排名对比」；评分记录页「使用中」改为「当时使用」并移出默认列（每条历史记录都带当时状态，才会出现多个）。评分记录页保留。
- **验证**：`vue-tsc` 类型检查、改动文件 eslint、`pnpm run build` 通过。**未验证**：浏览器 e2e（临时 QA 环境与 Playwright 在会话中丢失，未重建），e2e 脚本已改但未运行；operator 新镜像已构建但因 Kyverno webhook 不可用未能滚动更新。

### R11：算法评分、历史数据、资源推荐、快照历史（2026-10-28）

- **算法评分**：准确度改为只针对资源（CPU、内存、GPU），副本数由资源推出、不再是独立准确度（只有模块没有任何资源来源时才退回副本数）；每个资源有自己的分与排名（`scoring.Result.Metrics`、接口 `breakdown[].score`）；当前排名页加「指标」选择（综合/CPU/内存/GPU），汇总行显示使用中算法及各资源准确度；评分记录的模块名称修复（列 key 与字段不一致，没有渲染）；「使用中」在记录里改称「当时使用」；新系统参数 `forecast.score_max_per_module`（默认 300）与 `PruneScores`，评分后和定时清理时按“评估次数”保留。公式与例子见规格第 17.1 节。
- **历史数据**：缺口处同色虚线连接两端并用灰色底纹标出，图下说明未补值，虚线不进提示框；没有副本轴时右侧不留空；无数据原因写在图里。数据量少的原因：load-web 的 `lookback_hours` 是 1 小时（样本被清理）、Prometheus 本身只有约 2 小时数据并有 17 分钟与 6 分钟的空洞——是配置与数据源，没有改。
- **算法实时排行榜**：列收窄，表头补问号说明（排名、状态、次数、点数、胜过基线、已晋升）。
- **训练记录**：独立页面与菜单 `forecast-results_runs` 删除（授权带到算法运行情况）；在算法运行情况里点「训练记录」开弹窗，详情在弹窗内切换。
- **资源推荐**：列表里零散的 9、3、2、1、4 是已删除模块（ID 1、2、3、4、9）留下的旧推荐；列表只含现存模块，删除模块时级联删除它的推荐/预测/评估/回测/训练记录，升级时清掉已有孤儿行。
- **快照历史**：独立菜单「推荐快照」与页面删除（授权带到资源推荐）；资源推荐页「快照历史」按钮开弹窗，版本名「模块名 快照 V300」，详情分模块与版本、推荐结果、产生原因、配置内容四块。
- **降级回退**：时间列标题改「创建时间」。
- **验证**：`go vet ./...`、`go test ./pkg/... ./cmd/...` 全部通过（新增评分资源口径、评分保留、孤儿推荐、模块删除级联、菜单退役测试）；前端 `vue-tsc`、eslint（0 错误）、`pnpm run build`、`node --test` 通过。**未验证**：浏览器 e2e（`modules-algorithms.mjs` 已按新页面改写并通过语法检查，但本会话的临时 QA 环境丢失、没有运行）；上面的页面行为（虚线缺口、全宽图、操作列不挡按钮、弹窗层级）没有在浏览器里看过。operator 新镜像已构建但因 Kyverno webhook 不可用未部署。

### R12：评分最新值与依据、WAPE 说明、算法清单、操作日志、乱码、字段名（2026-10-28）

- **算法评分**：`GET /algorithm-scores/latest` 每个模块×算法×资源只有最新一行（带资源排名、是否使用中），资源筛选（CPU/内存/GPU/综合）；评分把每个窗口的预测需要、实际需要、误差、使用的指标存进 `scoring.Result.details/sources`，详情分「评分结果 / 计算依据 / 历史评分」，依据里写公式与代入的数字。
- **WAPE**：说明改成含义、公式、四步过程与例子、用途与局限；实测评估、实测排行榜、回测指标卡、评估详情、算法管理里的回测/实测 WAPE 都有问号（`MetricLabel`/`helpTitle`）；历史图例的 WAPE 文字后写出公式（图例里放不了图标）。
- **算法清单**：内置 15 个算法已注册并由测试固定清单（含 theta；formula 是自写公式算法的基础）；A0–A5 已完成，A6 KNN 互斥区、A7 MSTL、A8 全局模型设计、A9 小型时序基础模型评估**未实现**。线上看不到 theta 是因为平台还是旧二进制，重启升级后出现。基线与统计类算法新增 `Definition.Method`（计算过程、公式、意义），naive 的说明写明它是最低对照。
- **操作日志**：来源增加 Operator（operator 请求带 `X-kube-resource-fcst-Client: operator` 与 Pod 名），新增操作识别码 `request_id`（也在响应头）和 `client_name` 两列；列定义键 `createdAt`≠`created_at` 导致的原始字段名已改，其余未定义的列由 `fieldTitle()` 给中文名。
- **乱码根因**：线上 `secret_key` 的 `operator` 备注与对应日志是非 UTF-8（GBK）请求体被解码成 U+FFFD 后入库，原文无法恢复。新增 `UTF8BodyMiddleware` 拒绝这类请求，截断改为字符边界，升级把已损坏的备注换成「请重新填写」的说明。
- **用户管理**：用户接口补 email/phone/language，列表增加头像、更新时间、邮箱、手机、语言，昵称为空显示用户名。
- **验证**：`go vet`、`go test ./pkg/... ./cmd/...` 通过（新增窗口明细、latest 端点、UTF-8 中间件、操作识别码、乱码修复、算法清单测试）；前端 `vue-tsc`、eslint（0 错误）、build、`node --test` 通过。**未验证**：浏览器 e2e（已改写、未运行）；operator 新头在集群里的实际效果（operator 镜像需重建部署）。

### R13：历史样本不足的根治（2026-10-28）

- **根因**（线上库副本核对）：load-web 在页面上保存为回看 1 小时、训练窗口 10 步（操作日志 10-27 19:31 起，窗口先 30 后 10），库里最多 60 条样本，算法只看到最近 10 条；训练记录写着 `holt_winters needs 60 samples, got 10`、`knn needs 54 samples, got 10`。其余三个模块是 180 条样本、窗口 0，不受影响。样本没有被删（load-web 60 条连续、最新 1 分钟前）。模块保存只检查数值范围，不检查「启用的算法需要多少步」，`/requirements` 只是诊断，不拦也不补。
- **修复**：回看范围与训练窗口不再能饿死算法。`pkg/forecastsvc/history_need.go`：算法需要的步数（含预测步长）乘 1.5 留余量，范围 = max(模块回看, 该算法需要)，窗口 = max(模块窗口, 需要)；采集、训练、回测、评分、清理都按这个范围（`collectLookback`/`lookbackFor`/`windowFor`），范围被延长时补采更早的一段（`SampleDAO.Earliest`）。需要超过 30 天的算法不延长，照旧跳过并写明需要多少步。`/requirements` 增加 `effective_lookback_hours`，并按延长后的范围判断。
- **没有改的**：模块自己存的 lookback_hours / window_steps 不被改写（页面上仍是用户填的值）；真正年轻的模块（采集不足以满足）仍会被跳过并写明「需要 N，只有 M」；连续可用比例（fallback_min_history_ratio）判断未动。
- **验证**：`go test ./pkg/...` 新增 `TestAnAlgorithmIsNotSkippedForAShortLookbackOrWindow`（不加修复时算法被跳过，加了通过）、`TestTheLookbackAndTheWindowNeverStarveAnAlgorithm`；两个旧测试按新行为改写。**未验证**：线上平台需用新二进制重启后，load-web 才会补采并让这些算法出结果（采集在下一次任务运行时进行）；浏览器未验证。

### R14：P7 A6 KNN 互斥区与时刻约束、A7 MSTL（2026-10-28）

- **A6**：`knn` 选中的 K 段两两至少相隔一个窗口长度，不再把同一次事件错开几步的副本算成 K 次（参数 `exclusion`：`window` 默认，`none` 是旧行为）；可选 `same_time_of_day` + `time_tolerance_minutes` 只比较同一时刻（UTC 时分，跨零点环形）结束的窗口。所需历史随之变成约 K×窗口（默认 5×48+1 = 241 步，原来 54），数据需求页、回测起点和 R13 的范围自动延长都按它算。`ImplVersion` 升到 2，**旧的 KNN 晋升失效（预期）**，新参数默认值不进规范化存储。
- **A7**：新算法 `mstl`（统计类，默认不启用，`daily_season_steps` 必填）：趋势 + 日、周两个季节分量，去季节后的序列用 Theta 预测再加回季节，预测不低于 0。**与论文不同：季节分量是固定形状（各位置均值，反复三轮），不是 LOESS 缓慢变化的形状。** 历史至少两个完整的最长周期（周季节需两周）。内置算法由 15 个变成 16 个，清单测试已更新；算法管理页读注册表，前端无需改。
- **验证**：`go vet`、`go test ./pkg/... ./cmd/...` 通过。新增单测：互斥区选中窗口间隔、平滑负载上 K 段来自不同周期（关掉互斥区则是副本）、历史要求、时刻约束、注册表参数；MSTL 在合成日+周信号的周末边界上 MAE 不到只看日周期的 seasonal_naive 的四分之一、不到只做日季节的 MSTL 的一半，无泄漏、确定性、非负、历史要求；一个月 1 分钟步（43200 点、日 1440 + 周 10080）一次预测约 10 ms。
- **没有验证**：真实或测试平台数据上的准确率——测试平台只有约 2.9 小时的历史，满足不了 MSTL 的两周要求、也满足不了新 KNN 的 241 步（1 分钟步约 4 小时）以外的评估，所以这两个包的效果只在合成信号上证实，不是生产结论；线上要用新二进制重启并升级后才会出现 mstl。

### R15：模块当前配置、降级与回退按资源拆行、算法清理、全局规范（2026-10-28）

- **模块列表**：新增副本数、CPU、内存、GPU 四列（后端 `ModuleSummary` 带最新快照的值，没设置为 null 显示为空，实际副本数取 operator 最近一次上报）；运行状态不再为空（运行正常／尚无推荐／已停用）；操作列 168px，且不再被保存过的旧宽度撑宽。
- **降级与回退**：一个资源一行，创建时间、模块、类型、资源、触发原因（只出现一次）、原配置、目标配置、结果各一列；恢复时间和持续时长改为可选列；「决策说明」直接打开依据。
- **快照**：执行记录的「第 N 版推荐」在当前页弹出快照详情（`GET /snapshots?module_id=&covers_version=`），不再跳转；版本名改为「模块名 第 N 版推荐」。
- **算法**：移除 macd_trend 与公式里的 macd/macdsig；升级迁移删除它的设置和数据，未注册的内置设置不会被运行。算法评分页算法名不再带资源，资源是单独一列。
- **全局**：表格只固定操作列（列设置里去掉固定图钉）；行内按钮统一配色；提示框限宽 360px 自动换行；评分与百分比两位小数；资源值统一写法；曲线点位更小、工具栏间距、监控指标曲线区域可折叠；个人中心增加账户信息卡。
- **验证**：`go vet`、`go test ./pkg/... ./cmd/...` 通过（新增：模块摘要当前配置、覆盖版本的快照、迁移删除退役算法、未注册设置不被运行）；前端 `vue-tsc`、eslint（0 错误）、locale 测试通过。**未验证**：浏览器里的任何页面效果（QA 环境未重建）；没有逐页审计全部中文化和精度。优先级与验收见规格第 19 节。

### R15 补充：移除意义不大的算法类型（2026-10-28）

- **移除**：naive_bayes（分桶后预测一个桶，分辨率受桶宽限制）、rnn、gru、lstm（每次运行在单个工作负载的几千个点上现训练网络，受时间预算限制，A0 的基线表里没有它们，没有任何测量显示它们胜过统计算法），加上之前的 macd_trend。内置算法 11 个：naive、seasonal_naive、ewma、hist_percentile、holt_winters、theta、mstl、ridge_ar、knn、random_forest、gbdt。保留的机器学习算法都有 A0 测量。
- **类别**：「技术指标」「深度学习」两个类别没有算法了，前端分类选项与语言包里已去掉；公式算法的基础类别改为 custom，新建自定义算法默认类别是 statistical。
- **升级**：迁移删除这五个算法的设置、预测、评估、回测、训练、评分数据，以及以它们为基础的自定义算法；推荐记录里的算法名保留。注意：已把它们设为「启用」的模块在升级前不会再运行这些算法（未注册的内置设置不被运行）。
- **验证**：`go vet`、`go test ./pkg/... ./cmd/...` 通过（清单测试 11 个；删除了只测这些算法的单测）；前端 `vue-tsc`、locale 测试通过。**未验证**：浏览器里的算法管理页。

### R16：P7 A10～A13 四个新算法（2026-10-28）

- **A10 `seasonal_profile` 周期画像分位数**：按「周几 + 几点」分桶，每桶带时间衰减的 P90/P95 作为该时刻的预测；桶样本少退到每天同一时刻、再退到整段历史的百分位。补的缺口：hist_percentile 是平线、seasonal_naive 会把一次异常的一天原样复制。
- **A11 `peak_envelope` 峰值包络趋势**：滚动最大值作包络，Theil-Sen 稳健外推，默认不低于当前包络、不为负；`TimeToLimit` 算包络到上限的时间（**只是函数，页面和 OOM 说明里还没有显示**）。补的缺口：内存工作集是锯齿叠加缓慢上涨，均值类算法会低估。
- **A12 `combination` 组合预测**：成员各自预测，取中位数或按近期误差倒数加权。**与你给的计划不同**：成员用各自的默认参数运行（只能选不需要必填参数的算法），权重来自这份历史自己的滚动误差，不读平台的晋升状态和算法页给成员设置的参数；组合和别的算法一样被回测、评分、晋升。要按晋升状态配权重，得把组合做进预测流水线，没有做。
- **A13 变点后重置**：做成每个非基线算法的参数 `reset_on_change`（默认关）、`change_threshold`、`change_min_shift`，与 A3 保形分位数同样的做法，所以 holt_winters、theta、mstl 等都能用；不是一个单独的算法条目。平稳上升不算突变，突变后至少 6 步才确认，永远给算法够它所需的历史。
- **前端**：算法参数编辑器支持文本参数（组合的 `members`）；其余算法页读注册表，不用改。
- **验证**：`go vet`、`go test ./pkg/... ./cmd/...` 通过；新增单测见 `profile_envelope_combination_regime_test.go`、`new_cost_test.go`（一个月 1 分钟步 43200 点一次预测：组合约 53 ms、变点重置约 16 ms、周期画像约 14 ms、峰值包络约 10 ms）。**没有验证**：真实数据上的准确率——测试平台只有约 2.9 小时历史，周期画像区分周几要两周、变点要足够长的历史，所以这四个只在合成信号上证实，效果要用 A0 的方法在更长的历史上重测；浏览器里的算法管理页没看过。内置算法由 11 个变成 14 个；线上要用新二进制重启后才会出现。

### R17：动态副本数被实测不确定度放大 10 倍（2026-10-29）

- **现象**：`stress.sh --mode ramp` 峰值需求 700m，余量系数 1.1、目标利用率 0.9、每 Pod 请求 100m，应是 ceil(700×1.1÷90)＝9 个，平台给了 10（被最大值截住），计划写着 `ceil(0.3973 x (1 + 9) / 0.09)`＝45。
- **根因**（线上库副本核对）：动态副本用「实测不确定度」代替余量系数：取该算法最近 30 次评估里峰值被低估比例的 P90，换成放大倍数 1/(1−s)。使用中的 seasonal_naive 在压测的几次需求突变里 5 次低估了约 95%，P90 取到 0.95，被截断到上限 0.9，倍数 1+9＝10。几次压测或发布的突变就能让倍数永久变成 10，并且盖过模块设置的余量系数。
- **修复**：实测低估比例的上限从 0.9 降到 0.5，预测最多放大 2 倍（`maxShortfall`）；超过上限时计划的说明里多一句话（「某算法的峰值低估 P90 为 95%，超过 50% 的上限：它的预测最多放大 x2.0，而不是 x20」）。低估超过一半的算法不是靠更大的倍数变安全的，是选错了算法。
- **没改的**：选哪个算法（这次选了 seasonal_naive）没动；没有改余量系数的语义（没有实测数据时仍用它）。同样的需求现在是 ceil(0.3973×2÷0.09)＝9。
- **验证**：新增 `TestAMeasuredShortfallIsCutAtTheCapAndThePlanSaysSo`（修复前算出 9 倍并失败，修复后通过），`go test ./pkg/forecastsvc/` 通过。**未验证**：线上重跑一次压测；需要用新二进制重启平台后才生效。
- **补充（对所有算法）**：上限不只在 `forecastsvc` 里：求解器 `decision.factorOf` 对每个算法、每个约束（吞吐、CPU、内存、GPU）也把不确定度限制在 `MaxUncertainty`（1.0，即最多 2 倍），不管测量来自评估还是回测、是哪个调用方给的。新增测试 `TestNoAlgorithmIsScaledByMoreThanTwoWhateverItsMeasuredShortfall` 对目录里的每个算法 id 用最坏的测量值验证（并复现这次的 9 个）。
- **还没解决的相关问题**：这次选中 seasonal_naive 的原因是它的回放评分最高（92.78，对 ewma 74.22），而它的预测峰值只有 0.397 核，ewma 和 naive 是 0.65，实际需求 0.7 核。回放评分用的是历史窗口，历史较平稳时抄一个周期前的值也能得高分，遇到需求突变就低估很多。选算法的规则（评分×权重）没有改。


### R18：决策先于动作、识别落后的算法（2026-10-29）

- **现象**（00:20 的截图和事件）：负载在 00:15 前后从 600+ mCPU 掉到 71 mCPU，实际副本一直停在 10，事件里「ScaleDown from 10 to 9」间隔很久；图上 holt_winters 已预测 51 mCPU（1 个副本），而 top1 的 seasonal_naive 预测 658 mCPU（9 个）。
- **根因**：①operator 每 30 秒问一次决策，但决策只由 5 分钟一次的 `forecast.run` 任务产生，所以它拿到的是最长 5 分钟前存的那版（库里 16:20 的第 523 版还写着 10）；②选算法只看回放评分，评分每 6 小时才重算一次（92.78 一直没变），还要领先 2 轮才切换，算法的预测已经落后很多这件事没有被用上。③（配置，没改）测试台的缩容档位每次最多减 1 个、间隔 2 分钟，从 10 减到 1 至少要 18 分钟。
- **决策先于动作**：`GET /operator/decisions` 在存着的推荐比 `forecast.decision_max_age_seconds`（系统参数，默认 60 秒，0 = 关闭）老时，先由平台重新采集新样本、用每个算法预测、再决策（`Service.FreshRecommendation`，和任务同一条流水线），再回答；响应多一个 `refreshed`。每个模块同一时刻只刷新一次（并发请求读同一个新版本），刷新失败或超时（15 秒）读存着的那版，刷新后 `max_age` 内不再重试，自己设了 `run_interval_minutes` 的模块和 observe 模块不刷新；任务和刷新用同一把模块锁，不会同时写同一模块的预测和推荐版本。
- **识别落后的算法**：对每个可用的算法，取最近 10 步里「平台在该时刻之前最后一次的预测」与「该时刻测到的值」比（`Tracking`：WAPE、偏差、步数）。至少 3 步且 WAPE 超过 50% 就是落后，只要还有别的算法没有落后，落后的这一轮不参与选择（`dropLagging`），不管它的评分多高，也不需要等切换轮数；全部都落后时不动，仍按评分。决策说明的选算法表多一列「最近跟踪误差」，落后的标红，原因写「落后：最近 N 步的预测与实际值相差 X%（上限 50%），预测高于/低于实际值，本轮不参与选择」。
- **验证**：`go vet`、`go test ./pkg/... ./cmd/...` 通过（新增：落后的 top1 不被选、步数太少或全部落后时不动、推荐过期就重做、新鲜不重做、0 关闭、间隔模块与 observe 模块不刷新、并发请求只做一次）；前端类型检查、locale 与单测通过。**未验证**：线上重跑 `stress.sh --mode ramp`，需用新二进制重启平台。
- **没改**：缩容档位（模块或 PredictiveScaler 的 `scaleDown` maxStep 与 cooldown）和 `forecast.run` 的 5 分钟周期；落后的判定阈值是常数（50%、10 步、至少 3 步），没有做成设置项。刷新会让每次轮询（超过 60 秒）多跑一遍这个模块的全部算法，算法很重的模块应把 `forecast.decision_max_age_seconds` 调大。

### R19：前瞻性副本决策：提前量、多算法投票、实时兜底、扩容及时性（2026-10-29）

- **问题**：决策跟着排名第一的算法走；负载变化时 top1 可能还没反应，别的算法即使对了也进不了决策；副本被要求到真正可用要一段时间，决策却没算这段时间；预测失效时没有实时兜底；没有记录扩容是否赶在负载之前。
- **提前量**（`decision.ComputeLead`）：Pod 就绪时间＋节点扩容＋决策等待（`forecast.decision_max_age_seconds`＋30 秒轮询）＋近期偏晚扩容的 P90 余量，取整到步长、至少两步、不超过预测范围。就绪时间用本模块已评价扩容的实测 P90（至少 3 次，已含节点扩容），不足时用系统参数 `forecast.lead_pod_ready_seconds`（60）＋`forecast.lead_node_provision_seconds`（0）。
- **前瞻窗口**：每个算法按提前量、5、10、15……分钟（预测的前 n 步）各算一遍所需副本数（`truncatedCalc` 重算多指标，`windowNeeds` 复用副本求解器）；决策说明页「前瞻决策」列出。
- **多算法并行与冲突处理**：所有可用算法在提前量窗口上投票，权重＝评分／100 × (1−最近跟踪误差)；用可信度加权分位数取共识（`decision.Reconcile`，扩容 0.75、缩容 0.9，系统参数可调）。可信的算法一致就不被低可信的高要求拖动，可信的算法看到上涨就跟随；不是取最大或最小。
- **动态风险控制**（`decision.Settle`）：扩容看提前量窗口的共识；缩容需要整个预测范围在更严分位上一致，且当前实测负载放得进更少的副本；仍受原有的最少确认步数、最大/最小副本、动态上限约束。
- **实时兜底**：实测负载（CPU/内存/请求量，最近两个样本的较低者）需要的副本数超过预测与现有，则直接要求该数并标 `urgent`；operator 对 urgent 的扩容跳过冷却和单步限制，缩容不变。`forecast.emergency_scale_up` 可关。
- **效果评估**：新表 `scale_outcome`（升级时自动建）；每次向上的决策记一条，之后用就绪副本数序列和实测负载判定及时／偏晚／负载没来，结果回到提前量；`GET /scale-outcomes`，扩缩容记录页「扩容及时性」弹窗。
- **改了什么文件**：`pkg/decision/ahead.go`、`pkg/forecastsvc/ahead.go`、`recommend.go`、`replicas.go`（不确定度缓存、返回求解配置）、`pkg/model/scale_outcome.go`、`Recommendation.Urgent`、`pkg/dao/rdb/scale_outcome.go`、`handle_runtime.go`、`pkg/operator/controller/{plan,decider}.go`、迁移种子 5 个系统参数；前端 `ahead-explain.vue`、`scale-outcome-modal.vue`、执行记录页、类型与中英文语言包。
- **验证**：`go vet`、`go test ./pkg/... ./cmd/...` 通过（新增：提前量实测／配置／偏晚变长／封顶、可信度加权共识、结算扩缩与实时兜底、效果判定、窗口与步数；forecastsvc 里窗口与提前量、实测负载触发紧急、重复决策只记一条、效果评价及时、偏晚影响下一次提前量；operator 对 urgent 跳过冷却与单步限制）；前端 `vue-tsc`、oxlint、eslint、`node --test` 通过。
- **没有验证**：真实集群上的就绪时间、节点扩容时间与及时率；浏览器页面；新参数默认值（60 秒、0.75、0.9）只是起点，需要用真实扩容的实测结果调。operator 镜像需重建才认识 `urgent`；平台用新二进制重启后建表和写入参数。
- **没做**：缩容档位（operator 每次最多减几个、冷却多久）仍是模块配置；效果评估只记向上的决策，向下的不评；模块级（而非全局）的 Pod 就绪时间设置没有做，靠实测覆盖。

### R20：历史保留配置、准入记录只留真实的部署／更新、界面精简（2026-10-29）

- **准入记录的排查结论**（用户库副本，148 条，最新到 10-28 19:36 UTC）：写 `admission_decision` 的只有 `GET /admission/recommendations` 一处，预测、周期计算、副本决策都不会写。逐条对时间：02:58（18:58 UTC）、03:03、03:06、03:07 的记录，和 operator 同一秒的 `ScaleUp` 执行记录完全对得上——是 operator 扩容建出新 Pod，Kyverno 对每个新 Pod 都问了平台。它们是真实的 Pod 准入，但不是「部署／更新」，扩缩容一多就把准入页铺满。另外，一次新版本发布的多个 Pod 会写多条。
- **修复**：Kyverno 策略多传 `revision`（Deployment 的 pod-template-hash，StatefulSet/DaemonSet 的 controller-revision-hash）。平台对一个模块：创建工作负载时的询问（`container=-`）记为「创建」；某个版本的第一个 Pod 记为「部署／更新」；同一版本其余的 Pod（扩容、被替换）照常回答但默认不记录，系统参数 `forecast.admission_record_scaling=true` 才记，记成「扩容／替换」。判断与写入在一把锁里，同时到达的同一次发布只记一条。没传 `revision` 的旧策略仍是每个请求一条。新增列 `revision`、`origin`；页面加「来源」列与筛选，帮助文字改了。
- **历史保留**：`forecast.history_retention_days`（默认 0＝沿用内置：预测、训练、扫描报告、记录 30 天，回测、评估、推荐 180 天；填 N 则预测、训练、回测、实测评估、推荐、operator 报告、准入记录、扩容记录都保留 N 天）；`forecast.evaluation_max_per_module`（默认 20000，至少 100，每个模块只留最新的 N 条实测评估）。每小时的评估任务清理，快照、算法评分仍由各自的参数限制。
- **界面**：排行榜去掉「#」（改「排名」）并收窄各列；决策说明里的「#」同样改了；资源推荐的「决策说明」「快照历史」两个按钮同为主题色次要按钮；扩缩容记录的操作列只写「决策说明」，版本列是普通文字，不再是按钮；降级与回退记录的创建时间列和后端列元数据的 `created_at` 合成一个键，不再出现两列。
- **验证**：`go vet`、`go test ./pkg/... ./cmd/...` 通过（新增：只记创建与每版本第一个 Pod、打开开关后记扩容 Pod、无 revision 保持旧行为；实测评估按模块保留最新）；前端 `vue-tsc`、oxlint、`node --test` 通过。
- **没验证**：浏览器页面；Kyverno 策略在真实集群里取到 `revision`（JMESPath 对带连字符标签的写法没有在 Kyverno 上试过，请先在测试环境应用后看一条记录的「来源」）。**没做**：已在库里的旧准入记录没有被清理或回填来源（它们的「来源」为空）；若要删，可用页面的批量删除。

### R21：预测模式与 OOM 配置、operator 本地缓存、预测滞后（提前量/短期修正/组合预测/验收指标）、指标页整合、统一 UTC+8（2026-10-29）

- **核对用户库副本（只读拷贝）的结论**：① `load-web` 配置预测期 1 分钟，实际预测只到约 285 秒，而它的提前量是 300 秒（Pod 就绪 P90 84 s＋决策等待 90 s＋偏晚余量 102 s），前瞻窗口与预测期重合，没有富余；② 这个模块的负载是不可预测的突发（0.01→0.7 核，15 分钟内上去，每天几次，不按钟点），长记忆算法（hist_percentile、seasonal_profile、peak_envelope）对已经开始的突发几乎不响应；③ Combination 的成员（ewma、holt_winters、theta、hist_percentile）等权的是平滑，排名在 6 个里第 4、12 个里第 10；④ OOM 的扣分乘在综合分上，CPU 也被扣，且 CPU 欠配的窗口也会计入 OOM。
- **预测模式**：`predict_enabled`（默认关）＋`predict_mode`（none／fixed／dynamic）＋`predict_algorithm`；关闭或 none 时不运行算法、用兜底值且不记降级；fixed 只跑一个算法（仍受回测晋升约束）；升级时已存在的模块沿用 `enabled` 保持预测（迁移只在列刚新增时执行一次）。字段表见 `docs/02-系统架构.md` 三（附二）。
- **OOM**：配置独立成一节（追溯天数、内存系数、保护压力、评分权重开关与权重），OOM 指标单独的编辑器（`ModuleMetrics scope=oom`）；评分里 OOM 只扣内存得分、只计内存给少了的窗口、开关默认关。清理了模块表单里 `clone` 会把复制来的指标源删掉的缺陷（编辑器只删自己加载过的源）。
- **Operator 缓存**：`pkg/operator/controller/cache.go`——最后一次决策顶替不可达的平台至多 `--decision-cache-ttl`（10 分钟）、只上调不下调、不沿用 urgent；模块列表缓存 `--module-cache-ttl`（1 小时）；平台恢复后前 `--decision-recover-answers`−1 次只上调不下调；平台回答「无决策」不被缓存顶替。规则表见架构文档。
- **预测滞后**：预测范围延长到提前量的 2 倍（`forecast.lead_cover_factor`）、评分窗口随之；短期修正（5 个长记忆算法）；组合预测改按「少给更贵」的误差、误差倒数平方、剔除最差成员、计入最新几步。**在 `load-web` 的真实历史上回放**（884 个点，14 个突发起点；起点之后 10 步里预测低于实际的部分占实际的比例）：hist_percentile 0.43→0.11、seasonal_profile 0.66→0.14、peak_envelope 0.34→0.16，全程 WAPE 基本不变；holt_winters 本来就响应（0.08）不受影响。组合预测在 `load-web` 上 WAPE 0.255→0.207、突发起点的低估 0.141→0.102，在平稳的 `load-cache` 上全程低估由 0.035 升到 0.043（WAPE 0.106→0.110）——**有小幅代价**。回放只用了一个模块的一天数据，不是准确率结论。
- **验收指标**：`GET /modules/{id}/delay-report`（六项，目标见架构文档）；页面在「扩容及时性」对话框。
- **指标管理整合**：「内置指标预设库」页面与菜单删除（预设只读，唯一的用途是给指标模板表单挑选）：预设的场景、用法、限制、来源组件、读取的指标与标签现在显示在指标模板表单的挑选框下；演示数据的创建／刷新／删除移到模块列表工具栏；`datasource_presets` 菜单的授权随升级转给指标模板与模块列表。指标模板（定义）→监控指标（模块上的关联）→数据集（采集后的结果）三层各自的职责没有合并：这次没有把「指标模板」搬到「监控指标」目录下（需要迁移路由与授权，没有浏览器验证的条件下不做）。
- **界面**：模块列表只显示当前实际副本数并新增「预测」列；历史页的曲线占满宽度，缺数据的说明收进问号提示（历史页**不要**数据列表：用户说过多次，需求文字里写了也不加）；当前排名的行高、字号、徽章、进度条缩小。
- **UTC+8**：见架构文档；时间显示 `formatDateTime` 固定 UTC+8（单测在 UTC、Asia/Shanghai、America/New_York、Asia/Kolkata、Pacific/Auckland 下通过）；图表坐标轴与提示、日志页查询时间、周期编辑与时间范围选择器都按 UTC+8。
- **验证**：`go vet`、`go test ./pkg/... ./cmd/...` 通过；新增测试覆盖评分只扣内存、预测模式与迁移、operator 缓存全过程（含断线→降级→恢复）、短期修正、组合权重、验收指标、时区；前端 `vue-tsc`、oxlint、eslint、oxfmt、`node --test`、`pnpm run build` 通过。
- **浏览器冒烟**（Playwright + Chromium，临时库＋演示数据，浏览器时区设成 America/New_York）：模块列表有「预测」列、副本数里没有「推荐 N」；编辑表单有「启用预测」「OOM 保护」「OOM 监控指标」且 OOM 指标是独立的一份列表；历史页曲线、折叠的数据列表（展开 235 行，时间按 UTC+8 显示，坐标轴 05:00～08:00 也是 UTC+8）；当前排名行高 54 px；菜单里没有预设库；扩容及时性对话框在选了模块、并往临时库里补了 12 条扩容记录后显示六项验收；页面没有脚本错误（控制台只有被拒绝的外部资源请求）。**没覆盖**：保存表单（含固定算法的选择）、克隆、缺数据的问号提示（演示数据没有缺口）、时间选择器的交互、英文界面、1024／390 px 宽度。
- **没验证**：真实集群里 operator 的缓存行为；Kyverno 之外的 MySQL／PostgreSQL 连接串（只测了字符串）；短期修正与组合预测在其他工作负载上的效果；已有的 MySQL 数据若按 UTC 写入，改成 UTC+8 的连接会读出差 8 小时（SQLite 不受影响）。
- **升级要点**：平台用新二进制重启并带 `--migration`（新列与系统参数）；长记忆算法和组合预测的模型版本变了，**需要重新回测晋升**（`forecast.backtest` 任务每 6 小时一次，或手动触发），在此之前它们不被采用；operator 需重新构建以带上缓存与新参数；已有模块的时区和已存的算法参数不变。
