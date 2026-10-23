package rdb

import (
	"gorm.io/gorm"
	"sync"
)

// existingTables remembers, per database (its Config is shared by every session of it) and table, that the table is there.
var existingTables sync.Map

type tableKey struct {
	config *gorm.Config
	table  string
}

// TableExists says whether the table of a model exists. A table that exists is remembered: asking the database costs a
// statement on its (single, with SQLite) connection, and the signal runs ask it for every coin in every round. A table
// that does not exist yet is asked again next time, so it is picked up once it is created.
func TableExists(db *gorm.DB, model any) bool {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(model); err != nil {
		return db.Migrator().HasTable(model)
	}
	key := tableKey{config: db.Config, table: stmt.Table}
	if _, ok := existingTables.Load(key); ok {
		return true
	}
	if db.Migrator().HasTable(model) {
		existingTables.Store(key, true)
		return true
	}
	return false
}
