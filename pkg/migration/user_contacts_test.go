package migration

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

func TestUserContactsAreSeededFromTheColumnsOnceAndNeverOverwritten(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:contacts?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserContact{}, &model.Role{}))
	require.NoError(t, db.Create(&model.User{Username: "a", PasswordHash: "x", Email: "a@example.com", Phone: "123"}).Error)
	require.NoError(t, db.Create(&model.User{Username: "b", PasswordHash: "x"}).Error)
	require.NoError(t, seedUserContacts(db))
	require.NoError(t, seedUserContacts(db)) // the second run adds nothing
	var contacts []model.UserContact
	require.NoError(t, db.Order("type").Find(&contacts).Error)
	require.Len(t, contacts, 2)
	assert.Equal(t, "email", contacts[0].Type)
	assert.Equal(t, "phone", contacts[1].Type)

	// an administrator's edit stays: the user has an email contact already, so the column is not copied again
	require.NoError(t, db.Model(&model.UserContact{}).Where("type = ?", "email").Update("value", "edited@example.com").Error)
	require.NoError(t, seedUserContacts(db))
	var email model.UserContact
	require.NoError(t, db.Where("type = ?", "email").First(&email).Error)
	assert.Equal(t, "edited@example.com", email.Value)
}
