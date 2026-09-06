package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// LogicalDigest returns a deterministic digest of all non-SQLite tables. It
// deliberately excludes schema objects: structural compatibility is checked
// separately by Open/Migrate.
func LogicalDigest(path string) (string, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return "", err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return "", err
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return "", err
		}
		tables = append(tables, n)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	rows.Close()
	sort.Strings(tables)
	h := sha256.New()
	for _, table := range tables {
		cols, pk, err := digestColumns(db, table)
		if err != nil {
			return "", err
		}
		put(h, table)
		for _, c := range cols {
			put(h, c)
		}
		order := pk
		if len(order) == 0 {
			order = cols
		}
		quoted := make([]string, len(order))
		for i, c := range order {
			quoted[i] = quote(c)
		}
		q := `SELECT ` + strings.Join(mapQuote(cols), ",") + ` FROM ` + quote(table)
		if len(quoted) > 0 {
			q += ` ORDER BY ` + strings.Join(quoted, ",")
		}
		rr, err := db.Query(q)
		if err != nil {
			return "", err
		}
		for rr.Next() {
			vals := make([]any, len(cols))
			ptr := make([]any, len(cols))
			for i := range vals {
				ptr[i] = &vals[i]
			}
			if err := rr.Scan(ptr...); err != nil {
				rr.Close()
				return "", err
			}
			put(h, "row")
			for _, v := range vals {
				putValue(h, v)
			}
		}
		if err := rr.Err(); err != nil {
			rr.Close()
			return "", err
		}
		rr.Close()
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func digestColumns(db *sql.DB, table string) ([]string, []string, error) {
	r, err := db.Query(`PRAGMA table_xinfo(` + quote(table) + `)`)
	if err != nil {
		return nil, nil, err
	}
	defer r.Close()
	type col struct {
		name string
		pk   int
	}
	var cs []col
	for r.Next() {
		var cid int
		var name, typ string
		var notnull, pk, hidden int
		var d any
		if err := r.Scan(&cid, &name, &typ, &notnull, &d, &pk, &hidden); err != nil {
			return nil, nil, err
		}
		if hidden == 0 {
			cs = append(cs, col{name, pk})
		}
	}
	if err := r.Err(); err != nil {
		return nil, nil, err
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].name < cs[j].name })
	// Column identity is canonicalized by name; PK ordering remains ordinal.
	cols := make([]string, len(cs))
	for i, c := range cs {
		cols[i] = c.name
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].pk == 0 || cs[j].pk == 0 {
			return cs[i].pk > cs[j].pk
		}
		return cs[i].pk < cs[j].pk
	})
	var pk []string
	for _, c := range cs {
		if c.pk > 0 {
			pk = append(pk, c.name)
		}
	}
	return cols, pk, nil
}
func mapQuote(a []string) []string {
	b := make([]string, len(a))
	for i, v := range a {
		b[i] = quote(v)
	}
	return b
}
func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func put(h interface{ Write([]byte) (int, error) }, s string) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(len(s)))
	h.Write(b[:])
	h.Write([]byte(s))
}
func putValue(h interface{ Write([]byte) (int, error) }, v any) {
	switch x := v.(type) {
	case nil:
		put(h, "N")
	case int64:
		put(h, "I")
		put(h, fmt.Sprint(x))
	case float64:
		put(h, "R")
		put(h, fmt.Sprintf("%.17g", x))
	case []byte:
		put(h, "B")
		put(h, string(x))
	case string:
		put(h, "T")
		put(h, x)
	default:
		put(h, fmt.Sprintf("X:%T", x))
		put(h, fmt.Sprint(x))
	}
}
