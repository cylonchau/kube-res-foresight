# 资源预测算法调研与筛选依据

调研日期：2026-10-23。外部库仅作算法定义参考，实现全部为 Go；本轮核查官方资料能力与限制，未安装模型、运行集群数据或实测准确率；项目优先级是工程建议，不是流行度排名。

| 方向 | 官方证据 | 本项目建议 |
|---|---|---|
| 统计 | [StatsForecast](https://github.com/Nixtla/statsforecast)：ETS、ARIMA、MSTL 的算法定义与预测区间（仅作算法参考） | 以 Go 自行实现可解释基线，季节模型先检查历史覆盖，并以参考实现输出做单测对照 |
| 树模型时序 | [MLForecast](https://github.com/Nixtla/mlforecast)：lag、滚动变换、日期特征的组织方式（仅作设计参考） | 以 Go 实现岭回归与 GBDT 的特征预测，不运行 Python |
| 分位数 | [LightGBM 参数](https://lightgbm.readthedocs.io/en/stable/Parameters.html)：quantile objective 的定义（仅作算法参考） | Go 实现分位数损失，评估 P50/P90/P95，需单独验证覆盖，不当作自动校准保证 |
| 资源规格 | [VPA 组件](https://github.com/kubernetes/autoscaler/blob/master/vertical-pod-autoscaler/docs/components.md) 与 [衰减直方图源码](https://github.com/kubernetes/autoscaler/blob/master/vertical-pod-autoscaler/pkg/recommender/util/decaying_histogram.go) | 借鉴历史用量/近期加权推荐 requests，不当作未来流量预测，也不引入第二写入控制器 |

正式清单排除金融指标和资金策略；首期用统计基线对照岭回归/GBDT，算法全部以纯 Go 实现。厂商通用基准不能证明本项目容器负载上的优势，须比较共同切分、输入可见性、horizon、资源成本及 SLO；不能用 stars、模型大小或新旧推断准确率。
