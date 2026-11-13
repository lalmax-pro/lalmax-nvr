package transcode

import (
 "context"
 "errors"
 "crypto/rand"
 "time"
)

// DemandLease is ephemeral consumer interest in an enabled on-demand job.
// Tokens are random, never persisted, and do not convey media playback rights.
type DemandLease struct {
 Token string `json:"token"`
 JobID string `json:"job_id"`
 Generation uint64 `json:"generation"`
 ExpiresAt time.Time `json:"expires_at"`
}

const demandLeaseTTL = 90*time.Second

func (s *Service) invalidateDemandLeasesLocked(jobID string) {
 for token,lease := range s.leases {
  if lease.JobID == jobID { delete(s.leases,token) }
 }
}

func (s *Service) demandOwnersLocked(jobID string) int {
 owners:=0
 now:=time.Now()
 for _,l:=range s.leases {if l.JobID==jobID && now.Before(l.ExpiresAt){owners++}}
 return owners
}

// AcquireDemand creates a lease only for an enabled on-demand job. Multiple
// leases for one job share the same manager owner and physical transcode work.
func (s *Service) AcquireDemand(ctx context.Context, id string, generation uint64) (DemandLease, Runtime, error) {
 s.mu.Lock()
 defer s.mu.Unlock()
 j,err:=s.job(ctx,id,generation)
 if err!=nil{return DemandLease{},Runtime{},err}
 if j.RunPolicy!=OnDemand || !j.Enabled || j.StoppedByUser {
  return DemandLease{},Runtime{},&Error{Code:"demand_unavailable",Message:"on-demand job is not enabled for consumers"}
 }
 p,err:=s.profile(ctx,j)
 if err!=nil{return DemandLease{},Runtime{},err}
 if err=s.allow(p);err!=nil{return DemandLease{},Runtime{},err}
 runtime,err:=s.manager.Acquire(j,p)
 if err!=nil{return DemandLease{},Runtime{},err}
 lease:=DemandLease{Token:rand.Text(),JobID:id,Generation:generation,ExpiresAt:time.Now().Add(demandLeaseTTL)}
 if s.leases==nil{s.leases=make(map[string]DemandLease)}
 s.leases[lease.Token]=lease
 return lease,runtime,nil
}

// RenewDemand requires the existing unexpired token; it never changes the job
// generation or creates a new process.
func (s *Service) RenewDemand(token string) (DemandLease,error) {
 s.mu.Lock()
 defer s.mu.Unlock()
 lease,ok:=s.leases[token]
 if !ok || !time.Now().Before(lease.ExpiresAt) {
  return DemandLease{},&Error{Code:"lease_not_found",Message:"consumer lease has expired"}
 }
 lease.ExpiresAt=time.Now().Add(demandLeaseTTL)
 s.leases[token]=lease
 return lease,nil
}

// ReleaseDemand stops this job's manager ownership only after the final
// consumer releases it. Other jobs sharing the effective work are unaffected.
func (s *Service) ReleaseDemand(ctx context.Context, token string) error {
 s.mu.Lock()
 defer s.mu.Unlock()
 lease,ok:=s.leases[token]
 if !ok {return &Error{Code:"lease_not_found",Message:"consumer lease not found"}}
 delete(s.leases,token)
 if s.demandOwnersLocked(lease.JobID)!=0 || s.manager==nil {return nil}
 return s.manager.Release(ctx,lease.JobID,lease.Generation)
}

func (s *Service) expireDemandLeases(ctx context.Context) {
 s.mu.Lock()
 defer s.mu.Unlock()
 now:=time.Now()
 expired:=map[string]uint64{}
 for token,l:=range s.leases{
  if !now.Before(l.ExpiresAt){
   delete(s.leases,token)
   expired[l.JobID]=l.Generation
  }
 }
 for id,gen:=range expired{
  if s.demandOwnersLocked(id)==0 && s.manager!=nil{
   _=s.manager.Release(ctx,id,gen)
  }
 }
}

// ReconcileDemand restarts failed on-demand work only while an unexpired
// consumer lease exists. It never starts a job after reboot without a new
// consumer and it uses bounded exponential backoff to avoid restart storms.
func (s *Service) ReconcileDemand(ctx context.Context) map[string]error {
 s.mu.Lock()
 defer s.mu.Unlock()
 results:=map[string]error{}
 if s.repo==nil || s.manager==nil{return results}
 jobs,err:=s.repo.ListTranscodeJobs(ctx)
 if err!=nil{results["repository"]=err;return results}
 now:=time.Now()
 for _,j:=range jobs {
  if ctx.Err()!=nil {break}
  if j.RunPolicy!=OnDemand {continue}
  consumers:=0
  for token,l:=range s.leases {
   if l.JobID!=j.ID {continue}
   if l.Generation!=j.Generation || !now.Before(l.ExpiresAt) {
    delete(s.leases,token)
    continue
   }
   consumers++
  }
  if consumers==0 {
   // A generation change or a stale lease can leave a running on-demand
   // owner behind. Reconciliation must release it even when no lease remains.
   if _,active:=s.manager.Status(j.ID);active {
    if err:=s.manager.Release(ctx,j.ID,j.Generation);err!=nil {
     results[j.ID]=err
    }
   }
   continue
  }
  if !j.Enabled || j.StoppedByUser {
   s.invalidateDemandLeasesLocked(j.ID)
   _=s.manager.Release(ctx,j.ID,j.Generation)
   continue
  }
  runtime,active:=s.manager.Status(j.ID)
  if active && (runtime.State==Running || runtime.State==Starting) {
   if runtime.State==Running {
    if backoff,exists:=s.retry[j.ID];exists && backoff.generation==j.Generation {
     if backoff.healthySince.IsZero() || backoff.healthyVariantID!=runtime.VariantID {
      backoff.healthySince=now
      backoff.healthyVariantID=runtime.VariantID
      s.retry[j.ID]=backoff
     } else if now.Sub(backoff.healthySince)>=recoveryHealthyResetInterval {
      delete(s.retry,j.ID)
     }
    }
   }
   continue
  }
  backoff:=s.retry[j.ID]
  if backoff.generation!=j.Generation {backoff=retryState{generation:j.Generation}}
  if now.Before(backoff.next) {continue}
  p,e:=s.profile(ctx,j)
  if e==nil {e=s.allow(p)}
  if e==nil {_,e=s.manager.Acquire(j,p)}
  backoff.attempts++
  delay:=5*time.Second
  for k:=1;k<backoff.attempts && delay<time.Minute;k++ {delay*=2}
  if delay>time.Minute {delay=time.Minute}
  backoff.next=now.Add(delay)
  backoff.healthySince=time.Time{}
  backoff.healthyVariantID=""
  backoff.errorCode=""
  if e!=nil {
   backoff.errorCode="demand_reconcile_failed"
   var domain *Error
   if errors.As(e,&domain){backoff.errorCode=domain.Code}
   results[j.ID]=e
  }
  s.retry[j.ID]=backoff
 }
 return results
}
