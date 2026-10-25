# 融合约定与来源追踪

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
- hpa-operator 即 kubeforecast-operator，指本项目kubeforecast-operator，用于替代 HPA/KEDA，不能与原生 HPA 双写；研究八个组件是逻辑职责，不要求八个微服务。
- 系统六项能力现跨 system/authorization 分组保留，不为名称统一强迁菜单；当前四入口与未来规划明确区分。
- 数据持久化统一复用现有 GORM ORM，兼容 SQLite、MySQL、PostgreSQL，由部署配置选择；PostgreSQL 不是唯一或强制后端。系统设置、用户、角色、权限、菜单和操作日志复用现有模型、DAO、API、页面及权限体系，只做增量扩展，不重复建表或重建模块。
- 研究 predictive_* 与 Factory 默认 kubeforecast_ 前缀需实施时明确兼容映射。
- bypass/调用故障保留原资源；模块合法 fallback 只在启用控制而模型不可用时使用，无效则保留原值，不能拼造资源。

## 未决事项

正式 CRD API group、服务间协议、快照存储/签名、投递保障、保留期/配额、LDAP 同步周期、模型晋升阈值、冷却/限幅默认值须有配置与依据；示例不是生产默认值。

日期端点是否包含、数值优先级方向和完整稳定排序须在 schema/UI 明示并测试。时区使用 IANA 名称，UTC 保存，验证 DST 缺失/重复时间；研究不能替代这些实现选择。

算法部分于 2026-10-23 核查官方文档及作者仓库，记录在 algorithm-library/references/research.md；未运行真实集群回测。其余继承调研的 Kubernetes 版本结论仍需实施时核实，不把索引当运行证据。
