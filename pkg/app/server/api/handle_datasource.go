package api

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/apis"
	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/promclient"
	cryptoutils "github.com/cylonchau/kube-res-foresight/pkg/utils"
	"github.com/gin-gonic/gin"
)

const (
	defaultDatasourceTimeoutSeconds = 30
	maxDatasourceTimeoutSeconds     = 300
	defaultDatasourceConcurrency    = 1
)

// DatasourceRouter serves datasource management and connection tests.
type DatasourceRouter struct {
	DAO *rdb.DatasourceDAO
}

func datasourceDTO(ds *model.Datasource, mappingCount int64) *apis.DatasourceDTO {
	return &apis.DatasourceDTO{
		ID: ds.ID, CreatedAt: ds.CreatedAt, UpdatedAt: ds.UpdatedAt,
		Name: ds.Name, Type: ds.Type, BaseURL: ds.BaseURL, AuthType: ds.AuthType, Username: ds.Username,
		HasPassword: ds.PasswordEncrypted != "", HasBearerToken: ds.BearerTokenEncrypted != "",
		TLSSkipVerify: ds.TLSSkipVerify, HasCACert: ds.CACert != "",
		TimeoutSeconds: ds.TimeoutSeconds, MaxConcurrency: ds.MaxConcurrency, Enabled: ds.Enabled,
		Status: ds.Status, ServerVersion: ds.ServerVersion, LastCheckedAt: ds.LastCheckedAt,
		LastLatencyMs: ds.LastLatencyMs, LastError: ds.LastError,
		MappingCount: mappingCount, Remark: ds.Remark,
	}
}

func parseUintParam(c *gin.Context, name string) (uint, bool) {
	id, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil || id == 0 {
		query.BadRequest(c, fmt.Errorf("invalid %s", name))
		return 0, false
	}
	return uint(id), true
}

// normalizeDatasource validates the assembled datasource and canonicalizes its fields.
func normalizeDatasource(ds *model.Datasource) error {
	ds.Name = strings.TrimSpace(ds.Name)
	if ds.Name == "" {
		return errors.New("name is required")
	}
	if ds.Type == "" {
		ds.Type = model.DatasourceTypePrometheus
	}
	if ds.Type != model.DatasourceTypePrometheus {
		return fmt.Errorf("unsupported type %q", ds.Type)
	}
	base, err := promclient.NormalizeBaseURL(ds.BaseURL)
	if err != nil {
		return err
	}
	ds.BaseURL = base
	if ds.AuthType == "" {
		ds.AuthType = model.DatasourceAuthNone
	}
	switch ds.AuthType {
	case model.DatasourceAuthNone:
		ds.Username, ds.PasswordEncrypted, ds.BearerTokenEncrypted = "", "", ""
	case model.DatasourceAuthBasic:
		ds.BearerTokenEncrypted = ""
		if strings.TrimSpace(ds.Username) == "" || ds.PasswordEncrypted == "" {
			return errors.New("basic auth requires username and password")
		}
	case model.DatasourceAuthBearer:
		ds.Username, ds.PasswordEncrypted = "", ""
		if ds.BearerTokenEncrypted == "" {
			return errors.New("bearer auth requires a token")
		}
	default:
		return fmt.Errorf("unsupported auth_type %q", ds.AuthType)
	}
	if ds.TimeoutSeconds == 0 {
		ds.TimeoutSeconds = defaultDatasourceTimeoutSeconds
	}
	if ds.TimeoutSeconds < 1 || ds.TimeoutSeconds > maxDatasourceTimeoutSeconds {
		return fmt.Errorf("timeout_seconds must be between 1 and %d", maxDatasourceTimeoutSeconds)
	}
	if ds.MaxConcurrency == 0 {
		ds.MaxConcurrency = defaultDatasourceConcurrency
	}
	// There is no ceiling: how many requests a Prometheus can take is the operator's knowledge, not the platform's. The
	// collector never runs more requests at once than it has chunks to fetch, so a large value costs nothing.
	if ds.MaxConcurrency < 1 {
		return fmt.Errorf("max_concurrency must be at least 1")
	}
	cfg, err := promclient.ConfigFromDatasource(ds)
	if err != nil {
		return err
	}
	_, err = promclient.New(cfg) // validates the CA bundle and auth combination
	return err
}

// encryptSecret encrypts a write-only secret; blank input means "keep the current value".
func encryptSecret(current, input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return current, nil
	}
	return cryptoutils.EncryptValue(input)
}

func (r *DatasourceRouter) List(c *gin.Context) {
	var req apis.DatasourceListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	list, total, err := r.DAO.List(c.Request.Context(), &req, boolQuery(c, "enabled"))
	if err != nil {
		query.InternalError(c, err)
		return
	}
	ids := make([]uint, 0, len(list))
	for _, ds := range list {
		ids = append(ids, ds.ID)
	}
	counts, err := r.DAO.MappingCounts(c.Request.Context(), ids)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	dtos := make([]*apis.DatasourceDTO, 0, len(list))
	for _, ds := range list {
		dtos = append(dtos, datasourceDTO(ds, counts[ds.ID]))
	}
	query.SuccessResponse(c, nil, gin.H{"list": dtos, "total": total, "page": req.Page, "pageSize": req.PageSize})
}

// Options lists enabled datasources for selectors.
func (r *DatasourceRouter) Options(c *gin.Context) {
	list, err := r.DAO.ListEnabled(c.Request.Context())
	if err != nil {
		query.InternalError(c, err)
		return
	}
	options := make([]apis.DatasourceOptionDTO, 0, len(list))
	for _, ds := range list {
		options = append(options, apis.DatasourceOptionDTO{ID: ds.ID, Name: ds.Name})
	}
	query.SuccessResponse(c, nil, options)
}

func (r *DatasourceRouter) Get(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	ds, err := r.DAO.GetByID(c.Request.Context(), id)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	counts, err := r.DAO.MappingCounts(c.Request.Context(), []uint{id})
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, datasourceDTO(ds, counts[id]))
}

func (r *DatasourceRouter) Create(c *gin.Context) {
	var req apis.DatasourceCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	ds := &model.Datasource{
		Name: req.Name, Type: req.Type, BaseURL: req.BaseURL, AuthType: req.AuthType,
		Username: strings.TrimSpace(req.Username), TLSSkipVerify: req.TLSSkipVerify, CACert: strings.TrimSpace(req.CACert),
		TimeoutSeconds: req.TimeoutSeconds, MaxConcurrency: req.MaxConcurrency, Enabled: true,
		Status: model.DatasourceStatusUnknown, Remark: req.Remark,
	}
	if req.Enabled != nil {
		ds.Enabled = *req.Enabled
	}
	var err error
	if ds.PasswordEncrypted, err = encryptSecret("", req.Password); err != nil {
		query.InternalError(c, err)
		return
	}
	if ds.BearerTokenEncrypted, err = encryptSecret("", req.BearerToken); err != nil {
		query.InternalError(c, err)
		return
	}
	if err := normalizeDatasource(ds); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := r.DAO.Create(c.Request.Context(), ds); err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, datasourceDTO(ds, 0))
}

func (r *DatasourceRouter) Update(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req apis.DatasourceUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	ds, err := r.DAO.GetByID(c.Request.Context(), id)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	before := *ds
	if req.Name != nil {
		ds.Name = *req.Name
	}
	if req.BaseURL != nil {
		ds.BaseURL = *req.BaseURL
	}
	if req.AuthType != nil {
		ds.AuthType = *req.AuthType
	}
	if req.Username != nil {
		ds.Username = strings.TrimSpace(*req.Username)
	}
	if req.TLSSkipVerify != nil {
		ds.TLSSkipVerify = *req.TLSSkipVerify
	}
	if req.TimeoutSeconds != nil {
		ds.TimeoutSeconds = *req.TimeoutSeconds
	}
	if req.MaxConcurrency != nil {
		ds.MaxConcurrency = *req.MaxConcurrency
	}
	if req.Enabled != nil {
		ds.Enabled = *req.Enabled
	}
	if req.Remark != nil {
		ds.Remark = *req.Remark
	}
	if req.ClearCACert {
		ds.CACert = ""
	}
	if req.CACert != nil && strings.TrimSpace(*req.CACert) != "" {
		ds.CACert = strings.TrimSpace(*req.CACert)
	}
	if req.Password != nil {
		if ds.PasswordEncrypted, err = encryptSecret(ds.PasswordEncrypted, *req.Password); err != nil {
			query.InternalError(c, err)
			return
		}
	}
	if req.BearerToken != nil {
		if ds.BearerTokenEncrypted, err = encryptSecret(ds.BearerTokenEncrypted, *req.BearerToken); err != nil {
			query.InternalError(c, err)
			return
		}
	}
	if err := normalizeDatasource(ds); err != nil {
		query.BadRequest(c, err)
		return
	}
	// A changed endpoint or credential invalidates the previous probe result.
	if ds.BaseURL != before.BaseURL || ds.AuthType != before.AuthType || ds.Username != before.Username ||
		ds.PasswordEncrypted != before.PasswordEncrypted || ds.BearerTokenEncrypted != before.BearerTokenEncrypted ||
		ds.TLSSkipVerify != before.TLSSkipVerify || ds.CACert != before.CACert {
		ds.Status, ds.ServerVersion, ds.LastError, ds.LastLatencyMs, ds.LastCheckedAt = model.DatasourceStatusUnknown, "", "", 0, nil
	}
	if err := r.DAO.Update(c.Request.Context(), ds); err != nil {
		r.writeError(c, err)
		return
	}
	counts, _ := r.DAO.MappingCounts(c.Request.Context(), []uint{id})
	query.SuccessResponse(c, nil, datasourceDTO(ds, counts[id]))
}

func (r *DatasourceRouter) Delete(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	if err := r.DAO.Delete(c.Request.Context(), []uint{id}); err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

func (r *DatasourceRouter) BatchDelete(c *gin.Context) {
	var req apis.QueryByIDs
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	ids := make([]uint, 0, len(req.IDs))
	for _, id := range req.IDs {
		ids = append(ids, uint(id))
	}
	if err := r.DAO.Delete(c.Request.Context(), ids); err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

// Test probes the stored endpoint and persists the outcome. A failed probe is a normal
// result (status "error"), not an HTTP error, so the UI can show the reason.
func (r *DatasourceRouter) Test(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	ds, err := r.DAO.GetByID(c.Request.Context(), id)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	result := apis.DatasourceTestResult{Status: model.DatasourceStatusOK}
	if ds.Type == model.DatasourceTypeDemo {
		// nothing to probe: the data is generated inside the program
		result.ServerVersion = "synthetic"
		if err := r.DAO.RecordTest(c.Request.Context(), id, result.Status, result.ServerVersion, 0, "", time.Now()); err != nil {
			query.InternalError(c, err)
			return
		}
		query.SuccessResponse(c, nil, result)
		return
	}
	cfg, err := promclient.ConfigFromDatasource(ds)
	var probe *promclient.TestResult
	if err == nil {
		var client *promclient.Client
		if client, err = promclient.New(cfg); err == nil {
			probe, err = client.Test(c.Request.Context())
		}
	}
	if err != nil {
		result = apis.DatasourceTestResult{Status: model.DatasourceStatusError, Error: err.Error()}
	} else {
		result.ServerVersion, result.LatencyMs = probe.Version, probe.LatencyMs
	}
	if err := r.DAO.RecordTest(c.Request.Context(), id, result.Status, result.ServerVersion, result.LatencyMs, result.Error, time.Now()); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, result)
}

func (r *DatasourceRouter) writeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, rdb.ErrNameTaken), errors.Is(err, rdb.ErrDatasourceInUse):
		query.BadRequest(c, err)
	default:
		query.HandleDBError(c, err)
	}
}
