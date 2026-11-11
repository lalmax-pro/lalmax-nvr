package transcode

import (
 "context"
 "sync"
 "testing"
 "time"
)

func setupDemandLeaseService(t *testing.T) (*Service,*Manager,*fakeExecutor,Job) {
 t.Helper()
 ctx:=context.Background()
 p:=testProfile(t)
 j:=managerJob(t,"leased",p)
 j.RunPolicy=OnDemand
 executor:=&fakeExecutor{}
 executor.ready.Store(true)
 manager:=NewManager(ctx,executor,2)
 probes:=NewProbeService(nil)
 cfg:=Config{Enabled:true}
 fingerprint,err:=ProbeFingerprint(cfg.Tools(),p)
 if err!=nil{t.Fatal(err)}
 probes.reports[p.ID]=CapabilityReport{CanEnable:true,Fingerprint:fingerprint,CheckedAt:time.Now()}
 service:=NewService(&recoveryRepository{profile:p,job:j},manager,probes,cfg)
 t.Cleanup(func(){_ = manager.Close(context.Background())})
 return service,manager,executor,j
}

func TestDemandLeaseSharesOneProcessAndReleasesLastOwner(t *testing.T){
 service,manager,executor,job:=setupDemandLeaseService(t)
 const n=8
 type output struct{lease DemandLease;runtime Runtime;err error}
 results:=make(chan output,n)
 var wg sync.WaitGroup
 for i:=0;i<n;i++{
  wg.Add(1)
  go func(){defer wg.Done();l,r,e:=service.AcquireDemand(context.Background(),job.ID,job.Generation);results<-output{l,r,e}}()
 }
 wg.Wait();close(results)
 tokens:=make([]string,0,n)
 var variant string
 for result:=range results {
  if result.err!=nil{t.Fatalf("acquire: %v",result.err)}
  if result.lease.Token=="" || result.lease.JobID!=job.ID {t.Fatalf("bad lease: %+v",result.lease)}
  if variant!="" && result.runtime.VariantID!=variant {t.Fatal("shared demand created different physical outputs")}
  variant=result.runtime.VariantID
  tokens=append(tokens,result.lease.Token)
 }
 deadline:=time.After(4*time.Second)
 for executor.starts.Load()==0 {
  select {
  case <-deadline: t.Fatal("backend did not start after demand acquisition")
  case <-time.After(10*time.Millisecond):
  }
 }
 if executor.starts.Load()!=1 {t.Fatalf("wanted one backend start, got %d",executor.starts.Load())}
 for _,token:=range tokens[:len(tokens)-1]{
  if err:=service.ReleaseDemand(context.Background(),token);err!=nil{t.Fatal(err)}
  if _,ok:=manager.Status(job.ID);!ok {t.Fatal("a remaining consumer lost the media owner")}
 }
 if err:=service.ReleaseDemand(context.Background(),tokens[len(tokens)-1]);err!=nil{t.Fatal(err)}
 if _,ok:=manager.Status(job.ID);ok {t.Fatal("last lease did not release process owner")}
}

func TestDemandLeaseExpiryAndNoRestartWithoutConsumers(t *testing.T){
 service,manager,_,job:=setupDemandLeaseService(t)
 if _,err:=service.Start(context.Background(),job.ID,job.Generation);err==nil{
  t.Fatal("on-demand job started without a consumer lease")
 }
 lease,_,err:=service.AcquireDemand(context.Background(),job.ID,job.Generation)
 if err!=nil{t.Fatal(err)}
 service.mu.Lock()
 lease.ExpiresAt=time.Now().Add(-time.Second)
 service.leases[lease.Token]=lease
 service.mu.Unlock()
 if _,err=service.RenewDemand(lease.Token);err==nil{t.Fatal("expired lease renewed")}
 service.expireDemandLeases(context.Background())
 if _,ok:=manager.Status(job.ID);ok{t.Fatal("expired demand lease kept process alive")}
 if _,err=service.RenewDemand(lease.Token);err==nil{t.Fatal("removed token renewed")}
 if errs:=service.ReconcileAlways(context.Background());len(errs)!=0{t.Fatalf("unexpected reconcile errors: %v",errs)}
 if _,ok:=manager.Status(job.ID);ok {t.Fatal("on-demand job resumed without consumer")}
}

func TestDemandLeaseInvalidGenerationAndManualPolicyFailClosed(t *testing.T){
 service,manager,_,job:=setupDemandLeaseService(t)
 if _,_,err:=service.AcquireDemand(context.Background(),job.ID,job.Generation+1);err==nil{
  t.Fatal("stale or future generation accepted")
 }
 service.config.Enabled=false
 if _,_,err:=service.AcquireDemand(context.Background(),job.ID,job.Generation);err==nil{
  t.Fatal("on-demand started while globally disabled")
 }
 if _,ok:=manager.Status(job.ID);ok {t.Fatal("failed request started media")}
}

func TestDemandLeaseRecoversFailedMediaWithoutNewConsumer(t *testing.T) {
 service,manager,executor,job:=setupDemandLeaseService(t)
 manager.healthTimeout=150*time.Millisecond
 lease,first,err:=service.AcquireDemand(context.Background(),job.ID,job.Generation)
 if err!=nil{t.Fatal(err)}
 deadline:=time.After(4*time.Second)
 for {
  state,ok:=manager.Status(job.ID)
  if ok && state.State==Running {break}
  select {case <-deadline:t.Fatal("initial stream did not become ready");case <-time.After(15*time.Millisecond):}
 }
 manager.mu.Lock()
 done:=manager.work[first.Key].done
 manager.mu.Unlock()
 executor.ready.Store(false)
 select {case <-done:case <-time.After(4*time.Second):t.Fatal("source loss did not terminate backend")}
 executor.ready.Store(true)
 if errors:=service.ReconcileDemand(context.Background());len(errors)!=0{
  t.Fatalf("valid lease could not recover: %v",errors)
 }
 deadline=time.After(4*time.Second)
 for {
  state,ok:=manager.Status(job.ID)
  if ok && state.State==Running && state.VariantID!=first.VariantID {break}
  select {case <-deadline:t.Fatal("leased media did not recover");case <-time.After(15*time.Millisecond):}
 }
 if executor.starts.Load()!=2 {t.Fatalf("expected exactly one recovery start, got %d",executor.starts.Load())}
 if _,err:=service.RenewDemand(lease.Token);err!=nil{t.Fatal(err)}
 if err:=service.ReleaseDemand(context.Background(),lease.Token);err!=nil{t.Fatal(err)}
 if errors:=service.ReconcileDemand(context.Background());len(errors)!=0 {t.Fatal(errors)}
 if _,ok:=manager.Status(job.ID);ok {t.Fatal("released demand lease restarted media")}
}

func TestDemandLeaseReleaseIgnoresExpiredOtherConsumer(t *testing.T) {
 service,manager,_,job:=setupDemandLeaseService(t)
 first,_,err:=service.AcquireDemand(context.Background(),job.ID,job.Generation)
 if err!=nil {t.Fatal(err)}
 second,_,err:=service.AcquireDemand(context.Background(),job.ID,job.Generation)
 if err!=nil {t.Fatal(err)}
 service.mu.Lock()
 second.ExpiresAt=time.Now().Add(-time.Second)
 service.leases[second.Token]=second
 service.mu.Unlock()
 if err:=service.ReleaseDemand(context.Background(),first.Token);err!=nil {t.Fatal(err)}
 if _,ok:=manager.Status(job.ID);ok {t.Fatal("expired second lease kept media process alive after final live consumer released")}
 if _,err:=service.RenewDemand(second.Token);err==nil {t.Fatal("expired lease unexpectedly renewed")}
}

func TestReconcileDemandReleasesOrphanedProcessWithoutLiveLeases(t *testing.T) {
 service,manager,_,job:=setupDemandLeaseService(t)
 lease,_,err:=service.AcquireDemand(context.Background(),job.ID,job.Generation)
 if err!=nil {t.Fatal(err)}
 // Simulate a lost lease entry while the media manager still owns the job.
 service.mu.Lock()
 delete(service.leases,lease.Token)
 service.mu.Unlock()
 if errs:=service.ReconcileDemand(context.Background());len(errs)!=0 {t.Fatalf("orphan reconciliation failed: %v",errs)}
 if _,ok:=manager.Status(job.ID);ok {t.Fatal("on-demand media retained an owner without a live lease")}
 if errs:=service.ReconcileDemand(context.Background());len(errs)!=0 {t.Fatalf("repeated reconciliation should be harmless: %v",errs)}
}

type mutableStoppedDemandRepo struct{ *recoveryRepository }
func (r *mutableStoppedDemandRepo) SetTranscodeJobStopped(_ context.Context, id string, generation uint64, stopped bool) error {
    if id != r.job.ID || generation != r.job.Generation { return conflictError() }
    r.job.StoppedByUser = stopped
    return nil
}

func TestStoppedOnDemandExplicitRearmDoesNotStartWithoutLease(t *testing.T) {
    service, manager, executor, job := setupDemandLeaseService(t)
    repo := &mutableStoppedDemandRepo{service.repo.(*recoveryRepository)}
    service.repo = repo
    lease, _, err := service.AcquireDemand(context.Background(), job.ID, job.Generation)
    if err != nil { t.Fatal(err) }
    if err := service.Stop(context.Background(), job.ID, job.Generation); err != nil { t.Fatal(err) }
    if _, err := service.RenewDemand(lease.Token); err == nil { t.Fatal("stopped lease remained valid") }
    if _, _, err := service.AcquireDemand(context.Background(), job.ID, job.Generation); err == nil { t.Fatal("stopped job accepted consumer") }
    before := executor.starts.Load()
    if err := service.RearmDemand(context.Background(), job.ID, job.Generation+1); err == nil { t.Fatal("stale generation rearmed") }
    if err := service.RearmDemand(context.Background(), job.ID, job.Generation); err != nil { t.Fatal(err) }
    if repo.job.StoppedByUser { t.Fatal("stop intent was not cleared") }
    if _, active := manager.Status(job.ID); active { t.Fatal("rearm started a job without consumer") }
    if executor.starts.Load() != before { t.Fatal("rearm spawned a backend without consumer") }
    newLease, _, err := service.AcquireDemand(context.Background(), job.ID, job.Generation)
    if err != nil { t.Fatal(err) }
    if newLease.Token == lease.Token { t.Fatal("old lease was reused") }
    if err := service.ReleaseDemand(context.Background(), newLease.Token); err != nil { t.Fatal(err) }
}
