package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
	_ "modernc.org/sqlite"
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

func TestNanoFloorChangesWithoutMutatingRawGrant(t *testing.T) {
	raw := commerce.Grant{CapabilityID: CapabilityRepositoryPublish, Limits: commerce.Limits{StorageBytes: 3, EgressBytes: 3}, Source: "test"}
	floorA := commerce.Grant{CapabilityID: CapabilityRepositoryPublish, OfferID: "nano-a", Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 5, EgressBytes: 5}, ValidFrom: time.Unix(1, 0)}
	floorB := floorA
	floorB.OfferID, floorB.Limits.StorageBytes, floorB.Limits.EgressBytes = "nano-b", 10, 10
	gotA, err := resolveNanoGrant(raw, nil, floorA)
	if err != nil || gotA.Limits.StorageBytes != 5 || gotA.Limits.EgressBytes != 5 {
		t.Fatalf("floor A=%+v err=%v", gotA, err)
	}
	gotB, err := resolveNanoGrant(raw, nil, floorB)
	if err != nil || gotB.Limits.StorageBytes != 10 || gotB.Limits.EgressBytes != 10 {
		t.Fatalf("floor B=%+v err=%v", gotB, err)
	}
	if raw.Limits.StorageBytes != 3 || raw.Limits.EgressBytes != 3 {
		t.Fatalf("raw grant mutated=%+v", raw)
	}
}

func TestNanoFloorLeavesPersistedHistoryUntouched(t *testing.T) {
	s, _ := testServer(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	offer := tpahubproduct.NanoOffer()
	purchase := store.BillingPurchase{ID: "nano-floor-history", AccountID: "a", ProductID: string(offer.ProductID), PlanCode: offer.ID, OfferVersion: offer.Version, RateCardID: "nano-history", RateCardMode: "test", TaxClassification: "saas-personal", Currency: offer.Currency, AmountMinor: offer.AmountMinor, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: now.AddDate(1, 0, 0).Format(time.RFC3339Nano), CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
	if _, err := s.db.CreatePendingPurchase(ctx, purchase, purchase.CreatedAt, offer.Grants...); err != nil {
		t.Fatal(err)
	}
	if err := s.db.PutGrant(ctx, commerce.Grant{CustomerID: "a", ProductID: tpahubproduct.ProductID, CapabilityID: CapabilityRepositoryPublish, OfferID: "historical", Enabled: true, ValidFrom: now.Add(-time.Hour), Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 3, EgressBytes: 3}, Source: "test", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	before := logicalDatabaseDigest(t, s.cfg.DataDir)
	raw := commerce.Grant{CapabilityID: CapabilityRepositoryPublish, Limits: commerce.Limits{StorageBytes: 3, EgressBytes: 3}}
	for _, floor := range []int64{5, 10, 120} {
		if _, err := resolveNanoGrant(raw, nil, commerce.Grant{CapabilityID: raw.CapabilityID, Limits: commerce.Limits{StorageBytes: floor, EgressBytes: floor}}); err != nil {
			t.Fatal(err)
		}
	}
	after := logicalDatabaseDigest(t, s.cfg.DataDir)
	if before != after {
		t.Fatalf("floor resolution mutated persisted database: before=%s after=%s", before, after)
	}
}

func TestNanoFloorPreservesHigherAndRejectsInvalid(t *testing.T) {
	floor := commerce.Grant{CapabilityID: CapabilityRepositoryDownload, Limits: commerce.Limits{StorageBytes: 5, EgressBytes: 10}}
	higher := commerce.Grant{CapabilityID: floor.CapabilityID, Limits: commerce.Limits{StorageBytes: 100, EgressBytes: 100}}
	got, err := resolveNanoGrant(higher, nil, floor)
	if err != nil || got.Limits != higher.Limits {
		t.Fatalf("higher grant changed=%+v err=%v", got, err)
	}
	for _, invalid := range []commerce.Grant{{Limits: commerce.Limits{StorageBytes: -1, EgressBytes: 10}}, {Limits: commerce.Limits{StorageBytes: 10, EgressBytes: -1}}, {Limits: commerce.Limits{StorageBytes: -1, EgressBytes: -1}}} {
		if _, err = resolveNanoGrant(invalid, nil, floor); !errors.Is(err, ErrInvalidEntitlement) {
			t.Fatalf("negative grant was not rejected: %+v err=%v", invalid, err)
		}
	}
	if _, err = resolveNanoGrant(commerce.Grant{}, errors.New("Nano policy unavailable"), floor); err == nil {
		t.Fatal("policy failure was softened")
	}
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
