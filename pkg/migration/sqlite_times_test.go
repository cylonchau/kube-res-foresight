package migration

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestNormalizeSQLiteTimes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE rec (id integer primary key, created_at datetime, note text)").Error; err != nil {
		t.Fatal(err)
	}
	db.Exec("INSERT INTO rec (created_at, note) VALUES ('2026-10-25 22:51:31.2537732+08:00','a'), ('2026-10-25 14:51:31.254+00:00','b'), (NULL,'c')")
	for i := 0; i < 2; i++ { // the second run changes nothing
		if err := normalizeSQLiteTimes(db); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	db.Raw("SELECT COALESCE(created_at,'') FROM rec ORDER BY id").Scan(&got)
	want := []string{"2026-10-25 14:51:31.254+00:00", "2026-10-25 14:51:31.254+00:00", ""}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d: got %q want %q", i, got[i], want[i])
		}
	}
}
