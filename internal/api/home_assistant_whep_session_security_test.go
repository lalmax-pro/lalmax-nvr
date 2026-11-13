package api

import (
    "bytes"
    "encoding/base64"
    "strings"
    "testing"
    "time"
)

func TestWHEPSessionSealsBearerCredentialConfidentially(t *testing.T) {
    h:=&Handler{homeAssistantWHEPKey:[]byte("isolated-32-byte-secret-for-tests")}
    original:=homeAssistantWHEPSession{
        StreamID:"source",InstanceID:"instance",AppName:"live",Derived:true,
        URL:"http://127.0.0.1:1290/webrtc/whep?streamid=physical&token=private-bearer",
        Expires:time.Now().Add(time.Hour).Unix(),
    }
    token1:=h.sealHomeAssistantSession(original)
    token2:=h.sealHomeAssistantSession(original)
    if token1=="" || token2=="" || token1==token2 { t.Fatal("missing randomized authenticated encryption") }
    encoded,err:=base64.RawURLEncoding.DecodeString(token1)
    if err!=nil{t.Fatal(err)}
    if bytes.Contains(encoded,[]byte("private-bearer")) || strings.Contains(token1,"private-bearer") {
        t.Fatal("opaque WHEP token exposed media credential")
    }
    decoded,err:=h.openHomeAssistantSession(token1)
    if err!=nil || decoded!=original {t.Fatalf("session could not be opened: %+v, %v",decoded,err)}
    corrupted:=[]byte(token1)
    if corrupted[0]=='A'{corrupted[0]='B'}else{corrupted[0]='A'}
    if _,err=h.openHomeAssistantSession(string(corrupted));err==nil {
        t.Fatal("tampered session accepted")
    }
    h.homeAssistantWHEPKey=[]byte("different-process-key")
    if _,err=h.openHomeAssistantSession(token1);err==nil {
        t.Fatal("session decrypted with wrong key")
    }
}
