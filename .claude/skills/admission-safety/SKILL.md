---
name: admission-safety
description: 用于 KubeForecast Pod 资源准入、固定周期、默认配置、bypass、推荐快照及运行中资源调整的实现和审查，检查数值安全与发布故障旁路。
---

# 资源准入与故障回退

先读 [代码地图](../requirements/references/repository-map.md)、[架构职责](../control-plane/references/architecture.md) 和 [安全决策表](references/safety-policy.md)。现有管理 HTTP handler 不是 Admission Webhook。

## 实施流程

1. 实现 API Server 调用的 Mutating Admission Webhook，不改 kubelet。明确处理范围、AdmissionReview 版本、TLS、超时、对象选择、JSON Patch 和幂等。
2. 同步路径只读已验证、有版本、未过期快照，不等待 Kafka、Prometheus 或推理。读取异常保留原值；审计用非阻塞 outbox/有界缓冲并观察丢失。
3. 先判 bypass/禁止修改，再选固定覆盖、有效模型、合法模块 fallback、原始值。failurePolicy Ignore 只处理调用故障，不能代替 TTL 和数值安全校验。
4. 按容器保存完整原资源、建议资源及应用资源，记录推荐 ID、快照时间、策略/模型版本、操作者或服务身份、规则、原因和请求关联；不能只存 patch。
5. CREATE 改实际 Pod 不等于 Deployment template 已更新；模板管理需防漂移和无限滚动。运行中 resize 独立开关，按目标版本核实；未经回测、安全检查和授权不得自动调低 Memory limits。

固定周期的时区、端点、优先级方向、冲突排序、未填 requests、到期恢复都需显式定义。只覆盖指定字段，不让模型覆盖固定字段。复用管理配置/鉴权/审计，但不能把浏览器 JWT 流程当 Webhook 身份验证。

验证超时、服务断开、过期快照、非法 fallback、重复准入、配额/LimitRange、周期边界与时区；对照 [阶段验收](../requirements/references/acceptance.md)。

## 已实现的准入侧（未在集群验证）

- 平台接口 `GET /api/v1/admission/recommendations?namespace=&kind=&workload=&container=[&limit_cpu_milli=&limit_memory_bytes=]`（pkg/app/server/api/handle_runtime.go）：回 requests（CPU 毫核、内存字节），模块配置了 `limit_cpu_ratio` / `limit_memory_ratio`（0 或 1–10）时再回 `limits`（= 应用的 request × 比例；0 保持工作负载自己声明的 limit，此时推荐超过声明 limit 的字段仍被丢弃）；从不回副本，副本只归 operator。对 enabled、mode 为 admission 或 auto（auto ⊇ admission）、未 bypass、推荐未过期的模块返回 apply=true；无字段可用则 apply=false 并给原因。没有为该工作负载添加模块时返回 apply=false，不是错误。每次回答都检查 forecast.allow_admission_mode（默认 false，缓存约 10 秒），所以关闭开关即刻停止准入。凭据权限 `Admission Recommendations (read)`。
- Kyverno 策略 deploy/kyverno/kubeforecast-requests.yaml（ClusterPolicy `kubeforecast-resources`，Kyverno 1.19.1 上用 kubeadm Docker Desktop 集群验证过：有模块的新 Pod requests/limits 被改写，没有模块或平台不可用时 Pod 原样创建且不报错）：一个 Pod 只调一次平台；凭据读自 Secret `kyverno/kubeforecast-admission`（Role 只允许读这一个 Secret，见 deploy/testbed/kyverno-credential.yaml）；failurePolicy=Ignore、超时 5 秒，准入链路不等待模型、Kafka 或 Prometheus。只改**新建**的 Pod；运行中的 Pod 要等工作负载滚动，副本数不由 Kyverno 管。ReplicaSet 哈希剥离、LimitRange/ResourceQuota 交互、MySQL 以外库的行为仍未验证。
- 决策优先级（bypass > 固定周期 > 算法建议 > 模块 fallback > 原值）、硬边界裁剪、TTL 与 UTC/IANA 周期在 pkg/decision，推荐快照 recommendation.detail 保存每个字段的来源/原因。
