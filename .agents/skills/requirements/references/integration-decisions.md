# 融合约定与来源追踪

> **2026-10-31：自定义资源 PredictiveScaler（CRD）已移除。** operator 只读平台的模块并写副本，不在集群里创建任何资源；下文提到 CRD、PredictiveScaler、Static/Prometheus 决策来源、Observe 模式的地方是当时的设计，不再适用。现状见 `.agents/skills/control-plane/SKILL.md`。


用户当前任务决定范围，AGENTS.md 提供边界，技能提供执行流程，references 保存详细研究。根目录 docs 原稿保留；技能包自带参考，不依赖不存在的 docs/01-… 路径。

| 原调研 | 技能内落点（以 .agents/skills 为基准） |
|---|---|
| 01-功能需求 | requirements/references/features.md |
| 02-系统架构 | control-plane/references/architecture.md |
| 03-数据与接口契约 | requirements/references/contracts.md |
| 04-验收标准与实施阶段 | requirements/references/acceptance.md |
| 05-算法目录与参数 | algorithm-library/references/catalog.md |
| 06-官方参考资料 | requirements/references/official-sources.md |

参考副本是需求/设计，代码与测试证明是否交付。修改需求时同步对应参考，避免静默分歧；不能把原稿现时状态当自动生效的新实现。

## 统一口径

原有 .agents/skills/constraint 的前后端调研已补成可发现技能，保留列表、表单、采集预算和 SDK 优先等约束，清理旧品牌、失效章节、交易项目路径与镜像流程。覆盖率是研究目标，不能宣称现有代码已满足。

- 目标是单集群资源预测和 Pod 扩缩容。金融指标只是过去的多算法示例，已从正式候选、参数 UI 与验收中排除；不再安排交易指标资源化改造。算法以统计基线、树模型、深度时序与预训练时序模型分层，依据 algorithm-library/references/research.md 的调研及实际回测筛选。
- hpa-operator 即 kube-res-foresight-operator，指本项目kube-res-foresight-operator，用于替代 HPA/KEDA，不能与原生 HPA 双写；研究八个组件是逻辑职责，不要求八个微服务。
- **2026-11-19 阶段调整**：当前阶段是 P10 资源超配分析与可观测大盘（见 [overprovision-p10.md](overprovision-p10.md)）：只依据真实历史指标判断超配，**不用预测模型**，不做成本，以 Observe/Recommend 为主。成本口径（P9，见 [cost-closure-p9.md](cost-closure-p9.md)）暂缓，需求整理在 [cost-module-requirements.md](cost-module-requirements.md)；以下约束对成本模块仍然成立：不替换副本唯一控制者、不引入跨集群。副本只由 operator 控制；Karpenter/Cluster Autoscaler 管节点，平台只模拟和记录；准入记录只来自真实准入事件。
- 系统六项能力现跨 system/authorization 分组保留，不为名称统一强迁菜单；当前四入口与未来规划明确区分。
- 数据持久化统一复用现有 GORM ORM，兼容 SQLite、MySQL、PostgreSQL，由部署配置选择；PostgreSQL 不是唯一或强制后端。系统设置、用户、角色、权限、菜单和操作日志复用现有模型、DAO、API、页面及权限体系，只做增量扩展，不重复建表或重建模块。
- 研究 predictive_* 与 Factory 默认 kube_res_foresight_ 前缀需实施时明确兼容映射。
- bypass/调用故障保留原资源；模块合法 fallback 只在启用控制而模型不可用时使用，无效则保留原值，不能拼造资源。

## 未决事项

正式 CRD API group、服务间协议、快照存储/签名、投递保障、保留期/配额、LDAP 同步周期、模型晋升阈值、冷却/限幅默认值须有配置与依据；示例不是生产默认值。

P11 开工前待确认的十项（G0 出问题的接口与网关日志、对话内容的可见性、保留期、异步任务放哪、进度推送、缓存键、验收数值、工具权限范围、数据外发与脱敏、顺序）见 [ai-and-performance-p11.md](ai-and-performance-p11.md) 的“待你确认”，每项给了建议和理由；其中 G1 会改变“对话不入库”的现有设计。

P10 开工前待确认的十项（集群维度、operator 新增只读 RBAC、工作负载范围、公式与阈值、使用量怎么来、保留期、大盘位置、Grafana 地址、LLM 周期、P9 代码怎么处理）见 [overprovision-p10.md](overprovision-p10.md) 的“待你确认”，每项给了建议和理由。

（暂缓）P9 成本闭环开工前待定的七项（价格来源、节点数据来源与 operator 只读权限、基线窗口、首页、模拟器保真度、自动执行与回滚、币种）见 [cost-closure-p9.md](cost-closure-p9.md) 的“待你决定”，每项给了建议和理由。C1～C4 已按建议默认值做了（用户没有逐项答复）；C5 起仍要先确认 D3（基线）和 D6（自动执行与回滚），D2 的 operator 只读上报要动 RBAC，也要先确认。

日期端点是否包含、数值优先级方向和完整稳定排序须在 schema/UI 明示并测试。时区使用 IANA 名称，UTC 保存，验证 DST 缺失/重复时间；研究不能替代这些实现选择。

算法部分于 2026-10-23 核查官方文档及作者仓库，记录在 algorithm-library/references/research.md；未运行真实集群回测。其余继承调研的 Kubernetes 版本结论仍需实施时核实，不把索引当运行证据。
