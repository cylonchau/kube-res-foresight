package timing

// ForecastReuse is how long a forecast that was made is reused when the same forecast is asked for again (P11 B3). A forecast is a function of the history
// it is given, the algorithm and its parameters: when the newest history, the parameters and the model version are the same as the run that made the last
// forecast, the algorithm would write the same values again, so it is not run and the rows are not written twice; a record of the run that says which forecast
// stands for it is filed instead, so the forecast still counts as current. After this many minutes the forecast is made again whatever the inputs are, so a
// forecast is never older than this. 0 turns the reuse off.
var ForecastReuse = Param{Key: "forecast.reuse_minutes", Default: 30, Min: 0, Max: 720,
	Name:   "Reuse a Forecast Made From the Same Inputs (minutes)",
	Remark: "A forecast made from the same history, the same algorithm parameters and the same model version as the last one is not computed and stored again for this many minutes: the last forecast stands for it. After that it is made again whatever the inputs. Algorithms that ask an external model service are never reused. 0 = always compute (0-720)."}

// ResultCache is how long the answer of a heavy report is kept for the next request that asks the same thing (P11 B3). An answer is kept only with the version
// of the data it was made from (the newest history, forecast, recommendation, execution and evaluation of the module, and its settings): when the data has
// changed the answer is made again at once, so a cached answer is never an old one, only one that nothing has changed under. Requests that come while one is being
// worked out wait for it instead of working the same thing out again. 0 turns the cache off.
var ResultCache = Param{Key: "cache.result_minutes", Default: 30, Min: 0, Max: 720,
	Name:   "Keep the Answer of a Heavy Report (minutes)",
	Remark: "The effect report, the resource analysis, the change preview and the quantiles read from Prometheus are kept for the next request that asks the same, as long as the data they were made from has not changed (and for the quantiles of Prometheus, for at most 5 minutes: that data always changes). Requests that come while one is being worked out wait for it. 0 = no cache (0-720)."}
