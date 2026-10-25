package forecast

import (
	"context"
	"math"
	"testing"
)

// BenchmarkGBDTProductionSize trains on the default window (5000 steps) with 1440-step seasonal data, the size one
// scheduled prediction pass faces per module and metric.
func BenchmarkGBDTProductionSize(b *testing.B) {
	in := series(30*1440, func(i int) float64 { return 100 + 30*math.Sin(2*math.Pi*float64(i%1440)/1440) + float64((i*37)%11) })
	m := GBDT{Lags: 24, Calendar: true, Trees: 100, LearningRate: 0.1, MaxDepth: 3, MinLeaf: 5, MaxTrainRows: 5000}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := m.Forecast(context.Background(), in, 15); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHoltWintersDaily(b *testing.B) {
	in := series(30*1440, func(i int) float64 { return 100 + 30*math.Sin(2*math.Pi*float64(i%1440)/1440) })
	m := HoltWinters{Season: 1440, Trend: true}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := m.Forecast(context.Background(), in, 15); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRidgeAR(b *testing.B) {
	in := series(30*1440, func(i int) float64 { return 100 + 30*math.Sin(2*math.Pi*float64(i%1440)/1440) })
	m := RidgeAR{Lags: 60, Calendar: true, L2Lambda: 0.01}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := m.Forecast(context.Background(), in, 15); err != nil {
			b.Fatal(err)
		}
	}
}
