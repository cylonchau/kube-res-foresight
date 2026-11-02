# Predictive Autoscaling Platform — Agent 总指令

## 项目目标
构建**单 Kubernetes 集群**智能资源预测与调度平台，中文界面与中文需求。通过 Prometheus 历史/实时指标、多算法插件、回测与模型选优，输出资源规格建议和目标副本数；在准入阶段安全改写 Pod CPU/Memory requests/limits；由 **Operator**（读平台模块、写副本，不创建任何集群资源）负责副本数量，另行暴露 Prometheus Metrics。全部预测/准入/扩缩容行为可审计，故障时自动回退默认配置。不是多云项目。

## Agent 工作准则
1. 所有开发任务先读 [requirements](.agents/skills/requirements/SKILL.md) 及其 [代码地图](.agents/skills/requirements/references/repository-map.md)，再按下方索引读取专项技能和对应 references。`docs/` 是保留的调研原稿，融合与未决事项见 [融合约定](.agents/skills/requirements/references/integration-decisions.md)。缺乏证据不得自行发明 Kubernetes 原生 API 参数。
2. 使用 `.agents/skills/*/SKILL.md` 分工实现模块；所有新增算法必须具备输入约束、参数 schema、warm-up、预测窗口、输出单位、离线回测、失效回退和版本信息。
3. **工作负载的 replicas 仅允许一个控制器拥有写权限**；选择 kube-res-foresight-operator（替代 HPA/KEDA）后不得再由 HPA/KEDA 同时控制同一 Deployment 副本。可以保留 HPA 作为未来可切换的执行后端，切换时先释放字段所有权。
4. 预测/指标输出不得直接等同于真实最大承载能力；永远区分观测值、推断值、算法预测值和执行结果。
5. Webhook 决不等待 Kafka、Prometheus 长查询或模型推理；仅读取最新已验证、未过期的推荐快照；异常即保留原资源配置。`failurePolicy: Ignore` 是网络/调用失败保护，不是错误推荐的数值保护。
6. 不修改 kubelet 二进制。准入是 Kubernetes API Server 的 Admission Webhook；运行中资源修改须通过 Kubernetes API（版本适配）和 Operator 完成。
7. 重大默认值、算法权重、节日覆盖、Namespace/工作负载开关都必须可由 UI 管理，有版本、操作者和审计记录。
8. 存储用 UTC（SQLite 为带 +00:00 的文本）；平台默认时区 UTC+8（`KUBE_RES_FORESIGHT_TIMEZONE` 可改），用于日志、数据库会话、新模块的周期和页面显示（页面固定按 UTC+8，不随浏览器）；调度周期按显式 IANA 时区解释，区间起止清晰定义。时间只在显示时转换一次，不得静默转换时区，也不得把显示时区写回存储。
9. 不在未经回测、安全检查和授权的情况下自动调低内存 limits；按 Observe→Recommend→Admission→Auto 渐进上线。

## 技术栈基线（设计选型，非现成产品事实）
- 控制层：Go；kube-res-foresight-operator 使用 Operator SDK / Kubebuilder（controller-runtime）实现；Admission Webhook；Metrics 内置于 Platform。operator 没有自定义资源（CRD 已于 2026-10-31 移除）。
- 算法层：纯 Go 实现，在 Platform 进程内运行，不引入 Python 或外部模型服务；首期 Naive/SeasonalNaive、EWMA/Holt-Winters 统计基线与岭回归/GBDT 机器学习候选；按真实负载滚动回测晋升。金融技术指标不进入算法目录，没有例外（曾有的 macd_trend 已移除，迁移会删除它的设置与历史数据）；详见 algorithm-library。
- 指标：Prometheus HTTP API（`query`, `query_range`），可扩展 Thanos/Mimir。
- 异步事件：Kafka；业务与审计数据复用现有 GORM ORM 和数据库配置，兼容 SQLite、MySQL、PostgreSQL，由部署配置选择；模型产物对象存储；可视化管理控制台。
- 身份：本地用户与 LDAP/LDAPS、RBAC；敏感凭证以 Secret 存储。

## 技能索引

- [constraint](.agents/skills/constraint/SKILL.md)：公共前后端开发约束；开发任务按修改范围读取 frontend.md / bankend.md
- [requirements](.agents/skills/requirements/SKILL.md)：需求、代码接入、菜单、数据源与交付验收
- [algorithm-library](.agents/skills/algorithm-library/SKILL.md)：算法注册、动态参数、训练与策略组合
- [control-plane](.agents/skills/control-plane/SKILL.md)：Operator、Metrics 和工作负载副本执行
- [admission-safety](.agents/skills/admission-safety/SKILL.md)：资源准入、资源变更、故障旁路与固定周期覆盖
- [model-evaluation](.agents/skills/model-evaluation/SKILL.md)：准确率、回测、决策与执行审计

## 当前代码基线

当前为 kube-res-foresight：管理底座（系统管理、授权管理、任务中心；工作流引擎与 Telegram 已移除，退役表仅在显式 --upgrade 时清理）之上，已实现数据源与指标映射、历史采集与质量、纯 Go 预测算法与滚动回测、决策与推荐快照、实测回填与排行榜、Platform 指标与 /readyz、LDAP 认证（配置在系统管理→系统参数的 LDAP 分类）、算法管理（每个目录算法一份平台级设置：启用、参数、权重、采用门槛，对所有模块统一生效）、模块（一个 Kubernetes 工作负载，自带多指标来源与阈值、历史窗口、预测周期，自动使用所有已启用算法，无需创建实例或绑定，见 requirements/references/module-algorithm-spec.md）、算法框架（forecast.Register 注册表 + 公式算法 + 训练记录，见 requirements/references/algorithm-framework-spec.md）、Prometheus 指标数据集与数据周期、算法评分/当前排名/历史排名、动态副本上限、OOM 地板/JVM 堆压力/分层降级记录、kube-res-foresight-operator（按平台模块写副本的逻辑与 RBAC 清单，没有 CRD）、准入推荐接口与 Kyverno 参考策略草稿；后端 Go/Gin/GORM，前端 Vue/Naive UI。以上均只在 SQLite、合成数据、模拟 Prometheus/Kubernetes/LDAP 上验证：真实 Prometheus、真实集群（operator 扩缩容、RBAC）、Kyverno、MySQL/PostgreSQL、真实 LDAP 的 StartTLS 以及真实负载上的预测准确率都未验证，admission 与 auto 模式默认由系统设置关闭。新增功能应沿用现有分层、任务调度、权限与前端模式；不能将技术栈设计和研究示例当成现成实现。

系统设置、认证授权、用户、角色、权限、菜单及操作日志均复用现有模型、DAO、API、页面和权限体系，只做必要增量扩展，不重新建设或调整既有菜单归属。预测业务与审计新增模型同样接入现有 GORM 和显式迁移，保持 SQLite/MySQL/PostgreSQL 三种数据库兼容，不以 PostgreSQL 为唯一或强制后端。

`.agents/skills/` 与 `.claude/skills/` 为双向同步的技能位置（见文末“技能双向同步”）；不使用已移除的 quant-system-development。新增参数按锁定版本核实 SDK/官方资料，优先采用维护中的库。

## 交付完成定义

实施阶段、工作包、依赖与实际完成状态统一维护在 [执行进度](docs/platform-progress.md)。完成工作包时同步更新顶部基线和阶段状态，再补充验证记录；规划项不得标记为已实现。
整个平台交付需具备文档、API schema、控制器、Webhook、算法插件、数据库迁移、管理界面、指标、故障回退、测试和部署清单；仅存在演示配置不算已实现。单次任务仅按所涉功能完成对应交付与验证，无需为局部修复扩展到全部平台。

## 技能双向同步（Codex ↔ Claude）

`.agents/skills/`（Codex）与 `.claude/skills/`（Claude）内容保持一致，两边都是可编辑的源。用任一工具修改技能后，必须同步另一边：

- 执行 `bash scripts/sync-skills.sh`：逐文件比较，以修改时间较新的一份覆盖较旧的一份；只存在一侧的文件会复制到另一侧。
- `--check` 只报告差异（有差异退出码为 1）；同一文件两边修改时间相同但内容不同会报 CONFLICT，需人工处理。
- 删除或重命名技能文件无法被双向比较识别，改用 `--mirror agents` 或 `--mirror claude`，以指定一侧为准并删除另一侧多余文件。
- Claude Code 通过 `.claude/settings.json` 的 PostToolUse 钩子在编辑后自动同步；Codex 无此钩子，修改技能后须手动执行脚本。
- 提交前运行 `--check`，确认两边一致。
