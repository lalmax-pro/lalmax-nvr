package api

import (
    "context"
    "net/http"
    "net/http/httptest"
    "net/url"
    "strings"
    "testing"
)

func TestDerivedWHEPDoesNotForwardCallerAuthorization(t *testing.T) {
    seenAuthorization := ""
    upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request) {
        seenAuthorization = r.Header.Get("Authorization")
        if r.URL.Query().Get("token")!="scoped-media-secret" {
            t.Error("derived media URL lost its scoped token")
        }
        w.WriteHeader(http.StatusNoContent)
    }))
    defer upstream.Close()
    target, err := url.Parse(upstream.URL+"/webrtc/whep?streamid=private&token=scoped-media-secret")
    if err!=nil {t.Fatal(err)}
    handler:=&Handler{}
    request:=httptest.NewRequest(http.MethodPost,"/",strings.NewReader("v=0\r\n")).WithContext(context.Background())
    request.Header.Set("Authorization","Basic administrator-password")
    response,err:=handler.requestHomeAssistantWHEP(request,http.MethodPost,target,strings.NewReader("v=0\r\n"),false)
    if err!=nil{t.Fatal(err)}
    response.Body.Close()
    if seenAuthorization!="" {
        t.Fatal("derived WHEP request forwarded administrator credentials")
    }
}
