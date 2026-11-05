(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const form = $('join-form'), token = $('token'), micOption = $('microphone');
  const status = $('status-text'), badge = $('connection-badge'), dot = $('status-dot');
  const join = $('join'), leave = $('leave'), mute = $('mute'), error = $('error');
  const notice = $('refresh-notice'), tiles = $('tiles'), audioContainer = $('audio-container');
  let current = null, generation = 0;

  function state(text, connected = false) {
    status.textContent = text; badge.querySelector('span').textContent = connected ? '已连接' : text;
    badge.classList.toggle('connected', connected); dot.classList.toggle('on', connected);
  }
  function showError(message) { error.textContent = message; error.hidden = false; }
  function closeCurrent() {
    const c = current; current = null; generation++;
    if (c) {
      for (const pending of c.pending.values()) { clearTimeout(pending.timer); pending.reject(new Error('连接已关闭')); }
      c.pending.clear(); c.ws?.close(); c.pc?.close(); c.stream?.getTracks().forEach(t => t.stop());
    }
    audioContainer.replaceChildren(); $('enable-audio').hidden = true; notice.hidden = true; leave.disabled = true; mute.disabled = true;
    mute.textContent = '◉ 静音'; mute.setAttribute('aria-pressed', 'false'); join.disabled = false;
  }
  function render(snapshot, c) {
    const members = snapshot.Participants || [], publications = snapshot.Publications || [];
    $('member-count').textContent = `${members.length} 人在线`;
    tiles.replaceChildren();
    if (!members.length) {
      const empty = document.createElement('div'); empty.className = 'empty-state'; empty.textContent = '当前没有成员'; tiles.append(empty);
    } else {
      members.forEach((member, index) => {
        const tile = document.createElement('div'); tile.className = 'tile';
        const avatar = document.createElement('div'); avatar.className = 'avatar'; avatar.textContent = String(index + 1).padStart(2, '0');
        const label = document.createElement('strong'); label.textContent = member.Name || `成员 ${index + 1}`;
        const detail = document.createElement('small'); detail.textContent = publications.some(p => p.ParticipantID === member.ID) ? '音频已发布' : '已加入房间';
        tile.append(avatar, label, detail); tiles.append(tile);
      });
    }
    if (c.negotiated) {
      const remote = new Set(publications.filter(p => p.ID !== c.ownPublication).map(p => p.ID));
      notice.hidden = remote.size === c.subscribed.size && [...remote].every(id => c.subscribed.has(id));
    }
  }
  function openSocket(c) {
    return new Promise((resolve, reject) => {
      const ws = new WebSocket(`${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/ws`); c.ws = ws;
      let initialized = false;
      const timeout = setTimeout(() => { if (!initialized) { ws.close(); reject(new Error('连接超时')); } }, 10000);
      ws.onmessage = event => {
        let msg; try { msg = JSON.parse(event.data); } catch { return; }
        if (current !== c) return;
        if (msg.type === 'snapshot' && msg.snapshot) {
          c.snapshot = msg.snapshot; render(msg.snapshot, c);
          if (!initialized) { initialized = true; clearTimeout(timeout); resolve(msg.snapshot); }
        } else if (msg.type === 'result') {
          const p = c.pending.get(msg.id); if (!p) return;
          clearTimeout(p.timer); c.pending.delete(msg.id);
          if (msg.error) p.reject(new Error(msg.error === 'forbidden' ? '操作未获授权' : `服务返回：${msg.error}`)); else p.resolve(msg);
        }
      };
      ws.onerror = () => { if (!initialized) { clearTimeout(timeout); reject(new Error('无法连接信令服务')); } };
      ws.onclose = () => {
        clearTimeout(timeout);
        if (!initialized) reject(new Error('信令连接已关闭'));
        if (current === c) { closeCurrent(); state('连接已断开'); showError('连接已断开，请重新加入。'); }
      };
    });
  }
  function request(c, type, fields = {}) {
    return new Promise((resolve, reject) => {
      if (current !== c || c.ws.readyState !== WebSocket.OPEN) return reject(new Error('连接已关闭'));
      const id = String(++c.requestID); const timer = setTimeout(() => { c.pending.delete(id); reject(new Error('请求超时')); }, 12000);
      c.pending.set(id, { resolve, reject, timer });
      c.ws.send(JSON.stringify({ id, type, ...fields }));
    });
  }
  function gather(pc) {
    if (pc.iceGatheringState === 'complete') return Promise.resolve();
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { pc.removeEventListener('icegatheringstatechange', check); reject(new Error('ICE 收集超时')); }, 10000);
      function check() { if (pc.iceGatheringState === 'complete') { clearTimeout(timer); pc.removeEventListener('icegatheringstatechange', check); resolve(); } }
      pc.addEventListener('icegatheringstatechange', check); check();
    });
  }
  async function connect() {
    closeCurrent(); error.hidden = true; join.disabled = true; state('正在连接');
    const c = { epoch: generation, pending: new Map(), requestID: 0, subscribed: new Set(), ownPublication: '', negotiated: false };
    current = c;
    try {
      const login = await fetch('/session', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token: token.value }) });
      if (!login.ok) throw new Error('访问口令无效，请检查终端中的口令。');
      if (micOption.checked) {
        if (!navigator.mediaDevices?.getUserMedia) throw new Error('当前浏览器无法访问麦克风。');
        c.stream = await navigator.mediaDevices.getUserMedia({ audio: true, video: false });
        if (current !== c) { c.stream.getTracks().forEach(t => t.stop()); return; }
      }
      const initial = await openSocket(c); if (current !== c) return;
      const pc = new RTCPeerConnection(); c.pc = pc;
      pc.onconnectionstatechange = () => { if (current !== c) return; if (pc.connectionState === 'connected') state('音频连接正常', true); else if (['failed','disconnected'].includes(pc.connectionState)) { state('连接中断'); showError('媒体连接中断，请重新连接。'); } };
      pc.ontrack = event => {
        if (current !== c || event.track.kind !== 'audio') return;
        const audio = document.createElement('audio'); audio.autoplay = true; audio.controls = true;
        audio.srcObject = new MediaStream([event.track]); audioContainer.append(audio);
        audio.play().catch(() => { showError('浏览器阻止自动播放，请点击下方“启用声音”。'); $('enable-audio').hidden = false; });
      };
      const pubs = (initial.Publications || []).filter(p => p.Kind === 'audio');
      for (const p of pubs) { await request(c, 'subscribe', { publication_id: p.ID }); c.subscribed.add(p.ID); pc.addTransceiver('audio', { direction: 'recvonly' }); }
      if (c.stream) {
        const track = c.stream.getAudioTracks()[0]; if (!track) throw new Error('未找到麦克风音轨。');
        const publication = await request(c, 'publish', { kind: 'audio', source: 'microphone', codec: 'audio/opus', track_id: track.id });
        c.ownPublication = publication.publication.ID; pc.addTrack(track, c.stream);
      } else if (!pubs.length) pc.addTransceiver('audio', { direction: 'recvonly' });
      const offer = await pc.createOffer(); await pc.setLocalDescription(offer); await gather(pc);
      const answer = await request(c, 'offer', { sdp: pc.localDescription.sdp });
      await pc.setRemoteDescription({ type: 'answer', sdp: answer.sdp });
      if (current !== c) return;
      c.negotiated = true; if (c.snapshot) render(c.snapshot, c); leave.disabled = false; mute.disabled = !c.stream; state('正在建立音频连接');
    } catch (err) {
      if (current !== c) return;
      closeCurrent(); state('未连接'); showError(`${err.message || '连接失败'}${err.message?.includes('operation_failed') ? '；发布端可能尚未准备好，请稍后重试。' : ''}`);
    }
  }
  form.addEventListener('submit', event => { event.preventDefault(); void connect(); });
  leave.addEventListener('click', () => { closeCurrent(); state('已离开'); tiles.replaceChildren(); $('member-count').textContent = '0 人在线'; });
  $('refresh').addEventListener('click', () => void connect());
  mute.addEventListener('click', () => { const track = current?.stream?.getAudioTracks()[0]; if (!track) return; track.enabled = !track.enabled; mute.setAttribute('aria-pressed', String(!track.enabled)); mute.textContent = track.enabled ? '◉ 静音' : '◎ 取消静音'; });
  $('enable-audio').addEventListener('click', () => { for (const audio of audioContainer.querySelectorAll('audio')) void audio.play(); $('enable-audio').hidden = true; });
  window.addEventListener('pagehide', closeCurrent);
})();
