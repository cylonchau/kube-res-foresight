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
	"macd_trend": "计算过程：快、慢两条 EMA 之差给出趋势斜率，信号线 EMA 确认动量，在快 EMA 水平上外推并限幅：ŷ(T+h) = EMA_fast(T) + clamp(slope)·h。只作资源趋势修正。\n" +
		"意义：在短期趋势明显时对 ewma 做有上限的修正，风险受控。",
}

// methodOf is the calculation text of a built-in algorithm; an algorithm with none is described by its Description.
func methodOf(id string) string { return builtinMethods[id] }
