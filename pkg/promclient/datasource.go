package promclient

import (
	"errors"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	cryptoutils "github.com/cylonchau/kube-res-foresight/pkg/utils"
)

// ConfigFromDatasource decrypts the stored secrets of a datasource into a client configuration.
func ConfigFromDatasource(ds *model.Datasource) (Config, error) {
	cfg := Config{
		BaseURL: ds.BaseURL, AuthType: ds.AuthType, Username: ds.Username,
		TLSSkipVerify: ds.TLSSkipVerify, CACertPEM: ds.CACert,
		Timeout: time.Duration(ds.TimeoutSeconds) * time.Second,
	}
	var err error
	if ds.PasswordEncrypted != "" {
		if cfg.Password, err = cryptoutils.DecryptValue(ds.PasswordEncrypted); err != nil {
			return cfg, errors.New("stored password cannot be decrypted; re-enter it")
		}
	}
	if ds.BearerTokenEncrypted != "" {
		if cfg.BearerToken, err = cryptoutils.DecryptValue(ds.BearerTokenEncrypted); err != nil {
			return cfg, errors.New("stored bearer token cannot be decrypted; re-enter it")
		}
	}
	return cfg, nil
}
