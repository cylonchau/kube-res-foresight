package forecastsvc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

func gibOf(n int64) int64 { return n << 30 }

// A module that uses node pools gets the pool its Pod fills best with the recommendation, and the answer says why; a module that does not gets none.
func TestTheRecommendationNamesTheNodePoolThePodFitsBest(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	require.NoError(t, f.db.AutoMigrate(&model.NodePool{}))
	require.NoError(t, f.db.Create(&model.NodePool{Name: "cpu-8c16g", Kind: model.NodePoolCPU, Enabled: true, NodeCPUMilli: 8000, NodeMemoryBytes: gibOf(16), NodeSelector: map[string]string{"pool": "cpu"}}).Error)
	require.NoError(t, f.db.Create(&model.NodePool{Name: "mem-4c32g", Kind: model.NodePoolMemory, Enabled: true, NodeCPUMilli: 4000, NodeMemoryBytes: gibOf(32), NodeSelector: map[string]string{"pool": "mem"}}).Error)
	f.dynamic(t, func(m *model.ForecastModule) {
		m.PredictEnabled, m.NodePoolEnabled = false, false
		m.FallbackCPUMilli, m.FallbackMemoryBytes = 200, gibOf(4)
	})

	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Nil(t, detail.NodePool, "a module that does not use pools is told of none")
	assert.Zero(t, rec.NodePoolID)

	f.dynamic(t, func(m *model.ForecastModule) { m.NodePoolEnabled = true })
	rec, detail, err = f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	require.NotNil(t, detail.NodePool)
	require.NotNil(t, detail.NodePool.Pool, detail.NodePool.Reason)
	assert.Equal(t, "mem-4c32g", detail.NodePool.Pool.Name, detail.NodePool.Reason)
	assert.Equal(t, "mem-4c32g", rec.NodePool)
	assert.NotZero(t, rec.NodePoolID)
	assert.Equal(t, map[string]string{"pool": "mem"}, detail.NodePool.Pool.NodeSelector)
}
