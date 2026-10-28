package forecast

import "fmt"

// withConformal wraps a builder so that a positive quantile_level turns the point forecast of the model into a forecast of
// that quantile (see Conformal). At the default 0 the model is returned unchanged.
func withConformal(build Builder) Builder {
	return func(p map[string]any) (Model, error) {
		m, err := build(p)
		if err != nil {
			return nil, err
		}
		if level := pFloat(p, "quantile_level"); level > 0 {
			return Conformal{Base: m, Level: level, Windows: pInt(p, "calibration_windows")}, nil
		}
		return m, nil
	}
}

// conformalParams are added to every algorithm that is not a baseline. They stay out of the stored parameters while they
// have their defaults, so nothing that exists changes version.
func conformalParams() []ParamSpec {
	return []ParamSpec{
		{OmitWhenDefault: true, Name: "quantile_level", Type: "float", Default: 0.0, Min: num(0), Max: num(0.99),
			Description: "推荐分位数：0 = 点预测（现状）；0.9 等 = 预测「实际值有约 90% 的把握不超过」的线（保形预测：用本算法在这个负载最近几个窗口里的真实误差校准，需要更多历史和时间）。必须大于 0.5。"},
		{OmitWhenDefault: true, Name: "calibration_windows", Type: "int", Default: DefaultCalibrationWindows, Min: num(3), Max: num(50),
			Description: "校准窗口数：用最近多少个历史窗口测本算法的误差；越多越稳，也越慢（每个窗口多跑一次预测）。仅推荐分位数大于 0 时使用。"},
	}
}

// withQuantileCheck adds the quantile_level rule to an algorithm's own parameter check: the level is 0 (off) or above 0.5, so
// the line is an upper bound; a level that is valid for the range but means nothing is refused.
func withQuantileCheck(inner func(raw map[string]any) error) func(raw map[string]any) error {
	return func(raw map[string]any) error {
		if level, ok := rawNum(raw, "quantile_level"); ok && level != 0 && level <= 0.5 {
			return fmt.Errorf("quantile_level must be 0 (the point forecast) or above 0.5, got %v", level)
		}
		if inner != nil {
			return inner(raw)
		}
		return nil
	}
}
