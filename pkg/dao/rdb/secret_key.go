package rdb

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/cylonchau/kube-resource-fcst/pkg/apis"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/cylonchau/kube-resource-fcst/pkg/utils"
	"gorm.io/gorm"
	"sort"
	"strings"
	"time"
)

var ErrDeveloperCredentialInvalid = errors.New("invalid developer credential")

type SecretKeyDAO struct{ db *gorm.DB }

func NewSecretKeyDAO(db *gorm.DB) *SecretKeyDAO { return &SecretKeyDAO{db: db} }

func hashAccessKey(accessKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(accessKey)))
	return hex.EncodeToString(sum[:])
}

func generateDeveloperToken(prefix string, size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func generateDeveloperCredentialPair() (string, string, error) {
	accessKey, err := generateDeveloperToken("ak_kube_resource_fcst_", 18)
	if err != nil {
		return "", "", err
	}
	accessSecret, err := generateDeveloperToken("sk_kube_resource_fcst_", 32)
	return accessKey, accessSecret, err
}

func decodeDeveloperCredential(encrypted string) (apis.DeveloperAccessCredential, error) {
	var credential apis.DeveloperAccessCredential
	plain, err := utils.DecryptValue(encrypted)
	if err != nil {
		return credential, err
	}
	err = json.Unmarshal([]byte(plain), &credential)
	return credential, err
}

func developerSecretDigest(credential apis.DeveloperAccessCredential) ([sha256.Size]byte, error) {
	if credential.AccessSecretHash != "" {
		decoded, err := hex.DecodeString(credential.AccessSecretHash)
		if err != nil || len(decoded) != sha256.Size {
			return [sha256.Size]byte{}, errors.New("invalid access secret hash")
		}
		var digest [sha256.Size]byte
		copy(digest[:], decoded)
		return digest, nil
	}
	if strings.TrimSpace(credential.AccessSecret) == "" {
		return [sha256.Size]byte{}, errors.New("access secret is required")
	}
	return sha256.Sum256([]byte(strings.TrimSpace(credential.AccessSecret))), nil
}

func normalizeDeveloperCredential(credential apis.DeveloperAccessCredential) (apis.DeveloperAccessCredential, error) {
	credential.AccessKey = strings.TrimSpace(credential.AccessKey)
	if credential.AccessKey == "" {
		return credential, errors.New("access_key is required")
	}
	digest, err := developerSecretDigest(credential)
	if err != nil {
		return credential, err
	}
	credential.AccessSecretHash = hex.EncodeToString(digest[:])
	credential.AccessSecret = ""

	idSet := make(map[uint]struct{}, len(credential.PermissionIDs))
	permissionIDs := make([]uint, 0, len(credential.PermissionIDs))
	for _, id := range credential.PermissionIDs {
		if id == 0 {
			continue
		}
		if _, exists := idSet[id]; !exists {
			idSet[id] = struct{}{}
			permissionIDs = append(permissionIDs, id)
		}
	}
	sort.Slice(permissionIDs, func(i, j int) bool { return permissionIDs[i] < permissionIDs[j] })
	credential.PermissionIDs = permissionIDs

	if credential.ServerEnabled && len(credential.PermissionIDs) == 0 {
		return credential, errors.New("server permission_ids are required when server access is enabled")
	}
	return credential, nil
}

func encodeDeveloperCredential(credential apis.DeveloperAccessCredential) (normalized apis.DeveloperAccessCredential, encrypted, masked string, err error) {
	normalized, err = normalizeDeveloperCredential(credential)
	if err != nil {
		return normalized, "", "", err
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return normalized, "", "", err
	}
	encrypted, err = utils.EncryptValue(string(raw))
	if err != nil {
		return normalized, "", "", err
	}
	return normalized, encrypted, normalized.AccessKey + " / ********", nil
}

func populateDeveloperCredential(row *model.SecretKey) {
	if row == nil || row.KeyType != model.SecretKeyTypeDeveloperAccess {
		return
	}
	if credential, err := decodeDeveloperCredential(row.EncryptedValue); err == nil {
		row.AccessKey = credential.AccessKey
		row.ServerEnabled = credential.ServerEnabled
		row.PermissionIDs = credential.PermissionIDs
		row.ExpiresAt = credential.ExpiresAt
	}
}

func (dao *SecretKeyDAO) validatePermissionIDs(ctx context.Context, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	var count int64
	if err := dao.db.WithContext(ctx).Model(&model.Permission{}).
		Where("id IN ? AND status = ?", ids, true).Count(&count).Error; err != nil {
		return err
	}
	if count != int64(len(ids)) {
		return errors.New("one or more server permissions are invalid or disabled")
	}
	return nil
}

func (dao *SecretKeyDAO) List(ctx context.Context, req *apis.SecretKeyListReq) ([]model.SecretKey, int64, error) {
	var rows []model.SecretKey
	db := dao.db.WithContext(ctx).Model(&model.SecretKey{})
	if req.Keyword != "" {
		like := "%" + req.Keyword + "%"
		db = db.Where("name LIKE ? OR key_type LIKE ? OR remark LIKE ? OR masked_value LIKE ?", like, like, like, like)
	}
	if req.KeyType != "" {
		db = db.Where("key_type = ?", normalizeSecretKeyType(req.KeyType))
	}
	if req.Status != nil && *req.Status != "" {
		switch *req.Status {
		case "1", "true":
			db = db.Where("status = ?", true)
		case "0", "false":
			db = db.Where("status = ?", false)
		}
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := db.Order("id desc").Offset(int(req.Offset())).Limit(int(req.PageSize)).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	for i := range rows {
		populateDeveloperCredential(&rows[i])
	}
	return rows, total, nil
}

func (dao *SecretKeyDAO) GetByID(ctx context.Context, id uint64) (*model.SecretKey, error) {
	var row model.SecretKey
	if err := dao.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, err
	}
	populateDeveloperCredential(&row)
	return &row, nil
}

func (dao *SecretKeyDAO) createWithCredential(ctx context.Context, req *apis.SecretKeyCreateReq, accessKey, accessSecret string) (*model.SecretKey, error) {
	name, keyType := strings.TrimSpace(req.Name), normalizeSecretKeyType(req.KeyType)
	if name == "" {
		return nil, errors.New("secret key name is required")
	}
	if keyType != model.SecretKeyTypeDeveloperAccess {
		return nil, errors.New("unsupported secret key type")
	}
	credential, encrypted, masked, err := encodeDeveloperCredential(apis.DeveloperAccessCredential{
		AccessKey: accessKey, AccessSecret: accessSecret,
		ServerEnabled: req.ServerEnabled, PermissionIDs: req.PermissionIDs, ExpiresAt: req.ExpiresAt,
	})
	if err != nil {
		return nil, err
	}
	if err := dao.validatePermissionIDs(ctx, credential.PermissionIDs); err != nil {
		return nil, err
	}
	status := true
	if req.Status != nil {
		status = *req.Status
	}
	accessKeyHash := hashAccessKey(credential.AccessKey)
	row := &model.SecretKey{
		Name: name, KeyType: keyType, AccessKeyHash: &accessKeyHash,
		EncryptedValue: encrypted, MaskedValue: masked, Status: status, Remark: req.Remark,
	}
	if err := dao.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, err
	}
	populateDeveloperCredential(row)
	row.AccessSecret = accessSecret
	return row, nil
}

func (dao *SecretKeyDAO) Create(ctx context.Context, req *apis.SecretKeyCreateReq) (*model.SecretKey, error) {
	accessKey, accessSecret, err := generateDeveloperCredentialPair()
	if err != nil {
		return nil, err
	}
	return dao.createWithCredential(ctx, req, accessKey, accessSecret)
}

// ImportDeveloperCredential preserves a legacy key pair while converting the
// stored Secret to a one-way hash. It is only used by data migration.
func (dao *SecretKeyDAO) ImportDeveloperCredential(ctx context.Context, req *apis.SecretKeyCreateReq, accessKey, accessSecret string) (*model.SecretKey, error) {
	return dao.createWithCredential(ctx, req, accessKey, accessSecret)
}

func (dao *SecretKeyDAO) Update(ctx context.Context, id uint64, req *apis.SecretKeyUpdateReq) (*model.SecretKey, error) {
	current, err := dao.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	keyType := current.KeyType
	if req.KeyType != nil {
		keyType = normalizeSecretKeyType(*req.KeyType)
	}
	if keyType != model.SecretKeyTypeDeveloperAccess || current.KeyType != model.SecretKeyTypeDeveloperAccess {
		return nil, errors.New("unsupported secret key type")
	}
	credential, err := decodeDeveloperCredential(current.EncryptedValue)
	if err != nil {
		return nil, err
	}
	newSecret := ""
	if req.RotateSecret {
		newSecret, err = generateDeveloperToken("sk_kube_resource_fcst_", 32)
		if err != nil {
			return nil, err
		}
		credential.AccessSecret = newSecret
		credential.AccessSecretHash = ""
	}
	if req.ServerEnabled != nil {
		credential.ServerEnabled = *req.ServerEnabled
	}
	if req.PermissionIDs != nil {
		credential.PermissionIDs = *req.PermissionIDs
	}
	if req.ClearExpiresAt {
		credential.ExpiresAt = nil
	} else if req.ExpiresAt != nil {
		credential.ExpiresAt = req.ExpiresAt
	}
	credential, encrypted, masked, err := encodeDeveloperCredential(credential)
	if err != nil {
		return nil, err
	}
	if err := dao.validatePermissionIDs(ctx, credential.PermissionIDs); err != nil {
		return nil, err
	}
	accessKeyHash := hashAccessKey(credential.AccessKey)
	updates := map[string]interface{}{
		"key_type": keyType, "access_key_hash": accessKeyHash,
		"encrypted_value": encrypted, "masked_value": masked,
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			return nil, errors.New("secret key name is required")
		}
		updates["name"] = name
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if req.Remark != nil {
		updates["remark"] = *req.Remark
	}
	if err := dao.db.WithContext(ctx).Model(&model.SecretKey{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return nil, err
	}
	updated, err := dao.GetByID(ctx, id)
	if updated != nil {
		updated.AccessSecret = newSecret
	}
	return updated, err
}

func (dao *SecretKeyDAO) Delete(ctx context.Context, id uint64) error {
	return dao.db.WithContext(ctx).Delete(&model.SecretKey{}, id).Error
}

func (dao *SecretKeyDAO) AuthenticateServerCredential(ctx context.Context, accessKey, accessSecret string) (*apis.DeveloperAccessCredential, error) {
	hash := hashAccessKey(accessKey)
	var row model.SecretKey
	if err := dao.db.WithContext(ctx).
		Where("access_key_hash = ? AND key_type = ? AND status = ?", hash, model.SecretKeyTypeDeveloperAccess, true).
		First(&row).Error; err != nil {
		return nil, ErrDeveloperCredentialInvalid
	}
	credential, err := decodeDeveloperCredential(row.EncryptedValue)
	if err != nil || !credential.ServerEnabled {
		return nil, ErrDeveloperCredentialInvalid
	}
	if credential.ExpiresAt != nil && !credential.ExpiresAt.After(time.Now()) {
		return nil, ErrDeveloperCredentialInvalid
	}
	expected, err := developerSecretDigest(credential)
	if err != nil {
		return nil, ErrDeveloperCredentialInvalid
	}
	provided := sha256.Sum256([]byte(strings.TrimSpace(accessSecret)))
	if subtle.ConstantTimeCompare(expected[:], provided[:]) != 1 {
		return nil, ErrDeveloperCredentialInvalid
	}
	return &credential, nil
}

func normalizeSecretKeyType(keyType string) string {
	keyType = strings.ToLower(strings.TrimSpace(keyType))
	if keyType == "" {
		return model.SecretKeyTypeDeveloperAccess
	}
	return keyType
}
