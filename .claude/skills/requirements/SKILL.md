---
name: requirements
description: 用于 kube-resource-fcst 需求拆解、菜单权限、管理页面、数据源接入和阶段验收；将预测调度需求映射到现有 Go/Vue 代码。
---

# 需求与平台接入

先读根目录 AGENTS.md 和 [代码地图](references/repository-map.md)，定位最近实现再扩展。区分已实现能力与规划。

工程实现同时读取 [constraint](../constraint/SKILL.md) 中对应前后端规范。

## 按需读取

- 算法管理（平台级统一配置）与模块的职责划分、自动关联、菜单与迁移：[module-algorithm-spec](references/module-algorithm-spec.md)。
- 算法注册框架与自定义公式算法、训练记录、Metrics 数据集（分类、换算/精度/聚合/缺失处理、自定义 PromQL）、数据周期与算法需求、评分/当前排名/历史排名页面：[algorithm-framework-spec](references/algorithm-framework-spec.md)。
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
