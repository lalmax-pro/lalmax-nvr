const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
(async () => {
  const browser = await chromium.launch({executablePath:process.env.LIVEKIT_BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox','--mute-audio','--use-fake-device-for-media-stream','--use-fake-ui-for-media-stream']});
  const context = await browser.newContext({permissions:['camera','microphone']});
  await context.addInitScript(() => {
    const NativeWebSocket = window.WebSocket;
    window.WebSocket = class extends NativeWebSocket {
      constructor(url, protocols) {
        super(url, protocols);
        window.__liveKitTestSocket = this;
      }
    };
    navigator.mediaDevices.getDisplayMedia = async () => {
      const canvas = document.createElement('canvas'); canvas.width = 320; canvas.height = 180;
      const ctx = canvas.getContext('2d'); let n = 0;
      const draw = () => {ctx.fillStyle = n++ % 2 ? '#dfc' : '#acd';ctx.fillRect(0,0,320,180);};
      draw(); const timer = setInterval(draw, 100);
      const stream = canvas.captureStream(10);
      stream.getVideoTracks()[0].addEventListener('ended', () => clearInterval(timer), {once:true});
      const ac = new AudioContext(); const dest = ac.createMediaStreamDestination();
      const oscillator = ac.createOscillator(); oscillator.connect(dest); oscillator.start();
      stream.addTrack(dest.stream.getAudioTracks()[0]);
      stream.getVideoTracks()[0].addEventListener('ended', () => {oscillator.stop();ac.close();}, {once:true});
      return stream;
    };
  });
  const publisher = await context.newPage(); const viewer = await context.newPage();
  let udpProxy;
  const errors=[];
  try {
    if (process.env.LK_UDP_LOSS_TEST) {
      const targetPort = Number(process.env.LK_LIVEKIT_UDP_PORT);
      if (!targetPort) throw new Error('LK_UDP_LOSS_TEST requires LK_LIVEKIT_UDP_PORT');
      udpProxy = await require('./udp-loss-proxy.cjs')(targetPort);
      await viewer.addInitScript(({targetPort, proxyPort}) => {
        window.__networkCandidateRewrites = 0;
        window.__networkPeerConnections = [];
        const NativePeerConnection = window.RTCPeerConnection;
        const iceEvent = Object.getOwnPropertyDescriptor(NativePeerConnection.prototype, 'onicecandidate');
        window.RTCPeerConnection = class extends NativePeerConnection {
          constructor(...args) {
            super(...args);
            window.__networkPeerConnections.push(this);
            // For LiveKit's publisher/subscriber PCs, prevent the server from
            // learning a direct candidate and bypassing the relay. The server
            // can discover the relayed peer-reflexive candidate from ICE checks.
            if (window.__networkPeerConnections.length <= 2 && iceEvent?.set) {
              Object.defineProperty(this, 'onicecandidate', {
                configurable:true,
                get:() => iceEvent.get.call(this),
                set:handler => iceEvent.set.call(this, handler && (event => { if (!event.candidate) handler(event); })),
              });
            }
          }
        };
        const rewrite = candidate => {
          const parts = candidate.split(' ');
          if (parts.length >= 8 && parts[2].toLowerCase() === 'udp' && Number(parts[5]) === targetPort) {
            parts[4] = '127.0.0.1'; parts[5] = String(proxyPort);
            window.__networkCandidateRewrites++;
            return parts.join(' ');
          }
          return candidate;
        };
        const addCandidate = NativePeerConnection.prototype.addIceCandidate;
        NativePeerConnection.prototype.addIceCandidate = function(candidate) {
          if (candidate?.candidate) candidate = {candidate:rewrite(candidate.candidate), sdpMid:candidate.sdpMid, sdpMLineIndex:candidate.sdpMLineIndex, usernameFragment:candidate.usernameFragment};
          return addCandidate.call(this, candidate);
        };
        const setDescription = NativePeerConnection.prototype.setRemoteDescription;
        NativePeerConnection.prototype.setRemoteDescription = function(description) {
          if (description?.sdp) description = {type:description.type, sdp:description.sdp.split('\n').map(line => line.startsWith('a=candidate:') ? rewrite(line) : line).join('\n')};
          return setDescription.call(this, description);
        };
      }, {targetPort, proxyPort:udpProxy.port});
    }
    if (process.env.LK_TURN_TEST) {
      await viewer.addInitScript(() => {
        const NativePeerConnection = window.RTCPeerConnection;
        window.__turnPeerConnections = [];
        window.RTCPeerConnection = class extends NativePeerConnection {
          constructor(configuration, ...args) {
            const useRelay = configuration?.iceServers?.length > 0;
            super(useRelay ? {...configuration, iceTransportPolicy:'relay'} : configuration, ...args);
            if (useRelay) window.__turnPeerConnections.push(this);
          }
        };
      });
    }
    for(const p of [publisher, viewer]) p.on('pageerror', e => errors.push(e.message));
    for(const page of [publisher,viewer]) {
      await page.goto(process.argv[2]+(process.env.LK_DEMO_LOCAL ? '/' : '/sfu-meet/'),{waitUntil:'load'});
      if (!process.env.LK_DEMO_LOCAL) await page.fill('#nvr-username','admin');
      await page.fill('#nvr-password',process.env.LK_DEMO_LOCAL ? process.env.LK_DEMO_SECRET : 'test-password');
    }
    if (!process.env.LK_DEMO_LOCAL) {
      await viewer.fill('#room', 'managed-demo');
      await viewer.click('#room-create');
      const managed = viewer.locator('#room-list .track-row').filter({hasText:'managed-demo'});
      await managed.waitFor({timeout:15000});
      await managed.getByRole('button', {name:'关闭'}).click();
      await managed.waitFor({state:'detached',timeout:15000});
      await viewer.fill('#room', 'default');
    }
    await publisher.check('#microphone'); await publisher.check('#camera');
    await publisher.click('#join'); await publisher.waitForFunction(() => document.querySelector('#status-text').textContent==='已连接', null, {timeout:20000});
    await viewer.click('#join'); await viewer.waitForFunction(() => document.querySelector('#status-text').textContent==='已连接', null, {timeout:20000});
    await viewer.waitForFunction(() => document.querySelectorAll('#track-list .track-row').length>=2 && [...document.querySelectorAll('#tiles video')].some(v=>v.videoWidth>0), null, {timeout:25000});
    await viewer.waitForSelector('#audio-container audio', {state:'attached', timeout:15000});
    await viewer.locator('#tiles').scrollIntoViewIfNeeded();
    if (process.env.LK_TURN_TEST) {
      await viewer.waitForFunction(async () => {
        for (const pc of window.__turnPeerConnections) {
          const stats = await pc.getStats();
          let relay = false; let audio = false; let video = false;
          for (const stat of stats.values()) {
            if (stat.type === 'transport' && stat.selectedCandidatePairId) {
              const pair = stats.get(stat.selectedCandidatePairId);
              const local = pair && stats.get(pair.localCandidateId);
              relay = relay || local?.candidateType === 'relay';
            }
            if (stat.type === 'inbound-rtp' && stat.packetsReceived > 0) {
              audio = audio || stat.kind === 'audio';
              video = video || stat.kind === 'video';
            }
          }
          if (relay && audio && video) return true;
        }
        return false;
      }, null, {timeout:20000});
    }
    if (udpProxy) {
      assert.ok(await viewer.evaluate(() => window.__networkCandidateRewrites > 0), 'viewer did not use the UDP proxy');
      const sample = () => viewer.evaluate(async () => {
        let audio = 0; let video = 0; const remote = [];
        for (const pc of window.__networkPeerConnections) {
          const stats = await pc.getStats();
          for (const stat of stats.values()) {
            if (stat.type === 'transport' && stat.selectedCandidatePairId) {
              const pair = stats.get(stat.selectedCandidatePairId);
              const candidate = pair && stats.get(pair.remoteCandidateId);
              if (candidate) remote.push({address:candidate.address, port:candidate.port, protocol:candidate.protocol});
            }
            if (stat.type !== 'inbound-rtp') continue;
            if (stat.kind === 'audio') audio += stat.packetsReceived || 0;
            if (stat.kind === 'video') video += stat.packetsReceived || 0;
          }
        }
        return {audio, video, remote, frames:[...document.querySelectorAll('#tiles video')].reduce((total, v) => total + v.getVideoPlaybackQuality().totalVideoFrames, 0)};
      });
      const before = await sample();
      udpProxy.setImpaired(true);
      await viewer.waitForTimeout(5000);
      const during = await sample();
      udpProxy.setImpaired(false);
      assert.ok(during.remote.some(candidate => candidate.port === udpProxy.port), `viewer did not select the UDP proxy: ${JSON.stringify({proxy:udpProxy.stats(), remote:during.remote})}`);
      assert.ok(udpProxy.dropped() >= 10, `UDP proxy did not drop enough media packets: ${JSON.stringify({proxy:udpProxy.stats(), during})}`);
      assert.ok(during.audio > before.audio && during.video > before.video && during.frames > before.frames, `media stopped during UDP loss: ${JSON.stringify({before, during})}`);
      await viewer.waitForTimeout(2000);
      const after = await sample();
      assert.ok(after.audio > during.audio && after.video > during.video && after.frames > during.frames, `media did not recover after UDP loss: ${JSON.stringify({during, after})}`);
    }
    if (process.env.LIVEKIT_BROWSER_SCREENSHOT) await viewer.screenshot({path:process.env.LIVEKIT_BROWSER_SCREENSHOT, fullPage:true});
    for (const source of ['摄像头', '麦克风']) {
      await viewer.getByRole('button', {name:new RegExp(`^取消订阅 .*${source}`)}).click();
      await viewer.getByRole('button', {name:new RegExp(`^订阅 .*${source}`)}).waitFor({timeout:5000});
      await viewer.getByRole('button', {name:new RegExp(`^订阅 .*${source}`)}).click();
      await viewer.getByRole('button', {name:new RegExp(`^取消订阅 .*${source}`)}).waitFor({timeout:5000});
    }
    await viewer.waitForFunction(() => document.querySelector('#audio-container audio') && [...document.querySelectorAll('#tiles video')].some(v=>v.videoWidth>0), null, {timeout:15000});
    for (const selector of ['#camera-device', '#microphone-device']) {
      const device = await publisher.locator(`${selector} option`).evaluateAll(options => options.find(option => option.value)?.value || '');
      if (device) await publisher.selectOption(selector, device);
    }
    await viewer.locator('#tiles').scrollIntoViewIfNeeded();
    await viewer.waitForFunction(() => document.querySelector('#audio-container audio') && [...document.querySelectorAll('#tiles video')].some(v=>v.videoWidth>0), null, {timeout:15000});
    await viewer.evaluate(() => {
      window.__testConnectionStates = [];
      new MutationObserver(() => window.__testConnectionStates.push(document.querySelector('#status-text').textContent))
        .observe(document.querySelector('#status-text'), {childList:true, subtree:true, characterData:true});
      if (!window.__liveKitTestSocket || window.__liveKitTestSocket.readyState !== WebSocket.OPEN) throw new Error('LiveKit signaling socket unavailable');
      window.__liveKitTestSocket.close(4000, 'synthetic interruption');
    });
    try {
      await viewer.waitForFunction(() => window.__testConnectionStates.includes('正在重连') && document.querySelector('#status-text').textContent === '已连接', null, {timeout:30000});
    } catch (error) {
      const state = await viewer.evaluate(() => ({events:window.__testConnectionStates, status:document.querySelector('#status-text').textContent, socketState:window.__liveKitTestSocket?.readyState, socketPath:window.__liveKitTestSocket ? new URL(window.__liveKitTestSocket.url).pathname : ''}));
      throw new Error(`reconnect failed: ${JSON.stringify(state)}: ${error.message}`);
    }
    await viewer.locator('#tiles').scrollIntoViewIfNeeded();
    await viewer.waitForFunction(() => document.querySelector('#audio-container audio') && [...document.querySelectorAll('#tiles video')].some(v=>v.videoWidth>0), null, {timeout:20000});
    const framesAfterReconnect = await viewer.evaluate(() => [...document.querySelectorAll('#tiles video')].find(v => v.videoWidth > 0).getVideoPlaybackQuality().totalVideoFrames);
    await viewer.waitForFunction(previous => [...document.querySelectorAll('#tiles video')].some(v => v.getVideoPlaybackQuality().totalVideoFrames > previous + 2), framesAfterReconnect, {timeout:15000});
    if (process.env.LK_BRIDGE_TEST) {
      await viewer.waitForFunction(() => [...document.querySelector('#bridge-track').options].some(o => o.text.includes('摄像头')) && [...document.querySelector('#bridge-audio').options].some(o => o.text.includes('麦克风')));
      await viewer.selectOption('#bridge-track', {label: await viewer.locator('#bridge-track option').filter({hasText:'摄像头'}).first().textContent()});
      await viewer.selectOption('#bridge-audio', {label: await viewer.locator('#bridge-audio option').filter({hasText:'麦克风'}).first().textContent()});
      await viewer.click('#bridge-create');
      await viewer.waitForSelector('#bridge-list .track-row', {timeout:20000});
      const stream = await viewer.locator('#bridge-list .track-info strong').first().textContent();
      assert.match(stream, /^sfu_/);
      const outcome = await viewer.evaluate(async (id) => {
        const authorization = `Basic ${sessionStorage.getItem('nvr_auth')}`;
        const pc = new RTCPeerConnection();
        pc.addTransceiver('video', {direction:'recvonly'});
        pc.addTransceiver('audio', {direction:'recvonly'});
        const media = new MediaStream();
        pc.ontrack = ({track}) => media.addTrack(track);
        await pc.setLocalDescription(await pc.createOffer());
        if (pc.iceGatheringState !== 'complete') await new Promise(resolve => pc.addEventListener('icegatheringstatechange', () => { if (pc.iceGatheringState === 'complete') resolve(); }));
        let response;
        for (let i=0; i<30; i++) {
          response = await fetch(`/api/sfu/bridges/${encodeURIComponent(id)}/whep`, {method:'POST',headers:{Authorization:authorization,'Content-Type':'application/sdp'},body:pc.localDescription.sdp});
          if (response.status !== 503) break;
          await new Promise(resolve => setTimeout(resolve, 200));
        }
        if (response.status !== 201) throw new Error(`WHEP ${response.status}: ${await response.text()}`);
        const location = response.headers.get('Location');
        const answer = await response.text();
        await pc.setRemoteDescription({type:'answer', sdp:answer});
        const video = document.createElement('video'); video.muted = true; video.autoplay = true; video.playsInline = true; video.srcObject = media; document.body.append(video);
        const deadline = Date.now() + 20000;
        let audioBytes = 0; let inbound = [];
        while (Date.now() < deadline) {
          const stats = await pc.getStats();
          inbound = [];
          for (const stat of stats.values()) if (stat.type === 'inbound-rtp') { inbound.push({kind:stat.kind, mediaType:stat.mediaType, bytes:stat.bytesReceived}); if (stat.kind === 'audio' || stat.mediaType === 'audio') audioBytes = stat.bytesReceived || 0; }
          if (video.videoWidth > 0 && audioBytes > 0) break;
          await new Promise(resolve => setTimeout(resolve, 200));
        }
        const audioSection = answer.split(/\r?\nm=/).find(s => s.startsWith('audio')) || '';
        const result = {width:video.videoWidth, audioBytes, inbound, tracks:media.getTracks().map(t => ({kind:t.kind, state:t.readyState, muted:t.muted})), answerAudio:audioSection.split(/\r?\n/).filter(s => /^(audio |a=(send|recv|inactive|rtpmap|mid))/.test(s))};
        pc.close(); video.remove();
        if (location) await fetch(location, {method:'DELETE',headers:{Authorization:authorization}});
        return result;
      }, stream);
      assert.ok(outcome.width > 0 && outcome.audioBytes > 0, JSON.stringify(outcome));
      await viewer.getByRole('button', {name:'播放', exact:true}).click();
      await viewer.waitForFunction(() => {
        const video = document.querySelector('#bridge-player');
        return video.videoWidth > 0 && video.srcObject?.getAudioTracks().some(track => track.readyState === 'live' && !track.muted);
      }, null, {timeout:25000});
      const bridgeFrames = await viewer.locator('#bridge-player').evaluate(video => video.getVideoPlaybackQuality().totalVideoFrames);
      await publisher.evaluate(() => {
        window.__publisherConnectionStates = [];
        new MutationObserver(() => window.__publisherConnectionStates.push(document.querySelector('#status-text').textContent))
          .observe(document.querySelector('#status-text'), {childList:true, subtree:true, characterData:true});
        if (!window.__liveKitTestSocket || window.__liveKitTestSocket.readyState !== WebSocket.OPEN) throw new Error('publisher signaling socket unavailable');
        window.__liveKitTestSocket.close(4000, 'synthetic interruption');
      });
      await publisher.waitForFunction(() => window.__publisherConnectionStates.includes('正在重连') && document.querySelector('#status-text').textContent === '已连接', null, {timeout:30000});
      await viewer.waitForFunction(previous => {
        const video = document.querySelector('#bridge-player');
        return !video.hidden && video.getVideoPlaybackQuality().totalVideoFrames > previous + 2 && video.srcObject?.getAudioTracks().some(track => track.readyState === 'live' && !track.muted);
      }, bridgeFrames, {timeout:20000});
      assert.equal(await viewer.locator('#bridge-list .track-info strong').first().textContent(), stream);
      if (process.env.LK_RECORD_TEST) {
        const plan = await viewer.evaluate(async streamID => {
          const response = await fetch('/api/recording-plans', {method:'POST',headers:{Authorization:`Basic ${sessionStorage.getItem('nvr_auth')}`,'Content-Type':'application/json'},body:JSON.stringify({stream_id:streamID,name:'LiveKit browser test',mode:'continuous',enabled:true})});
          if (response.status !== 201) throw new Error(`recording plan ${response.status}: ${await response.text()}`);
          return response.json();
        }, stream);
        await viewer.waitForTimeout(9000);
        await viewer.evaluate(async id => {
          const response = await fetch(`/api/recording-plans/${encodeURIComponent(id)}`, {method:'DELETE',headers:{Authorization:`Basic ${sessionStorage.getItem('nvr_auth')}`}});
          if (!response.ok) throw new Error(`delete recording plan ${response.status}`);
        }, plan.id);
        console.log(`RECORDED_STREAM=${stream}`);
      }
      if (process.env.LK_RESTART_TEST) {
        const status = await viewer.evaluate(async () => (await fetch('/_test/restart-media', {method:'POST'})).status);
        assert.equal(status, 204);
        await viewer.waitForFunction(async () => {
          const response = await fetch('/api/sfu/bridges/', {headers:{Authorization:`Basic ${sessionStorage.getItem('nvr_auth')}`}});
          return response.ok && (await response.json()).length === 0;
        }, null, {timeout:20000, polling:500});
        await viewer.waitForFunction(() => !document.querySelector('#bridge-list .track-row'), null, {timeout:10000});
        assert.equal(await viewer.locator('#status-text').textContent(), '已连接');
        // Adaptive streaming may suspend an off-screen tile while controls are
        // in view; make the meeting video visible before checking reception.
        await viewer.locator('#tiles').scrollIntoViewIfNeeded();
        await viewer.waitForFunction(() => [...document.querySelectorAll('#tiles video')].some(v => v.videoWidth > 0), null, {timeout:10000});
        await viewer.click('#bridge-create');
        await viewer.waitForSelector('#bridge-list .track-row', {timeout:20000});
        const replacement = await viewer.locator('#bridge-list .track-info strong').first().textContent();
        assert.match(replacement, /^sfu_/);
        assert.notEqual(replacement, stream);
        await viewer.getByRole('button', {name:'播放', exact:true}).click();
        await viewer.waitForFunction(() => {
          const video = document.querySelector('#bridge-player');
          return video.videoWidth > 0 && video.srcObject?.getAudioTracks().some(track => track.readyState === 'live' && !track.muted);
        }, null, {timeout:25000});
      }
      await viewer.getByRole('button', {name:'停止', exact:true}).click();
      await viewer.getByRole('button', {name:'移除', exact:true}).waitFor({timeout:15000});
      assert.equal(await viewer.locator('#bridge-player').isHidden(), true);
      await viewer.getByRole('button', {name:'移除', exact:true}).click();
    }
    await publisher.check('#screen-audio'); await publisher.click('#share');
    await viewer.waitForFunction(() => document.querySelectorAll('#track-list .track-row').length>=4, null, {timeout:20000});
    const screenTile = viewer.locator('#tiles .video-tile').filter({hasText:'屏幕共享'});
    await screenTile.waitFor({timeout:15000});
    await screenTile.scrollIntoViewIfNeeded();
    await viewer.waitForFunction(() => {
      const tile = [...document.querySelectorAll('#tiles .video-tile')].find(item => item.textContent.includes('屏幕共享'));
      return tile?.querySelector('video')?.videoWidth > 0 && document.querySelector('#audio-container audio[data-source="屏幕音频"]')?.srcObject;
    }, null, {timeout:20000});
    const screenSignal = await viewer.evaluate(async () => {
      const stream = document.querySelector('#audio-container audio[data-source="屏幕音频"]').srcObject;
      const context = new AudioContext();
      await context.resume();
      const source = context.createMediaStreamSource(stream);
      const analyser = context.createAnalyser();
      source.connect(analyser); // Deliberately never connect to speakers.
      const samples = new Uint8Array(analyser.fftSize);
      let peak = 0;
      for (let i = 0; i < 30 && peak < 5; i++) {
        await new Promise(resolve => setTimeout(resolve, 100));
        analyser.getByteTimeDomainData(samples);
        peak = Math.max(peak, ...samples.map(value => Math.abs(value - 128)));
      }
      source.disconnect();
      await context.close();
      return peak;
    });
    assert.ok(screenSignal >= 5, `synthetic screen audio was silent (peak ${screenSignal})`);
    if (process.env.LK_BRIDGE_TEST) {
      await viewer.waitForFunction(() => [...document.querySelector('#bridge-track').options].some(o => o.text.includes('屏幕共享')) && [...document.querySelector('#bridge-audio').options].some(o => o.text.includes('屏幕音频')));
      await viewer.selectOption('#bridge-track', {label: await viewer.locator('#bridge-track option').filter({hasText:'屏幕共享'}).first().textContent()});
      await viewer.selectOption('#bridge-audio', {label: await viewer.locator('#bridge-audio option').filter({hasText:'屏幕音频'}).first().textContent()});
      await viewer.click('#bridge-create');
      await viewer.getByRole('button', {name:'播放', exact:true}).waitFor({timeout:20000});
      await viewer.getByRole('button', {name:'播放', exact:true}).click();
      await viewer.waitForFunction(() => {
        const video = document.querySelector('#bridge-player');
        return video.videoWidth > 0 && video.srcObject?.getAudioTracks().some(track => track.readyState === 'live' && !track.muted);
      }, null, {timeout:25000});
      const bridgedSignal = await viewer.evaluate(async () => {
        const context = new AudioContext();
        await context.resume();
        const source = context.createMediaStreamSource(document.querySelector('#bridge-player').srcObject);
        const analyser = context.createAnalyser();
        source.connect(analyser); // Deliberately never connect to speakers.
        const samples = new Uint8Array(analyser.fftSize);
        let peak = 0;
        for (let i = 0; i < 30 && peak < 5; i++) {
          await new Promise(resolve => setTimeout(resolve, 100));
          analyser.getByteTimeDomainData(samples);
          peak = Math.max(peak, ...samples.map(value => Math.abs(value - 128)));
        }
        source.disconnect();
        await context.close();
        return peak;
      });
      assert.ok(bridgedSignal >= 5, `bridged screen audio was silent (peak ${bridgedSignal})`);
    }
    await publisher.click('#share');
    await viewer.waitForFunction(() => document.querySelectorAll('#track-list .track-row').length===2, null, {timeout:20000});
    if (process.env.LK_BRIDGE_TEST) {
      await viewer.getByRole('button', {name:'移除', exact:true}).waitFor({timeout:20000});
      assert.equal(await viewer.locator('#bridge-player').isHidden(), true);
      await viewer.getByRole('button', {name:'移除', exact:true}).click();
    }
    // Exercise a meeting in both directions after the receive-only and bridge
    // scenarios; all tracks still come from Chrome's fake capture devices.
    await viewer.click('#mute');
    await viewer.waitForFunction(() => document.querySelector('#mute').textContent === '关闭麦克风', null, {timeout:15000});
    await viewer.click('#camera-toggle');
    await viewer.waitForFunction(() => document.querySelector('#camera-toggle').textContent === '关闭摄像头', null, {timeout:15000});
    await publisher.locator('#tiles').scrollIntoViewIfNeeded();
    await publisher.waitForFunction(() => {
      const remote = [...document.querySelectorAll('#tiles .video-tile')].find(tile => !tile.textContent.includes('我 ·'));
      return remote?.querySelector('video')?.videoWidth > 0 && document.querySelector('#audio-container audio[data-source="麦克风"]')?.srcObject;
    }, null, {timeout:25000});
    const remoteFrames = await publisher.evaluate(() => [...document.querySelectorAll('#tiles .video-tile')].find(tile => !tile.textContent.includes('我 ·')).querySelector('video').getVideoPlaybackQuality().totalVideoFrames);
    await publisher.waitForFunction(previous => {
      const remote = [...document.querySelectorAll('#tiles .video-tile')].find(tile => !tile.textContent.includes('我 ·'));
      return remote?.querySelector('video')?.getVideoPlaybackQuality().totalVideoFrames > previous + 2;
    }, remoteFrames, {timeout:15000});
    await viewer.locator('#tiles').scrollIntoViewIfNeeded();
    await viewer.waitForFunction(() => {
      const remote = [...document.querySelectorAll('#tiles .video-tile')].find(tile => !tile.textContent.includes('我 ·'));
      return remote?.querySelector('video')?.videoWidth > 0 && document.querySelector('#audio-container audio[data-source="麦克风"]')?.srcObject;
    }, null, {timeout:15000});
    const viewerRemoteFrames = await viewer.evaluate(() => [...document.querySelectorAll('#tiles .video-tile')].find(tile => !tile.textContent.includes('我 ·')).querySelector('video').getVideoPlaybackQuality().totalVideoFrames);
    await viewer.waitForFunction(previous => {
      const remote = [...document.querySelectorAll('#tiles .video-tile')].find(tile => !tile.textContent.includes('我 ·'));
      return remote?.querySelector('video')?.getVideoPlaybackQuality().totalVideoFrames > previous + 2;
    }, viewerRemoteFrames, {timeout:15000});
    await viewer.click('#camera-toggle');
    await viewer.waitForFunction(() => document.querySelector('#camera-toggle').textContent === '开启摄像头', null, {timeout:15000});
    await viewer.click('#mute');
    await viewer.waitForFunction(() => document.querySelector('#mute').textContent === '开启麦克风', null, {timeout:15000});
    await viewer.click('#leave'); await publisher.click('#leave');
    if (!process.env.LK_DEMO_LOCAL) {
      const refreshed = viewer.waitForResponse(response => response.url().endsWith('/api/sfu/rooms/') && response.request().method() === 'GET');
      await viewer.click('#room-refresh');
      assert.equal((await refreshed).status(), 200);
    }
    assert.deepEqual(errors, []);
    console.log(`PASS: browser bidirectional synthetic camera/microphone, device switching, signal reconnection, dynamic subscriptions, synthetic screen video/audio${udpProxy ? ', UDP media loss and recovery' : ''}${process.env.LK_TURN_TEST ? ', authenticated TURN relay' : ''}${process.env.LK_BRIDGE_TEST ? ', camera and screen Group WHEP audio/video and bridge cleanup' : ''}, and leave`);
  } finally { await context.close(); await browser.close(); if (udpProxy) udpProxy.close(); }
})().catch(e => { console.error(e.stack || e.message); process.exitCode=1; });
