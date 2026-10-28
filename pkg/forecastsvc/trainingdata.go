package forecastsvc

import (
	"context"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// DataPoint is one value at one time.
type DataPoint struct {
	Time  time.Time `json:"time"`
	Value float64   `json:"value"`
}

// TrainingData is what one training record worked on: the stored samples of its history window and the forecast it
// produced, so a record can be read as "this data went in, this came out".
type TrainingData struct {
	Run      *model.TrainingRun `json:"run"`
	Unit     string             `json:"unit"`
	Input    []DataPoint        `json:"input"`
	Forecast []DataPoint        `json:"forecast"`
}

// maxTrainingPoints bounds the points of one answer.
const maxTrainingPoints = 5000

// TrainingData reads the samples the run was given (the stored samples of its window, before gap filling) and the
// median forecast it wrote. A skipped or failed run has no forecast; a window whose samples were pruned has no input.
func (s *Service) TrainingData(ctx context.Context, id uint) (*TrainingData, error) {
	run, err := s.Predictions.GetTraining(ctx, id)
	if err != nil {
		return nil, err
	}
	out := &TrainingData{Run: run, Input: []DataPoint{}, Forecast: []DataPoint{}}
	if mod, err := s.Modules.Get(ctx, run.ModuleID); err == nil {
		out.Unit = MetricUnit(dataOf(mod).KindOf(run.Metric))
	}
	if run.WindowStart != nil && run.WindowEnd != nil {
		if series, err := s.Samples.Series(ctx, run.ModuleID, run.Metric); err == nil {
			samples, err := s.Samples.Range(ctx, series.ID, *run.WindowStart, *run.WindowEnd)
			if err != nil {
				return nil, err
			}
			stride := 1 + len(samples)/maxTrainingPoints
			for i := 0; i < len(samples); i += stride {
				out.Input = append(out.Input, DataPoint{Time: time.Unix(samples[i].TS, 0).UTC(), Value: samples[i].Value})
			}
		}
	}
	if run.Status == model.TrainingOK {
		rows, err := s.Predictions.RunForecast(ctx, run.ModuleID, run.Metric, run.AlgorithmID, run.PredictionTime)
		if err != nil {
			return nil, err
		}
		for _, p := range rows {
			if p.QuantilePermille != 0 && p.QuantilePermille != 500 {
				continue
			}
			out.Forecast = append(out.Forecast, DataPoint{Time: p.TargetTime.UTC(), Value: p.Value})
		}
	}
	return out, nil
}
