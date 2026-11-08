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
    await publisher.check('#screen-audio'); await publisher.click('#share');
    await viewer.waitForFunction(() => document.querySelectorAll('#track-list .track-row').length>=4, null, {timeout:20000});
    await publisher.click('#share');
    await viewer.waitForFunction(() => document.querySelectorAll('#track-list .track-row').length===2, null, {timeout:20000});
    await viewer.click('#leave'); await publisher.click('#leave');
    assert.deepEqual(errors, []);
    console.log('PASS: browser camera/microphone, dynamic subscriptions, synthetic screen video/audio, and leave');
  } finally { await context.close(); await browser.close(); }
})().catch(e => { console.error(e.stack || e.message); process.exitCode=1; });
