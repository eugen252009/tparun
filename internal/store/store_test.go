package store

import (
	"context"
	"testing"
)

func TestRestartMarksRunningJobsFailed(t *testing.T) {
	d := t.TempDir()
	s, e := Open(d)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	if e = s.CreateRepository(ctx, Repository{ID: "r", Name: "r", CreatedAt: "now"}); e != nil {
		t.Fatal(e)
	}
	if e = s.CreateJob(ctx, Job{ID: "j", RepositoryID: "r", State: "running", IncomingPath: "x", CreatedAt: "now"}); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = Open(d)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	j, e := s.Job(ctx, "j")
	if e != nil || j.State != "failed" || j.Error != "interrupted by service restart" {
		t.Fatalf("%+v %v", j, e)
	}
}
func TestRepositoryAndJobPersistence(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	r := Repository{ID: "r", Name: "demo", CreatedAt: "now"}
	if e = s.CreateRepository(ctx, r); e != nil {
		t.Fatal(e)
	}
	j := Job{ID: "j", RepositoryID: "r", State: "queued", IncomingPath: "/tmp/j", CreatedAt: "now"}
	if e = s.CreateJob(ctx, j); e != nil {
		t.Fatal(e)
	}
	got, e := s.Claim(ctx)
	if e != nil || got.ID != "j" || got.State != "running" {
		t.Fatalf("claim: %+v %v", got, e)
	}
	if e = s.Finish(ctx, "j", "failed", "bad"); e != nil {
		t.Fatal(e)
	}
	got, e = s.Job(ctx, "j")
	if e != nil || got.State != "failed" || got.Error != "bad" {
		t.Fatalf("job: %+v %v", got, e)
	}
}
