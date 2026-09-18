// This fixture builder is compiled against frozen pre-release revisions only
// to qualify their rejection. It does not assert historical grant semantics.
package main

import (
	"database/sql"
	"fmt"
	"github.com/eugen252009/tpahub/internal/store"
	"os"
	"path/filepath"
)

func main() {
	s, err := store.Open(os.Args[1])
	if err != nil {
		panic(err)
	}
	if err = s.Close(); err != nil {
		panic(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(os.Args[1], "tpahub.db"))
	if err != nil {
		panic(err)
	}
	defer db.Close()
	if _, err = db.Exec(`INSERT INTO accounts(id,email,password_hash,status,created_at) VALUES('fixture','fixture@example.invalid','fixture-not-a-secret','active','2026-01-01T00:00:00Z')`); err != nil {
		panic(err)
	}
	var version int
	if err = db.QueryRow(`SELECT version FROM tpahub_schema`).Scan(&version); err != nil {
		panic(err)
	}
	fmt.Println(version)
}
