package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	_ "modernc.org/sqlite"
	"strconv"
	"strings"
	"testing"
)

func TestCanonicalSnapshotEncodingIsUnambiguous(t *testing.T) {
	if bytes.Equal(canonicalRecord("t", []string{"a", "b"}, []any{"ab", "c"}), canonicalRecord("t", []string{"a", "b"}, []any{"a", "bc"})) {
		t.Fatal("value boundaries collide")
	}
	if bytes.Equal(canonicalRecord("t", []string{"a"}, []any{nil}), canonicalRecord("t", []string{"a"}, []any{""})) {
		t.Fatal("NULL and empty text collide")
	}
	if bytes.Equal(canonicalRecord("t", []string{"a"}, []any{"1"}), canonicalRecord("t", []string{"a"}, []any{int64(1)})) {
		t.Fatal("text and integer collide")
	}
	if bytes.Equal(canonicalRecord("t", []string{"a"}, []any{[]byte("1")}), canonicalRecord("t", []string{"a"}, []any{"1"})) {
		t.Fatal("blob and text collide")
	}
	if bytes.Equal(canonicalRecord("one", []string{"a"}, []any{"1"}), canonicalRecord("two", []string{"a"}, []any{"1"})) {
		t.Fatal("table identities collide")
	}
	if bytes.Equal(canonicalRecord("t", []string{"a"}, []any{"1"}), canonicalRecord("t", []string{"b"}, []any{"1"})) {
		t.Fatal("column identities collide")
	}
}

func canonicalRecord(table string, columns []string, values []any) []byte {
	var out bytes.Buffer
	writeFrame(&out, 'T', []byte(table))
	writeFrame(&out, 'C', []byte(strconv.Itoa(len(columns))))
	for _, column := range columns {
		writeFrame(&out, 'c', []byte(column))
	}
	writeFrame(&out, 'R', nil)
	for _, value := range values {
		tag, encoded := canonicalValue(value)
		writeFrame(&out, tag, encoded)
	}
	return out.Bytes()
}

func writeFrame(h interface{ Write([]byte) (int, error) }, tag byte, value []byte) {
	var length [10]byte
	n := binary.PutUvarint(length[:], uint64(len(value)))
	_, _ = h.Write([]byte{tag})
	_, _ = h.Write(length[:n])
	_, _ = h.Write(value)
}

func canonicalValue(value any) (byte, []byte) {
	switch x := value.(type) {
	case nil:
		return 'N', nil
	case string:
		return 'S', []byte(x)
	case []byte:
		return 'B', x
	case int64:
		return 'I', []byte(strconv.FormatInt(x, 10))
	case float64:
		return 'R', []byte(strconv.FormatFloat(x, 'g', -1, 64))
	default:
		return 'X', []byte(fmt.Sprintf("%T:%v", value, value))
	}
}

func logicalDatabaseDigest(t *testing.T, dir string) string {
	t.Helper()
	db, err := sql.Open("sqlite", dir+"/tpahub.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	h := sha256.New()
	for _, table := range tables {
		colsRows, err := db.Query(`PRAGMA table_info("` + table + `")`)
		if err != nil {
			t.Fatal(err)
		}
		var cols []string
		for colsRows.Next() {
			var cid int
			var name, typ string
			var notNull, pk int
			var defaultValue any
			if err := colsRows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
				t.Fatal(err)
			}
			cols = append(cols, name)
		}
		colsRows.Close()
		writeFrame(h, 'T', []byte(table))
		writeFrame(h, 'C', []byte(strconv.Itoa(len(cols))))
		for _, col := range cols {
			writeFrame(h, 'c', []byte(col))
		}
		quoted := make([]string, len(cols))
		for i, col := range cols {
			quoted[i] = `"` + col + `"`
		}
		data, err := db.Query(`SELECT ` + strings.Join(quoted, ",") + ` FROM "` + table + `" ORDER BY ` + strings.Join(quoted, ","))
		if err != nil {
			t.Fatal(err)
		}
		for data.Next() {
			values := make([]any, len(cols))
			pointers := make([]any, len(cols))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := data.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			writeFrame(h, 'R', nil)
			for _, value := range values {
				tag, encoded := canonicalValue(value)
				writeFrame(h, tag, encoded)
			}
		}
		data.Close()
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
