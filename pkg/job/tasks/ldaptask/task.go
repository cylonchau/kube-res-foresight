// Package ldaptask holds the scheduled directory sync: accounts the directory disabled or removed are disabled
// here too, so access ends without waiting for the person to try to log in.
package ldaptask

import (
	"context"

	"github.com/cylonchau/kube-res-foresight/pkg/ldapauth"
	"github.com/cylonchau/kube-res-foresight/pkg/logger"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// JobType is the identifier of the sync job.
const JobType = "ldap.sync"

// Sync runs ldapauth.Service.SyncAll.
type Sync struct {
	Service *ldapauth.Service
}

func (t *Sync) Type() string { return JobType }

// Execute returns an error when the directory cannot be reached, so the job engine retries with backoff; the
// service guarantees that no account is changed in that case.
func (t *Sync) Execute(ctx context.Context, _ *model.Job) error {
	res, err := t.Service.SyncAll(ctx)
	if err != nil {
		return err
	}
	if res.Disabled > 0 || res.Enabled > 0 {
		logger.Info("ldap sync changed accounts", logger.Int("checked", res.Checked), logger.Int("disabled", res.Disabled), logger.Int("enabled", res.Enabled))
	}
	return nil
}
