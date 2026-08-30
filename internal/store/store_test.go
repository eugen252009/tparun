package store

import("context";"testing")
func TestRepositoryAndJobPersistence(t *testing.T){s,e:=Open(t.TempDir());if e!=nil{t.Fatal(e)};defer s.Close();ctx:=context.Background();r:=Repository{ID:"r",Name:"demo",CreatedAt:"now"};if e=s.CreateRepository(ctx,r);e!=nil{t.Fatal(e)};j:=Job{ID:"j",RepositoryID:"r",State:"queued",IncomingPath:"/tmp/j",CreatedAt:"now"};if e=s.CreateJob(ctx,j);e!=nil{t.Fatal(e)};got,e:=s.Claim(ctx);if e!=nil||got.ID!="j"||got.State!="running"{t.Fatalf("claim: %+v %v",got,e)};if e=s.Finish(ctx,"j","failed","bad");e!=nil{t.Fatal(e)};got,e=s.Job(ctx,"j");if e!=nil||got.State!="failed"||got.Error!="bad"{t.Fatalf("job: %+v %v",got,e)}}
