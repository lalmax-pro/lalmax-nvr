package storage

import (
    "context"
    "path/filepath"
    "testing"
)

// An existing installation has a relay_tasks table without the P5 job column.
// Initialization must upgrade in place without losing original relay tasks.
func TestLegacyRelayTasksMigrationPreservesRows(t *testing.T) {
    ctx:=context.Background()
    db,err:=New(filepath.Join(t.TempDir(),"legacy-relay.db"))
    if err!=nil{t.Fatal(err)}
    defer db.Close()
    _,err=db.execContext(ctx,`CREATE TABLE relay_tasks (
        id TEXT PRIMARY KEY,stream_id TEXT NOT NULL,target_url TEXT NOT NULL,
        status TEXT NOT NULL DEFAULT 'stopped',error_msg TEXT DEFAULT '',
        created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
        started_at DATETIME,stopped_at DATETIME
    )`)
    if err!=nil{t.Fatal(err)}
    _,err=db.execContext(ctx,`INSERT INTO relay_tasks
        (id,stream_id,target_url,status,error_msg)
        VALUES ('old-task','camera','rtmp://example.invalid/live','stopped','')`)
    if err!=nil{t.Fatal(err)}
    if err:=db.Init(ctx);err!=nil{t.Fatal(err)}
    tasks,err:=db.ListRelayTasks(ctx)
    if err!=nil{t.Fatal(err)}
    if len(tasks)!=1 || tasks[0].ID!="old-task" ||
        tasks[0].StreamID!="camera" || tasks[0].TranscodeJobID!="" {
        t.Fatalf("old relay not preserved: %+v",tasks)
    }
    if err:=db.migrateRelayTranscodeJob(ctx);err!=nil{t.Fatal(err)}
    tasks,err=db.ListRelayTasks(ctx)
    if err!=nil || len(tasks)!=1 {t.Fatalf("repeat migration changed data: %v %+v",err,tasks)}
}
