package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/cost"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/forecastsvc"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// CostRouter serves the cost side: the price rules, what the cluster costs, what the workloads could give back and how many nodes that could
// empty. Everything but the price rules is read-only.
type CostRouter struct {
	Prices  *rdb.CostPriceDAO
	Service *forecastsvc.Service
}

// priceRuleReq is a price rule as typed in.
type priceRuleReq struct {
	Name        string  `json:"name"`
	MatchKind   string  `json:"match_kind"`
	LabelKey    string  `json:"label_key"`
	LabelValue  string  `json:"label_value"`
	NamePattern string  `json:"name_pattern"`
	HourlyPrice float64 `json:"hourly_price"`
	Currency    string  `json:"currency"`
	Priority    int     `json:"priority"`
	Enabled     *bool   `json:"enabled"`
	Remark      string  `json:"remark"`
}

// ruleOf validates the request and fills a row from it; the row keeps its id and time.
func (req priceRuleReq) apply(row *model.CostPriceRule) error {
	rule := cost.Rule{Name: req.Name, MatchKind: req.MatchKind, LabelKey: req.LabelKey, LabelValue: req.LabelValue, NamePattern: req.NamePattern,
		HourlyPrice: req.HourlyPrice, Currency: req.Currency, Priority: req.Priority}
	rule.Normalize()
	if err := rule.Validate(); err != nil {
		return err
	}
	row.Name, row.MatchKind, row.LabelKey, row.LabelValue, row.NamePattern = rule.Name, rule.MatchKind, rule.LabelKey, rule.LabelValue, rule.NamePattern
	row.HourlyPrice, row.Currency, row.Priority = rule.HourlyPrice, rule.Currency, rule.Priority
	// a rule that matches by all/label/name keeps only the field that kind uses, so an old value does not linger in the row
	switch rule.MatchKind {
	case cost.MatchAll:
		row.LabelKey, row.LabelValue, row.NamePattern = "", "", ""
	case cost.MatchLabel:
		row.NamePattern = ""
	case cost.MatchName:
		row.LabelKey, row.LabelValue = "", ""
	}
	row.Remark = strings.TrimSpace(req.Remark)
	if req.Enabled != nil {
		row.Enabled = *req.Enabled
	}
	return nil
}

// ListPrices returns every price rule.
// @Summary List the cost price rules
// @Tags Cost
// @Router /api/v1/cost/prices [get]
func (r *CostRouter) ListPrices(c *gin.Context) {
	list, err := r.Prices.All(c.Request.Context())
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": list, "total": len(list)})
}

// CreatePrice adds a price rule.
// @Summary Create a cost price rule
// @Tags Cost
// @Router /api/v1/cost/prices [post]
func (r *CostRouter) CreatePrice(c *gin.Context) {
	var req priceRuleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	row := &model.CostPriceRule{Enabled: true}
	if err := req.apply(row); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := r.Prices.Save(c.Request.Context(), row); err != nil {
		if errors.Is(err, rdb.ErrNameTaken) {
			query.BadRequest(c, err)
			return
		}
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, row)
}

// UpdatePrice changes a price rule.
// @Summary Update a cost price rule
// @Tags Cost
// @Router /api/v1/cost/prices/{id} [put]
func (r *CostRouter) UpdatePrice(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		query.BadRequest(c, errors.New("id is not a number"))
		return
	}
	var req priceRuleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	row, err := r.Prices.Get(c.Request.Context(), uint(id))
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	if err := req.apply(row); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := r.Prices.Save(c.Request.Context(), row); err != nil {
		if errors.Is(err, rdb.ErrNameTaken) {
			query.BadRequest(c, err)
			return
		}
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, row)
}

// DeletePrice removes a price rule.
// @Summary Delete a cost price rule
// @Tags Cost
// @Router /api/v1/cost/prices/{id} [delete]
func (r *CostRouter) DeletePrice(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		query.BadRequest(c, errors.New("id is not a number"))
		return
	}
	if err := r.Prices.Delete(c.Request.Context(), uint(id)); err != nil {
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, nil)
}

// costError answers an error of the cost service: a missing choice or a cluster that cannot be read is the person's to fix.
func costError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, forecastsvc.ErrCostNoDatasource):
		query.BadRequest(c, err)
	case errors.Is(err, forecastsvc.ErrCostCluster):
		query.ErrorResponse(c, http.StatusBadGateway, err)
	default:
		query.HandleDBError(c, err)
	}
}

func queryUint(c *gin.Context, key string) uint {
	n, _ := strconv.ParseUint(c.Query(key), 10, 64)
	return uint(n)
}

// Overview prices the cluster and shares the price among the workloads by their requests.
// @Summary What the cluster costs
// @Tags Cost
// @Router /api/v1/cost/overview [get]
func (r *CostRouter) Overview(c *gin.Context) {
	out, err := r.Service.CostOverview(c.Request.Context(), queryUint(c, "datasource_id"))
	if err != nil {
		costError(c, err)
		return
	}
	query.SuccessResponse(c, nil, out)
}

// Modules diagnoses every enabled module: request against use, the surplus, the candidates.
// @Summary What the workloads could give back
// @Tags Cost
// @Router /api/v1/cost/modules [get]
func (r *CostRouter) Modules(c *gin.Context) {
	days, _ := strconv.Atoi(c.Query("window_days"))
	if days < 0 || days > forecastsvc.MaxCostWindowDays {
		query.BadRequest(c, errors.New("window_days is between 1 and "+strconv.Itoa(forecastsvc.MaxCostWindowDays)))
		return
	}
	out, err := r.Service.CostModules(c.Request.Context(), days, queryUint(c, "datasource_id"))
	if err != nil {
		costError(c, err)
		return
	}
	query.SuccessResponse(c, nil, out)
}

type simulateReq struct {
	DatasourceID uint   `json:"datasource_id"`
	Level        string `json:"level"`
	WindowDays   int    `json:"window_days"`
}

// Simulate asks how many nodes could be emptied now and with the candidates of a level.
// @Summary Simulate emptying nodes
// @Tags Cost
// @Router /api/v1/cost/simulate [post]
func (r *CostRouter) Simulate(c *gin.Context) {
	var req simulateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if req.WindowDays < 0 || req.WindowDays > forecastsvc.MaxCostWindowDays {
		query.BadRequest(c, errors.New("window_days is between 1 and "+strconv.Itoa(forecastsvc.MaxCostWindowDays)))
		return
	}
	out, err := r.Service.Simulate(c.Request.Context(), req.DatasourceID, req.Level, req.WindowDays)
	if err != nil {
		if errors.Is(err, forecastsvc.ErrCostLevel) {
			query.BadRequest(c, err)
			return
		}
		costError(c, err)
		return
	}
	query.SuccessResponse(c, nil, out)
}
