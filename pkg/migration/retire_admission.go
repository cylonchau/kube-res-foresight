package migration

import (
	"gorm.io/gorm"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// retiredAdmissionScaleKey was the switch that also kept a record for the Pods that scaling or a replacement creates. Those are not
// admissions of a deployment, an update or a creation, so they are no longer recorded and the switch is gone.
const retiredAdmissionScaleKey = "forecast.admission_record_scaling"

// retireScaleAdmissions removes the switch and the records of origin "scale" that it let in. The upgrade reports how many.
func retireScaleAdmissions(db *gorm.DB) (records int64, err error) {
	if err := db.Unscoped().Where("config_key = ?", retiredAdmissionScaleKey).Delete(&model.SysConfig{}).Error; err != nil {
		return 0, err
	}
	res := db.Where("origin = ?", "scale").Delete(&model.AdmissionDecision{})
	return res.RowsAffected, res.Error
}
