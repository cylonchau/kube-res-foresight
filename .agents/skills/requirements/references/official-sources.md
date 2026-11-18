# 官方与项目资料（已按能力类型区分）

> **2026-10-31：自定义资源 PredictiveScaler（CRD）已移除。** operator 只读平台的模块并写副本，不在集群里创建任何资源；下文提到 CRD、PredictiveScaler、Static/Prometheus 决策来源、Observe 模式的地方是当时的设计，不再适用。现状见 `.agents/skills/control-plane/SKILL.md`。


> 来源：根目录 `docs/06-官方参考资料.md`。算法来源于 2026-10-23 核查，详见 [算法调研](../../algorithm-library/references/research.md)；其余继承资料的版本结论仍需实施时按目标集群核实，不代表已运行验证。

- [Kubernetes Admission Webhook](https://kubernetes.io/docs/reference/access-authn-authz/extensible-admission-controllers/)：由 API Server 调用，分 Mutating/Validating。
- [Admission Webhook Good Practices](https://kubernetes.io/docs/concepts/cluster-administration/admission-webhooks-good-practices/)：控制超时、Fail-Open、安全验证。
- [Kubernetes HPA](https://kubernetes.io/docs/concepts/workloads/autoscaling/horizontal-pod-autoscale/)：副本计算、行为限制和多指标。
- [Kubernetes Resize Container Resources](https://kubernetes.io/docs/tasks/configure-pod-container/resize-container-resources/)：v1.35 起稳定；不能在 v1.30 默认假设可用。
- [Prometheus HTTP API](https://prometheus.io/docs/prometheus/latest/querying/api/)：`query_range` 等时序查询。
- [StatsForecast](https://github.com/Nixtla/statsforecast)：统计算法定义参考（Go 自研实现）。
- [MLForecast](https://github.com/Nixtla/mlforecast)：时序特征组织参考（Go 自研实现）。
- [LightGBM 参数](https://lightgbm.readthedocs.io/en/stable/Parameters.html)：分位数目标定义参考。
- [VPA Recommender](https://github.com/kubernetes/autoscaler/blob/master/vertical-pod-autoscaler/docs/components.md)：历史用量资源规格推荐参考，不等同未来负载预测。
- [Kubebuilder Book](https://book.kubebuilder.io/)：CRD、Operator、Webhook 的开发参考。
- [Grafana LDAP](https://grafana.com/docs/grafana/latest/setup-grafana/configure-access/configure-authentication/ldap/)：仅作为 LDAP 认证功能参考，平台不直接复制 Grafana 配置字段。
- [OpenCost](https://opencost.io/)：集群成本分摊、闲置成本与 API 的参考，P9 的可选价格来源；指标名和接口按目标版本核实，核实前不写进指标预设，索引不是已验证证据。
- [Karpenter Disruption/Consolidation](https://karpenter.sh/)：节点整合行为的参考；P9 的装箱模拟只是 what-if，不等价于它，也不调用它。
- [Kubernetes Resource Management for Pods and Containers](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/)：requests 参与调度、limits 约束运行的官方定义，成本方案不能把 requests 等同于账单。
- [Multiple HPA conflict on same target](https://docs.cloud.google.com/kubernetes-engine/docs/troubleshooting/horizontal-pod-autoscaling)：同一个 scaleTarget 多控制器会争抢副本数。

以上链接支持各项 Kubernetes/算法原则。`PredictiveScaler` CRD、平台 metrics、业务 API、模块参数属于本项目拟定契约，非官方已存在能力。
