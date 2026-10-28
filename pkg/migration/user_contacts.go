package migration

import (
	"gorm.io/gorm"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// seedUserContacts moves the email and phone columns of the users into contact rows (type email / phone), so the contacts of
// every user are rows. It adds a contact only when the user has none of that type yet, so it can run on every migration and never
// duplicates or overwrites what an administrator has edited since.
func seedUserContacts(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.UserContact{}) {
		return nil
	}
	var users []model.User
	if err := db.Unscoped().Select("id", "email", "phone").Where("email <> '' OR phone <> ''").Find(&users).Error; err != nil {
		return err
	}
	for _, u := range users {
		for typ, value := range map[string]string{"email": u.Email, "phone": u.Phone} {
			if value == "" {
				continue
			}
			var n int64
			if err := db.Model(&model.UserContact{}).Where("user_id = ? AND type = ?", u.ID, typ).Count(&n).Error; err != nil {
				return err
			}
			if n > 0 {
				continue
			}
			if err := db.Create(&model.UserContact{UserID: u.ID, Type: typ, Value: value}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
