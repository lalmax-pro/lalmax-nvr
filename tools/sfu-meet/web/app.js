(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const form = $('join-form'), token = $('token'), micOption = $('microphone'), cameraOption = $('camera');
  const status = $('status-text'), badge = $('connection-badge'), dot = $('status-dot');
  const join = $('join'), leave = $('leave'), mute = $('mute'), cameraToggle = $('camera-toggle'), share = $('share'), error = $('error');
  const tiles = $('tiles'), audioContainer = $('audio-container');
  let current = null;

  function state(text, connected = false) {
    status.textContent = text; badge.querySelector('span').textContent = connected ? '已连接' : text;
    badge.classList.toggle('connected', connected); dot.classList.toggle('on', connected);
  }
  function showError(message) { error.textContent = message; error.hidden = false; }
  function closeCurrent() {
    const c = current; current = null;
    if (c) {
      for (const pending of c.pending.values()) { clearTimeout(pending.timer); pending.reject(new Error('连接已关闭')); }
      c.pending.clear(); c.ws?.close(); c.pc?.close(); c.stream?.getTracks().forEach(t => t.stop()); c.screen?.getTracks().forEach(t => t.stop());
    }
    audioContainer.replaceChildren(); $('enable-audio').hidden = true; $('refresh-notice').hidden = true;
    leave.disabled = true; mute.disabled = true; cameraToggle.disabled = true; share.disabled = true;
    mute.textContent = '◉ 静音'; mute.setAttribute('aria-pressed', 'false'); cameraToggle.textContent = '◈ 关闭摄像头'; cameraToggle.setAttribute('aria-pressed', 'false'); share.textContent = '▣ 共享屏幕'; join.disabled = false;
  }
  function render(snapshot, c) {
    const members = snapshot.Participants || [], publications = snapshot.Publications || [];
    $('member-count').textContent = `${members.length} 人在线`;
    tiles.replaceChildren();
    if (!members.length) { const empty=document.createElement('div');empty.className='empty-state';empty.textContent='当前没有成员';tiles.append(empty);return; }
    members.forEach((member, index) => {
      const videoPubs=publications.filter(p=>p.ParticipantID===member.ID && p.Kind==='video');
      if (!videoPubs.length) {
        const tile=document.createElement('div');tile.className='tile';const avatar=document.createElement('div');avatar.className='avatar';avatar.textContent=String(index+1).padStart(2,'0');
        const label=document.createElement('strong');label.textContent=member.Name||`成员 ${index+1}`;const detail=document.createElement('small');detail.textContent=publications.some(p=>p.ParticipantID===member.ID&&p.Kind==='audio')?'音频已发布':'已加入房间';tile.append(avatar,label,detail);tiles.append(tile);return;
      }
      for (const p of videoPubs) {
        const tile=document.createElement('div');tile.className='tile video-tile';const media=c.media.get(p.ID);const own=c.own.get(p.ID);
        if (media) tile.append(media); else if (own) {const video=document.createElement('video');video.autoplay=true;video.muted=true;video.playsInline=true;video.srcObject=new MediaStream([own.track]);tile.append(video);} else {const avatar=document.createElement('div');avatar.className='avatar';avatar.textContent=String(index+1).padStart(2,'0');tile.append(avatar);}
        const label=document.createElement('span');label.className='video-label';label.textContent=`${member.Name||`成员 ${index+1}`} · ${p.Source==='screen'?'屏幕共享':'摄像头'}`;tile.append(label);tiles.append(tile);
      }
    });
  }
  function openSocket(c) {
    return new Promise((resolve,reject)=>{
      const ws=new WebSocket(`${location.protocol==='https:'?'wss':'ws'}://${location.host}/ws`);c.ws=ws;let initialized=false;
      const timeout=setTimeout(()=>{if(!initialized){ws.close();reject(new Error('连接超时'));}},10000);
      ws.onmessage=event=>{let msg;try{msg=JSON.parse(event.data);}catch{return;}if(current!==c)return;
        if(msg.type==='snapshot'&&msg.snapshot){c.snapshot=msg.snapshot;render(msg.snapshot,c);if(!initialized){initialized=true;clearTimeout(timeout);resolve(msg.snapshot);}else scheduleSync(c);}
        else if(msg.type==='result'){const p=c.pending.get(msg.id);if(!p)return;clearTimeout(p.timer);c.pending.delete(msg.id);if(msg.error)p.reject(new Error(msg.error==='forbidden'?'操作未获授权':`服务返回：${msg.error}`));else p.resolve(msg);}
      };
      ws.onerror=()=>{if(!initialized){clearTimeout(timeout);reject(new Error('无法连接信令服务'));}};
      ws.onclose=()=>{clearTimeout(timeout);if(!initialized)reject(new Error('信令连接已关闭'));if(current===c){closeCurrent();state('连接已断开');showError('连接已断开，请重新加入。');}};
    });
  }
  function request(c,type,fields={}) {
    return new Promise((resolve,reject)=>{if(current!==c||c.ws.readyState!==WebSocket.OPEN)return reject(new Error('连接已关闭'));const id=String(++c.requestID);const timer=setTimeout(()=>{c.pending.delete(id);reject(new Error('请求超时'));},12000);c.pending.set(id,{resolve,reject,timer});c.ws.send(JSON.stringify({id,type,...fields}));});
  }
  function gather(pc) {
    if(pc.iceGatheringState==='complete')return Promise.resolve();
    return new Promise((resolve,reject)=>{const timer=setTimeout(()=>{pc.removeEventListener('icegatheringstatechange',check);reject(new Error('ICE 收集超时'));},10000);function check(){if(pc.iceGatheringState==='complete'){clearTimeout(timer);pc.removeEventListener('icegatheringstatechange',check);resolve();}}pc.addEventListener('icegatheringstatechange',check);check();});
  }
  function preferH264(transceiver) {
    if(!transceiver.setCodecPreferences || !window.RTCRtpSender?.getCapabilities)return;
    const codecs=RTCRtpSender.getCapabilities('video')?.codecs||[];
    const compatible=codecs.filter(c=>c.mimeType.toLowerCase()==='video/h264' && /packetization-mode=1/i.test(c.sdpFmtpLine||'') && /profile-level-id=42e0/i.test(c.sdpFmtpLine||''));
    if(!compatible.length)throw new Error('浏览器不支持此 Demo 使用的 H.264 编码。');
    transceiver.setCodecPreferences(compatible);
  }
  async function negotiate(c) {
    if(current!==c)throw new Error('连接已关闭');
    const offer=await c.pc.createOffer();await c.pc.setLocalDescription(offer);await gather(c.pc);
    const answer=await request(c,'offer',{sdp:c.pc.localDescription.sdp});await c.pc.setRemoteDescription({type:'answer',sdp:answer.sdp});
  }
  function fail(c,err) {
    if(current!==c)return;closeCurrent();state('未连接');showError(`${err.message||'连接失败'}，请重新加入。`);
  }
  function enqueue(c,work) {
    const next=c.queue.then(async()=>{if(current!==c)throw new Error('连接已关闭');return work();});
    c.queue=next.catch(err=>fail(c,err));return next;
  }
  function scheduleSync(c) {
    if(!c.negotiated||current!==c)return;c.syncPending=true;if(c.syncRunning)return;c.syncRunning=true;
    void enqueue(c,async()=>{while(c.syncPending&&current===c){c.syncPending=false;await syncSubscriptions(c);}}).finally(()=>{c.syncRunning=false;if(c.syncPending&&current===c)scheduleSync(c);}).catch(()=>{});
  }
  async function syncSubscriptions(c) {
    const desired=new Map((c.snapshot?.Publications||[]).filter(p=>p.RTPReady && !c.own.has(p.ID)).map(p=>[p.ID,p]));let changed=false;
    for(const [id,entry] of c.subscribed){if(!desired.has(id)){try{await request(c,'unsubscribe',{publication_id:id});}catch(err){if((c.snapshot?.Publications||[]).some(p=>p.ID===id))throw err;}entry.transceiver.stop();c.subscribed.delete(id);const media=c.media.get(id);if(media){media.remove();c.media.delete(id);}changed=true;}}
    if(changed)await negotiate(c);
    for(const [id,p] of desired){if(c.subscribed.has(id))continue;await request(c,'subscribe',{publication_id:id});const transceiver=c.pc.addTransceiver(p.Kind,{direction:'recvonly'});if(p.Kind==='video')preferH264(transceiver);c.subscribed.set(id,{transceiver});await negotiate(c);}if(c.snapshot)render(c.snapshot,c);
  }
  async function publishTrack(c,track,source) {
    const video=track.kind==='video';const p=await request(c,'publish',{kind:track.kind,source,codec:video?'video/H264':'audio/opus',track_id:track.id});
    const sender=c.pc.addTrack(track,new MediaStream([track]));const transceiver=c.pc.getTransceivers().find(t=>t.sender===sender);if(video&&transceiver)preferH264(transceiver);
    c.own.set(p.publication.ID,{track,sender,source});if(c.snapshot)render(c.snapshot,c);return p.publication.ID;
  }
  async function removeTrack(c,id) {
    const entry=c.own.get(id);if(!entry)return;c.own.delete(id);c.pc.removeTrack(entry.sender);entry.track.stop();await request(c,'unpublish',{publication_id:id});await negotiate(c);if(c.snapshot)render(c.snapshot,c);
  }
  async function connect() {
    closeCurrent();error.hidden=true;join.disabled=true;state('正在连接');
    const c={pending:new Map(),requestID:0,subscribed:new Map(),own:new Map(),media:new Map(),queue:Promise.resolve(),negotiated:false,syncPending:false,syncRunning:false};current=c;
    try {
      const login=await fetch('/session',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({token:token.value})});if(!login.ok)throw new Error('访问口令无效，请检查终端中的口令');
      if(micOption.checked||cameraOption.checked){if(!navigator.mediaDevices?.getUserMedia)throw new Error('当前浏览器无法访问媒体设备');c.stream=await navigator.mediaDevices.getUserMedia({audio:micOption.checked,video:cameraOption.checked});if(current!==c){c.stream.getTracks().forEach(t=>t.stop());return;}}
      await openSocket(c);if(current!==c)return;
      const pc=new RTCPeerConnection();c.pc=pc;
      pc.onconnectionstatechange=()=>{if(current!==c)return;if(pc.connectionState==='connected')state('媒体连接正常',true);else if(['failed','disconnected'].includes(pc.connectionState)){state('连接中断');showError('媒体连接中断，请重新加入。');}};
      pc.ontrack=event=>{if(current!==c)return;const id=[...c.subscribed].find(([,entry])=>entry.transceiver===event.transceiver)?.[0];if(!id)return;
        if(event.track.kind==='audio'){const audio=document.createElement('audio');audio.autoplay=true;audio.srcObject=new MediaStream([event.track]);audioContainer.append(audio);c.media.set(id,audio);audio.play().catch(()=>{showError('浏览器阻止自动播放，请点击“启用声音”。');$('enable-audio').hidden=false;});}
        else {const video=document.createElement('video');video.autoplay=true;video.playsInline=true;video.srcObject=new MediaStream([event.track]);c.media.set(id,video);if(c.snapshot)render(c.snapshot,c);}
        event.track.onended=()=>{const media=c.media.get(id);if(media){media.remove();c.media.delete(id);}if(current===c&&c.snapshot)render(c.snapshot,c);};
      };
      if(c.stream){for(const track of c.stream.getTracks())await publishTrack(c,track,track.kind==='audio'?'microphone':'camera');}
      if(!c.stream?.getTracks().length)pc.createDataChannel('control');
      await negotiate(c);if(current!==c)return;c.negotiated=true;leave.disabled=false;mute.disabled=!c.stream?.getAudioTracks().length;cameraToggle.disabled=!c.stream?.getVideoTracks().length;share.disabled=false;state(pc.connectionState==='connected'?'媒体连接正常':'正在建立媒体连接',pc.connectionState==='connected');scheduleSync(c);
    } catch(err){fail(c,err);}
  }
  form.addEventListener('submit',event=>{event.preventDefault();void connect();});
  leave.addEventListener('click',()=>{closeCurrent();state('已离开');tiles.replaceChildren();$('member-count').textContent='0 人在线';});
  $('refresh').addEventListener('click',()=>void connect());
  mute.addEventListener('click',()=>{const track=current?.stream?.getAudioTracks()[0];if(!track)return;track.enabled=!track.enabled;mute.setAttribute('aria-pressed',String(!track.enabled));mute.textContent=track.enabled?'◉ 静音':'◎ 取消静音';});
  cameraToggle.addEventListener('click',()=>{const track=current?.stream?.getVideoTracks()[0];if(!track)return;track.enabled=!track.enabled;cameraToggle.setAttribute('aria-pressed',String(!track.enabled));cameraToggle.textContent=track.enabled?'◈ 关闭摄像头':'◈ 开启摄像头';});
  share.addEventListener('click',async()=>{
    const c=current;if(!c||!c.negotiated)return;
    if(c.screenPublication){share.disabled=true;try{await enqueue(c,()=>removeTrack(c,c.screenPublication));c.screenPublication='';c.screen=null;share.textContent='▣ 共享屏幕';}catch{}finally{if(current===c)share.disabled=false;}return;}
    share.disabled=true;
    try {
      if(!navigator.mediaDevices?.getDisplayMedia)throw new Error('当前浏览器不支持屏幕共享');
      const stream=await navigator.mediaDevices.getDisplayMedia({video:true,audio:false});if(current!==c){stream.getTracks().forEach(t=>t.stop());return;}
      const track=stream.getVideoTracks()[0];if(!track){stream.getTracks().forEach(t=>t.stop());throw new Error('未获得屏幕画面');}c.screen=stream;
      let id='', ended=track.readyState==='ended';
      const stopScreen=async()=>{if(c.screenPublication!==id)return;await removeTrack(c,id);c.screenPublication='';c.screen=null;share.textContent='▣ 共享屏幕';};
      track.onended=()=>{ended=true;if(current===c&&id)void enqueue(c,stopScreen).catch(()=>{});};
      await enqueue(c,async()=>{if(ended){c.screen=null;return;}id=await publishTrack(c,track,'screen');c.screenPublication=id;if(ended){await stopScreen();return;}await negotiate(c);});
      if(current===c&&c.screenPublication&&!ended)share.textContent='▣ 停止共享';
    }catch(err){if(current===c){c.screen?.getTracks().forEach(t=>t.stop());c.screen=null;showError(err.message||'屏幕共享失败');}}finally{if(current===c)share.disabled=false;}
  });
  $('enable-audio').addEventListener('click',()=>{for(const audio of audioContainer.querySelectorAll('audio'))void audio.play();$('enable-audio').hidden=true;});
  window.addEventListener('pagehide',closeCurrent);
})();
