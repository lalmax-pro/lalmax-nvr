package recorder

import (
 "context"
 "testing"
 "time"

 "github.com/lalmax-pro/lalmax-nvr/internal/media"
 "github.com/stretchr/testify/require"
)

// Scheduler must retain operator-controlled derived recording without a plan,
// yet still stop it on derived publisher loss or an explicit Stop.
func TestSchedulerRetainsExplicitDerivedRecordingUntilStopOrStreamDown(t *testing.T) {
 ctx:=context.Background()
 db:=newSchedulerTestDB(t)
 id:="derived-h264-test"
 engine:=&stubTaskEngine{byID:map[string]*media.StreamInfo{
  id: {StreamID:id,VideoCodec:"h264",Active:true},
 }}
 tasks:=NewTaskManager(engine,nil,nil,nil,nil,time.Second)
 t.Cleanup(tasks.StopAll)
 require.NoError(t,tasks.EnsureDerived(ctx,id))
 require.True(t,tasks.IsExplicitDerived(id))
 s:=NewRecordingScheduler(db)
 s.SetTasks(tasks)
 s.SetAliveStreams(func(context.Context)([]string,error){return []string{id},nil})
 s.check(ctx)
 require.True(t,tasks.Running(id),"plan-inactive stopped manual derived recording")
 require.NoError(t,tasks.Stop(ctx,id,"derived_recording_stopped"))
 require.False(t,tasks.IsExplicitDerived(id),"ownership leaked after explicit stop")
 require.NoError(t,tasks.EnsureDerived(ctx,id))
 s.SetAliveStreams(func(context.Context)([]string,error){return nil,nil})
 s.check(ctx)
 require.False(t,tasks.Running(id),"derived stream loss did not stop recording")
 require.False(t,tasks.IsExplicitDerived(id))
}
