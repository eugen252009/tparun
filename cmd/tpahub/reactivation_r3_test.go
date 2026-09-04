package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
)

func TestReactivationRestoresReaderAndNewWorkerAuthorization(t *testing.T) {
	s, h := testServer(t)
	s.ctx = context.Background()
	s.runner = copyingPublicationRunner{}
	repo := "56565656565656565656565656565656"
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: repo, AccountID: "a", Name: "reactivation", Visibility: "private", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.CreateCredential(context.Background(), store.ReaderCredential{ID: "reader", RepositoryID: repo, Name: "reader", CreatedAt: "now"}, store.HashToken("token")); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.cfg.DataDir, "repos", repo)
	os.MkdirAll(root, 0755)
	os.WriteFile(filepath.Join(root, "package.deb"), []byte("payload"), 0644)
	read := func() int {
		req, _ := http.NewRequest(http.MethodGet, h.URL+"/r/"+repo+"/package.deb", nil)
		req.SetBasicAuth("reader", "token")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if read() != http.StatusOK {
		t.Fatal("active reader denied")
	}
	if err := s.db.SetAccountStatus(context.Background(), "a", "suspended"); err != nil {
		t.Fatal(err)
	}
	if read() == http.StatusOK {
		t.Fatal("suspended reader accepted")
	}
	incoming := filepath.Join(s.cfg.DataDir, "incoming", "suspended-job")
	os.MkdirAll(incoming, 0700)
	os.WriteFile(filepath.Join(incoming, "new.deb"), []byte("new"), 0600)
	job := store.Job{ID: "suspended-job", RepositoryID: repo, State: "running", IncomingPath: incoming, CreatedAt: "now"}
	s.db.CreateJob(context.Background(), job)
	s.process(job)
	stored, _ := s.db.Job(context.Background(), job.ID)
	if stored.State != "failed" {
		t.Fatalf("suspended worker state=%s", stored.State)
	}
	if err := s.db.SetAccountStatus(context.Background(), "a", store.AccountStatusActive); err != nil {
		t.Fatal(err)
	}
	if read() != http.StatusOK {
		t.Fatal("existing reader did not recover")
	}
	incoming = filepath.Join(s.cfg.DataDir, "incoming", "reactivated-job")
	os.MkdirAll(incoming, 0700)
	os.WriteFile(filepath.Join(incoming, "new.deb"), []byte("new"), 0600)
	next := store.Job{ID: "reactivated-job", RepositoryID: repo, State: "running", IncomingPath: incoming, CreatedAt: "later"}
	s.db.CreateJob(context.Background(), next)
	s.process(next)
	stored, _ = s.db.Job(context.Background(), next.ID)
	if stored.State != "succeeded" {
		t.Fatalf("reactivated worker state=%s error=%s", stored.State, stored.Error)
	}
	if err := s.db.SetAccountStatus(context.Background(), "a", "suspended"); err != nil {
		t.Fatal(err)
	}
	for _, capability := range []commerce.CapabilityID{CapabilityRepositoryReadPrivate, CapabilityRepositoryPublish} {
		grant, err := s.db.GetGrant(context.Background(), "a", commerce.ProductTPAHub, capability, s.currentTime())
		if err != nil {
			t.Fatal(err)
		}
		grant.Enabled = false
		if err = s.db.PutGrant(context.Background(), grant); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.SetAccountStatus(context.Background(), "a", store.AccountStatusActive); err != nil {
		t.Fatal(err)
	}
	if read() == http.StatusOK {
		t.Fatal("reactivation restored reader access after grant disablement")
	}
	incoming = filepath.Join(s.cfg.DataDir, "incoming", "reactivated-without-grant")
	if err := os.MkdirAll(incoming, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incoming, "new.deb"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	withoutGrant := store.Job{ID: "reactivated-without-grant", RepositoryID: repo, State: "running", IncomingPath: incoming, CreatedAt: "later"}
	if err := s.db.CreateJob(context.Background(), withoutGrant); err != nil {
		t.Fatal(err)
	}
	s.process(withoutGrant)
	stored, _ = s.db.Job(context.Background(), withoutGrant.ID)
	if stored.State != "failed" {
		t.Fatalf("reactivated worker without grant state=%s", stored.State)
	}
}
