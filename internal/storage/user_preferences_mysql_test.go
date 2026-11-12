package storage

import (
 "context"
 "fmt"
 "os"
 "testing"
 "time"
)

// Use a dedicated test-only MySQL database via LALMAX_TEST_MYSQL_DSN.
// This reproduces MySQL's default changed-rows semantics on identical UPDATEs.
func TestMySQLUserPreferencesIdempotentUpsert(t *testing.T) {
 dsn:=os.Getenv("LALMAX_TEST_MYSQL_DSN")
 if dsn=="" {t.Skip("isolated MySQL test DSN not configured")}
 ctx,cancel:=context.WithTimeout(context.Background(),30*time.Second)
 defer cancel()
 db,err:=Open(ctx,DatabaseOptions{Driver:DriverMySQL,DSN:dsn})
 if err!=nil {t.Fatal(err)}
 defer db.Close()
 if err:=db.InitUserPreferences(ctx);err!=nil {t.Fatal(err)}
 username:=fmt.Sprintf("pr91-upsert-%d",time.Now().UnixNano())
 namespace:="dashboard"
 defer func(){_,_=db.execContext(context.Background(),"DELETE FROM user_preferences WHERE username=? AND namespace=?",username,namespace)}()
 for _,value:=range []string{`{"layout":4}`,`{"layout":4}`,`{"layout":2}`,`{"layout":2}`} {
  if err:=db.SetUserPreference(ctx,username,namespace,value);err!=nil {t.Fatalf("upsert %s: %v",value,err)}
 }
 value,found,err:=db.GetUserPreference(ctx,username,namespace)
 if err!=nil || !found || value!=`{"layout":2}` {t.Fatalf("final preference: value=%q found=%v err=%v",value,found,err)}
}
