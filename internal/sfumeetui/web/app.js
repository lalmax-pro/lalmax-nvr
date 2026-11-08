(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const form = $('join-form'), token = $('token'), micOption = $('microphone'), cameraOption = $('camera');
  const micDevice=$('microphone-device'), cameraDevice=$('camera-device');
  const status = $('status-text'), badge = $('connection-badge'), dot = $('status-dot');
  const join = $('join'), leave = $('leave'), mute = $('mute'), cameraToggle = $('camera-toggle'), share = $('share'), error = $('error');
  const tiles = $('tiles'), audioContainer = $('audio-container'), trackList = $('track-list');
  const networkPanel=$('network-stats'), networkStatus=$('network-status');
  const nvrMode = document.documentElement?.dataset?.mode === 'nvr';
  if (nvrMode) { token.required=false; token.hidden=true; $('token-label').hidden=true; $('back-nvr').hidden=false; $('bridge-panel').hidden=false; $('bridge-management').hidden=false; $('nvr-room-controls').hidden=false; $('join').textContent='加入会议 →'; $('mode-pill').textContent='NVR 会议'; $('mode-instructions').textContent='使用 NVR 管理员账号加入。'; $('nvr-login').hidden=!!sessionStorage.getItem('nvr_auth'); $('room-description').textContent='默认房间 · Opus / H.264'; $('mode-footer').textContent='NVR SFU'; }
  let current = null;
  let recovery = null;
  let deviceRefresh = 0;
  let roomBusy = false;
  const roomGenerations = new Map();

  function deviceConstraint(id) { return id ? {deviceId:{exact:id}} : true; }
  async function refreshDevices() {
    if(!navigator.mediaDevices?.enumerateDevices)return;
    const request=++deviceRefresh;
    try {
      const devices=await navigator.mediaDevices.enumerateDevices();
      if(request!==deviceRefresh)return;
      for(const [select,kind,fallback] of [[micDevice,'audioinput','麦克风'],[cameraDevice,'videoinput','摄像头']]) {
        const previous=select.value, options=[];let count=0;
        const system=document.createElement('option');system.value='';system.textContent='系统默认';options.push(system);
        for(const device of devices)if(device.kind===kind&&device.deviceId){const option=document.createElement('option');option.value=device.deviceId;option.textContent=device.label||`${fallback} ${++count}`;options.push(option);}
        select.replaceChildren(...options);select.value=options.some(o=>o.value===previous)?previous:'';
      }
    } catch(_) { /* Device enumeration is optional until permission is granted. */ }
  }

  async function switchDevice(kind,select) {
    const c=current;
    if(!c?.negotiated)return;
    const source=kind==='audio'?'microphone':'camera';
    const entry=[...c.own.values()].find(item=>item.source===source);
    if(!entry)return;
    const old=entry.track, previous=c.deviceSelection[kind], selected=select.value;
    let replacement;
    select.disabled=true;
    try {
      const stream=await navigator.mediaDevices.getUserMedia({[kind]:deviceConstraint(selected)});
      const tracks=stream.getTracks();replacement=tracks.find(t=>t.kind===kind);
      for(const track of tracks)if(track!==replacement)track.stop();
      if(!replacement)throw new Error('未找到所选设备');
      if(current!==c){replacement.stop();return;}
      // Keep the replacement silent until the swap completes, then read the
      // current mute state in case it changed while replaceTrack was pending.
      replacement.enabled=false;
      await entry.sender.replaceTrack(replacement);
      if(current!==c){replacement.stop();return;}
      replacement.enabled=old.enabled;
      c.stream.removeTrack(old);c.stream.addTrack(replacement);entry.track=replacement;c.deviceSelection[kind]=selected;old.stop();
      c.tileSignature=undefined;if(c.snapshot)render(c.snapshot,c);
      error.hidden=true;void refreshDevices();
    } catch(_) {
      replacement?.stop();
      if(current===c){select.value=previous;showError('设备切换失败，请检查设备和浏览器权限。');}
    } finally { if(current===c)select.disabled=false; }
  }
  micDevice.addEventListener('change',()=>void switchDevice('audio',micDevice));
  cameraDevice.addEventListener('change',()=>void switchDevice('video',cameraDevice));
  navigator.mediaDevices?.addEventListener?.('devicechange',()=>void refreshDevices());
  void refreshDevices();

  function state(text, connected = false) {
    status.textContent = text; badge.querySelector('span').textContent = connected ? '已连接' : text;
    badge.classList.toggle('connected', connected); dot.classList.toggle('on', connected);
  }
  function showError(message) { error.textContent = message; error.hidden = false; }
  function cancelRecovery() {
    const r=recovery;recovery=null;
    if(!r)return;
    clearTimeout(r.timer);
    if(current?.stream!==r.stream)r.stream?.getTracks().forEach(t=>t.stop());
    if(nvrMode)updateRoomControls();
  }
  let networkTimer=null, networkGeneration=0, networkPrevious=new Map();
  function clearNetworkStats(message='加入会议后可查看。') {
    networkGeneration++;clearTimeout(networkTimer);networkTimer=null;networkPrevious=new Map();
    for(const id of ['network-send','network-receive','network-loss','network-rtt'])$(id).textContent='—';
    networkStatus.textContent=message;
  }
  function networkRate(kbps) { return kbps>=1000?`${(kbps/1000).toFixed(2)} Mbps`:`${kbps.toFixed(0)} kbps`; }
  function updateNetworkStats(report) {
    const previous=networkPrevious, next=new Map(), all=new Map();let sent=0,received=0,sentKnown=false,receivedKnown=false,lost=0,packets=0,lossKnown=false,hasMedia=false,rtt;
    report.forEach(item=>all.set(item.id,item));
    for(const item of all.values()) {
      if(item.type==='transport' && item.selectedCandidatePairId) {
        const pair=all.get(item.selectedCandidatePairId);
        if(Number.isFinite(pair?.currentRoundTripTime) && pair.currentRoundTripTime>=0)rtt=pair.currentRoundTripTime;
      }
      if(item.type!=='inbound-rtp' && item.type!=='outbound-rtp')continue;
      if(item.isRemote || (item.kind && item.kind!=='audio' && item.kind!=='video'))continue;
      hasMedia=true;next.set(item.id,item);const old=previous.get(item.id);
      if(!old || !Number.isFinite(item.timestamp) || !Number.isFinite(old.timestamp))continue;
      const elapsed=item.timestamp-old.timestamp;if(elapsed<=0)continue;
      const key=item.type==='outbound-rtp'?'bytesSent':'bytesReceived';
      if(Number.isFinite(item[key]) && Number.isFinite(old[key]) && item[key]>=old[key]) {
        const value=(item[key]-old[key])*8/elapsed;
        if(item.type==='outbound-rtp'){sent+=value;sentKnown=true;}else{received+=value;receivedKnown=true;}
      }
      if(item.type==='inbound-rtp' && Number.isFinite(item.packetsReceived) && Number.isFinite(old.packetsReceived) && Number.isFinite(item.packetsLost) && Number.isFinite(old.packetsLost) && item.packetsReceived>=old.packetsReceived) {
        const got=item.packetsReceived-old.packetsReceived, missing=Math.max(0,item.packetsLost-old.packetsLost);
        lost+=missing;packets+=got+missing;lossKnown=true;
      }
    }
    if(rtt===undefined)for(const item of all.values())if(item.type==='candidate-pair' && item.selected===true && Number.isFinite(item.currentRoundTripTime) && item.currentRoundTripTime>=0){rtt=item.currentRoundTripTime;break;}
    networkPrevious=next;
    $('network-send').textContent=sentKnown?networkRate(sent):'—';$('network-receive').textContent=receivedKnown?networkRate(received):'—';
    $('network-loss').textContent=lossKnown&&packets>0?`${(lost*100/packets).toFixed(1)}%`:'—';$('network-rtt').textContent=rtt===undefined?'—':`${(rtt*1000).toFixed(0)} ms`;
    networkStatus.textContent=hasMedia?'每 2 秒更新 · 丢包率为接收方向':'暂无媒体流';
  }
  function startNetworkStats(c) {
    clearNetworkStats(c?'正在获取统计…':'加入会议后可查看。');
    if(!networkPanel.open || !c?.pc)return;
    if(typeof c.pc.getStats!=='function'){networkStatus.textContent='当前浏览器不支持网络统计。';return;}
    const generation=networkGeneration;
    const collect=async()=>{
      if(generation!==networkGeneration || current!==c || !networkPanel.open)return;
      try {const report=await c.pc.getStats();if(generation!==networkGeneration || current!==c || !networkPanel.open)return;updateNetworkStats(report);}
      catch(_){if(generation!==networkGeneration || current!==c || !networkPanel.open)return;networkPrevious=new Map();for(const id of ['network-send','network-receive','network-loss','network-rtt'])$(id).textContent='—';networkStatus.textContent='暂时无法获取统计。';}
      networkTimer=setTimeout(collect,2000);
    };
    void collect();
  }
  networkPanel.addEventListener('toggle',()=>{if(networkPanel.open)startNetworkStats(current);else clearNetworkStats();});

  function closeCurrent() {
    const c = current; current = null;clearNetworkStats();
    if (c) {
      clearTimeout(c.disconnectTimer?.id);c.disconnectTimer=null;
      for (const pending of c.pending.values()) { clearTimeout(pending.timer); pending.reject(new Error('连接已关闭')); }
      if(nvrMode) for(const bridge of c.bridges.values()) { bridge.closeRequested=true; void stopBridge(c,bridge,true); }
      c.pending.clear(); c.ws?.close(); c.pc?.close(); c.stream?.getTracks().forEach(t => t.stop()); c.screen?.getTracks().forEach(t => t.stop());
    }
    audioContainer.replaceChildren(); trackList.replaceChildren(); const empty=document.createElement('p');empty.className='track-empty';empty.textContent='加入会议后显示远端轨道。';trackList.append(empty); $('enable-audio').hidden = true; $('refresh-notice').hidden = true;
    if(nvrMode){$('bridge-list').replaceChildren();$('bridge-status').textContent='';$('bridge-video').srcObject=null;$('bridge-video').hidden=true;}
    leave.disabled = true; mute.disabled = true; cameraToggle.disabled = true; share.disabled = true;micDevice.disabled=false;cameraDevice.disabled=false;
    mute.textContent = '◉ 静音'; mute.setAttribute('aria-pressed', 'false'); cameraToggle.textContent = '◈ 关闭摄像头'; cameraToggle.setAttribute('aria-pressed', 'false'); share.textContent = '▣ 共享屏幕'; join.disabled = false; if(nvrMode)updateRoomControls();
  }
  function renderTracks(snapshot, c) {
    const members = new Map((snapshot.Participants || []).map((p, index) => [p.ID, p.Name || `成员 ${index + 1}`]));
    const tracks = (snapshot.Publications || []).filter(p => !c.own.has(p.ID));
    trackList.replaceChildren();
    if (!tracks.length) { const empty=document.createElement('p');empty.className='track-empty';empty.textContent='暂无远端轨道。';trackList.append(empty);return; }
    for (const p of tracks) {
      const row=document.createElement('div');row.className='track-row';
      const info=document.createElement('div');info.className='track-info';
      const title=document.createElement('strong');const source=p.Source==='screen'?'屏幕共享':p.Source==='camera'?'摄像头':'麦克风';
      title.textContent=`${members.get(p.ParticipantID)||'远端成员'} · ${source}`;
      const detail=document.createElement('small');detail.textContent=`${p.Codec} · ${p.ID.slice(0,8)}`;info.append(title,detail);
      const button=document.createElement('button');button.type='button';button.className='track-toggle';
      const enabled=!c.disabledTracks.has(p.ID);button.setAttribute('aria-pressed',String(enabled));
      button.setAttribute('aria-label',`${enabled?'取消订阅':'订阅'} ${title.textContent} ${p.ID.slice(0,8)}`);
      button.textContent=enabled?'取消订阅':'订阅';button.disabled=!c.negotiated;
      button.addEventListener('click',()=>{if(current!==c)return;if(c.disabledTracks.has(p.ID))c.disabledTracks.delete(p.ID);else c.disabledTracks.add(p.ID);renderTracks(c.snapshot,c);scheduleSync(c);});
      row.append(info,button);trackList.append(row);
    }
  }
  function nvrAuth() {
    let value=sessionStorage.getItem('nvr_auth');
    if(!value){const username=$('nvr-username').value, password=$('nvr-password').value;if(!username||!password)throw new Error('请输入 NVR 用户名和密码');value=btoa(`${username}:${password}`);}
    return `Basic ${value}`;
  }
  async function nvrRequest(path,options={},retryBody=false) {
    let response;const authorization=nvrAuth();
    try { response=await fetch(path,{...options,headers:{'Authorization':authorization,'Content-Type':'application/json',...(options.headers||{})},cache:'no-store'}); }
    catch (_) { const err=new Error('网络连接失败');err.network=true;throw err; }
    if(!response.ok){const body=await response.json().catch(()=>({}));const err=new Error(body.error||`请求失败 (${response.status})`);err.status=response.status;throw err;}
    if(response.status===204)return null;
    try { return await response.json(); }
    catch (err) { if(!retryBody)throw err;const failure=new Error('网络连接失败');failure.network=true;throw failure; }
  }
  async function closeBridgePlayer(bridge,quiet=false) {
    if(!bridge?.location)return;
    // Player cleanup is best effort: a stalled WHEP request must not prevent
    // the authoritative bridge stop from revoking playback and ending media.
    const timeout=new AbortController(),timer=setTimeout(()=>timeout.abort(),5000);
    try { await fetch(bridge.location,{method:'DELETE',headers:{'Authorization':nvrAuth()},keepalive:quiet,cache:'no-store',signal:timeout.signal}); }
    catch (_) {} finally { clearTimeout(timer); }
  }
  let bridgeManagementBusy=false;
  async function manageBridges(action, id) {
    if(bridgeManagementBusy)return;
    bridgeManagementBusy=true;const refresh=$('bridge-management-refresh'), list=$('bridge-management-list'), message=$('bridge-management-status');
    refresh.disabled=true;for(const button of list.querySelectorAll('button'))button.disabled=true;
    message.textContent=action==='stop'?'正在停止桥接…':action==='remove'?'正在删除记录…':'正在加载桥接…';
    try {
      if(action==='stop'){
        const c=current, local=c&&[...c.bridges.values()].find(bridge=>bridge.id===id);
        // If this tab owns the player, close its WHEP session before the
        // bridge stops and revokes the playback credential.
        await closeBridgePlayer(local);
        await nvrRequest(`/api/sfu/bridges/${encodeURIComponent(id)}/stop`,{method:'POST',body:'{}'});
        if(local){local.closeRequested=true;local.abort.abort();local.pc?.close();c.bridges.delete(local.publication);
          if(current===c){$('bridge-video').srcObject=null;$('bridge-video').hidden=true;$('bridge-status').textContent='桥接已停止';if(c.snapshot)renderBridgeTracks(c.snapshot,c);}}
      }
      if(action==='remove')await nvrRequest(`/api/sfu/bridges/${encodeURIComponent(id)}`,{method:'DELETE'});
      const bridges=await nvrRequest('/api/sfu/bridges/');
      if(!Array.isArray(bridges)||bridges.some(b=>typeof b.id!=='string'||typeof b.publication_id!=='string'||!['pending','stopping','stopped','failed'].includes(b.state)))throw new Error('桥接列表无效');
      list.replaceChildren();
      if(!bridges.length){message.textContent='没有可管理的桥接。';return;}
      const labels={pending:'运行中或等待媒体',stopping:'正在停止',stopped:'已停止',failed:'已失败'};
      for(const bridge of bridges){
        const row=document.createElement('div');row.className='track-row';
        const info=document.createElement('div');info.className='track-info';
        const title=document.createElement('strong');title.textContent=`桥接 ${bridge.id}`;
        const detail=document.createElement('small');detail.textContent=`${labels[bridge.state]} · 轨道 ${bridge.publication_id}`;info.append(title,detail);
        const button=document.createElement('button');button.type='button';button.className='track-toggle';
        const terminal=bridge.state==='stopped'||bridge.state==='failed';button.textContent=terminal?'删除记录':'停止桥接';
        button.setAttribute('aria-label',`${button.textContent} ${bridge.id}`);
        button.addEventListener('click',()=>void manageBridges(terminal?'remove':'stop',bridge.id));
        row.append(info,button);list.append(row);
      }
      message.textContent=action==='stop'?'已请求停止；如仍在停止中，请稍后刷新。':action==='remove'?'记录已删除。':`找到 ${bridges.length} 条桥接。`;
    } catch(err) { message.textContent=err.message||'桥接管理失败，请重试。'; }
    finally { bridgeManagementBusy=false;refresh.disabled=false;for(const button of list.querySelectorAll('button'))button.disabled=false; }
  }
  if(nvrMode)$('bridge-management-refresh').addEventListener('click',()=>void manageBridges());
  function updateRoomControls() {
    if(!nvrMode)return;
    const disabled=roomBusy||!!current||!!recovery;
    if(!current){$('room-title').textContent=$('nvr-room').value||'default';$('room-description').textContent='NVR 房间 · Opus / H.264';}
    for(const id of ['nvr-room','nvr-room-new','nvr-rooms-refresh','nvr-room-create'])$(id).disabled=disabled;
    $('nvr-room-close').disabled=disabled||$('nvr-room').value==='default'||!roomGenerations.has($('nvr-room').value);
    if(roomBusy||recovery)join.disabled=true;else if(!current)join.disabled=false;
  }
  async function loadRooms(selected) {
    const rooms=await nvrRequest('/api/sfu/rooms/');
    if(!Array.isArray(rooms))throw new Error('房间列表无效');
    const options=[];roomGenerations.clear();
    for(const room of rooms){if(typeof room.id!=='string'||typeof room.generation!=='string')throw new Error('房间列表无效');const option=document.createElement('option');option.value=room.id;option.textContent=`${room.id} · ${room.participants} 人`;options.push(option);roomGenerations.set(room.id,room.generation);}
    if(!roomGenerations.has('default'))throw new Error('默认房间不可用');
    $('nvr-room').replaceChildren(...options);$('nvr-room').value=roomGenerations.has(selected)?selected:'default';
  }
  async function roomAction(action) {
    if(current||roomBusy)return;
    roomBusy=true;updateRoomControls();error.hidden=true;
    try {
      const selected=$('nvr-room').value||'default';
      if(action==='create'){
        const id=$('nvr-room-new').value.trim();if(!/^[A-Za-z0-9_-]{1,64}$/.test(id))throw new Error('房间 ID 只能包含 1–64 个字母、数字、下划线或连字符');
        await nvrRequest('/api/sfu/rooms/',{method:'POST',body:JSON.stringify({id})});$('nvr-room-new').value='';await loadRooms(id);$('nvr-room-status').textContent=`已创建房间 ${id}`;
      } else if(action==='close') {
        const generation=roomGenerations.get(selected);if(selected==='default'||!generation)throw new Error('请先刷新房间列表');
        await nvrRequest(`/api/sfu/rooms/${encodeURIComponent(selected)}?generation=${encodeURIComponent(generation)}`,{method:'DELETE'});await loadRooms('default');$('nvr-room-status').textContent=`已关闭房间 ${selected}`;
      } else { await loadRooms(selected);$('nvr-room-status').textContent='房间列表已更新'; }
    } catch(err){showError(err.message||'房间操作失败');}finally{roomBusy=false;updateRoomControls();}
  }
  if(nvrMode){
    $('nvr-rooms-refresh').addEventListener('click',()=>void roomAction('refresh'));
    $('nvr-room-create').addEventListener('click',()=>void roomAction('create'));
    $('nvr-room-close').addEventListener('click',()=>void roomAction('close'));
    $('nvr-room').addEventListener('change',updateRoomControls);updateRoomControls();
  }
  function renderBridgeTracks(snapshot,c) {
    const list=$('bridge-list');list.replaceChildren();
    const tracks=(snapshot.Publications||[]).filter(p=>(p.Kind==='video'||p.Kind==='audio')&&p.RTPReady);
    for(const [id,b] of c.bridges)if(!tracks.some(p=>p.ID===id))tracks.push({ID:id,Source:b.source,RTPReady:false});
    if(!tracks.length){const empty=document.createElement('p');empty.className='track-empty';empty.textContent='没有可桥接的音视频轨道。';list.append(empty);return;}
    for(const p of tracks){
      const row=document.createElement('div');row.className='track-row';
      const info=document.createElement('div');info.className='track-info';
      const title=document.createElement('strong');title.textContent=`${p.Source==='screen'?'屏幕':p.Source==='microphone'?'麦克风':'摄像头'} · ${p.ID.slice(0,8)}`;
      const detail=document.createElement('small');const b=c.bridges.get(p.ID);detail.textContent=b?b.label:(p.RTPReady?'已就绪':'源已结束');info.append(title,detail);
      const button=document.createElement('button');button.type='button';button.className='track-toggle';button.disabled=!!b?.busy||(!b&&(!p.RTPReady||c.bridges.size>0));button.textContent=b?'停止桥接':'桥接并播放';
      button.addEventListener('click',()=>{if(current!==c)return;if(b)void stopBridge(c,b,false);else void startBridge(c,p);});row.append(info,button);
      const audio=(snapshot.Publications||[]).find(track=>track.Kind==='audio'&&track.ParticipantID===p.ParticipantID&&track.RTPReady);
      if(p.Kind==='video'&&audio&&!b){const av=document.createElement('button');av.type='button';av.className='track-toggle';av.textContent='音视频桥接';av.disabled=!p.RTPReady||c.bridges.size>0;av.addEventListener('click',()=>{if(current===c)void startBridge(c,p,audio);});row.append(av);}
      list.append(row);
    }
  }
  async function wait(ms){return new Promise(resolve=>setTimeout(resolve,ms));}
  function bridgeRequestKey() {
    const bytes=new Uint8Array(16);crypto.getRandomValues(bytes);
    return Array.from(bytes,b=>b.toString(16).padStart(2,'0')).join('');
  }
  async function createBridge(p,audio,key) {
    const body=JSON.stringify({publication_id:p.ID,...(audio?{audio_publication_id:audio.ID}:{})});
    for(let attempt=0;attempt<3;attempt++){
      try { return await nvrRequest('/api/sfu/bridges/',{method:'POST',headers:{'Idempotency-Key':key},body},true); }
      catch(err) { if(!err.network||attempt===2)throw err;await wait(attempt===0?100:500); }
    }
  }
  async function startBridge(c,p,audio) {
    const bridge={publication:p.ID,audioPublication:audio?.ID||'',requestKey:'',source:p.Source,label:'正在创建',busy:true,pc:null,location:'',abort:new AbortController()};
    c.bridges.set(p.ID,bridge);renderBridgeTracks(c.snapshot,c);
    try {
      bridge.requestKey=bridgeRequestKey();
      const result=await createBridge(p,audio,bridge.requestKey);
      if(typeof result?.id!=='string'||!result.id)throw new Error('桥接创建响应无效');
      bridge.id=result.id;
      if(current!==c||bridge.closeRequested||bridge.abort.signal.aborted){await stopBridge(c,bridge,true);return;}
      bridge.label='等待媒体';bridge.busy=false;renderBridgeTracks(c.snapshot,c);
      const pc=new RTCPeerConnection({iceServers:c.iceServers||[]});bridge.pc=pc;const transceiver=pc.addTransceiver(p.Kind,{direction:'recvonly'});if(p.Kind==='video')preferH264(transceiver);if(audio)pc.addTransceiver('audio',{direction:'recvonly'});
      const video=$('bridge-video');video.muted=p.Kind!=='audio'&&!audio;video.controls=p.Kind==='audio'||!!audio;video.hidden=false;video.onplaying=()=>{if(current===c){if(error.textContent==='浏览器阻止了播放，请使用播放器控件重试。')error.hidden=true;bridge.label='播放中';$('bridge-status').textContent='WHEP 播放中';renderBridgeTracks(c.snapshot,c);}};
      const media=new MediaStream();pc.ontrack=event=>{if(current!==c)return;media.addTrack(event.track);video.srcObject=media;void video.play().catch(err=>{if(current===c&&err.name!=='AbortError'&&video.paused)showError('浏览器阻止了播放，请使用播放器控件重试。');});};
      const offer=await pc.createOffer();await pc.setLocalDescription(offer);await gather(pc);
      let response;
      for(let attempt=0;attempt<10;attempt++){
        if(bridge.abort.signal.aborted||current!==c)return;
        response=await fetch(`/api/sfu/bridges/${encodeURIComponent(bridge.id)}/whep`,{method:'POST',headers:{'Authorization':nvrAuth(),'Content-Type':'application/sdp'},body:pc.localDescription.sdp,cache:'no-store',signal:bridge.abort.signal});
        if(response.status!==503&&response.status!==404)break;
        const status=await nvrRequest(`/api/sfu/bridges/${encodeURIComponent(bridge.id)}`);
        if(status.state==='failed'||status.state==='stopped')throw new Error('桥接已停止，未能发布媒体');
        if(response.status===404)break;
        await wait(500);
      }
      if(!response?.ok)throw new Error(`桥接播放暂不可用 (${response?.status||'超时'})`);
      const locationHeader=response.headers.get('Location');const answer=await response.text();
      if(!locationHeader||!locationHeader.startsWith(`/api/sfu/bridges/${encodeURIComponent(bridge.id)}/whep/`))throw new Error('无效的播放会话');
      bridge.location=locationHeader;
      if(current!==c||bridge.closeRequested||bridge.abort.signal.aborted){await stopBridge(c,bridge,true);return;}
      await pc.setRemoteDescription({type:'answer',sdp:answer});
      bridge.label='正在连接播放器';renderBridgeTracks(c.snapshot,c);
    } catch(err) {
      if(bridge.abort.signal.aborted)return;
      bridge.label='播放失败';bridge.busy=false;if(current===c){$('bridge-status').textContent=err.network?'网络连接失败，请在“我的桥接”中检查并停止可能遗留的桥接。':err.message||'桥接失败';renderBridgeTracks(c.snapshot,c);}
    }
  }
  async function stopBridge(c,bridge,quiet) {
    if(bridge.stopping){await bridge.stopPromise;return;}
    let finishStop;bridge.stopPromise=new Promise(resolve=>{finishStop=resolve;});bridge.stopping=true;bridge.busy=true;bridge.label='正在停止';bridge.abort.abort();bridge.pc?.close();
    if(current===c){$('bridge-video').srcObject=null;$('bridge-video').hidden=true;renderBridgeTracks(c.snapshot,c);}
    try {
      if(!bridge.id && bridge.requestKey)await resolveBridgeRequest(bridge);
      await closeBridgePlayer(bridge,quiet);
      if(bridge.id){await nvrRequest(`/api/sfu/bridges/${encodeURIComponent(bridge.id)}/stop`,{method:'POST',body:'{}',keepalive:quiet});
        for(let i=0;i<30;i++){const status=await nvrRequest(`/api/sfu/bridges/${encodeURIComponent(bridge.id)}`);if(status.state==='stopped'||status.state==='failed')break;await wait(200);}
        await nvrRequest(`/api/sfu/bridges/${encodeURIComponent(bridge.id)}`,{method:'DELETE',keepalive:quiet});}
      c.bridges.delete(bridge.publication);if(current===c){$('bridge-status').textContent='桥接已停止';renderBridgeTracks(c.snapshot,c);}
    } catch(err){if(err.status===404&&!err.unresolved){c.bridges.delete(bridge.publication);if(current===c){$('bridge-status').textContent='桥接已不存在';renderBridgeTracks(c.snapshot,c);}}else{bridge.busy=false;bridge.label='停止失败，可重试';if(current===c&&!quiet){$('bridge-status').textContent=err.message||'停止失败';renderBridgeTracks(c.snapshot,c);}}}
    finally {bridge.stopping=false;finishStop();}
  }
  async function resolveBridgeRequest(bridge) {
    let missing=0;
    for(let attempt=0;attempt<32&&!bridge.id;attempt++){
      let result;const timeout=new AbortController(),timer=setTimeout(()=>timeout.abort(),5000);
      try { result=await nvrRequest('/api/sfu/bridge-requests',{headers:{'Idempotency-Key':bridge.requestKey},signal:timeout.signal}); }
      catch(err) {
        if(bridge.id)return;
        if(err.status===404&&++missing<3){await wait(500);continue;}
        if(err.status===404){const unresolved=new Error('无法确认桥接是否创建，请在“我的桥接”中检查。');unresolved.unresolved=true;throw unresolved;}
        err.unresolved=true;throw err;
      } finally { clearTimeout(timer); }
      if(bridge.id)return;
      if(typeof result?.id==='string'&&result.id){
        if(result.publication_id!==bridge.publication || (result.audio_publication_id||'')!==bridge.audioPublication)throw new Error('桥接请求不匹配');
        bridge.id=result.id;return;
      }
      if(result?.state!=='pending')throw new Error('桥接请求状态无效');
      await wait(1000);
    }
    if(!bridge.id)throw new Error('桥接创建仍在处理中，请稍后重试');
  }
  async function cleanupRecoveredBridges(r,c) {
    for(const item of r.bridgeCleanup||[]){
      if(item.bridge.stopPromise)await item.bridge.stopPromise;
      const bridge=item.bridge;
      if(item.context.bridges.get(bridge.publication)===bridge)await stopBridge(item.context,bridge,true);
    }
    if(current===c && (r.bridgeCleanup||[]).some(item=>item.context.bridges.get(item.bridge.publication)===item.bridge))showError('部分桥接未能自动清理，请在“我的桥接”中检查并停止。');
  }
  function render(snapshot, c) {
    // NVR bridge workers use reserved IDs and are not meeting participants.
    const members = [...(snapshot.Participants || [])].filter(p=>!nvrMode||!p.ID.startsWith('bridge_')).sort((a,b)=>a.ID.localeCompare(b.ID));
    const publications = [...(snapshot.Publications || [])].sort((a,b)=>a.ID.localeCompare(b.ID));
    renderTracks(snapshot,c);
    if(nvrMode) renderBridgeTracks(snapshot,c);
    $('member-count').textContent = `${members.length} 人在线`;
    const signature=JSON.stringify([members.map(p=>[p.ID,p.Name]),publications.map(p=>[p.ID,p.ParticipantID,p.Kind,p.Source,p.Kind==='video'&&c.media.has(p.ID),p.Kind==='video'&&c.own.has(p.ID)])]);
    if(c.tileSignature===signature)return;
    c.tileSignature=signature;
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
        if(media)void media.play().catch(()=>{});
      }
    });
  }
  function openSocket(c) {
    return new Promise((resolve,reject)=>{
      const ws=new WebSocket(`${location.protocol==='https:'?'wss':'ws'}://${location.host}${nvrMode?`/api/sfu/ws?ticket=${encodeURIComponent(c.ticket)}`:'/ws'}`);c.ws=ws;let initialized=false;
      const timeout=setTimeout(()=>{if(!initialized){ws.close();reject(new Error('连接超时'));}},10000);
      ws.onmessage=event=>{let msg;try{msg=JSON.parse(event.data);}catch{return;}if(current!==c)return;
        if(msg.type==='snapshot'&&msg.snapshot){c.snapshot=msg.snapshot;const ids=new Set((msg.snapshot.Publications||[]).map(p=>p.ID));for(const id of c.disabledTracks)if(!ids.has(id))c.disabledTracks.delete(id);render(msg.snapshot,c);if(!initialized){initialized=true;clearTimeout(timeout);resolve(msg.snapshot);}else scheduleSync(c);}
        else if(msg.type==='result'){const p=c.pending.get(msg.id);if(!p)return;clearTimeout(p.timer);c.pending.delete(msg.id);if(msg.error)p.reject(new Error(msg.error==='forbidden'?'操作未获授权':`服务返回：${msg.error}`));else p.resolve(msg);}
      };
      ws.onerror=()=>{if(!initialized){clearTimeout(timeout);reject(new Error('无法连接信令服务'));}};
      ws.onclose=()=>{clearTimeout(timeout);if(!initialized){reject(new Error('信令连接已关闭'));return;}if(current===c)fail(c,new Error('信令连接已断开'),true);};
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
  function fail(c,err,connectionLost=false) {
    if(current!==c)return;
    if((c.recovery||(c.negotiated&&c.snapshot?.Generation&&(connectionLost||c.ws?.readyState!==WebSocket.OPEN))) && !err.terminal) {
      const r=c.recovery||{stream:c.stream,roomID:c.roomID,generation:c.snapshot.Generation,disabledTracks:new Set(c.disabledTracks),attempt:0,screenStopped:!!c.screen,hadBridge:nvrMode&&c.bridges.size>0,bridgeCleanup:nvrMode?[...c.bridges.values()].map(bridge=>({context:c,bridge})):[]};
      c.stream=null;closeCurrent();recovery=r;
      if(r.attempt<3){
        const delay=1000*2**r.attempt++;
        state(`正在重连 (${r.attempt}/3)`);showError(`连接已断开，正在重连。${r.screenStopped?'屏幕共享已停止，请重连后重新共享。':''}`);
        join.disabled=true;leave.disabled=false;if(nvrMode)updateRoomControls();
        r.timer=setTimeout(()=>{r.timer=null;if(recovery===r)void connect(r);},delay);return;
      }
      cancelRecovery();
    } else { closeCurrent();cancelRecovery(); }
    state('未连接');showError(`${err.message||'连接失败'}，请重新加入。`);
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
    const desired=new Map((c.snapshot?.Publications||[]).filter(p=>p.RTPReady && !c.own.has(p.ID) && !c.disabledTracks.has(p.ID)).map(p=>[p.ID,p]));let changed=false;
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
  async function connect(retry = null) {
    if(roomBusy)return;
    if(retry && recovery!==retry)return;
    if(!retry)cancelRecovery();
    closeCurrent();error.hidden=true;join.disabled=true;micDevice.disabled=true;cameraDevice.disabled=true;state('正在连接');
    const c={pending:new Map(),requestID:0,subscribed:new Map(),own:new Map(),media:new Map(),disabledTracks:new Set(retry?.disabledTracks||[]),bridges:new Map(),queue:Promise.resolve(),negotiated:false,syncPending:false,syncRunning:false,disconnectTimer:null,recovery:retry,roomID:retry?.roomID,deviceSelection:{audio:micDevice.value,video:cameraDevice.value}};current=c;if(retry){c.stream=retry.stream;leave.disabled=false;}if(nvrMode)updateRoomControls();
    try {
      if(!nvrMode){const login=await fetch('/session',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({token:token.value})});if(!login.ok)throw new Error('访问口令无效，请检查终端中的口令');if(current!==c)return;}
      if(retry&&c.stream)for(const track of c.stream.getTracks())if(track.readyState==='ended')c.stream.removeTrack(track);
      if(!retry&&(micOption.checked||cameraOption.checked)){if(!navigator.mediaDevices?.getUserMedia)throw new Error('当前浏览器无法访问媒体设备');c.stream=await navigator.mediaDevices.getUserMedia({audio:micOption.checked?deviceConstraint(micDevice.value):false,video:cameraOption.checked?deviceConstraint(cameraDevice.value):false});if(current!==c){c.stream.getTracks().forEach(t=>t.stop());return;}void refreshDevices();}
      if(nvrMode){const auth=nvrAuth();const roomID=retry?.roomID||$('nvr-room').value||'default';c.roomID=roomID;const generation=retry?.generation||roomGenerations.get(roomID);const ticket=await nvrRequest('/api/sfu/ticket',{method:'POST',body:JSON.stringify({room_id:roomID,...(generation?{room_generation:generation}:{})})});if(current!==c)return;$('room-title').textContent=roomID;$('room-description').textContent='NVR 房间 · Opus / H.264';sessionStorage.setItem('nvr_auth',auth.slice(6));$('nvr-password').value='';$('nvr-login').hidden=true;c.ticket=ticket.ticket;c.iceServers=ticket.ice_servers||[];}
      await openSocket(c);if(current!==c)return;
      if(retry && c.snapshot.Generation!==retry.generation){const err=new Error('房间已关闭或重新创建');err.terminal=true;throw err;}
      const pc=new RTCPeerConnection({iceServers:c.iceServers||[]});c.pc=pc;if(networkPanel.open)startNetworkStats(c);
      pc.onconnectionstatechange=()=>{
        if(current!==c)return;
        if(['connected','failed','closed'].includes(pc.connectionState)){clearTimeout(c.disconnectTimer?.id);c.disconnectTimer=null;}
        if(pc.connectionState==='connected')state('媒体连接正常',true);
        else if(pc.connectionState==='failed')fail(c,new Error('媒体连接失败'),true);
        else if(pc.connectionState==='disconnected'){
          state('媒体连接中断');
          if(c.disconnectTimer===null){
            const timer={id:null};c.disconnectTimer=timer;
            timer.id=setTimeout(()=>{if(current!==c||c.disconnectTimer!==timer)return;c.disconnectTimer=null;if(pc.connectionState!=='connected'&&pc.connectionState!=='closed')fail(c,new Error('媒体连接中断超时'),true);},10000);
          }
        }
      };
      pc.ontrack=event=>{if(current!==c)return;const id=[...c.subscribed].find(([,entry])=>entry.transceiver===event.transceiver)?.[0];if(!id)return;
        if(event.track.kind==='audio'){const audio=document.createElement('audio');audio.autoplay=true;audio.srcObject=new MediaStream([event.track]);audioContainer.append(audio);c.media.set(id,audio);audio.play().catch(()=>{showError('浏览器阻止自动播放，请点击“启用声音”。');$('enable-audio').hidden=false;});}
        else {const video=document.createElement('video');video.autoplay=true;video.muted=true;video.playsInline=true;video.srcObject=new MediaStream([event.track]);c.media.set(id,video);if(c.snapshot)render(c.snapshot,c);}
        event.track.onended=()=>{const media=c.media.get(id);if(media){media.remove();c.media.delete(id);}if(current===c&&c.snapshot)render(c.snapshot,c);};
      };
      if(c.stream){for(const track of c.stream.getTracks())if(track.readyState!=='ended')await publishTrack(c,track,track.kind==='audio'?'microphone':'camera');}
      if(!c.stream?.getTracks().length)pc.createDataChannel('control');
      await negotiate(c);if(current!==c)return;c.negotiated=true;if(c.snapshot)render(c.snapshot,c);leave.disabled=false;mute.disabled=!c.stream?.getAudioTracks().length;cameraToggle.disabled=!c.stream?.getVideoTracks().length;share.disabled=false;micDevice.disabled=!c.stream?.getAudioTracks().length;cameraDevice.disabled=!c.stream?.getVideoTracks().length;for(const [button,track,on,off] of [[mute,c.stream?.getAudioTracks()[0],'◉ 静音','◎ 取消静音'],[cameraToggle,c.stream?.getVideoTracks()[0],'◈ 关闭摄像头','◈ 开启摄像头']])if(track){button.setAttribute('aria-pressed',String(!track.enabled));button.textContent=track.enabled?on:off;}if(retry){recovery=null;c.recovery=null;const notices=[];if(retry.screenStopped)notices.push('请重新开启屏幕共享');if(retry.hadBridge)notices.push('正在检查先前桥接的清理状态');if(notices.length)showError(`已重新连接；${notices.join('；')}。`);void cleanupRecoveredBridges(retry,c);}state(pc.connectionState==='connected'?'媒体连接正常':'正在建立媒体连接',pc.connectionState==='connected');scheduleSync(c);
    } catch(err){fail(c,err);}
  }
  form.addEventListener('submit',event=>{event.preventDefault();void connect();});
  leave.addEventListener('click',()=>{cancelRecovery();closeCurrent();state('已离开');tiles.replaceChildren();$('member-count').textContent='0 人在线';});
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
  window.addEventListener('pagehide',()=>{cancelRecovery();closeCurrent();});
})();
