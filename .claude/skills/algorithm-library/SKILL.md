---
name: algorithm-library
description: 用于 KubeForecast 算法注册、动态参数编辑、特征映射、训练推理和组合策略；新增或审查资源预测插件与算法任务时使用。
---

# 算法插件与动态参数

先读 [代码地图](../requirements/references/repository-map.md)、[算法目录](references/catalog.md)、[调研依据](references/research.md) 和 [融合约定](../requirements/references/integration-decisions.md)。目录是经过适用性筛选的候选能力，不要求一次实现全部。

## 实施流程

1. 区分特征、预测模型与控制策略。定义 AlgorithmDefinition 的 ID、类别、版本、输入、JSON Schema、最小样本、warm-up、训练窗口、horizon、输出单位、超时、回退和评估指标。
2. 核实来源、粒度、单位、NaN/缺样、采样间隔与窗口；Service RPS 不冒充单 Pod 真值。参数提供中文解释、默认值来源、边界、依赖和 schema 版本。
3. 算法纯 Go 实现（已实现 Naive、SeasonalNaive、EWMA、Holt-Winters、岭回归 AR、GBDT）、进程内运行，不引入 Python 或外部模型服务。首期实现 Naive/SeasonalNaive、EWMA/Holt-Winters 与岭回归/GBDT 对照；深度时序、预训练模型和 Prophet 不在范围内。自研实现须配单测与已知输出对照，目录字段是平台参数，由 Go 校验。
4. 多算法独立保存参数、权重、优先级、条件和频率，记录所有候选和选择原因。失败转候补，全部失败或样本不足使用已验证模块固定默认值，融合结果仍需安全校验。
5. 评估与晋升读 [model-evaluation](../model-evaluation/SKILL.md)；算法不能直接改 Pod 或副本，扩缩容只由 kubeforecast-operator 按 CRD 执行。

## 代码接入

调度扩展 pkg/job/tasks 下独立任务并接 registry.go，复用持久化调度、重试和幂等。参数管理接现有 DTO/handler/DAO/model/migration；API 见 [共享契约](../requirements/references/contracts.md)。算法已在 pkg/forecast 实现（Model 接口、注册表 registry.go、参数 schema 与校验）：模型无状态，一次调用在给定历史上拟合并预测，必须遵守 context 取消，历史不足返回 ErrInsufficientHistory 由调用方跳过。新增 Go 算法 = 实现 Model，用 forecast.Register(Implementation{Definition, Build, Check}) 注册（参数 Schema 在 Definition 里，别处不得再写算法名），补单测（已知输出对照）并在回测中与基线比较。不上传代码的扩展走公式算法：管理员在算法管理页给出参数 Schema 与公式，由 pkg/forecast/formula.go 的解释器真实执行（保存前先在合成序列上试算，编辑公式即换实现版本），见 [算法框架规格](../requirements/references/algorithm-framework-spec.md)。平台不保存模型权重：每次运行写一条训练记录（历史窗口、参数、公式、模型版本、耗时、结果），同一版本、参数与窗口可复现同一预测。

算法中心（前端 views/forecast/algorithms，后端 handle_algorithm_center.go）：目录是固定的内置架构（类别、版本、复杂度与最小样本说明，registry.go 的 notes 必须与各模型 MinSamples 一致；参数 schema），**没有参数模板**。用户创建**算法实例**（module_algorithm：名称、目录算法、按 schema 校验并补默认值的参数 JSON、参与的指标来源键、预测视野、权重、训练历史天数、训练时间预算、优先级）；同一算法可用不同参数创建多个实例，预测、回测、评分、实测都以实例名归档。改参数或来源会改变模型版本签名，既有晋升自动失效。GET /algorithms/:id/usage 给出各模块的实例及其评分、各来源上的回测/实测表现，GET /modules/:id/algorithms/:bid/trace?source= 追溯 实例→forecast.run 任务→预测行（model_version）→回测→评估→评分；POST /modules/:id/score 立即评分，GET /modules/:id/ranking 当前排名，GET /algorithm-scores 评分历史。

UI 沿用 web/src/views/system/config 等表单，宽屏弹窗自适应小屏。金融指标仅为过去的插件示例，不纳入正式算法或验收，不构造 OHLCV；唯一例外是 macd_trend（单变量，借用快/慢/信号 EMA 差分结构做有上限的趋势修正，结果下限为 0，不产生买卖信号）。算法目录另含 hist_percentile（衰减直方图百分位，VPA/Autopilot 式，内存与 OOM 最稳妥）、random_forest、knn、naive_bayes，以及纯 Go 进程内小模型 rnn / gru / lstm（受训练时间预算限制，超时按失败走兜底）。多指标预测 = 实例参与的每个来源都被预测；声明支持协变量的算法（岭回归 AR、GBDT、随机森林、朴素贝叶斯、RNN/GRU/LSTM）同时把实例的其余来源作协变量（滞后特征，只用目标步之前的值；预测步沿用最后观测值），其余算法单变量；协变量集合进入模型版本签名，回测在每个截止点同样切片，不泄漏未来。资源规格推荐与未来负载预测分开；内存峰值/OOM 保护不由点预测替代。

模型只有在工作负载滚动回测、峰值低估、SLO 和推理预算验证通过后才可晋升；未优于统计基线时保留基线。选型记录模型/库/权重版本与使用条件，不默认最新版适合生产。

验证样本不足、非法参数、单位错配、模型超时、版本兼容、候补全部失败和无未来泄漏的滚动回测。
