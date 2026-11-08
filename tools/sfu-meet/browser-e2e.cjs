const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
(async () => {
  const browser = await chromium.launch({executablePath:process.env.LIVEKIT_BROWSER_EXECUTABLE,headless:true,args:['--no-sandbox','--mute-audio','--use-fake-device-for-media-stream','--use-fake-ui-for-media-stream']});
  const context = await browser.newContext({permissions:['camera','microphone']});
  await context.addInitScript(() => {
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
  const errors=[]; for(const p of [publisher, viewer]) p.on('pageerror', e => errors.push(e.message));
  try {
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
    await publisher.click('#share');
    await viewer.waitForFunction(() => document.querySelectorAll('#track-list .track-row').length===2, null, {timeout:20000});
    await viewer.click('#leave'); await publisher.click('#leave');
    if (!process.env.LK_DEMO_LOCAL) {
      const refreshed = viewer.waitForResponse(response => response.url().endsWith('/api/sfu/rooms/') && response.request().method() === 'GET');
      await viewer.click('#room-refresh');
      assert.equal((await refreshed).status(), 200);
    }
    assert.deepEqual(errors, []);
    console.log(`PASS: browser synthetic camera/microphone, dynamic subscriptions, synthetic screen video/audio${process.env.LK_BRIDGE_TEST ? ', Group WHEP audio/video and bridge cleanup' : ''}, and leave`);
  } finally { await context.close(); await browser.close(); }
})().catch(e => { console.error(e.stack || e.message); process.exitCode=1; });
