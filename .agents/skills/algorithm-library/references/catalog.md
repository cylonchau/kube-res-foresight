# Kubernetes 资源预测算法目录与参数

本目录是目标设计，插件尚未实现。调研日期：2026-10-23。依据与官方来源见 [调研记录](research.md)，执行流程见 [算法技能](../SKILL.md)。与 docs/05-算法目录与参数.md 同步维护。

## 1. 范围与筛选

预测未来 RPS、CPU cores、Memory bytes，明确 target_time、horizon、单位和聚合粒度。预测模型、规格推荐、异常检测、执行策略分别注册和评估。

布林带/Bollinger、RSI、MFI、KDJ、ATR、ADX、金融交叉信号、马丁格尔及交易网格不纳入正式插件、参数页面和验收；MACD 及公式里的 macd/macdsig 函数也已移除（macd_trend 曾是唯一例外，因与资源预测无关被删除）。

候选若输入不可获得、依赖未来未知特征、输出单位不符、缺少可复现回测、超出推理预算、权重使用条件不符、关键负载持续低估或无法安全回退，应淘汰或降级。异常检测不是完整预测器；PID/MPC、限幅/冷却是控制策略，固定日期窗是管理员覆盖。

## 2. 分层候选

以下顺序是结合本项目的工程判断，不是流行度或准确率排行，必须用真实 workload 验证。

| 类别 | 候选 | 适用条件 / 限制 | 优先级 |
|---|---|---|---|
| 对照基线 | Naive、SeasonalNaive | 持续值/周期对照；季节模型需完整周期历史 | 首期必备对照 |
| 统计预测 | EWMA、Holt-Winters（ETS 族，自研 Go 实现；加性或乘性季节、阻尼系数可调、预测不低于 0）、Theta（SES 加半斜率漂移，M3 冠军方法，有季节时先做经典分解） | 平滑或水平/趋势/季节；EWMA 是低成本基线；Theta 在短而有噪声的序列上难被打败 | 首期；Theta 为 P7 A4 新增 |
| 统计补充 | AR/ARIMA、MSTL 季节分解 | 自相关或多重周期明显；限制拟合预算 | 按数据启用 |
| 机器学习 | 岭回归自回归、梯度提升回归树（GBDT，自研 Go）+ lag/rolling/calendar features | 多特征、跨工作负载共享；防泄漏和训练/服务不一致 | 首期重点候选 |
| 资源规格推荐 | 衰减分位数、峰值/OOM 保护（参考 VPA） | 推荐 requests 和安全边界，不是未来流量预测或自动 limits | 首期独立类别 |

所有算法以纯 Go 在 Platform 进程内实现，不引入 Python、外部模型服务或预训练权重；深度时序、预训练模型和 Prophet 不在范围内。Random Forest 等不列首期必做项，有回测收益或具体需求再以 Go 扩展。

## 3. 预测与决策分离

- RPS：优先预测 Service/workload 总速率；基于经压测或受控观测验证的每 Ready Pod 安全容量换算副本。历史总 RPS/副本数不等于真实最大承载能力。
- CPU：明确 container/pod/workload 目标，记录流量、当前/历史副本、requests、节流、部署版本；不能将当前单 Pod CPU 无条件外推到扩缩容后。
- Memory：区分 working set/RSS/cache、峰值、OOM、重启截断和泄漏。短期预测配合安全统计推荐规格；OOM 后低用量不能误判需求下降，点预测不能直接设为 memory limit。

副本换算示意 ceil(预测需求/已验证每 Pod 安全容量)，仅在单位、粒度和适用区间一致时成立，再经最低 Ready、上下界、冷却/限幅与唯一 Operator 执行。不是 Kubernetes 原生公式；预测插件不直接改副本。

## 4. 输入契约与特征

输入含序列/工作负载/容器标识、UTC timestamp、value、unit、aggregationLevel、采样间隔、质量标记。CPU rate、请求 counter→RPS 的转换说明窗口与 reset；累计 counter 不能当速率目标。

特征包括历史 lag、仅向后 rolling mean/std/quantiles/max、时间/星期/已知活动、历史 CPU/RPS/内存、Ready、副本、节流、版本和重启。未来星期/确定活动可用；未来实际流量、CPU、扩容结果不可当已知输入。未知未来协变量须另行预测并记录误差传播。

区分 local/global model，说明跨工作负载 pooling/归一化；避免大对象支配训练。发布/配置变动和漂移需分段或版本标记，不盲目混训。

## 5. 插件与参数

AlgorithmDefinition：id/category/version、implVersion（Go 实现版本）、inputSeries/features、parameters(JSON Schema)、minSamples、warmup、trainingWindow、predictionHorizon、outputs/unit、failurePolicy、accuracyMetrics。模型产物（Go 序列化的系数或树结构）记录训练数据范围、特征/schema 版本、hash、使用条件和存储位置。

| 参数组 | 平台字段示例 / 约束 |
|---|---|
| 公共 | enabled、sampleInterval、lookbackWindow、horizonSteps、minimumSamples、missingDataPolicy、timeout、modelVersion、quantiles、retrainInterval、fallbackOnError |
| SeasonalNaive/Holt-Winters | seasonLengthSteps、trend、damped、seasonal；周期按采样步数，不能硬编码 24=一天 |
| EWMA | alpha 或 span（二选一，互斥）、bias_correction、warmup；0<alpha≤1，明确多步输出规则 |
| 岭回归/GBDT | lags、rollingWindows、calendarFeatures、forecastStrategy(direct/recursive)、l2Lambda、maxDepth、learningRate、nEstimators、objective、quantiles、earlyStopping、seed |
| 校准/评估 | calibrationWindow、coverageTarget、rollingCutoffs、evaluationMetrics、promotionPolicy；校准仅用当时历史 |
| 规格推荐 | decayHalfLife、usageQuantile、peakWindow、headroom、oomPolicy、资源上下界；余量是策略参数 |

上述是平台 schema，由 Go 实现自行定义并校验。每字段需中文说明、类型、单位、范围、必填/依赖、默认依据和来源。样本数、权重、窗口、晋升阈值不得凭经验设为通用生产默认值；horizon 时长与步数精确换算。

## 6. 回测、淘汰与安全

同一滚动 cutoff/horizon/可见数据下比较 Naive/SeasonalNaive、统计、机器学习。不能随机打散时序；训练、归一化、选参、填补和校准仅使用训练窗口，另留测试窗口。

评估 MAE/RMSE/WAPE、pinball loss、分位数覆盖、峰值低估、burst precision/recall、提前量、SLO、额外 Pod-hours 与 P95 推理延迟。分位数不等于覆盖保证，验证校准、分位数交叉和漂移。

记录 champion/challenger 对比、样本量、训练/推理资源成本后按策略审批晋升。未胜出或超预算不进入自动执行，复杂模型未优于基线则保留基线。全部失败用已验证固定 fallback，无合法 fallback 保留用户资源。

## 7. 代码接入与交付

复用 pkg/job 注册预测/训练/评估任务；算法以纯 Go 实现并在 Platform 进程内执行，不为每模型拆独立服务，不依赖 Python；训练/推理有版本、超时和 context 取消，不进入准入同步链路。参数、模型元数据和评估走现有 GORM/DAO/显式迁移，兼容 SQLite/MySQL/PostgreSQL。

UI 用现有自适应宽屏弹窗，分输入、窗口、模型、输出校准、安全、回测、版本；不保留金融信号字段。

首期交付 Naive/SeasonalNaive、EWMA/Holt-Winters、岭回归/GBDT 的 Go 实现及真实可运行对照；不足周期跳过季节模型并给原因。验证输出时间/单位/版本、无泄漏、未知协变量、缺样、漂移、OOM 截断、超时、未胜出不晋升和 fallback。本轮仅修订研究与技能，不代表插件已实现。
