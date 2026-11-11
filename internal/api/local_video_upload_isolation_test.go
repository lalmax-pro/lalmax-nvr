package api

import (
 "os"
 "testing"
)

// Uploads with the same logical ID must never share a physical destination.
func TestLocalVideoConcurrentUploadPathsAreRequestOwned(t *testing.T) {
 dir := t.TempDir()
 first, err := newLocalVideoUploadFile(dir, "camera-1", ".mp4")
 if err != nil {t.Fatal(err)}
 winner := first.Name()
 if _,err:=first.Write([]byte("winning-media"));err!=nil {t.Fatal(err)}
 if err:=first.Close();err!=nil {t.Fatal(err)}
 second,err:=newLocalVideoUploadFile(dir,"camera-1",".mp4")
 if err!=nil {t.Fatal(err)}
 loser:=second.Name()
 if err:=second.Close();err!=nil {t.Fatal(err)}
 if winner==loser {t.Fatal("same stream ID reused destination")}
 if err:=os.Remove(loser);err!=nil {t.Fatal(err)}
 got,err:=os.ReadFile(winner)
 if err!=nil {t.Fatalf("losing upload deleted committed file: %v",err)}
 if string(got)!="winning-media" {t.Fatalf("winner file changed: %q",got)}
}
