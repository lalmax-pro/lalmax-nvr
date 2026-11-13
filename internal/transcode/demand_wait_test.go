package transcode

import (
    "context"
    "testing"
    "time"
)

func TestDemandWaitReadyNeedsActualMedia(t *testing.T) {
    service, _, executor, job := setupDemandLeaseService(t)
    executor.ready.Store(false)
    lease, _, err := service.AcquireDemand(context.Background(), job.ID, job.Generation)
    if err != nil { t.Fatal(err) }
    ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
    defer cancel()
    if _, err = service.WaitDemandReady(ctx, lease.Token); err == nil {
        t.Fatal("accepted process startup without playable media")
    }
    if err = service.ReleaseDemand(context.Background(), lease.Token); err != nil { t.Fatal(err) }
}

func TestDemandWaitReadyReturnsRunningOutput(t *testing.T) {
    service, _, _, job := setupDemandLeaseService(t)
    lease, started, err := service.AcquireDemand(context.Background(), job.ID, job.Generation)
    if err != nil { t.Fatal(err) }
    ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
    defer cancel()
    ready, err := service.WaitDemandReady(ctx, lease.Token)
    if err != nil { t.Fatalf("wait ready: %v", err) }
    if ready.State != Running || ready.VariantID != started.VariantID {
        t.Fatalf("invalid ready target: %+v", ready)
    }
    if err = service.ReleaseDemand(context.Background(), lease.Token); err != nil { t.Fatal(err) }
    if _, err = service.WaitDemandReady(context.Background(), lease.Token); err == nil {
        t.Fatal("released lease remained usable")
    }
}
