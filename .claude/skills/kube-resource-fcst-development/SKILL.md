---
name: kube-resource-fcst-development
description: 开发、排查、评审或规划当前仓库 kube-resource-fcst 资源预测与 Pod 扩缩容平台时使用；按任务接入现有 Go/Vue/GORM 架构、专项技能和执行进度。
---

# kube-resource-fcst 项目开发

这是 Claude Code 的项目技能入口，需在当前仓库内使用。共享规则维护在 `.agents/skills/`，直接读取对应 Markdown，不依赖其他工具的技能发现机制，也不复制整套研究。

## 开始任务

1. 必读 [AGENTS.md](../../../AGENTS.md)。[代码地图](../../../.agents/skills/requirements/references/repository-map.md) 只在需要定位文件时读；[执行进度](../../../docs/platform-progress.md) 只看末尾条目或用 Grep 查相关主题，不整篇读。核对源码与 Git 状态，区分已有实现、规划和待验证能力。
2. 只在需求拆解、菜单权限、系统设置、数据源接入或验收类任务时读取 [需求与接入](../../../.agents/skills/requirements/SKILL.md)；其余任务按下表只加载当前任务所需的一份技能及其 references；文件中的相对链接相对该文件解析。
3. 找到最近的实现，沿既有分层和界面模式增量修改；完成授权范围内工作，执行相关验证，更新进度中的当前基线、工作包状态和证据。
4. 查找文件、符号或用法直接用 Grep/Read，不为此启动子代理；只有用户明确要求，或任务确实需要并行的独立大块工作时才用。

## 按任务读取

| 任务 | 共享规则 |
|---|---|
| 前后端、配置、构建、采集与 SDK | [公共约束](../../../.agents/skills/constraint/SKILL.md)，按范围继续读 frontend.md / bankend.md |
| 需求、菜单、系统设置、数据源、验收 | [requirements](../../../.agents/skills/requirements/SKILL.md) |
| 算法、训练推理、特征、动态参数 | [algorithm-library](../../../.agents/skills/algorithm-library/SKILL.md) |
| Operator、动态副本与指标 | [control-plane](../../../.agents/skills/control-plane/SKILL.md) |
| Kyverno 接入、资源快照、准入与回退 | [admission-safety](../../../.agents/skills/admission-safety/SKILL.md) |
| 回测、准确率、晋升和领域审计 | [model-evaluation](../../../.agents/skills/model-evaluation/SKILL.md) |

## 本项目关键约束

- 复用系统管理、授权管理、任务中心；系统设置、用户、角色、权限、菜单和操作日志不重新建设。
- 持久化沿用现有 GORM、DAO 与显式迁移，兼容 SQLite/MySQL/PostgreSQL；不强制 PostgreSQL。
- 算法按当前调研筛选，纯 Go 实现，首期统计基线与岭回归/GBDT 对照；金融技术指标不进入算法目录或验收，没有例外（macd_trend 已移除）。准确率与性能需真实滚动回测证据。
- 预测流水线（pkg/collector、forecast、backtest、decision、forecastsvc）、LDAP、operator、准入接口已实现，只用合成数据/模拟外部系统验证：新算法走 forecast 注册表并配单测；回测必须保持无泄漏；decision、MakePlan 保持纯函数；admission/auto 由系统设置关闭，每次回答都检查开关。
- 副本由 kube-resource-fcst-operator 按平台模块唯一控制（替代 HPA/KEDA；没有 CRD），不能与 HPA/KEDA 或 Kyverno 双写；准入同步链路不等待模型、Kafka 或 Prometheus 长查询。
- kube-resource-fcst-operator 使用 Go 的 Operator SDK / Kubebuilder（controller-runtime）框架，不手写 informer/workqueue；已实现控制器逻辑与清单，只用 fake client 验证，未在真实集群验证。
- 新版文档与旧技能的 Kyverno/独立 Webhook、三模块和动态决策来源差异，按执行进度 P0 先对齐；不得默默采用冲突设计或宣称功能已实现。
- 前端复用现有 Vue/Naive UI 和表格模式；根仓库与 `web` 独立管理 Git，保留用户已有修改。
- 文档整理、代码实现、真实集群验证分开记录；不把通用模型基准或历史测试结果当本次新功能验收。

需求与共享规则变化时更新 `.agents/skills/`、相关 docs 和执行进度；此入口只在路由/公共接入方式变化时修改，不运行旧的量化项目镜像脚本。技能不授予额外部署、发送消息、删除数据或重写远端的权限。

格式依据：[Claude 官方 Agent Skills 文档](https://platform.claude.com/docs/en/agents-and-tools/agent-skills/overview)。此技能为仓库内入口，不是可脱离仓库直接上传的自包含技能包。
