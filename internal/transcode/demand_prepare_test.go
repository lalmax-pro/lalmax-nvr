package transcode

import (
    "context"
    "testing"
)

func TestPrepareDemandCapabilityRejectsInvalidIntent(t *testing.T) {
    service, manager, executor, job := setupDemandLeaseService(t)
    if err := service.PrepareDemandCapability(context.Background(), job.ID, job.Generation); err != nil {
        t.Fatalf("already approved demand capability rejected: %v",err)
    }
    if err := service.PrepareDemandCapability(context.Background(),job.ID,job.Generation+1);err==nil {
        t.Fatal("accepted incorrect generation")
    }
    service.config.Enabled = false
    if err := service.PrepareDemandCapability(context.Background(),job.ID,job.Generation);err==nil {
        t.Fatal("prepared disabled transcoding")
    }
    if executor.starts.Load()!=0 {
        t.Fatal("capability validation must not start a media process")
    }
    if _, active := manager.Status(job.ID);active {
        t.Fatal("capability validation created a media owner")
    }
}
