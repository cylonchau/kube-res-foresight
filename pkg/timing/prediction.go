package timing

// PredictionThinAfter is how old a stored forecast must be before the forecasts a newer run has replaced are deleted. Runs overlap: a forecast every
// five minutes of a fifteen minute horizon puts three forecasts on every target time, and every read (the history chart, the delay and tracking
// reports, the metric queries) takes only the newest forecast made at or before the target. The rows the newer ones replaced are read by nothing
// but the evaluation, which scores a whole run once its targets have passed, and that runs every hour.
var PredictionThinAfter = Param{Key: "forecast.prediction_thin_after_hours", Default: 24, Min: 1, Max: 720,
	Name:   "Thin Replaced Forecasts After (hours)",
	Remark: "Forecast runs overlap (a run every 5 minutes of a 15 minute horizon puts three forecasts on every target time) and only the newest forecast made at or before a target is ever read. The hourly clean-up deletes the older ones, from forecasts at least this old, so the biggest table keeps a fraction of its rows (about one in horizon / run interval). Keep it well above the hours the evaluation needs to score a run: a run that is thinned before it was evaluated is scored on what is left (1-720)."}
