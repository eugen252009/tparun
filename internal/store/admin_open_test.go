package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenForAdminRequiresExistingHubDatabase(t *testing.T) {
	for _, name := range []string{"missing", "empty", "unrelated"} {
		dir := t.TempDir()
		if name != "missing" {
			if err := os.WriteFile(filepath.Join(dir, "tpahub.db"), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if name == "unrelated" {
			db, err := sql.Open("sqlite", filepath.Join(dir, "tpahub.db"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`CREATE TABLE unrelated(value TEXT)`); err != nil {
				t.Fatal(err)
			}
			db.Close()
		}
		if got, err := OpenForAdmin(dir); err == nil || got != nil {
			t.Fatalf("%s accepted: db=%v err=%v", name, got, err)
		}
		if name == "empty" {
			info, err := os.Stat(filepath.Join(dir, "tpahub.db"))
			if err != nil || info.Size() != 0 {
				t.Fatalf("empty DB changed: info=%v err=%v", info, err)
			}
		}
	}
}
