package storage

import (
 "os"
 "path/filepath"
 "strings"
 "testing"
)

func TestRecordingDiskNamePortableAndReversible(t *testing.T) {
 ids := []string{"cam-01", "32010000001327000002:34020000001320000001", "a:b", "a_b", "id~6162", "../outside", "CON", "摄像头"}
 seen := map[string]string{}
 for _, id := range ids {
  disk := recordingDiskName(id)
  if strings.ContainsAny(disk, `<>:"/\\|?*`) || disk == "." || disk == ".." { t.Fatalf("unsafe name %q",disk) }
  decoded, ok := originalRecordingID(disk)
  if !ok || decoded != id { t.Fatalf("round-trip %q: %q, %v",id,decoded,ok) }
  if previous, ok := seen[disk]; ok && previous != id {t.Fatalf("collision %q %q",previous,id)}
  seen[disk] = id
 }
 if recordingDiskName("cam-01") != "cam-01" { t.Fatal("legacy IDs must retain layout") }
 if recordingDiskName("a:b") == recordingDiskName("a_b") { t.Fatal("unsafe ID collides with safe ID") }
}

func TestGB28181StoragePaths(t *testing.T) {
 root:=t.TempDir()
 m,err:=NewManager(root)
 if err!=nil {t.Fatal(err)}
 id:="32010000001327000002:34020000001320000001"
 temp,final,err:=m.CreateSegment(id,"h264")
 if err!=nil {t.Fatal(err)}
 if _,err:=os.Stat(temp);err!=nil {t.Fatal(err)}
 if strings.Contains(filepath.Base(final),":") {t.Fatalf("invalid MP4 filename %q",final)}
 if _,err:=m.WriteFrame(temp,[]byte("test"));err!=nil {t.Fatal(err)}
 if err:=m.CloseSegment(temp,final);err!=nil {t.Fatal(err)}
 files,err:=m.ListFiles(id)
 if err!=nil || len(files)!=1 || files[0]!=final {t.Fatalf("list: %v %v",files,err)}
 entries,err:=m.ListCameraDirEntries(id)
 if err!=nil || len(entries)!=1 {t.Fatalf("entries: %v %v",entries,err)}
 if err:=m.DeleteCameraDir(id);err!=nil {t.Fatal(err)}
 if _,err:=os.Stat(filepath.Dir(final));!os.IsNotExist(err) {t.Fatalf("directory not deleted: %v",err)}
}
