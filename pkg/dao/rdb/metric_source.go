package rdb

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// ---- scores

// SaveScores stores one evaluation of the instances of a module.
func (dao *PredictionDAO) SaveScores(ctx context.Context, rows []*model.AlgorithmScore) error {
	if len(rows) == 0 {
		return nil
	}
	return dao.db.WithContext(ctx).Create(&rows).Error
}

// ScoreFilter narrows the score history.
type ScoreFilter struct {
	ModuleID  uint
	Algorithm string
	Selected  *bool
	Since     *time.Time
	Until     *time.Time
	Sort      string
	Dir       string
}

var scoreSorts = map[string]string{"created_at": "created_at", "score": "score", "rank": "rank_no", "algorithm": "algorithm"}

// SearchScores lists score records with filters and a whitelisted sort.
func (dao *PredictionDAO) SearchScores(ctx context.Context, f ScoreFilter, page, pageSize int) ([]*model.AlgorithmScore, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.AlgorithmScore{})
	if f.ModuleID != 0 {
		q = q.Where("module_id = ?", f.ModuleID)
	}
	if f.Algorithm != "" {
		q = q.Where("algorithm = ?", f.Algorithm)
	}
	if f.Selected != nil {
		q = q.Where("selected = ?", *f.Selected)
	}
	if f.Since != nil {
		q = q.Where("created_at >= ?", *f.Since)
	}
	if f.Until != nil {
		q = q.Where("created_at <= ?", *f.Until)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, pageSize = normalizePage(page, pageSize)
	var list []*model.AlgorithmScore
	err := q.Order(order(f.Sort, f.Dir, scoreSorts, "id DESC")).Offset(pageOffset(page, pageSize)).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// LatestScores returns the newest score of every instance of a module.
func (dao *PredictionDAO) LatestScores(ctx context.Context, moduleID uint) (map[string]*model.AlgorithmScore, error) {
	var rows []*model.AlgorithmScore
	err := dao.db.WithContext(ctx).Table("algorithm_score AS s").Select("s.*").
		Joins("JOIN (SELECT algorithm, MAX(id) AS id FROM algorithm_score WHERE module_id = ? AND windows > 0 GROUP BY algorithm) AS l ON l.id = s.id", moduleID).
		Scan(&rows).Error
	out := make(map[string]*model.AlgorithmScore, len(rows))
	for _, r := range rows {
		out[r.Algorithm] = r
	}
	return out, err
}

// PreviousScores returns, for every algorithm of a module, the score it had before its latest one: what the ranking
// compares with to show how the algorithm moved. An algorithm that was scored once has none.
func (dao *PredictionDAO) PreviousScores(ctx context.Context, moduleID uint) (map[string]*model.AlgorithmScore, error) {
	var rows []*model.AlgorithmScore
	err := dao.db.WithContext(ctx).Table("algorithm_score AS s").Select("s.*").
		Joins(`JOIN (SELECT a.algorithm, MAX(a.id) AS id FROM algorithm_score a
			JOIN (SELECT algorithm, MAX(id) AS id FROM algorithm_score WHERE module_id = ? AND windows > 0 GROUP BY algorithm) m
			ON m.algorithm = a.algorithm AND a.id < m.id WHERE a.module_id = ? AND a.windows > 0 GROUP BY a.algorithm) AS p ON p.id = s.id`, moduleID, moduleID).
		Scan(&rows).Error
	out := make(map[string]*model.AlgorithmScore, len(rows))
	for _, r := range rows {
		out[r.Algorithm] = r
	}
	return out, err
}

// ---- admission decisions

// SaveAdmission stores the answer given to one admission request.
func (dao *PredictionDAO) SaveAdmission(ctx context.Context, d *model.AdmissionDecision) error {
	return dao.db.WithContext(ctx).Create(d).Error
}

// HasAdmissionRequest says whether the admission request with this uid already has a record.
func (dao *PredictionDAO) HasAdmissionRequest(ctx context.Context, uid string) (bool, error) {
	var n int64
	err := dao.db.WithContext(ctx).Model(&model.AdmissionDecision{}).Where("request_uid = ?", uid).Count(&n).Error
	return n > 0, err
}

// LatestAdmission is the newest admission record of a module, nil when it has none.
func (dao *PredictionDAO) LatestAdmission(ctx context.Context, moduleID uint) (*model.AdmissionDecision, error) {
	var d model.AdmissionDecision
	err := dao.db.WithContext(ctx).Where("module_id = ?", moduleID).Order("id DESC").Limit(1).Take(&d).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &d, err
}

// AdmissionFilter narrows the admission records.

type AdmissionFilter struct {
	ModuleID uint
	Applied  *bool
	Origin   string
	Keyword  string
	Since    *time.Time
	Until    *time.Time
	Sort     string
	Dir      string
}

var admissionSorts = map[string]string{"created_at": "created_at", "version": "version"}

// SearchAdmissions lists admission records with filters and a whitelisted sort.
func (dao *PredictionDAO) SearchAdmissions(ctx context.Context, f AdmissionFilter, page, pageSize int) ([]*model.AdmissionDecision, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.AdmissionDecision{})
	if f.ModuleID != 0 {
		q = q.Where("module_id = ?", f.ModuleID)
	}
	if f.Applied != nil {
		q = q.Where("applied = ?", *f.Applied)
	}
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("namespace LIKE ? OR workload LIKE ? OR reason LIKE ?", like, like, like)
	}
	if f.Origin != "" {
		q = q.Where("origin = ?", f.Origin)
	}
	if f.Since != nil {
		q = q.Where("created_at >= ?", *f.Since)
	}
	if f.Until != nil {
		q = q.Where("created_at <= ?", *f.Until)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, pageSize = normalizePage(page, pageSize)
	var list []*model.AdmissionDecision
	err := q.Order(order(f.Sort, f.Dir, admissionSorts, "id DESC")).Offset(pageOffset(page, pageSize)).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// PruneAdmissions deletes admission records older than the cutoff.
func (dao *PredictionDAO) PruneAdmissions(ctx context.Context, before time.Time) (int64, error) {
	res := dao.db.WithContext(ctx).Where("created_at < ?", before).Delete(&model.AdmissionDecision{})
	return res.RowsAffected, res.Error
}

// ScoreRun is one evaluation of a module: the scores of all its algorithms, saved together.
type ScoreRun struct {
	ModuleID    uint
	At          time.Time
	WindowStart time.Time
	WindowEnd   time.Time
	Scores      []*model.AlgorithmScore
}

// ScoreRuns pages the evaluations (newest first) with the scores of each, for the history of the ranking.
func (dao *PredictionDAO) ScoreRuns(ctx context.Context, f ScoreFilter, page, pageSize int) ([]ScoreRun, int64, error) {
	// an evaluation that replayed no window (windows = 0) holds no evidence, so it is not a ranking
	q := dao.db.WithContext(ctx).Model(&model.AlgorithmScore{}).Where("windows > 0")
	if f.ModuleID != 0 {
		q = q.Where("module_id = ?", f.ModuleID)
	}
	if f.Algorithm != "" {
		q = q.Where("algorithm = ?", f.Algorithm)
	}
	if f.Since != nil {
		q = q.Where("created_at >= ?", *f.Since)
	}
	if f.Until != nil {
		q = q.Where("created_at <= ?", *f.Until)
	}
	groups := func() *gorm.DB {
		return q.Session(&gorm.Session{}).Select("module_id, created_at").Group("module_id, created_at")
	}
	var total int64
	if err := dao.db.WithContext(ctx).Table("(?) AS g", groups()).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, pageSize = normalizePage(page, pageSize)
	var rows []struct {
		ModuleID uint      `gorm:"column:module_id"`
		At       time.Time `gorm:"column:created_at"`
	}
	if err := groups().Order("created_at DESC, module_id ASC").
		Offset(pageOffset(page, pageSize)).Limit(pageSize).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	out := make([]ScoreRun, 0, len(rows))
	for _, g := range rows {
		var scores []*model.AlgorithmScore
		if err := dao.db.WithContext(ctx).Where("module_id = ? AND created_at = ? AND windows > 0", g.ModuleID, g.At).Order("rank_no ASC, id ASC").Find(&scores).Error; err != nil {
			return nil, 0, err
		}
		run := ScoreRun{ModuleID: g.ModuleID, At: g.At, Scores: scores}
		if len(scores) > 0 {
			run.WindowStart, run.WindowEnd = scores[0].WindowStart, scores[0].WindowEnd
		}
		out = append(out, run)
	}
	return out, total, nil
}
