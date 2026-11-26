package timing

// PredictionThinAfter is how old a stored forecast must be before the forecasts a newer run has replaced are deleted. Runs overlap: a forecast every
// five minutes of a fifteen minute horizon puts three forecasts on every target time, and every read (the history chart, the delay and tracking
// reports, the metric queries) takes only the newest forecast made at or before the target. The rows the newer ones replaced are read by nothing
// but the evaluation, which scores a whole run once its targets have passed, and that runs every hour.
var PredictionThinAfter = Param{Key: "forecast.prediction_thin_after_hours", Default: 24, Min: 1, Max: 720,
	Name:   "Thin Replaced Forecasts After (hours)",
	Remark: "Forecast runs overlap (a run every 5 minutes of a 15 minute horizon puts three forecasts on every target time) and only the newest forecast made at or before a target is ever read. The hourly clean-up deletes the older ones, from forecasts at least this old, so the biggest table keeps a fraction of its rows (about one in horizon / run interval). Keep it well above the hours the evaluation needs to score a run: a run that is thinned before it was evaluated is scored on what is left (1-720)."}

// PredictionRetentionDays is how long a stored forecast is kept. The forecast table is the biggest of the platform: a module writes a forecast of every algorithm for every
// metric for every step of the horizon, so what is kept is (targets a day) x (algorithms) x (metrics) x (days kept) rows for each module, and a month of it is millions of
// rows for one module. What reads it is the chart of forecast against what happened, which a week shows well, and the evaluation, which scores a run within hours.
var PredictionRetentionDays = Param{Key: "forecast.prediction_retention_days", Default: 30, Min: 1, Max: 365,
	Name:   "Forecast Retention (days)",
	Remark: "How many days the stored forecasts are kept (1-365). They are the biggest table of the platform: every algorithm forecasts every metric of every module, so the rows kept are (steps a day) x (algorithms) x (metrics) x (days) for each module. The chart of forecast against actual reaches back this far, the evaluation scores a run within hours. Only the last day is kept for every step (forecast.prediction_coarsen_after_hours), so at the default 15 minutes 30 days hold about as many rows as 3.6 days kept for every step. When forecast.history_retention_days is set and smaller, that one holds."}

// PredictionCoarsenAfter and PredictionCoarsenMinutes thin the forecasts that are no longer looked at step by step: past PredictionCoarsenAfter hours only one forecast in every
// PredictionCoarsenMinutes minutes of target time is kept for each algorithm and metric, which is as fine as a chart of days can be read.
var PredictionCoarsenAfter = Param{Key: "forecast.prediction_coarsen_after_hours", Default: 24, Min: 1, Max: 720,
	Name:   "Keep Every Forecast Step For (hours)",
	Remark: "Forecasts up to this many hours old are kept for every step. Older ones are reduced to one in every forecast.prediction_coarsen_minutes minutes of target time (1-720). Keep it well above the hours the evaluation needs to score a run."}

var PredictionCoarsenMinutes = Param{Key: "forecast.prediction_coarsen_minutes", Default: 15, Min: 0, Max: 1440,
	Name:   "Forecast Resolution After That (minutes)",
	Remark: "Past the hours above, one forecast in every this many minutes of target time is kept for each algorithm and metric of a module; the others are deleted by the hourly clean-up (0-1440). 0 keeps every step until the retention ends. A day of 1-minute steps is 1440 rows for an algorithm and a metric, 96 at 15 minutes."}
