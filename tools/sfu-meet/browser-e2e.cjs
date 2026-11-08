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
    if (process.env.LK_BRIDGE_TEST) {
      await viewer.waitForFunction(() => [...document.querySelector('#bridge-track').options].some(o => o.text.includes('摄像头')) && [...document.querySelector('#bridge-audio').options].some(o => o.text.includes('麦克风')));
      await viewer.selectOption('#bridge-track', {label: await viewer.locator('#bridge-track option').filter({hasText:'摄像头'}).first().textContent()});
      await viewer.selectOption('#bridge-audio', {label: await viewer.locator('#bridge-audio option').filter({hasText:'麦克风'}).first().textContent()});
      await viewer.click('#bridge-create');
      await viewer.waitForSelector('#bridge-list .track-row', {timeout:20000});
      const stream = await viewer.locator('#bridge-list .track-info strong').first().textContent();
      assert.match(stream, /^sfu_/);
      const outcome = await viewer.evaluate(async (id) => {
        const pc = new RTCPeerConnection();
        pc.addTransceiver('video', {direction:'recvonly'});
        pc.addTransceiver('audio', {direction:'recvonly'});
        const media = new MediaStream();
        pc.ontrack = ({track}) => media.addTrack(track);
        await pc.setLocalDescription(await pc.createOffer());
        if (pc.iceGatheringState !== 'complete') await new Promise(resolve => pc.addEventListener('icegatheringstatechange', () => { if (pc.iceGatheringState === 'complete') resolve(); }));
        let response;
        for (let i=0; i<30; i++) {
          response = await fetch(`/api/sfu/bridges/${encodeURIComponent(id)}/whep`, {method:'POST',headers:{'Content-Type':'application/sdp'},body:pc.localDescription.sdp});
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
        if (location) await fetch(location, {method:'DELETE'});
        return result;
      }, stream);
      assert.ok(outcome.width > 0 && outcome.audioBytes > 0, JSON.stringify(outcome));
      await viewer.locator('#bridge-list button').first().click();
      await viewer.waitForFunction(() => document.querySelector('#bridge-list button')?.textContent === '移除', null, {timeout:15000});
      await viewer.locator('#bridge-list button').first().click();
    }
    await publisher.check('#screen-audio'); await publisher.click('#share');
    await viewer.waitForFunction(() => document.querySelectorAll('#track-list .track-row').length>=4, null, {timeout:20000});
    await publisher.click('#share');
    await viewer.waitForFunction(() => document.querySelectorAll('#track-list .track-row').length===2, null, {timeout:20000});
    await viewer.click('#leave'); await publisher.click('#leave');
    assert.deepEqual(errors, []);
    console.log(`PASS: browser synthetic camera/microphone, dynamic subscriptions, synthetic screen video/audio${process.env.LK_BRIDGE_TEST ? ', Group WHEP audio/video and bridge cleanup' : ''}, and leave`);
  } finally { await context.close(); await browser.close(); }
})().catch(e => { console.error(e.stack || e.message); process.exitCode=1; });
