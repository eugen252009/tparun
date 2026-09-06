package store

import (
	"os"
	"path/filepath"
)

// openMigrated makes the migration phase explicit in legacy-fixture tests.
// Normal production startup must use Open directly.
func openMigrated(dir string) (*Store, error) {
	if _, err := os.Stat(filepath.Join(dir, "tpahub.db")); err == nil {
		if err := Migrate(dir); err != nil {
			return nil, err
		}
	}
	return Open(dir)
}
