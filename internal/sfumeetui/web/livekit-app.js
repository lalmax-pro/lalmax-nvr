(() => {
  'use strict';
  const $ = (id) => document.getElementById(id);
  const { Room, RoomEvent, Track } = window.LivekitClient;
  const form = $('join-form');
  const tiles = $('tiles');
  const audioContainer = $('audio-container');
  const trackList = $('track-list');
  const controls = ['mute', 'camera-toggle', 'share', 'leave'];
  const localMode = document.documentElement.dataset.mode === 'local';
  if (localMode) {
    $('bridge-panel').hidden = true;
    $('rooms-panel').hidden = true;
    $('back-nvr').hidden = true;
    $('room').value = 'demo';
    $('room').readOnly = true;
    $('nvr-username').value = 'demo';
    $('nvr-username').hidden = true;
    document.querySelector('label[for="nvr-username"]').hidden = true;
    document.querySelector('label[for="nvr-password"]').textContent = '终端访问口令';
    document.querySelector('.card-intro').textContent = '输入终端中显示的访问口令。';
  }
  let current = null;
  let joining = false;
  let generation = 0;

  function bridgeError(message) {
    $('bridge-error').textContent = message || '';
    $('bridge-error').hidden = !message;
  }
  async function bridgeRequest(path, options = {}) {
    const response = await fetch(path, {
      ...options, cache: 'no-store',
      headers: { Authorization: `Basic ${current?.auth || basicAuth()}`, ...options.headers },
    });
    if (!response.ok) {
      const body = await response.json().catch(() => ({}));
      throw new Error(body.error || `操作失败 (${response.status})`);
    }
    return response.status === 204 ? null : response.json();
  }
  async function refreshRooms() {
    try {
      const rooms = await bridgeRequest('/api/sfu/rooms/');
      const list = $('room-list');
      list.replaceChildren();
      for (const room of rooms) {
        const row = document.createElement('div');
        row.className = 'track-row';
        const info = document.createElement('div');
        info.className = 'track-info';
        const name = document.createElement('strong');
        name.textContent = room.id;
        const detail = document.createElement('small');
        detail.textContent = `${room.participants} 人在线`;
        info.append(name, detail);
        const select = document.createElement('button');
        select.type = 'button'; select.className = 'track-toggle'; select.textContent = '选择';
        select.disabled = !!current || joining;
        select.addEventListener('click', () => { if (!current && !joining) $('room').value = room.id; });
        row.append(info, select);
        if (room.id !== 'default') {
          const close = document.createElement('button');
          close.type = 'button'; close.className = 'track-toggle'; close.textContent = '关闭';
          close.addEventListener('click', async () => {
            close.disabled = true;
            try {
              await bridgeRequest(`/api/sfu/rooms/${encodeURIComponent(room.id)}?generation=${encodeURIComponent(room.generation)}`, { method: 'DELETE' });
              showError(''); await refreshRooms();
            } catch (error) { showError(error.message); close.disabled = false; }
          });
          row.append(close);
        }
        list.append(row);
      }
    } catch (error) { showError(error.message); }
  }
  function closeBridgePlayer(state) {
    const player = state?.bridgePlayer;
    if (!player) return;
    state.bridgePlayer = null;
    player.controller.abort();
    player.pc.close();
    $('bridge-player').srcObject = null;
    $('bridge-player').hidden = true;
    $('bridge-player-close').hidden = true;
    if (player.location) fetch(player.location, { method: 'DELETE', headers: { Authorization: `Basic ${state.auth}` }, cache: 'no-store', keepalive: true }).catch(() => {});
  }
  async function playBridge(state, id) {
    closeBridgePlayer(state);
    const pc = new RTCPeerConnection();
    const player = { id, pc, controller: new AbortController(), location: '' };
    state.bridgePlayer = player;
    const active = () => current === state && state.bridgePlayer === player;
    try {
      const media = new MediaStream();
      pc.ontrack = ({ track }) => { if (active()) media.addTrack(track); };
      pc.addTransceiver('video', { direction: 'recvonly' });
      pc.addTransceiver('audio', { direction: 'recvonly' });
      await pc.setLocalDescription(await pc.createOffer());
      await new Promise((resolve, reject) => {
        if (pc.iceGatheringState === 'complete') { resolve(); return; }
        const timer = setTimeout(() => reject(new Error('播放连接准备超时')), 10000);
        const done = () => { if (pc.iceGatheringState === 'complete') { clearTimeout(timer); resolve(); } };
        pc.addEventListener('icegatheringstatechange', done);
        player.controller.signal.addEventListener('abort', () => { clearTimeout(timer); reject(new Error('播放已取消')); }, { once: true });
      });
      if (!active()) return;
      const path = `/api/sfu/bridges/${encodeURIComponent(id)}/whep`;
      let response;
      for (let attempt = 0; attempt < 5; attempt++) {
        response = await fetch(path, { method: 'POST', signal: player.controller.signal, cache: 'no-store', headers: { Authorization: `Basic ${state.auth}`, 'Content-Type': 'application/sdp' }, body: pc.localDescription.sdp });
        if (response.status !== 503 || attempt === 4) break;
        await new Promise(resolve => setTimeout(resolve, 500));
        if (!active()) return;
      }
      if (!response.ok) {
        const body = await response.json().catch(() => ({}));
        throw new Error(body.error || `播放失败 (${response.status})`);
      }
      const location = new URL(response.headers.get('Location') || '', window.location.href);
      if (location.origin !== window.location.origin || !location.pathname.startsWith(`${path}/`)) throw new Error('播放会话地址无效');
      player.location = location.href;
      if (!active()) {
        fetch(player.location, { method: 'DELETE', headers: { Authorization: `Basic ${state.auth}` }, cache: 'no-store', keepalive: true }).catch(() => {});
        return;
      }
      await pc.setRemoteDescription({ type: 'answer', sdp: await response.text() });
      if (!active()) return;
      $('bridge-player').srcObject = media;
      $('bridge-player').hidden = false;
      $('bridge-player-close').hidden = false;
      bridgeError('');
    } catch (error) {
      if (active()) { closeBridgePlayer(state); bridgeError(error.message || '播放失败'); }
    }
  }
  function renderBridgeTracks(room) {
    if (localMode) return;
    const main = $('bridge-track');
    const audio = $('bridge-audio');
    const oldMain = main.value;
    const oldAudio = audio.value;
    main.replaceChildren(new Option('选择轨道', ''));
    audio.replaceChildren(new Option('不附加', ''));
    for (const participant of [room.localParticipant, ...room.remoteParticipants.values()]) {
      for (const pub of participant.trackPublications.values()) {
        if (!pub.trackSid || pub.isMuted) continue;
        const name = participant === room.localParticipant ? '我' : participantName(participant);
        const label = `${name} · ${sourceName(pub.source)}`;
        main.add(new Option(label, pub.trackSid));
        if (pub.kind === Track.Kind.Audio) audio.add(new Option(label, pub.trackSid));
      }
    }
    main.value = oldMain;
    audio.value = oldAudio;
    if (main.selectedIndex < 0) main.selectedIndex = 0;
    if (audio.selectedIndex < 0) audio.selectedIndex = 0;
    main.disabled = false;
    audio.disabled = false;
    $('bridge-create').disabled = !!current?.bridgeBusy;
  }
  async function refreshBridges(state) {
    if (localMode || current !== state || state.refreshingBridges) return;
    state.refreshingBridges = true;
    try {
      const entries = await bridgeRequest('/api/sfu/bridges/');
      if (current !== state) return;
      if (state.bridgePlayer && !entries.some(entry => entry.id === state.bridgePlayer.id && entry.state === 'pending')) closeBridgePlayer(state);
      const list = $('bridge-list');
      list.replaceChildren();
      for (const entry of entries) {
        const row = document.createElement('div');
        row.className = 'track-row';
        const info = document.createElement('div');
        info.className = 'track-info';
        const name = document.createElement('strong');
        name.textContent = entry.stream_id;
        const detail = document.createElement('small');
        detail.textContent = ({ pending: '已提交', stopping: '正在停止', stopped: '已停止', failed: '已结束' })[entry.state] || entry.state;
        info.append(name, detail);
        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'track-toggle';
        const terminal = entry.state === 'stopped' || entry.state === 'failed';
        button.textContent = terminal ? '移除' : '停止';
        button.disabled = entry.state === 'stopping';
        button.addEventListener('click', async () => {
          button.disabled = true;
          try {
            const path = `/api/sfu/bridges/${encodeURIComponent(entry.id)}`;
            await bridgeRequest(terminal ? path : `${path}/stop`, terminal ? { method: 'DELETE' } : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
            if (state.bridgePlayer?.id === entry.id) closeBridgePlayer(state);
            bridgeError('');
            await refreshBridges(state);
          } catch (error) { bridgeError(error.message); button.disabled = false; }
        });
        row.append(info);
        if (entry.state === 'pending') {
          const play = document.createElement('button');
          play.type = 'button';
          play.className = 'track-toggle';
          play.textContent = '播放';
          play.addEventListener('click', () => playBridge(state, entry.id));
          row.append(play);
        }
        row.append(button);
        list.append(row);
      }
    } catch (error) {
      if (current === state) bridgeError(error.message);
    } finally { state.refreshingBridges = false; }
  }

  function showError(message) {
    $('error').textContent = message || '';
    $('error').hidden = !message;
  }
  function status(text, connected = false) {
    $('status-text').textContent = text;
    $('connection-badge').classList.toggle('connected', connected);
    $('connection-badge').lastElementChild.textContent = text;
    $('status-dot').classList.toggle('on', connected);
  }
  function basicAuth() {
    const user = $('nvr-username').value;
    const password = $('nvr-password').value;
    if (password) {
      if (!user || !password) throw new Error('请输入完整的 NVR 用户名和密码');
      const bytes = new TextEncoder().encode(`${user}:${password}`);
      return btoa(Array.from(bytes, (byte) => String.fromCharCode(byte)).join(''));
    }
    const saved = sessionStorage.getItem('nvr_auth');
    if (!saved) throw new Error('请输入 NVR 用户名和密码');
    if (user && sessionStorage.getItem('nvr_auth_user') !== user) throw new Error('请输入完整的 NVR 用户名和密码');
    return saved;
  }
  async function joinCredentials(room, authorization) {
    const response = await fetch('/api/sfu/livekit/join', {
      method: 'POST', cache: 'no-store',
      headers: { Authorization: `Basic ${authorization}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({ room }),
    });
    if (!response.ok) {
      const body = await response.json().catch(() => ({}));
      throw new Error(body.error || `加入失败 (${response.status})`);
    }
    return response.json();
  }
  function controlState(room) {
    const connected = !!room;
    for (const id of controls) $(id).disabled = !connected;
    $('join').disabled = joining || connected;
    $('room').disabled = joining || connected;
    const participant = room?.localParticipant;
    const microphone = !!participant?.isMicrophoneEnabled;
    const camera = !!participant?.isCameraEnabled;
    const screen = !!participant?.isScreenShareEnabled;
    $('mute').textContent = microphone ? '关闭麦克风' : '开启麦克风';
    $('camera-toggle').textContent = camera ? '关闭摄像头' : '开启摄像头';
    $('share').textContent = screen ? '停止共享' : '共享屏幕';
    $('mute').setAttribute('aria-pressed', String(microphone));
    $('camera-toggle').setAttribute('aria-pressed', String(camera));
    $('share').setAttribute('aria-pressed', String(screen));
    $('enable-audio').hidden = !connected || room.canPlaybackAudio;
  }
  function clearMedia(state) {
    for (const track of state.attached) {
      for (const element of track.detach()) element.remove();
    }
    state.attached.clear();
    tiles.replaceChildren();
    audioContainer.replaceChildren();
  }
  function sourceName(source) {
    switch (source) {
      case Track.Source.Camera: return '摄像头';
      case Track.Source.Microphone: return '麦克风';
      case Track.Source.ScreenShare: return '屏幕共享';
      case Track.Source.ScreenShareAudio: return '屏幕音频';
      default: return '媒体';
    }
  }
  function participantName(participant) {
    if (participant.name) return participant.name;
    const match = /^(?:nvr|demo)_([0-9a-f]{8})/i.exec(participant.identity);
    return match ? `成员 ${match[1]}` : participant.identity;
  }
  function addVideo(state, track, title, local) {
    const tile = document.createElement('div');
    tile.className = 'tile video-tile';
    const video = track.attach();
    video.autoplay = true;
    video.playsInline = true;
    if (local) video.muted = true;
    const label = document.createElement('span');
    label.className = 'video-label';
    label.textContent = title;
    tile.append(video, label);
    tiles.append(tile);
    state.attached.add(track);
  }
  function render(state) {
    if (current !== state) return;
    const room = state.room;
    clearMedia(state);
    renderBridgeTracks(room);
    trackList.replaceChildren();
    $('member-count').textContent = `${room.remoteParticipants.size + 1} 人在线`;
    let publications = 0;
    const local = room.localParticipant;
    for (const pub of local.trackPublications.values()) {
      if (pub.track && pub.kind === Track.Kind.Video && !pub.isMuted) addVideo(state, pub.track, `我 · ${sourceName(pub.source)}`, true);
    }
    for (const participant of room.remoteParticipants.values()) {
      for (const pub of participant.trackPublications.values()) {
        publications++;
        const name = participantName(participant);
        const description = `${name} · ${sourceName(pub.source)}`;
        if (pub.track && pub.isSubscribed && !pub.isMuted) {
          if (pub.kind === Track.Kind.Video) addVideo(state, pub.track, description, false);
          if (pub.kind === Track.Kind.Audio) {
            const audio = pub.track.attach();
            audio.autoplay = true;
            audioContainer.append(audio);
            state.attached.add(pub.track);
          }
        }
        const row = document.createElement('div');
        row.className = 'track-row';
        const info = document.createElement('div');
        info.className = 'track-info';
        const label = document.createElement('strong');
        label.textContent = description;
        const detail = document.createElement('small');
        detail.textContent = pub.isMuted ? '已静音' : pub.isSubscribed ? '已订阅' : '未订阅';
        info.append(label, detail);
        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'track-toggle';
        const desired = state.desired.has(pub.trackSid) ? state.desired.get(pub.trackSid) : true;
        button.textContent = desired ? '取消订阅' : '订阅';
        button.setAttribute('aria-pressed', String(desired));
        button.setAttribute('aria-label', `${button.textContent} ${description}`);
        button.addEventListener('click', () => {
          if (current !== state) return;
          state.desired.set(pub.trackSid, !desired);
          pub.setSubscribed(!desired);
          render(state);
        });
        row.append(info, button);
        trackList.append(row);
      }
    }
    if (!tiles.childElementCount) {
      const empty = document.createElement('div');
      empty.className = 'empty-state';
      empty.textContent = '当前没有视频轨道';
      tiles.append(empty);
    }
    if (!publications) {
      const empty = document.createElement('p');
      empty.className = 'track-empty';
      empty.textContent = '暂无远端轨道。';
      trackList.append(empty);
    }
    controlState(room);
  }
  async function devices() {
    if (!navigator.mediaDevices?.enumerateDevices) return;
    const list = await navigator.mediaDevices.enumerateDevices();
    for (const [id, kind] of [['microphone-device', 'audioinput'], ['camera-device', 'videoinput']]) {
      const select = $(id);
      const selected = select.value;
      select.replaceChildren(new Option('系统默认', ''));
      let index = 0;
      for (const device of list.filter((item) => item.kind === kind)) {
        index++;
        select.add(new Option(device.label || `设备 ${index}`, device.deviceId));
      }
      select.value = selected;
    }
  }
  function bindRoom(state) {
    const room = state.room;
    const update = () => render(state);
    for (const event of [RoomEvent.ParticipantConnected, RoomEvent.ParticipantDisconnected,
      RoomEvent.TrackPublished, RoomEvent.TrackUnpublished, RoomEvent.TrackSubscribed,
      RoomEvent.TrackUnsubscribed, RoomEvent.TrackMuted, RoomEvent.TrackUnmuted,
      RoomEvent.LocalTrackPublished, RoomEvent.LocalTrackUnpublished,
      RoomEvent.AudioPlaybackStatusChanged]) room.on(event, update);
    room.on(RoomEvent.Reconnecting, () => { if (current === state) status('正在重连'); });
    room.on(RoomEvent.Reconnected, () => { if (current === state) { status('已连接', true); render(state); } });
    room.on(RoomEvent.Disconnected, () => { if (current === state) cleanup(state, '连接已结束'); });
  }
  function cleanup(state, message) {
    if (current !== state) return;
    current = null;
    clearInterval(state.bridgeTimer);
    closeBridgePlayer(state);
    $('bridge-track').disabled = true;
    $('bridge-audio').disabled = true;
    $('bridge-create').disabled = true;
    $('bridge-list').replaceChildren();
    bridgeError('');
    generation++;
    clearMedia(state);
    trackList.replaceChildren();
    $('room-title').textContent = '尚未加入';
    $('member-count').textContent = '0 人在线';
    joining = false;
    controlState(null);
    status(message);
  }
  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (joining || current) return;
    const attempt = ++generation;
    joining = true;
    controlState(null);
    showError('');
    status('正在加入');
    let state;
    try {
      const auth = basicAuth();
      const credentials = await joinCredentials($('room').value, auth);
      if (attempt !== generation) return;
      const room = new Room({ adaptiveStream: true, dynacast: true, publishDefaults: { videoCodec: 'h264' } });
      state = { room, auth, attached: new Set(), desired: new Map() };
      current = state;
      bindRoom(state);
      await room.connect(credentials.url, credentials.token, { autoSubscribe: true });
      if (current !== state || attempt !== generation) { await room.disconnect(); return; }
      sessionStorage.setItem('nvr_auth', auth);
      sessionStorage.setItem('nvr_auth_user', $('nvr-username').value);
      $('nvr-password').value = '';
      $('room-title').textContent = credentials.room;
      joining = false;
      status('已连接', true);
      render(state);
      if (!localMode) {
        refreshBridges(state);
        refreshRooms();
        state.bridgeTimer = setInterval(() => refreshBridges(state), 5000);
      }
      const failures = [];
      if ($('microphone').checked) {
        try { await room.localParticipant.setMicrophoneEnabled(true, { deviceId: $('microphone-device').value || 'default' }); }
        catch (_) { failures.push('麦克风开启失败'); }
      }
      if (current !== state) return;
      if ($('camera').checked) {
        try { await room.localParticipant.setCameraEnabled(true, { deviceId: $('camera-device').value || 'default' }); }
        catch (_) { failures.push('摄像头开启失败'); }
      }
      if (current !== state) return;
      showError(failures.join('；'));
      await devices().catch(() => {});
      render(state);
    } catch (error) {
      const owned = state ? current === state : attempt === generation;
      if (state && owned) { cleanup(state, '未连接'); await state.room.disconnect().catch(() => {}); }
      if (owned) {
        joining = false;
        controlState(null);
        status('未连接');
        showError(error.message || '无法加入会议');
      }
    }
  });
  async function action(callback) {
    const state = current;
    if (!state) return;
    try { showError(''); await callback(state.room); if (current === state) { await devices().catch(() => {}); render(state); } }
    catch (error) { if (current === state) showError(error.message || '操作失败'); }
  }
  $('mute').addEventListener('click', () => action((room) => room.localParticipant.setMicrophoneEnabled(!room.localParticipant.isMicrophoneEnabled, { deviceId: $('microphone-device').value || 'default' })));
  $('camera-toggle').addEventListener('click', () => action((room) => room.localParticipant.setCameraEnabled(!room.localParticipant.isCameraEnabled, { deviceId: $('camera-device').value || 'default' })));
  $('share').addEventListener('click', () => action((room) => room.localParticipant.setScreenShareEnabled(!room.localParticipant.isScreenShareEnabled, { audio: $('screen-audio').checked })));
  $('enable-audio').addEventListener('click', () => action((room) => room.startAudio()));
  $('leave').addEventListener('click', () => { const state = current; if (state) { cleanup(state, '已离开'); state.room.disconnect().catch(() => {}); } });
  $('room-refresh').addEventListener('click', refreshRooms);
  $('room-create').addEventListener('click', async () => {
    const button = $('room-create');
    button.disabled = true;
    try {
      await bridgeRequest('/api/sfu/rooms/', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: $('room').value }) });
      showError(''); await refreshRooms();
    } catch (error) { showError(error.message); }
    finally { button.disabled = false; }
  });
  $('bridge-player-close').addEventListener('click', () => closeBridgePlayer(current));
  $('bridge-create').addEventListener('click', async () => {
    const state = current;
    if (!state || state.bridgeBusy) return;
    if (!$('bridge-track').value) { bridgeError('请选择主轨道'); return; }
    const button = $('bridge-create');
    state.bridgeBusy = true;
    button.disabled = true;
    try {
      const roomSID = await state.room.getSid();
      if (current !== state) return;
      await bridgeRequest('/api/sfu/bridges/', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': crypto.randomUUID() },
        body: JSON.stringify({ room_id: state.room.name, room_generation: roomSID, publication_id: $('bridge-track').value, audio_publication_id: $('bridge-audio').value }),
      });
      bridgeError('');
      await refreshBridges(state);
    } catch (error) { if (current === state) bridgeError(error.message); }
    finally { state.bridgeBusy = false; if (current === state) button.disabled = false; }
  });
  $('microphone-device').addEventListener('change', () => action((room) => room.switchActiveDevice('audioinput', $('microphone-device').value || 'default')));
  $('camera-device').addEventListener('change', () => action((room) => room.switchActiveDevice('videoinput', $('camera-device').value || 'default')));
  navigator.mediaDevices?.addEventListener('devicechange', () => devices().catch(() => {}));
  window.addEventListener('beforeunload', () => { if (current) current.room.disconnect(); });
  devices().catch(() => {});
  controlState(null);
})();
