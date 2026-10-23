package rdb

import (
	"context"
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"gorm.io/gorm"
	"strings"
	"time"
)

// ErrInstanceStateChanged is returned by WorkflowInstanceDAO.UpdateState when
// the instance's current_state no longer matches the expected fromState —
// another DispatchEvent already moved it, so this caller's transition must
// not be applied.
var ErrInstanceStateChanged = errors.New("workflow instance state changed concurrently")

type BotCommandDAO struct {
	db *gorm.DB
}

func NewBotCommandDAO(db *gorm.DB) *BotCommandDAO {
	return &BotCommandDAO{db: db}
}

func (dao *BotCommandDAO) GetByCommand(ctx context.Context, cmd string) (*model.BotCommandDef, error) {
	var item model.BotCommandDef
	if err := dao.db.WithContext(ctx).Where("command = ?", cmd).First(&item).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (dao *BotCommandDAO) ListAll(ctx context.Context) ([]model.BotCommandDef, error) {
	var list []model.BotCommandDef
	if err := dao.db.WithContext(ctx).Order("id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// ListForBot returns the commands one bot answers: its own first, then the
// ones bound to every bot (bot_id 0).
func (dao *BotCommandDAO) ListForBot(ctx context.Context, botID int64) ([]model.BotCommandDef, error) {
	var list []model.BotCommandDef
	if err := dao.db.WithContext(ctx).Where("bot_id IN ?", []int64{0, botID}).Order("bot_id DESC, id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (dao *BotCommandDAO) ListPaginated(ctx context.Context, page, pageSize int, keyword string) ([]model.BotCommandDef, int64, error) {
	return dao.ListFiltered(ctx, page, pageSize, keyword, nil)
}

// ListFiltered is ListPaginated limited to one bot's commands when botID is set
// (0 selects the commands shared by every bot).
func (dao *BotCommandDAO) ListFiltered(ctx context.Context, page, pageSize int, keyword string, botID *int64) ([]model.BotCommandDef, int64, error) {
	page, pageSize = normalizePage(page, pageSize)
	var list []model.BotCommandDef
	var total int64
	q := dao.db.WithContext(ctx).Model(&model.BotCommandDef{})
	if botID != nil {
		q = q.Where("bot_id = ?", *botID)
	}
	if keyword != "" {
		kw := "%" + strings.TrimSpace(keyword) + "%"
		q = q.Where("command LIKE ? OR name LIKE ? OR description LIKE ? OR workflow_key LIKE ?", kw, kw, kw, kw)
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := q.Order("id ASC").Offset(pageOffset(page, pageSize)).Limit(pageSize).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// GetByID fetches a bot command by its numeric primary key, for the admin
// edit page (distinct from GetByCommand, which is used for live command
// matching against user input).
func (dao *BotCommandDAO) GetByID(ctx context.Context, id int64) (*model.BotCommandDef, error) {
	var item model.BotCommandDef
	if err := dao.db.WithContext(ctx).First(&item, id).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

// Update edits a command by its numeric primary key. Matching by ID (not by
// the command string itself) is required so that renaming a command's text
// in the same request doesn't change which row gets updated.
func (dao *BotCommandDAO) Update(ctx context.Context, id int64, updates map[string]interface{}) error {
	res := dao.db.WithContext(ctx).Model(&model.BotCommandDef{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("bot command not found")
	}
	return nil
}

func (dao *BotCommandDAO) Create(ctx context.Context, item *model.BotCommandDef) error {
	return dao.db.WithContext(ctx).Create(item).Error
}

// Delete removes a command by its numeric primary key.
func (dao *BotCommandDAO) Delete(ctx context.Context, id int64) error {
	res := dao.db.WithContext(ctx).Where("id = ?", id).Delete(&model.BotCommandDef{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("bot command not found")
	}
	return nil
}

// BatchDelete removes multiple commands by their numeric primary keys.
func (dao *BotCommandDAO) BatchDelete(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	return dao.db.WithContext(ctx).Where("id IN ?", ids).Delete(&model.BotCommandDef{}).Error
}

type WorkflowDefDAO struct {
	db *gorm.DB
}

func NewWorkflowDefDAO(db *gorm.DB) *WorkflowDefDAO {
	return &WorkflowDefDAO{db: db}
}

func (dao *WorkflowDefDAO) GetByKey(ctx context.Context, key string) (*model.WorkflowDef, error) {
	var item model.WorkflowDef
	if err := dao.db.WithContext(ctx).Where("workflow_key = ?", key).First(&item).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (dao *WorkflowDefDAO) ListAll(ctx context.Context) ([]model.WorkflowDef, error) {
	var list []model.WorkflowDef
	if err := dao.db.WithContext(ctx).Order("id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (dao *WorkflowDefDAO) ListPaginated(ctx context.Context, page, pageSize int, keyword string) ([]model.WorkflowDef, int64, error) {
	page, pageSize = normalizePage(page, pageSize)
	var list []model.WorkflowDef
	var total int64
	q := dao.db.WithContext(ctx).Model(&model.WorkflowDef{})
	if keyword != "" {
		kw := "%" + strings.TrimSpace(keyword) + "%"
		q = q.Where("workflow_key LIKE ? OR name LIKE ? OR description LIKE ?", kw, kw, kw)
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := q.Order("id ASC").Offset(pageOffset(page, pageSize)).Limit(pageSize).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// Update applies updates to the pipeline identified by workflow_key and
// returns how many rows it touched. The count matters: an update that
// matches nothing is not a GORM error, so callers need it to tell "saved"
// apart from "silently changed nothing".
func (dao *WorkflowDefDAO) Update(ctx context.Context, key string, updates map[string]interface{}) (int64, error) {
	result := dao.db.WithContext(ctx).Model(&model.WorkflowDef{}).Where("workflow_key = ?", key).Updates(updates)
	return result.RowsAffected, result.Error
}

func (dao *WorkflowDefDAO) Create(ctx context.Context, item *model.WorkflowDef) error {
	return dao.db.WithContext(ctx).Create(item).Error
}

// Delete removes the pipeline identified by workflow_key, reporting a miss
// rather than succeeding silently — same reasoning as Update above.
func (dao *WorkflowDefDAO) Delete(ctx context.Context, key string) error {
	res := dao.db.WithContext(ctx).Where("workflow_key = ?", key).Delete(&model.WorkflowDef{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("workflow pipeline not found")
	}
	return nil
}

func (dao *WorkflowDefDAO) BatchDelete(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	return dao.db.WithContext(ctx).Where("workflow_key IN ?", keys).Delete(&model.WorkflowDef{}).Error
}

type WorkflowInstanceDAO struct {
	db *gorm.DB
}

func NewWorkflowInstanceDAO(db *gorm.DB) *WorkflowInstanceDAO {
	return &WorkflowInstanceDAO{db: db}
}

func (dao *WorkflowInstanceDAO) Create(ctx context.Context, inst *model.WorkflowInstance) error {
	return dao.db.WithContext(ctx).Create(inst).Error
}

func (dao *WorkflowInstanceDAO) GetByID(ctx context.Context, id int64) (*model.WorkflowInstance, error) {
	var inst model.WorkflowInstance
	if err := dao.db.WithContext(ctx).Where("id = ?", id).First(&inst).Error; err != nil {
		return nil, err
	}
	return &inst, nil
}

func (dao *WorkflowInstanceDAO) GetByBusiness(ctx context.Context, bType string, bID int64) (*model.WorkflowInstance, error) {
	var inst model.WorkflowInstance
	if err := dao.db.WithContext(ctx).Where("business_type = ? AND business_id = ?", bType, bID).Order("id DESC").First(&inst).Error; err != nil {
		return nil, err
	}
	return &inst, nil
}

func (dao *WorkflowInstanceDAO) UpdateState(ctx context.Context, id int64, fromState string, state string, status string, historyJSON string, contextJSON string, finishedAt *time.Time) error {
	updates := map[string]interface{}{
		"current_state": state,
		"status":        status,
		"history_json":  historyJSON,
	}
	if contextJSON != "" {
		updates["context_data_json"] = contextJSON
	}
	if finishedAt != nil {
		updates["finished_at"] = finishedAt
	}
	res := dao.db.WithContext(ctx).Model(&model.WorkflowInstance{}).
		Where("id = ? AND current_state = ?", id, fromState).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrInstanceStateChanged
	}
	return nil
}

// ListByWorkflowKey returns every instance of one workflow, newest first,
// with no pagination — for admin list endpoints
// history page) that filter/paginate in Go over a small, workflow-scoped
// result set rather than pushing a status-vocabulary translation into SQL.
func (dao *WorkflowInstanceDAO) ListByWorkflowKey(ctx context.Context, workflowKey string) ([]model.WorkflowInstance, error) {
	var list []model.WorkflowInstance
	if err := dao.db.WithContext(ctx).Where("workflow_key = ?", workflowKey).Order("id DESC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// ListPaginated filters by workflow key/status (exact match) and, when
// requesterName is set, a case-insensitive substring match against
// requester_name — the same "who filed this" search the admin console's
// 流程工单 search box and the Telegram card both show the requester as.
func (dao *WorkflowInstanceDAO) ListPaginated(ctx context.Context, page, pageSize int, workflowKey, status, requesterName string) ([]model.WorkflowInstance, int64, error) {
	page, pageSize = normalizePage(page, pageSize)
	var list []model.WorkflowInstance
	var total int64
	q := dao.db.WithContext(ctx).Model(&model.WorkflowInstance{})
	if workflowKey != "" {
		q = q.Where("workflow_key = ?", workflowKey)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if requesterName = strings.TrimSpace(requesterName); requesterName != "" {
		q = q.Where("requester_name LIKE ?", "%"+requesterName+"%")
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := q.Order("id DESC").Offset(pageOffset(page, pageSize)).Limit(pageSize).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

func (dao *WorkflowInstanceDAO) Delete(ctx context.Context, id int64) error {
	res := dao.db.WithContext(ctx).Where("id = ?", id).Delete(&model.WorkflowInstance{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("instance not found")
	}
	return nil
}

// BatchDelete removes every instance in ids, reporting how many rows were
// actually deleted — a ticket referenced by an id that's already gone (e.g.
// deleted by someone else since the caller's list loaded) is not an error,
// same reasoning as the single Delete above only matters when NONE of the
// ids resolved to anything.
func (dao *WorkflowInstanceDAO) BatchDelete(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := dao.db.WithContext(ctx).Where("id IN ?", ids).Delete(&model.WorkflowInstance{})
	if res.Error != nil {
		return 0, res.Error
	}
	if res.RowsAffected == 0 {
		return 0, errors.New("no matching instances found")
	}
	return res.RowsAffected, nil
}

type WorkflowStore struct {
	cmdDAO *BotCommandDAO
	defDAO *WorkflowDefDAO
	insDAO *WorkflowInstanceDAO
}

func NewWorkflowStore(db *gorm.DB) *WorkflowStore {
	return &WorkflowStore{
		cmdDAO: NewBotCommandDAO(db),
		defDAO: NewWorkflowDefDAO(db),
		insDAO: NewWorkflowInstanceDAO(db),
	}
}

func (s *WorkflowStore) GetWorkflowDef(ctx context.Context, key string) (*model.WorkflowDef, error) {
	return s.defDAO.GetByKey(ctx, key)
}

func (s *WorkflowStore) GetCommand(ctx context.Context, cmd string) (*model.BotCommandDef, error) {
	return s.cmdDAO.GetByCommand(ctx, cmd)
}

func (s *WorkflowStore) ListCommands(ctx context.Context) ([]model.BotCommandDef, error) {
	if botID, ok := model.BotFrom(ctx); ok {
		return s.cmdDAO.ListForBot(ctx, botID)
	}
	return s.cmdDAO.ListAll(ctx)
}

func (s *WorkflowStore) CreateInstance(ctx context.Context, inst *model.WorkflowInstance) error {
	return s.insDAO.Create(ctx, inst)
}

func (s *WorkflowStore) GetInstance(ctx context.Context, id int64) (*model.WorkflowInstance, error) {
	return s.insDAO.GetByID(ctx, id)
}

func (s *WorkflowStore) GetInstanceByBusiness(ctx context.Context, bType string, bID int64) (*model.WorkflowInstance, error) {
	return s.insDAO.GetByBusiness(ctx, bType, bID)
}

func (s *WorkflowStore) UpdateInstanceState(ctx context.Context, id int64, fromState, state, status, historyJSON, contextJSON string, finishedAt *time.Time) error {
	return s.insDAO.UpdateState(ctx, id, fromState, state, status, historyJSON, contextJSON, finishedAt)
}
