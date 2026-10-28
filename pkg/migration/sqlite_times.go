package migration

import (
	"fmt"

	"gorm.io/gorm"
)

// normalizeSQLiteTimes rewrites the datetime columns that an older run wrote with a local offset (+08:00) into UTC.
// SQLite keeps a time as text and the list filters compare that text, so rows in two zones drop out of any start/end
// range: the records existed but every filtered list showed none of them. It is a no-op on other databases and on rows
// that are already UTC, so it can run on every start.
func normalizeSQLiteTimes(db *gorm.DB) error {
	if db.Dialector.Name() != "sqlite" {
		return nil
	}
	var tables []string
	if err := db.Raw("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'").Scan(&tables).Error; err != nil {
		return err
	}
	for _, table := range tables {
		var columns []struct {
			Name string
			Type string
		}
		if err := db.Raw(fmt.Sprintf("SELECT name, type FROM pragma_table_info('%s')", table)).Scan(&columns).Error; err != nil {
			return err
		}
		for _, column := range columns {
			if column.Type != "datetime" {
				continue
			}
			stmt := fmt.Sprintf(
				"UPDATE `%[1]s` SET `%[2]s` = strftime('%%Y-%%m-%%d %%H:%%M:%%f+00:00', `%[2]s`) "+
					"WHERE `%[2]s` IS NOT NULL AND `%[2]s` NOT LIKE '%%+00:00' AND strftime('%%s', `%[2]s`) IS NOT NULL",
				table, column.Name)
			if err := db.Exec(stmt).Error; err != nil {
				return fmt.Errorf("normalize %s.%s times: %w", table, column.Name, err)
			}
		}
	}
	return nil
}
