package store

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func q3Frame(values ...any) []byte {
	var b bytes.Buffer
	for _, v := range values {
		putValue(&b, v)
	}
	return b.Bytes()
}
func q3Hash(table string, columns []string, rows ...[]any) []byte {
	h := sha256.New()
	put(h, table)
	for _, c := range columns {
		put(h, c)
	}
	for _, row := range rows {
		put(h, "row")
		for _, v := range row {
			putValue(h, v)
		}
	}
	return h.Sum(nil)
}
func TestDigestQ3B2Framing(t *testing.T) {
	t.Run("types", func(t *testing.T) {
		values := []any{nil, int64(0), float64(0), "", []byte{}}
		for i := range values {
			for j := range values {
				if i != j && bytes.Equal(q3Frame(values[i]), q3Frame(values[j])) {
					t.Fatalf("type collision %d/%d", i, j)
				}
			}
		}
		if bytes.Equal(q3Frame(int64(1)), q3Frame("1")) || bytes.Equal(q3Frame(int64(1)), q3Frame(float64(1))) || bytes.Equal(q3Frame("1"), q3Frame([]byte("1"))) {
			t.Fatal("cross-type collision")
		}
	})
	t.Run("boundaries", func(t *testing.T) {
		if bytes.Equal(q3Frame("ab", "c"), q3Frame("a", "bc")) || bytes.Equal(q3Frame("", "a"), q3Frame("a", "")) {
			t.Fatal("value boundary collision")
		}
		if bytes.Equal(q3Hash("ab", []string{"c"}, []any{"x"}), q3Hash("a", []string{"bc"}, []any{"x"})) {
			t.Fatal("table/column boundary collision")
		}
		if bytes.Equal(q3Hash("t", []string{"a", "b"}, []any{"x", "y"}), q3Hash("t", []string{"a", "b"}, []any{"y", "x"})) {
			t.Fatal("column identity collision")
		}
		if bytes.Equal(q3Hash("t", []string{"a"}, []any{"ab"}, []any{"c"}), q3Hash("t", []string{"a"}, []any{"a"}, []any{"bc"})) {
			t.Fatal("row boundary collision")
		}
		if bytes.Equal(q3Hash("ab", []string{"c"}, []any{"x"}), q3Hash("a", []string{"bc"}, []any{"x"})) {
			t.Fatal("table identity collision")
		}
	})
}

func TestDigestQ3B2SQLiteValuesAndOrdering(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.db")
	b := filepath.Join(dir, "b.db")
	for _, path := range []string{a, b} {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(`CREATE TABLE values_q3(id INTEGER PRIMARY KEY, real_value REAL, blob_value BLOB, nullable TEXT);`)
		if err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
	dba, _ := sql.Open("sqlite", a)
	dbb, _ := sql.Open("sqlite", b)
	if _, err := dba.Exec(`INSERT INTO values_q3 VALUES(2,1.0,X'00FF0A',''); INSERT INTO values_q3 VALUES(1,0.0,X'31',NULL);`); err != nil {
		t.Fatal(err)
	}
	if _, err := dbb.Exec(`INSERT INTO values_q3 VALUES(1,0.0,X'31',NULL); INSERT INTO values_q3 VALUES(2,1.0,X'00FF0A','');`); err != nil {
		t.Fatal(err)
	}
	dba.Close()
	dbb.Close()
	da, err := LogicalDigest(a)
	if err != nil {
		t.Fatal(err)
	}
	dbb2, err := LogicalDigest(b)
	if err != nil {
		t.Fatal(err)
	}
	if da != dbb2 {
		t.Fatalf("insertion order changed digest: %s != %s", da, dbb2)
	}
	for i := 0; i < 3; i++ {
		d, err := LogicalDigest(a)
		if err != nil {
			t.Fatal(err)
		}
		if d != da {
			t.Fatal("reopen digest changed")
		}
	}
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
}

func TestDigestQ3B2Multiplicity(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.db")
	b := filepath.Join(dir, "b.db")
	for _, p := range []string{a, b} {
		db, _ := sql.Open("sqlite", p)
		_, err := db.Exec(`CREATE TABLE duplicate_q3(value TEXT); INSERT INTO duplicate_q3 VALUES('x');`)
		if err != nil {
			t.Fatal(err)
		}
		if p == b {
			_, err = db.Exec(`INSERT INTO duplicate_q3 VALUES('x')`)
			if err != nil {
				t.Fatal(err)
			}
		}
		db.Close()
	}
	da, _ := LogicalDigest(a)
	db, _ := LogicalDigest(b)
	if da == db {
		t.Fatal("row multiplicity lost")
	}
}
