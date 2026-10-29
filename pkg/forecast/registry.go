package forecast

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// ParamSpec is one platform parameter of an algorithm. The names are the platform's own; each is validated here.
type ParamSpec struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"` // int, float, bool, string
	Required    bool     `json:"required"`
	Default     any      `json:"default,omitempty"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	Description string   `json:"description"`
	// OmitWhenDefault keeps a parameter added after the first release out of the canonical form while it has its
	// default value, so the stored parameters, the model version and every promotion made before it existed stay valid.
	OmitWhenDefault bool `json:"-"`
}

// Definition describes one registered algorithm.
type Definition struct {
	ID          string `json:"id"`
	Category    string `json:"category"` // baseline, statistical, machine_learning
	Version     string `json:"version"`
	ImplVersion string `json:"impl_version"`
	Baseline    bool   `json:"baseline"` // baselines are always eligible and are what others must beat
	// Complexity and MinHistory are plain statements for the algorithm center: the cost of one forecast and the
	// history it needs before it can run at all.
	Complexity string `json:"complexity"`
	MinHistory string `json:"min_history"`
	// Covariates says whether the algorithm can use the other metrics of an input set; one that cannot is refused
	// an input set with covariates rather than silently ignoring them.
	Covariates  bool   `json:"covariates"`
	Description string `json:"description"`
	// Method is the calculation in words and as a formula (see methods.go): what is done with the history to get a forecast.
	Method string      `json:"method,omitempty"`
	Params []ParamSpec `json:"params"`
	// Dynamic marks an implementation whose parameter schema is not fixed: it comes from the setting that uses it (the
	// formula implementation). Its Params here is empty.
	Dynamic bool `json:"dynamic"`
	// Custom marks an algorithm an administrator wrote as a formula; Formula and Recursive show how it computes and
	// MinSamples the history it needs.
	Custom     bool   `json:"custom"`
	Formula    string `json:"formula,omitempty"`
	Recursive  bool   `json:"recursive,omitempty"`
	MinSamples int    `json:"min_samples,omitempty"`
	// Base is the built-in algorithm a variant is derived from: the same code with its own id and its own parameter
	// values. Empty for a built-in algorithm and for a formula algorithm.
	Base string `json:"base,omitempty"`
}

// Builder constructs a model from parameters that were already validated and had their defaults filled in.
type Builder func(p map[string]any) (Model, error)

// Implementation is what a Go algorithm registers: its definition (the parameter schema the pages and the API are built
// from), the builder of its model, and an optional check of parameter combinations that are each valid alone.
// Registering is the one way an algorithm enters the platform: the list, the forms, the validation, the training and
// the scoring all start from the registry, so nothing else names an algorithm.
type Implementation struct {
	Definition
	Build Builder
	// Check rejects a combination of the given (not yet completed) parameters; nil accepts any.
	Check func(raw map[string]any) error
}

var (
	registry []Implementation
	byID     = map[string]int{}
)

// Register adds an algorithm implementation. It panics on a duplicate id, a missing builder or a missing version: those
// are programming errors that must stop the start-up, not surface as a missing algorithm later.
func Register(impl Implementation) {
	if impl.ID == "" || impl.Build == nil && !impl.Dynamic {
		panic("forecast: an implementation needs an id and a builder")
	}
	if _, dup := byID[impl.ID]; dup {
		panic("forecast: algorithm " + impl.ID + " is registered twice")
	}
	if impl.Version == "" {
		impl.Version = "1"
	}
	if impl.ImplVersion == "" {
		impl.ImplVersion = ImplVersion
	}
	if impl.Params == nil {
		impl.Params = []ParamSpec{}
	}
	byID[impl.ID] = len(registry)
	registry = append(registry, impl)
}

func num(v float64) *float64 { return &v }

var builtinDefinitions = []Definition{
	{ID: "naive", Category: "baseline", Baseline: true, Description: "持续值：以最后一个观测值作为所有未来步的预测，所有算法的最低对照。"},
	{ID: "seasonal_naive", Category: "baseline", Baseline: true, Description: "季节持续：重复一个周期之前的值；需要至少一个完整周期的历史。", Params: []ParamSpec{
		{Name: "season_length_steps", Type: "int", Required: true, Min: num(2), Unit: "steps", Description: "周期长度，按采样步数计，不能硬编码 24=一天；一天 = 86400 秒 / 步长秒数。"},
	}},
	{ID: "ewma", Category: "statistical", Description: "指数加权移动平均：水平平滑后平推，低成本统计基线。", Params: []ParamSpec{
		{Name: "alpha", Type: "float", Default: 0.3, Min: num(0.001), Max: num(1), Description: "平滑系数，越大越贴近最新值；取值 (0,1]。与 span 二选一。"},
		{OmitWhenDefault: true, Name: "span", Type: "int", Default: 0, Min: num(0), Max: num(100000), Unit: "steps", Description: "用跨度表示平滑：alpha = 2/(span+1)。0 表示不使用，改用 alpha；两者不能同时给出。"},
		{OmitWhenDefault: true, Name: "bias_correction", Type: "bool", Default: false, Description: "偏差修正：按 (1-alpha)^龄 加权后除以权重和，而不是以第一个观测值起算，短历史上更不受首个值影响。"},
	}},
	{ID: "hist_percentile", Category: "statistical", Description: "衰减直方图百分位（Kubernetes VPA / Google Autopilot 的推荐器）：把历史按指数衰减加权，取加权百分位作为平推预测；不建模未来，对内存与 OOM 保护最稳妥。", Params: []ParamSpec{
		{Name: "percentile", Type: "float", Default: 95.0, Min: num(1), Max: num(100), Description: "取第几百分位，如 95、99；越大越保守。"},
		{Name: "half_life_hours", Type: "float", Default: 24.0, Min: num(0), Max: num(24 * 180), Unit: "hours", Description: "半衰期：这么久之前的样本只算一半权重；0 表示不衰减。CPU 常取 12–24 小时，内存常取 48 小时。"},
		{Name: "safety_margin", Type: "float", Default: 0.0, Min: num(0), Max: num(5), Description: "安全边际：结果再乘 (1+边际)。"},
	}},
	{ID: "random_forest", Category: "machine_learning", Description: "随机森林：多棵在自助样本和随机特征子集上训练的回归树取平均，使用滞后、日历和参与指标的滞后特征，递归多步预测；固定随机种子，结果可复现。树模型无法外推到训练目标范围之外。", Params: []ParamSpec{
		{Name: "lags", Type: "int", Required: true, Min: num(1), Max: num(2000), Unit: "steps", Description: "滞后步数，仅使用目标步之前的值。"},
		{Name: "calendar_features", Type: "bool", Default: false, Description: "是否加入 UTC 日/周周期的正余弦特征。"},
		{Name: "n_estimators", Type: "int", Default: 50, Min: num(1), Max: num(500), Description: "树的数量。"},
		{Name: "max_depth", Type: "int", Default: 6, Min: num(1), Max: num(12), Description: "单棵树最大深度。"},
		{Name: "min_samples_leaf", Type: "int", Default: 5, Min: num(1), Description: "叶子最少样本数。"},
		{Name: "feature_ratio", Type: "float", Default: 0.6, Min: num(0.05), Max: num(1), Description: "每棵树可用于分裂的特征比例。"},
		{Name: "sample_ratio", Type: "float", Default: 0.8, Min: num(0.05), Max: num(1), Description: "每棵树自助采样的样本比例。"},
		{Name: "training_window_steps", Type: "int", Default: 0, Min: num(0), Unit: "steps", Description: "只用最近多少个样本训练；0 = 全部历史。"},
		{Name: "covariate_lags", Type: "int", Default: 3, Min: num(1), Max: num(48), Unit: "steps", Description: "每个参与指标取最近多少步的值作为特征（只用目标步之前的值）；没有其他参与指标时忽略。"},
		{Name: "seed", Type: "int", Default: 42, Min: num(0), Max: num(1000000), Description: "随机种子，固定后同样的历史给出同样的预测。"},
	}},
	{ID: "knn", Category: "machine_learning", ImplVersion: "2", Description: "K 近邻（相似窗口回归）：把最近一段与历史上每段相同长度的窗口比较，取最相似的 K 段之后发生的值加权平均；可按窗口均值做相对化，使低负载时见过的形态仍能匹配。选中的 K 段两两至少相隔一个窗口长度（互斥区），不会把同一次事件错开几步的副本算成 K 次；可选只比较同一时刻（时分）结束的窗口。", Params: []ParamSpec{
		{Name: "window", Type: "int", Default: 48, Min: num(2), Max: num(5000), Unit: "steps", Description: "比较窗口长度。"},
		{Name: "k", Type: "int", Default: 5, Min: num(1), Max: num(200), Description: "取最相似的几段。"},
		{Name: "normalize", Type: "string", Default: "relative", Enum: []string{"relative", "none"}, Description: "relative = 窗口除以自身均值后比较，再缩放回当前水平；none = 直接比较原值。"},
		{OmitWhenDefault: true, Name: "exclusion", Type: "string", Default: "window", Enum: []string{"window", "none"}, Description: "互斥区：window = 选中的 K 段两两至少相隔一个窗口长度，K 段是 K 次不同的事件（所需历史约 K×窗口）；none = 不限制，最像的几段可能只是同一次事件错开几步的副本（版本 2 之前的行为）。"},
		{OmitWhenDefault: true, Name: "same_time_of_day", Type: "bool", Default: false, Description: "只比较与当前窗口在同一时刻（UTC 时分）结束的历史窗口，适合形态属于一天中某个时刻的负载；候选变少，需要更长的历史。"},
		{OmitWhenDefault: true, Name: "time_tolerance_minutes", Type: "int", Default: 60, Min: num(0), Max: num(720), Unit: "minutes", Description: "同一时刻的容差，仅 same_time_of_day=true 时使用；跨零点按环形计算。"},
	}},
	{ID: "holt_winters", Category: "statistical", ImplVersion: "2", Description: "Holt-Winters（ETS 族）：水平、可选阻尼趋势、可选加性或乘性季节；平滑系数为 0 时在小网格内按样本内单步误差自动选择。预测不会低于 0。", Params: []ParamSpec{
		{Name: "season_length_steps", Type: "int", Default: 0, Min: num(0), Unit: "steps", Description: "周期长度（步数）；0 表示不含季节项，启用季节项需要至少两个完整周期。"},
		{Name: "trend", Type: "bool", Default: true, Description: "是否包含趋势项。"},
		{Name: "damped", Type: "bool", Default: false, Description: "趋势是否阻尼，避免长 horizon 无限外推。"},
		{Name: "alpha", Type: "float", Default: 0.0, Min: num(0), Max: num(1), Description: "水平平滑系数；0 = 自动选择。"},
		{Name: "beta", Type: "float", Default: 0.0, Min: num(0), Max: num(1), Description: "趋势平滑系数；0 = 自动选择。"},
		{Name: "gamma", Type: "float", Default: 0.0, Min: num(0), Max: num(1), Description: "季节平滑系数；0 = 自动选择。"},
		{OmitWhenDefault: true, Name: "seasonal_mode", Type: "string", Default: "additive", Enum: []string{"additive", "multiplicative"}, Description: "季节形式：additive = 每个季节位置加一个固定量；multiplicative = 按比例放大缩小（负载越高日内波动越大的资源用它）。乘性要求历史严格大于 0，触到 0 的序列会被拒绝，请改用加性。"},
		{OmitWhenDefault: true, Name: "damping_phi", Type: "float", Default: 0.98, Min: num(0.8), Max: num(1), Description: "趋势阻尼系数，仅 damped=true 时使用；越小趋势衰减越快，1 = 不阻尼。"},
		{OmitWhenDefault: true, Name: "non_negative", Type: "bool", Default: true, Description: "预测是否下限取 0：资源量不会为负，加性趋势可能外推到 0 以下。"},
	}},
	{ID: "model_service", Category: "model_service", Description: "模型服务：把历史数据通过 HTTP 发给外部的时序基础模型（如 Chronos-2、TimesFM）取得预测，模型本身不在平台里运行。协议 finetune 的服务还能按这里定义的微调参数，定期提交这条序列的数据去微调，之后的预测用微调后的模型；unitshub_* 是只读的现成容器，只能零样本预测。服务不可用或超时时这一次预测失败，其他算法照常运行。回测和重放评分用预训练模型（不会用到在回测截止点之后的数据上微调出来的模型），微调后的效果要看预测的实际回填。", Params: []ParamSpec{
		{Name: "endpoint", Type: "string", Required: true, Description: "模型服务的地址，如 http://chronos:8000，不带结尾斜杠。"},
		{Name: "protocol", Type: "string", Default: "finetune", Enum: []string{"finetune", "unitshub_chronos", "unitshub_timesfm"}, Description: "服务的接口协议：finetune = scripts/tsfm-eval/finetune 的服务（支持微调）；unitshub_chronos、unitshub_timesfm = kingfs/unitshub 的现成容器（只预测）。"},
		{Name: "api_key_env", Type: "string", Default: "", Description: "保存访问密钥的环境变量名（平台进程的环境变量），预测时作为 Bearer 密钥发给服务；留空表示服务不需要密钥。密钥本身不存进算法参数。"},
		{Name: "timeout_seconds", Type: "int", Default: 30, Min: num(1), Max: num(600), Unit: "s", Description: "一次预测请求的超时时间。"},
		{Name: "service_quantile", Type: "float", Default: 0.0, Min: num(0), Max: num(0.99), Description: "0 = 用服务给的均值预测；0.5～0.99 = 用服务给的这个分位数（如 0.9 = 实际值不超过这条线）。分位数预测按 pinball 损失和覆盖率回测。"},
		{Name: "finetune_interval_hours", Type: "int", Default: 0, Min: num(0), Max: num(720), Unit: "h", Description: "微调间隔：服务上微调后的模型超过这么久，平台就重新提交数据微调一次；0 = 不微调，一直用预训练模型。只有 finetune 协议的服务支持。微调在后台进行，期间预测仍用旧的微调模型（没有就用预训练模型）。"},
		{Name: "finetune_mode", Type: "string", Default: "full", Enum: []string{"full", "lora"}, Description: "微调方式：full = 全参数微调；lora = 低秩适配（服务端要装 peft，没装会让微调失败）。"},
		{Name: "finetune_steps", Type: "int", Default: 30, Min: num(1), Max: num(2000), Unit: "steps", Description: "微调的训练步数。步数越多越贴近这条序列，也越慢（CPU 上约 1 秒一步）、越容易过拟合。"},
		{Name: "finetune_learning_rate", Type: "float", Default: 0.00001, Min: num(0.00000001), Max: num(0.01), Description: "微调的学习率。全参数微调用很小的值（1e-5 量级）。"},
		{Name: "finetune_batch_size", Type: "int", Default: 16, Min: num(1), Max: num(256), Description: "微调每步用的窗口数。"},
		{Name: "finetune_context_length", Type: "int", Default: 256, Min: num(16), Max: num(8192), Unit: "steps", Description: "微调时每个训练窗口读取的最近步数。"},
		{Name: "finetune_min_points", Type: "int", Default: 200, Min: num(60), Max: num(100000), Unit: "points", Description: "序列至少有这么多点才提交微调；更短的序列微调只会学到噪声，继续用预训练模型。"},
	}},
	{ID: "theta", Category: "statistical", Description: "Theta 方法（M3 竞赛冠军）：指数平滑的水平加上线性趋势斜率的一半作为漂移，对短而有噪声的序列很难被打败。有季节时先做经典季节分解。预测不会低于 0。", Params: []ParamSpec{
		{Name: "season_length_steps", Type: "int", Default: 0, Min: num(0), Unit: "steps", Description: "周期长度（步数）；0 表示不含季节项，启用需要至少两个完整周期。历史全为正时按比例分解，否则按加性分解。"},
		{Name: "alpha", Type: "float", Default: 0.0, Min: num(0), Max: num(1), Description: "水平平滑系数；0 = 按样本内单步误差自动选择。"},
	}},
	{ID: "mstl", Category: "statistical", Description: "多重季节分解（MSTL）：把序列拆成趋势和日、周两个季节分量，去掉季节后的序列用 Theta 预测，再把季节分量加回。适合同时有日周期和周周期的负载。与论文的区别：每个季节分量是固定形状（各位置在所有周期上的均值，反复三轮使分量互相一致），不是随时间缓慢变化的 LOESS 形状。预测不会低于 0。", Params: []ParamSpec{
		{Name: "daily_season_steps", Type: "int", Required: true, Min: num(2), Unit: "steps", Description: "一天的步数 = 86400 / 步长秒数（1 分钟一步是 1440，5 分钟一步是 288）。"},
		{Name: "weekly_season_steps", Type: "int", Default: 0, Min: num(0), Unit: "steps", Description: "一周的步数 = 7 × 一天的步数；0 = 只做日季节。必须大于一天的步数。启用周季节至少需要两周历史。"},
	}},
	{ID: "seasonal_profile", Category: "statistical", Description: "周期画像分位数：把历史按「周几 + 几点」分桶，每个桶取带时间衰减的百分位（P90、P95）作为这个时刻的预测，是 VPA / Autopilot 百分位推荐器加上时钟。每天九点的高峰就预测成九点的高峰，周日夜里就预测成安静的夜里；一次异常的一天只是许多天里的一票，不像 seasonal_naive 那样被原样复制。某个桶样本太少时退到「每天同一时刻」，再不够退到整段历史的百分位。", Params: []ParamSpec{
		{Name: "percentile", Type: "float", Default: 90.0, Min: num(1), Max: num(100), Description: "取第几百分位，如 90、95；越大越保守。"},
		{Name: "bucket_minutes", Type: "int", Default: 60, Min: num(1), Max: num(720), Unit: "minutes", Description: "时间桶的长度（分钟），必须能整除一天（1440）：60 = 每小时一个桶，15 = 每 15 分钟一个桶。"},
		{Name: "by_weekday", Type: "bool", Default: true, Description: "是否区分周几：true = 周一九点和周六九点是两个桶（至少需要两周历史才有各自的数据，不够时退到每天同一时刻）；false = 只看一天中的时刻。"},
		{Name: "half_life_days", Type: "float", Default: 14.0, Min: num(0), Max: num(365), Unit: "days", Description: "半衰期：这么多天之前的样本只算一半权重；0 表示不衰减。"},
		{Name: "min_samples_per_bucket", Type: "int", Default: 6, Min: num(1), Max: num(100000), Description: "一个桶至少有多少个样本才被信任；不够就退到上一级（每天同一时刻，再退到整段历史）。"},
		{Name: "utc_offset_minutes", Type: "int", Default: 480, Min: num(-720), Max: num(840), Unit: "minutes", Description: "时区偏移（相对 UTC，分钟），决定「几点」「周几」按哪个时区的钟面分桶。默认 480（UTC+8，平台的时区）：这样「九点」是北京时间九点，周一从北京时间零点开始；部署在别的时区就改成那里的偏移。"},
		{Name: "safety_margin", Type: "float", Default: 0.0, Min: num(0), Max: num(5), Description: "安全边际：结果再乘 (1+边际)。"},
	}},
	{ID: "peak_envelope", Category: "statistical", Description: "峰值包络趋势：先取滚动最大值作包络线，再用 Theil-Sen（所有点对斜率的中位数）稳健地拟合直线并外推，预测的是峰值而不是均值。适合内存工作集这种「锯齿叠加缓慢上涨」（泄漏、缓存增长）的负载，均值类算法会低估它。预测不会低于当前的包络，也不会为负；还能算出包络「预计多久触顶」。", Params: []ParamSpec{
		{Name: "envelope_window_steps", Type: "int", Default: 30, Min: num(2), Max: num(100000), Unit: "steps", Description: "滚动最大值的窗口（步数）：要大于一个锯齿的周期（一次 GC 或缓存淘汰的间隔），否则包络还是锯齿。"},
		{Name: "fit_steps", Type: "int", Default: 0, Min: num(0), Unit: "steps", Description: "只拟合包络最近的这么多步；0 = 全部。负载的增长速度变过时用它只看最近的趋势。"},
		{Name: "allow_decline", Type: "bool", Default: false, Description: "是否允许预测下降：false（默认）= 下降的斜率按平处理，预测不低于当前包络，用于防 OOM；true = 照直线走，也可能低于当前峰值。"},
		{Name: "safety_margin", Type: "float", Default: 0.0, Min: num(0), Max: num(5), Description: "安全边际：结果再乘 (1+边际)。"},
	}},
	{ID: "combination", Category: "statistical", ImplVersion: "2", Description: "组合预测：把几个算法的预测合在一起，取每一步的中位数，或按各成员最近误差的倒数加权平均。不同类型的模型组合起来在预测竞赛（M4）里很难被单个算法打败，一个成员错在某个方向会被其他成员抵消，而且不需要训练。成员用各自的默认参数运行，所以只能选不需要必填参数的算法；它和其他算法一样被回测、评分和晋升，不是默认比它们好。", Params: []ParamSpec{
		{Name: "members", Type: "string", Default: defaultCombinationMembers, Description: "成员算法的 id，用逗号分隔，2 到 6 个，如 ewma,holt_winters,theta。只能选不需要必填参数的算法（ewma、holt_winters、theta、hist_percentile、knn、naive 等），不能选 combination 自己。成员按各自的默认参数运行，不读取算法管理页里给它们设置的参数。"},
		{Name: "mode", Type: "string", Default: "inverse_error", Enum: []string{"inverse_error", "median"}, Description: "inverse_error = 按每个成员在这个负载最近几个窗口里的平均绝对误差的倒数加权；median = 每一步取成员预测的中位数。"},
		{Name: "error_windows", Type: "int", Default: 4, Min: num(1), Max: num(20), Description: "测各成员误差用最近多少个预测视野；越多越稳，也越慢。仅 mode=inverse_error 时使用，历史不够时按等权。"},
		{OmitWhenDefault: true, Name: "under_weight", Type: "float", Default: 2.0, Min: num(1), Max: num(5), Description: "量成员误差时，预测低于实际的部分算几倍（与评分里“少给比多给代价更大”的倍数一致）。1 = 普通平均绝对误差；默认 2，使组合偏向少给得少的成员。仅 mode=inverse_error 时使用。"},
		{OmitWhenDefault: true, Name: "sharpness", Type: "float", Default: 2.0, Min: num(1), Max: num(6), Description: "权重 = (1/误差) 的这个次方。1 = 与误差成反比（最差的成员仍占相当的权重）；越大越偏向最近最好的成员，6 接近只选最好的。默认 2。仅 mode=inverse_error 时使用。"},
		{OmitWhenDefault: true, Name: "recent_steps", Type: "int", Default: 3, Min: num(0), Max: num(20), Description: "除了最近几个预测视野，再把最新的这几步也算进成员的误差：刚刚跟上一次变化的成员比一小时前才对的成员更值得信。0 = 不算。默认 3。仅 mode=inverse_error 时使用。"},
		{OmitWhenDefault: true, Name: "trim_ratio", Type: "float", Default: 3.0, Min: num(0), Max: num(20), Description: "误差超过最好成员这个倍数的成员不参与（权重为 0）。0 = 不剔除；默认 3。仅 mode=inverse_error 时使用，历史不够时所有成员按等权。"},
	}},
	{ID: "ridge_ar", Category: "machine_learning", Description: "岭回归自回归：滞后值与可选日历特征，递归多步预测，特征与目标标准化。", Params: []ParamSpec{
		{Name: "lags", Type: "int", Required: true, Min: num(1), Max: num(2000), Unit: "steps", Description: "滞后步数，仅使用目标步之前的值。"},
		{Name: "calendar_features", Type: "bool", Default: false, Description: "是否加入 UTC 日/周周期的正余弦特征（时间戳已知，不泄漏未来）。"},
		{Name: "l2_lambda", Type: "float", Default: 0.01, Min: num(0), Description: "L2 正则强度，相对样本数缩放。"},
		{Name: "training_window_steps", Type: "int", Default: 0, Min: num(0), Unit: "steps", Description: "只用最近多少个样本训练；0 = 全部历史。"},
		{OmitWhenDefault: true, Name: "covariate_lags", Type: "int", Default: 3, Min: num(1), Max: num(48), Unit: "steps", Description: "输入指标集里每个协变量取最近多少步的值作为特征（仅用目标步之前的值；未来步沿用最后观测值）。没有协变量时忽略。"},
		{OmitWhenDefault: true, Name: "multi_step", Type: "string", Default: "recursive", Enum: []string{"recursive", "direct"}, Description: "多步预测方式：recursive = 一个模型逐步往前滚，把上一步的预测当下一步的输入（误差会累积，视野越长越向均值收敛）；direct = 每个锚点步长各训练一个模型，直接预测那一步，只用已观测的值，不累积误差，但训练成本约为锚点数倍。"},
		{OmitWhenDefault: true, Name: "direct_anchors", Type: "int", Default: 8, Min: num(2), Max: num(60), Description: "直接多步时有多少个步长拥有自己的模型（从第 1 步到最后一步均匀取点，中间的步长线性插值）；视野不超过它时每一步都有自己的模型。仅 multi_step=direct 时使用。"},
	}},
	{ID: "gbdt", Category: "machine_learning", Description: "梯度提升回归树：滞后与日历特征，直方图分裂，递归多步预测；支持平方误差或分位数（pinball）目标。树模型无法外推到训练目标范围之外。", Params: []ParamSpec{
		{Name: "lags", Type: "int", Required: true, Min: num(1), Max: num(2000), Unit: "steps", Description: "滞后步数，仅使用目标步之前的值。"},
		{Name: "calendar_features", Type: "bool", Default: false, Description: "是否加入 UTC 日/周周期的正余弦特征。"},
		{Name: "n_estimators", Type: "int", Default: 100, Min: num(1), Max: num(1000), Description: "树的数量。"},
		{Name: "learning_rate", Type: "float", Default: 0.1, Min: num(0.001), Max: num(1), Description: "每棵树的收缩系数。"},
		{Name: "max_depth", Type: "int", Default: 3, Min: num(1), Max: num(8), Description: "单棵树最大深度。"},
		{Name: "min_samples_leaf", Type: "int", Default: 5, Min: num(1), Description: "叶子最少样本数。"},
		{Name: "objective", Type: "string", Default: "squared", Enum: []string{"squared", "quantile"}, Description: "squared = 平方误差点预测；quantile = alpha 分位数，用于峰值/容量保护，需单独验证覆盖率。"},
		{Name: "alpha", Type: "float", Default: 0.9, Min: num(0.01), Max: num(0.99), Description: "分位数目标的分位点，仅 objective=quantile 时使用。"},
		{Name: "training_window_steps", Type: "int", Default: 5000, Min: num(0), Unit: "steps", Description: "只用最近多少个样本训练，限制训练耗时；0 = 全部历史。"},
		{OmitWhenDefault: true, Name: "covariate_lags", Type: "int", Default: 3, Min: num(1), Max: num(48), Unit: "steps", Description: "输入指标集里每个协变量取最近多少步的值作为特征（仅用目标步之前的值；未来步沿用最后观测值）。没有协变量时忽略。"},
		{OmitWhenDefault: true, Name: "multi_step", Type: "string", Default: "recursive", Enum: []string{"recursive", "direct"}, Description: "多步预测方式：recursive = 一个模型逐步往前滚，把上一步的预测当下一步的输入（误差会累积，视野越长越向均值收敛）；direct = 每个锚点步长各训练一个模型，直接预测那一步，只用已观测的值，不累积误差，但训练成本约为锚点数倍。"},
		{OmitWhenDefault: true, Name: "direct_anchors", Type: "int", Default: 8, Min: num(2), Max: num(60), Description: "直接多步时有多少个步长拥有自己的模型（从第 1 步到最后一步均匀取点，中间的步长线性插值）；视野不超过它时每一步都有自己的模型。仅 multi_step=direct 时使用。"},
	}},
}

// notes holds the cost and data requirements shown in the algorithm center, by algorithm id.
var notes = map[string][2]string{
	"naive":            {"O(h) per forecast, no training", "1 observation"},
	"seasonal_naive":   {"O(h) per forecast, no training", "season_length_steps observations (one full season)"},
	"ewma":             {"O(n) per forecast, no training", "2 observations"},
	"holt_winters":     {"O(n) per smoothing pass, a small grid search when coefficients are automatic", "2 x season_length_steps with a seasonal term, else 4 observations"},
	"model_service":    {"one HTTP request per forecast (about 0.1 s on CPU for Chronos-2); a fine-tune is a background request of tens of seconds", "30 observations (the service has its own limit)"},
	"theta":            {"O(n) per forecast (a small grid for the smoothing coefficient), no training", "2 x season_length_steps with a seasonal term, else 4 observations"},
	"mstl":             {"O(n x periods x 3 passes) per forecast, no training", "2 x the longest season: 2 x daily_season_steps, or 2 x weekly_season_steps when a weekly season is set"},
	"seasonal_profile": {"O(n + horizon) per forecast, no training", "4 observations; with by_weekday two weeks of history for the weekday buckets, otherwise it falls back to the time of day and then to the whole history"},
	"peak_envelope":    {"O(n + points^2) per forecast with at most 300 envelope points, no training", "envelope_window_steps + 10 observations"},
	"combination":      {"the members' forecasts plus error_windows more forecasts per member, no training", "the shortest of the members; a member that needs more is left out"},
	"ridge_ar":         {"O(n*lags^2) to fit, O(h*lags) to forecast; retrained on every run", "lags + 2 x feature width + 1 observations (feature width = lags, plus 4 with calendar features)"},
	"gbdt":             {"O(trees*n*lags*bins) to fit, retrained on every run; bounded by training_window_steps", "lags + 4 x min_samples_leaf + 1 observations"},
	"hist_percentile":  {"O(n log n) per forecast, no training", "4 observations"},
	"random_forest":    {"O(trees*n*lags*bins) to fit, retrained on every run; bounded by training_window_steps", "lags + 4 x min_samples_leaf + 1 observations"},
	"knn":              {"O(n*window) per forecast, no training", "k x window + 1 observations with the exclusion zone (window + k + 1 without it), and k separate earlier windows followed by the horizon"},
}

// covariateAlgorithms are the algorithms that read the covariates of an input set.
var covariateAlgorithms = map[string]bool{"ridge_ar": true, "gbdt": true, "random_forest": true}

func init() {
	for i := range builtinDefinitions {
		d := builtinDefinitions[i]
		d.Covariates = covariateAlgorithms[d.ID]
		if n, ok := notes[d.ID]; ok {
			d.Complexity, d.MinHistory = n[0], n[1]
		}
		build, ok := builtinBuilders[d.ID]
		if !ok {
			panic("forecast: algorithm " + d.ID + " has no builder")
		}
		check := builtinChecks[d.ID]
		d.Method = methodOf(d.ID)
		if !d.Baseline {
			// every forecasting algorithm can be asked for a quantile instead of the middle; a baseline stays what it is
			d.Params = append(append(append([]ParamSpec(nil), d.Params...), regimeParams()...), conformalParams()...)
			build, check = withConformal(withRegime(build)), withQuantileCheck(check)
		}
		Register(Implementation{Definition: d, Build: build, Check: check})
	}
	registerFormula()
}

// Definitions returns the registered algorithms in a stable order: the built-in ones, then the custom ones by id.
func Definitions() []Definition {
	out := make([]Definition, 0, len(registry))
	for _, impl := range registry {
		out = append(out, impl.Definition)
	}
	return append(out, customDefinitions()...)
}

// find returns the implementation of a built-in or custom algorithm.
func find(id string) (Implementation, bool) {
	if i, ok := byID[id]; ok {
		return registry[i], true
	}
	return findCustom(id)
}

// Lookup returns the definition of an algorithm.
func Lookup(id string) (Definition, bool) {
	impl, ok := find(id)
	return impl.Definition, ok
}

// Normalize validates raw parameters against the definition of a registered algorithm and returns them with defaults
// filled in. Unknown names, wrong types, out-of-range values and missing required values are errors.
func Normalize(id string, raw map[string]any) (map[string]any, error) {
	impl, ok := find(id)
	if !ok {
		return nil, fmt.Errorf("unknown algorithm %q", id)
	}
	if impl.Dynamic {
		return nil, fmt.Errorf("algorithm %q takes its parameter schema from the setting that uses it", id)
	}
	if impl.Check != nil {
		if err := impl.Check(raw); err != nil {
			return nil, err
		}
	}
	return NormalizeSpecs(impl.Params, raw, id)
}

// NormalizeSpecs validates raw parameters against a parameter schema and returns them with the defaults filled in.
// It is what Normalize does for a registered algorithm and what a formula algorithm uses with the schema its author
// gave. Unknown names, wrong types, out-of-range values and missing required values are errors.
func NormalizeSpecs(specs []ParamSpec, raw map[string]any, label string) (map[string]any, error) {
	known := map[string]ParamSpec{}
	for _, p := range specs {
		known[p.Name] = p
	}
	for name := range raw {
		if _, ok := known[name]; !ok {
			return nil, fmt.Errorf("unknown parameter %q for %s", name, label)
		}
	}
	out := map[string]any{}
	for _, spec := range specs {
		v, given := raw[spec.Name]
		if !given || v == nil {
			if spec.Required {
				return nil, fmt.Errorf("parameter %q is required", spec.Name)
			}
			out[spec.Name] = spec.Default
			continue
		}
		checked, err := checkParam(spec, v)
		if err != nil {
			return nil, err
		}
		out[spec.Name] = checked
	}
	return out, nil
}

// ValidateSpecs checks a parameter schema an author wrote: names, types, defaults and ranges have to be coherent, so
// that a setting made from it can never fail on the schema itself.
func ValidateSpecs(specs []ParamSpec) error {
	seen := map[string]bool{}
	for _, p := range specs {
		if !paramName.MatchString(p.Name) {
			return fmt.Errorf("parameter name %q must be a lowercase identifier (letters, digits and underscore, starting with a letter)", p.Name)
		}
		if seen[p.Name] {
			return fmt.Errorf("parameter %q is declared twice", p.Name)
		}
		seen[p.Name] = true
		switch p.Type {
		case "int", "float", "bool", "string":
		default:
			return fmt.Errorf("parameter %q has type %q: it must be int, float, bool or string", p.Name, p.Type)
		}
		if p.Type == "string" && len(p.Enum) == 0 {
			return fmt.Errorf("parameter %q is a string and needs its allowed values (enum)", p.Name)
		}
		if p.Min != nil && p.Max != nil && *p.Min > *p.Max {
			return fmt.Errorf("parameter %q has a minimum above its maximum", p.Name)
		}
		if p.Default == nil && !p.Required {
			return fmt.Errorf("parameter %q needs a default value or must be required", p.Name)
		}
		if p.Default != nil {
			if _, err := checkParam(p, p.Default); err != nil {
				return fmt.Errorf("the default of %w", err)
			}
		}
		if strings.TrimSpace(p.Description) == "" {
			return fmt.Errorf("parameter %q needs a description: it is what the help mark of the form shows", p.Name)
		}
	}
	return nil
}

var paramName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

func checkParam(spec ParamSpec, v any) (any, error) {
	switch spec.Type {
	case "bool":
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("parameter %q must be a boolean", spec.Name)
		}
		return b, nil
	case "string":
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("parameter %q must be a string", spec.Name)
		}
		if len(spec.Enum) > 0 {
			found := false
			for _, e := range spec.Enum {
				found = found || e == s
			}
			if !found {
				return nil, fmt.Errorf("parameter %q must be one of %v", spec.Name, spec.Enum)
			}
		}
		return s, nil
	case "int", "float":
		var f float64
		switch n := v.(type) {
		case float64:
			f = n
		case float32:
			f = float64(n)
		case int:
			f = float64(n)
		case int64:
			f = float64(n)
		default:
			return nil, fmt.Errorf("parameter %q must be a number", spec.Name)
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("parameter %q must be finite", spec.Name)
		}
		if spec.Type == "int" {
			if f != math.Trunc(f) {
				return nil, fmt.Errorf("parameter %q must be an integer", spec.Name)
			}
		}
		if spec.Min != nil && f < *spec.Min {
			return nil, fmt.Errorf("parameter %q must be at least %v", spec.Name, *spec.Min)
		}
		if spec.Max != nil && f > *spec.Max {
			return nil, fmt.Errorf("parameter %q must be at most %v", spec.Name, *spec.Max)
		}
		if spec.Type == "int" {
			return int(f), nil
		}
		return f, nil
	}
	return nil, fmt.Errorf("parameter %q has unsupported type %q", spec.Name, spec.Type)
}

// Build validates the parameters and returns a configured model of a registered algorithm.
func Build(id string, raw map[string]any) (Model, error) {
	p, err := Normalize(id, raw)
	if err != nil {
		return nil, err
	}
	impl, _ := find(id)
	return impl.Build(p)
}

// IDs returns the ids of the built-in algorithms that are algorithms on their own, sorted (the system creates a
// setting for each of them; custom algorithms are created by an administrator). A dynamic implementation
// (formula) is not one: each custom algorithm built on it has its own id and its own definition.
func IDs() []string {
	ids := make([]string, 0, len(registry))
	for _, d := range registry {
		if !d.Dynamic {
			ids = append(ids, d.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// Canonical returns the parameters of a registered algorithm in the form that is stored and hashed: the normalized
// values without the parameters that were added later and still have their default (see ParamSpec.OmitWhenDefault).
func Canonical(id string, normalized map[string]any) map[string]any {
	def, ok := Lookup(id)
	if !ok {
		return normalized
	}
	return CanonicalSpecs(def.Params, normalized)
}

// CanonicalSpecs is Canonical for an explicit parameter schema.
func CanonicalSpecs(specs []ParamSpec, normalized map[string]any) map[string]any {
	out := make(map[string]any, len(normalized))
	for k, v := range normalized {
		out[k] = v
	}
	for _, spec := range specs {
		if spec.OmitWhenDefault && fmt.Sprint(out[spec.Name]) == fmt.Sprint(spec.Default) {
			delete(out, spec.Name)
		}
	}
	return out
}
