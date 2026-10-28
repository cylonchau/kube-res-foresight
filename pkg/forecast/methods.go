package forecast

// builtinMethods is the calculation of each built-in algorithm in words and, where there is one, as a formula: what it does with the
// history to get a forecast, and what that is good for. The algorithm page shows it, so a forecast can be checked by hand: an algorithm
// that only shows a forecast time and a number is a black box.
var builtinMethods = map[string]string{
	"naive": "计算过程：取历史里最后一个观测值 y(T)，把它作为之后每一步的预测：ŷ(T+h) = y(T)，h = 1…H。没有参数，没有训练，每个预测点相同。\n" +
		"意义：这是所有算法的最低对照（基线）。一个算法的误差如果不比它小，就没有存在的价值，晋升规则也要求候选算法在同样的起点和窗口上胜过它。负载短期变化平缓时它很难被打败；负载有明显的日周期或突然的变化时，它会一直停在最后一个值上，预测点越远越不准。\n" +
		"依据：只用最后一个观测值，所以预测只依赖「最近一次采样」；历史再长也不影响结果，历史不足一个样本时不能运行。",
	"seasonal_naive": "计算过程：重复一个周期之前的值：ŷ(T+h) = y(T+h−m)，m 为季节周期（例如 1 天对应的步数）。需要至少一个完整周期的历史。\n" +
		"意义：负载有明显的日周期或周周期时的基线，比 naive 更贴近形状，但不会跟随水平的变化。",
	"ewma": "计算过程：指数加权移动平均，水平 ℓ(t) = α·y(t) + (1−α)·ℓ(t−1)，预测 ŷ(T+h) = ℓ(T)；α 越大越贴近最近的观测。\n" +
		"意义：低成本的统计基线，能滤掉短期噪声，但不建模趋势和季节。",
	"hist_percentile": "计算过程：把历史按时间指数衰减加权（越近权重越大），取加权的第 p 百分位作为所有未来步的预测：ŷ(T+h) = Q_p(加权历史)。\n" +
		"意义：Kubernetes VPA 与 Google Autopilot 推荐器的做法，不预测变化，只给出「历史上多大比例的时间够用」的水平，用来给内存和 OOM 保护兜底最稳妥。",
	"holt_winters": "计算过程：水平 ℓ、趋势 b（可选阻尼 φ）、季节 s 三个状态逐点更新。加性：ŷ(T+h) = ℓ(T) + Σφⁱ·b(T) + s(T+h−m)；乘性：(ℓ(T) + Σφⁱ·b(T)) × s(T+h−m)。平滑系数为 0 时在小网格里按样本内单步误差自动选择；预测不低于 0。\n" +
		"意义：水平、趋势、季节都有时的稳定选择，速度快，可解释。",
	"theta": "计算过程：序列的指数平滑水平 ℓ(T) 加上线性趋势斜率 b 的一半作为每步的漂移：ŷ(T+h) = ℓ(T) + (b/2)·h；有季节时先做经典季节分解，预测后再还原季节。预测不低于 0。\n" +
		"意义：M3 竞赛冠军方法，对短而有噪声的序列很难被打败。",
	"seasonal_profile": "计算过程：把每个历史样本放进「周几 + 一天中的时刻」的桶（桶长 bucket_minutes，时间是 UTC 加 utc_offset_minutes），样本按时间衰减加权（半衰期 half_life_days）；预测 T+h 时，取它所在桶的加权百分位：ŷ(T+h) = Q_p(桶(T+h))，再乘 (1+边际)。桶样本少于 min_samples_per_bucket 时退到「每天同一时刻」的桶，再不够退到整段历史的加权百分位。\n" +
		"意义：VPA / Autopilot 的百分位推荐器加上时钟。hist_percentile 是一条平线，不知道九点会有高峰；seasonal_naive 只抄一个周期之前的那个值，一次异常的一天会被原样复制。这里每一天都参与，异常的一天只是一票，百分位（90、95）让线保持在通常的峰值之上。\n" +
		"依据：区分周几需要至少两周历史，历史短时它退化成 hist_percentile；不预测趋势，水平整体上移要靠半衰期慢慢跟上（遇到发布造成的水平突变可以打开 reset_on_change）。",
	"peak_envelope": "计算过程：包络 e(t) = 最近 envelope_window_steps 步（只用 t 之前）的最大值；对包络用 Theil-Sen 拟合直线：斜率 = 所有点对斜率的中位数，截距 = e(t) − 斜率·t 的中位数（最多取 300 个点）；预测 ŷ(T+h) = 截距 + 斜率·(T+h)。默认下降的斜率按平处理，预测不低于当前包络（allow_decline=false），不低于 0，再乘 (1+边际)。\n" +
		"意义：内存工作集是锯齿（涨了又被回收）叠加缓慢上涨（泄漏、缓存增长）。均值类算法预测的是锯齿中间，会低估下一个峰值；这里预测的就是峰值线，并且可以算「预计多久触顶」（TimeToLimit：直线到达给定上限的时间）。\n" +
		"依据：Theil-Sen 对少数异常峰和重启后的塌陷不敏感（最小二乘直线会被拉偏）；窗口要大于一个锯齿的周期，否则包络还是锯齿；它预测峰值，所以不适合拿去和均值比准确度。「预计多久触顶」目前只是算法包里的函数，还没有显示在页面或 OOM 说明里。",
	"combination": "计算过程：成员（members，用各自的默认参数）各自预测；mode=median 时每一步取成员预测的中位数；mode=inverse_error 时，用最近 error_windows 个预测视野量出每个成员的平均绝对误差 e_k，权重 w_k = 1/(e_k + ε)，预测 = Σ w_k·ŷ_k ÷ Σ w_k。历史不够量误差时按等权；历史不够某个成员时该成员被略去。\n" +
		"意义：不同类型的模型组合在预测竞赛（M4）里很难被单个算法打败：一个成员在某个方向错了，会被其他成员抵消。不需要训练。\n" +
		"依据：权重只来自这份历史自己的滚动误差（不用到预测起点之后的值）；它不读取平台的晋升状态，也不读取算法管理页给成员设置的参数，而是和其他算法一样被回测、评分、晋升——它是不是更好由回测说了算。成员只能选不需要必填参数的算法。",
	"mstl": "计算过程：把序列拆成趋势与日、周两个季节分量：y(t) = 趋势 + S_日(t mod 日) + S_周(t mod 周) + 余项。每个季节分量取其位置在所有周期上的均值（先扣掉趋势和另一个季节分量，反复三轮使两个分量互相一致，再减去均值使其和为 0）；去掉季节后的序列用 Theta 预测，再加回季节分量：ŷ(T+h) = Θ(y − S_日 − S_周)(h) + S_日((T+h) mod 日) + S_周((T+h) mod 周)。预测不低于 0。\n" +
		"意义：负载同时有日周期和周周期（工作日与周末不同）时，只看日周期的算法（seasonal_naive 以一天为周期、Holt-Winters）会在跨周末时出错，它能同时还原两者。\n" +
		"依据：季节分量是固定形状，不随时间缓慢变化（论文用 LOESS，这里没有）；历史至少要两个完整的最长周期，启用周季节需要两周数据。",
}

// methodOf is the calculation text of a built-in algorithm; an algorithm with none is described by its Description.
func methodOf(id string) string { return builtinMethods[id] }
