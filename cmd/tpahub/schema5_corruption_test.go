package main

import (
	"database/sql"
	"github.com/eugen252009/tpahub/internal/store"
	"path/filepath"
)

// Schema 5 normal writes require ownership. These defense-in-depth tests
// deliberately bypass FK enforcement to model corruption, not a supported
// pre-release repository. All runtime authorization assertions remain real.
func insertCorruptUnownedRepository(s *server, r store.Repository) error {
	db, err := sql.Open("sqlite", filepath.Join(s.cfg.DataDir, "tpahub.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO repositories(id,account_id,name,visibility,created_at) VALUES(?,'',?,'public',?)`, r.ID, r.Name, r.CreatedAt)
	return err
}
