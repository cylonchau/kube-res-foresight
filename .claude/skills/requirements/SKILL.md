---
name: requirements
description: 用于 kube-res-foresight 需求拆解、菜单权限、管理页面、数据源接入和阶段验收；将预测调度需求映射到现有 Go/Vue 代码。
---

# 需求与平台接入

先读根目录 AGENTS.md 和 [代码地图](references/repository-map.md)，定位最近实现再扩展。区分已实现能力与规划。

工程实现同时读取 [constraint](../constraint/SKILL.md) 中对应前后端规范。

## 按需读取

- 算法管理（平台级统一配置）与模块的职责划分、自动关联、菜单与迁移：[module-algorithm-spec](references/module-algorithm-spec.md)。
- 算法注册框架与自定义公式算法、训练记录（弹窗，无独立页面）、Metrics 数据集（分类、换算/精度/聚合/缺失处理、自定义 PromQL）、数据周期与算法需求、评分/排名（当前与历史在同一页）/算法运行情况页面：[algorithm-framework-spec](references/algorithm-framework-spec.md)。
- 页面、LDAP、数据源和模块：[功能范围](references/features.md)。
- API、表、事件和指标：[共享契约](references/contracts.md)。
- 阶段与测试：[验收标准](references/acceptance.md)。
- 来源、术语和未决事项：[融合约定](references/integration-decisions.md)。
- 外部实现：[官方资料索引](references/official-sources.md)，按目标版本核实；索引不是已验证证据。

## 实施流程

1. 列出本次需求、现有能力、缺口和不确定事实。保留系统参数、认证授权、用户、角色、菜单、操作日志，以及现有任务中心。
   系统设置、用户、角色、权限、菜单及日志直接复用现有模型、DAO、API、页面与权限体系，只做必要扩展，不重新建设。所有业务与审计模型复用 GORM，兼容 SQLite/MySQL/PostgreSQL，不强制指定 PostgreSQL。
2. 对照代码地图接入 DTO → handler → DAO → model → migration，以及页面 → API → 权限 → 菜单。只修改本次所需环节，小修复无需补齐整个平台。
3. 新接口明确字段、单位、分页排序、权限、错误码和兼容性；新模型接现有注册与显式迁移。凭据不得进入日志或普通配置响应。
4. 复用 Vue/Naive UI 页面、表格和弹窗模式，不独立重设计。算法参数使用自适应宽屏弹窗和 schema 字段说明。
5. 按验收参考选择相关验证；报告实际结果与未实现内容。示例、计划和依赖存在不能算交付。

LDAP 须覆盖 LDAPS/StartTLS、CA、用户/组查询与角色映射、禁用同步、失败审计及本地应急管理员；JWT/Casbin 不代表 LDAP 已完成。

**P12 Cluster Agent 与集群大盘（2026-11-25 规划，E1～E8 已答复，E1～E4 已做：集群登记与密钥绑定、Namespace 过滤、事件采集与解释器、3 天保留；其余未开始）**：把 Operator 扩展成只读的集群 Agent：启动绑定集群（多集群隔离，**现有的副本调整不受影响**）、全集群 Node/Pod/Metrics API 采集（连接后可配置过滤 Namespace，选主与其他 controller 一致，只有主 Operator 操作）、扩缩容过程上报（含非本 operator 的副本变化）、Node/Pod/工作负载/HPA 的状态变化追因（**原因取自 Event，支持多云扩展，不特指 Karpenter**；对象间有证据才连因果；变化当时的使用量从 Prometheus 取）、Pod 生命周期异常、集群 Requests/Limits/Used 大盘；保留默认 3 天、系统配置可改；**节点标签日报暂缓**；**除授权工作负载的 scale 外一切只读**。D0～D8 工作包与顺序、已确认的决定、目标 RBAC 与验收对照见 [cluster-agent-p12.md](references/cluster-agent-p12.md)。

**P10 资源超配分析与可观测大盘（2026-11-19 规划，A0～A6 全部未开始）**：当前阶段。先建一个只依据真实历史指标、可解释、可追溯的资源超配分析系统，再考虑预测、自动优化和成本：Controller 只读上报集群与工作负载配置，Prometheus 提供使用量，平台存配置快照与分析结果；多周期（15 分钟到 30 天）CPU/内存/JVM 超配分析；系统内大盘、Grafana Dashboard 与深链；LLM 周期建议。本阶段超配判断**不用预测模型**，成本相关内容全部移出，以 Observe/Recommend 为主、不自动改资源。开工前先读 [overprovision-p10.md](references/overprovision-p10.md)，其中“待你确认”的 E1～E10 没答复的不进开发；页面遵守用户已定的规则（不用页签含分段切换、不拆成多页、字段带“?”说明）。

**P11 AI 资源智能分析与对话、异步计算与 502/504 治理（2026-11-19 整理；C3、C7、C8、C9 已做（能力注册表、权限过滤、tool_mode、页面上下文、文本计划与规则路由，假服务验证）；B0、B5、B6 已做第一部分：诊断手段、可配置的超时、同步操作的预算压在网关超时之内、浏览器报错与重试与取消，根因未定位；其余未开始）**：把两份需求（AI 重新规划为资源智能分析与决策引擎；AI 对话页面与性能优化，含 AI 权限与审计、异步任务状态与生命周期、502/504 验收指标）按现状整理成两条轨道：T1 性能与异步（定位 502/504、异步任务框架、进度、缓存、前端体验、超时治理，不依赖 P10，建议先做）和 T2 AI 对话与分析（会话持久化、独立页面与悬浮窗共享会话、Tool Registry、权限与审计、流式输出、分析与推荐引擎；分析部分依赖 P10 A2）。核心原则：AI 只分析、评估、推荐，不执行任何资源修改；数字由规则引擎给，LLM 只解释；不重复做预测，不靠加长超时解决超时。开工前先读 [ai-and-performance-p11.md](references/ai-and-performance-p11.md)：现状证据（前端 60 秒超时、Helm 没配网关超时、手动运行和 AI 对话都是同步请求、对话只存 `sessionStorage`）、状态生命周期、权限与审计要求和验收指标都在里面；G0～G9 没答复的不进开发，其中 G0（要你提供出问题的接口和网关日志）是 B0 的前提。P10 的 A5 并入 C6。**AI 业务感知（2026-11-19 补充，C3、C7～C11）**：让 AI 知道系统能做什么并真的去调用：统一能力注册表（带权限标签和只读标记）、按用户权限和当前页面注入的上下文、三种工具模式共用（`native` 模型发 `tool_calls`；`text` 模型只输出 JSON 计划、后端严格校验后执行；`router` 规则路由兜底）、连接能力探测。**已核实（读 `codex-api/server.mjs`，没运行）：Codex 反代只有 `/v1/chat/completions`，忽略 `tools`、不返回 `tool_calls`，所以平台工具对它完全不可用**，这就是“AI 只能普通聊天”的原因；现有连接测试只验证“接受 `tools` 参数”，不验证会返回 `tool_calls`，是误导的；反代是带自己工具的代理，沙箱和凭据要核对（G10）。后端只执行注册表里的只读项，模型输出永远是数据；不让模型写任意 PromQL。详见 [ai-and-performance-p11.md](references/ai-and-performance-p11.md) 的“AI 业务感知能力”。

**P9 成本闭环（暂缓：2026-11-19 用户决定移出当前阶段，需求见 [cost-module-requirements.md](references/cost-module-requirements.md)；C1～C4 在暂缓前已实现，只读，只在合成数据上验证；C5～C8 未开始）**：把平台从“预测 CPU、内存”扩展到“资源成本优化与容量决策”：价格与成本（C1）、浪费诊断（C2）、候选方案（C3）、节点装箱模拟（C4）已有后端和一个页面（预测结果 → 成本与浪费，`pkg/cost`、`forecastsvc/cost.go`、`handle_cost.go`）；节省台账（C5）、SLO 保护与回滚（C6）、成本总览的完整形态（C7）、预算预测（C8）未做。开工前先读 [cost-closure-p9.md](references/cost-closure-p9.md)，末尾的“C1～C4 实际实现”写了与原计划的差别和没验证的部分；C1～C4 是按计划里的建议默认值做的（D1–D7 用户没有逐项答复），做 C5 前先确认 D3（基线）和 D6（自动执行与回滚）；四个财务口径（资源释放量、预计节省、成本避免量、实际节省）不得混用，“已实现”必须有节点小时或账单依据；副本仍只由 operator 控制，不引入 HPA 当执行后端，不 cordon/drain，不做跨集群。

**P8 产品闭环（B1、B2 已实现，B3 未开始）**：预测效果报告（B1，预测结果 → 预测效果报告页，`GET /modules/:id/effect-report`）、变更影响预览（B2，该页上的弹窗，`POST /modules/:id/change-preview`，只读回放）已实现并记录了与原计划的差别；告警联动（B3）未开始。范围、验收、口径和差别见 [product-closure-p8.md](references/product-closure-p8.md)；开工前先读它，页面遵守用户已定的规则（不用页签、历史页不加数据列表、字段带“?”说明）。
