const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');

// Exercise the actual page event handlers with a screen track that ends while
// the server is still acknowledging its publication.
test('screen stopped during publication is removed without leaving the room', async () => {
  const elements = new Map();
  function element(id) {
    if (!elements.has(id)) elements.set(id, {
      id, checked: false, disabled: false, hidden: true, value: '', textContent: '',
      listeners: {}, classList: { toggle() {} },
      addEventListener(type, fn) { this.listeners[type] = fn; },
      setAttribute() {}, querySelector() { return element('badge-label'); },
      replaceChildren() {}, append() {}, remove() {},
    });
    return elements.get(id);
  }
  const track = { kind: 'video', id: 'screen-track', readyState: 'live', stop() { this.readyState = 'ended'; } };
  const stream = { getTracks: () => [track], getVideoTracks: () => [track] };
  const commands = [];
  let socket;
  class Socket {
    static OPEN = 1;
    constructor() { this.readyState = 1; socket = this; setImmediate(() => this.onmessage({ data: JSON.stringify({ type: 'snapshot', snapshot: { Participants: [], Publications: [] } }) })); }
    send(data) {
      const msg = JSON.parse(data); commands.push(msg.type);
      if (msg.type === 'publish') {
        track.readyState = 'ended'; track.onended();
        setImmediate(() => this.onmessage({ data: JSON.stringify({ type: 'result', id: msg.id, publication: { ID: 'screen-publication' } }) }));
      } else setImmediate(() => this.onmessage({ data: JSON.stringify({ type: 'result', id: msg.id, sdp: 'answer' }) }));
    }
    close() { this.readyState = 3; }
  }
  class Peer {
    constructor() { this.iceGatheringState = 'complete'; this.connectionState = 'connected'; this.senders = []; }
    createDataChannel() {} async createOffer() { return { sdp: 'offer' }; }
    async setLocalDescription() { this.localDescription = { sdp: 'offer' }; }
    async setRemoteDescription() {} close() {} getTransceivers() { return []; }
    addTrack() { const sender = {}; this.senders.push(sender); return sender; }
    removeTrack(sender) { this.senders = this.senders.filter(s => s !== sender); }
  }
  const context = {
    document: { getElementById: element, createElement: () => element('created') },
    window: { addEventListener() {} }, location: { protocol: 'http:', host: '127.0.0.1' },
    navigator: { mediaDevices: { getDisplayMedia: async () => stream } },
    fetch: async () => ({ ok: true }), WebSocket: Socket, RTCPeerConnection: Peer,
    MediaStream: class { constructor(tracks) { this.tracks = tracks; } },
    setTimeout, clearTimeout, Map, Promise, Error,
  };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'app.js'), 'utf8'), context);
  element('join-form').listeners.submit({ preventDefault() {} });
  const waitFor = async predicate => {
    for (let i = 0; i < 100; i++) { if (predicate()) return; await new Promise(resolve => setTimeout(resolve, 5)); }
    assert.fail('operation timed out');
  };
  await waitFor(() => !element('share').disabled && commands.includes('offer'));
  await element('share').listeners.click();
  assert.deepEqual(commands, ['offer', 'publish', 'unpublish', 'offer']);
  assert.equal(element('share').textContent, '▣ 共享屏幕');
  assert.equal(element('leave').disabled, false);
  assert.equal(socket.readyState, Socket.OPEN);
  element('leave').listeners.click();
});
