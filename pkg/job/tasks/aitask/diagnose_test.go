package aitask

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

type silent struct{ stages []string }

func (s *silent) Progress(_ int, stage string) { s.stages = append(s.stages, stage) }
func (s *silent) Event(string, any)            {}
func (s *silent) Partial(string)               {}
func (s *silent) PartialResult(any)            {}

// The diagnosis task makes its own facts from the change it was queued for, and says why when it cannot: the request that queued it only checked that the change exists.
func TestTheDiagnosisTaskMakesItsFactsFromTheChangeAndSaysWhyWhenItCannot(t *testing.T) {
	ctx := context.Background()
	rep := &silent{}
	asked := [2]uint{}
	failing := DiagnoseHandler(nil, func(_ context.Context, cluster, change uint) (string, error) {
		asked = [2]uint{cluster, change}
		return "", errors.New("the change is gone")
	})
	_, err := failing(ctx, &model.AsyncTask{Payload: `{"cluster_id":3,"change_id":42,"lang":"en"}`}, rep)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the change is gone")
	assert.Equal(t, [2]uint{3, 42}, asked, "the facts were asked for the change that was queued")
	assert.Contains(t, rep.stages, "reading")

	// a payload that names no change and carries no facts cannot be run
	_, err = failing(ctx, &model.AsyncTask{Payload: `{"lang":"en"}`}, rep)
	require.Error(t, err)
	_, err = failing(ctx, &model.AsyncTask{Payload: `not json`}, rep)
	require.Error(t, err)

	// a handler without a way to make facts says so (and a payload of an earlier version, which carries the facts, does not need one)
	_, err = DiagnoseHandler(nil, nil)(ctx, &model.AsyncTask{Payload: `{"cluster_id":3,"change_id":42,"lang":"en"}`}, rep)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "facts")
}
