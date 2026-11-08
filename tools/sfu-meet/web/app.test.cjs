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
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, '../../../internal/sfumeetui/web/app.js'), 'utf8'), context);
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

test('remote tracks can be unsubscribed and resubscribed without reconnecting', async () => {
  class Element {
    constructor(id = '') { this.id=id; this.children=[]; this.listeners={}; this.attributes={}; this.disabled=false; this.hidden=true; this.checked=false; this.value=''; this.textContent=''; this.classList={ toggle() {} }; }
    addEventListener(type, fn) { this.listeners[type]=fn; }
    setAttribute(name, value) { this.attributes[name]=value; }
    querySelector() { return new Element(); }
    replaceChildren(...children) { this.children=children; }
    append(...children) { this.children.push(...children); }
    remove() {}
  }
  const elements=new Map();
  const element=id=>{ if(!elements.has(id))elements.set(id,new Element(id)); return elements.get(id); };
  const commands=[]; let socket, peers=0;
  class Socket {
    static OPEN=1;
    constructor() { socket=this; this.readyState=1; setImmediate(()=>this.snapshot([])); }
    snapshot(publications) { this.onmessage({data:JSON.stringify({type:'snapshot',snapshot:{Participants:[{ID:'remote',Name:'远端'}],Publications:publications}})}); }
    send(data) { const msg=JSON.parse(data);commands.push(msg);setImmediate(()=>this.onmessage({data:JSON.stringify({type:'result',id:msg.id,sdp:'answer'})})); }
    close() { this.readyState=3; }
  }
  class Peer {
    constructor() { peers++;this.iceGatheringState='complete';this.connectionState='connected'; }
    createDataChannel() {} async createOffer() { return {sdp:'offer'}; }
    async setLocalDescription() { this.localDescription={sdp:'offer'}; } async setRemoteDescription() {}
    addTransceiver() { return {stop() {}}; } close() {}
  }
  const context={document:{getElementById:element,createElement:()=>new Element()},window:{addEventListener(){}},location:{protocol:'http:',host:'127.0.0.1'},navigator:{},fetch:async()=>({ok:true}),WebSocket:Socket,RTCPeerConnection:Peer,MediaStream:class{},setTimeout,clearTimeout,Map,Set,Promise,Error};
  vm.runInNewContext(fs.readFileSync(path.join(__dirname,'../../../internal/sfumeetui/web/app.js'),'utf8'),context);
  element('join-form').listeners.submit({preventDefault(){}});
  const waitFor=async predicate=>{for(let i=0;i<100;i++){if(predicate())return;await new Promise(resolve=>setTimeout(resolve,5));}assert.fail('operation timed out');};
  await waitFor(()=>!element('share').disabled);
  const publication={ID:'remote-video-id',ParticipantID:'remote',Source:'camera',Kind:'video',Codec:'video/H264',RTPReady:true};
  socket.snapshot([publication]);
  await waitFor(()=>commands.filter(c=>c.type==='subscribe').length===1 && commands.filter(c=>c.type==='offer').length===2);
  let button=element('track-list').children[0].children[1];
  assert.equal(button.textContent,'取消订阅');
  button.listeners.click();
  await waitFor(()=>commands.filter(c=>c.type==='unsubscribe').length===1 && commands.filter(c=>c.type==='offer').length===3);
  button=element('track-list').children[0].children[1];
  assert.equal(button.textContent,'订阅');
  socket.snapshot([publication]);
  await new Promise(resolve=>setTimeout(resolve,10));
  assert.equal(commands.filter(c=>c.type==='subscribe').length,1,'snapshot must respect the user preference');
  button=element('track-list').children[0].children[1];button.listeners.click();
  await waitFor(()=>commands.filter(c=>c.type==='subscribe').length===2 && commands.filter(c=>c.type==='offer').length===4);
  assert.equal(peers,1,'switching subscriptions must not reconnect');
  element('leave').listeners.click();
});

test('NVR uses one-time ticket and cleans bridge created after leaving', async () => {
  class Element {
    constructor(id=''){this.id=id;this.children=[];this.listeners={};this.attributes={};this.disabled=false;this.hidden=true;this.checked=false;this.value='';this.textContent='';this.classList={toggle(){}};}
    addEventListener(type,fn){this.listeners[type]=fn;} setAttribute(name,value){this.attributes[name]=value;}
    querySelector(){return new Element();} replaceChildren(...children){this.children=children;} append(...children){this.children.push(...children);} remove(){}
  }
  const elements=new Map();const element=id=>{if(!elements.has(id))elements.set(id,new Element(id));return elements.get(id);};
  const calls=[];let socket,releaseCreate,peerConfig;
  class Socket {
    static OPEN=1;
    constructor(url){socket=this;this.url=url;this.readyState=1;setImmediate(()=>this.snapshot([]));}
    snapshot(publications){this.onmessage({data:JSON.stringify({type:'snapshot',snapshot:{Participants:[{ID:'remote'}],Publications:publications}})});}
    send(data){const msg=JSON.parse(data);setImmediate(()=>this.onmessage({data:JSON.stringify({type:'result',id:msg.id,sdp:'answer'})}));}
    close(){this.readyState=3;}
  }
  class Peer {
    constructor(config){peerConfig=config;this.iceGatheringState='complete';this.connectionState='connected';} createDataChannel(){} async createOffer(){return{sdp:'offer'};}
    async setLocalDescription(){this.localDescription={sdp:'offer'};} async setRemoteDescription(){} addTransceiver(){return{stop(){}};} close(){}
  }
  const fetch=async(url,options={})=>{
    calls.push({url,options});
    if(url==='/api/sfu/ticket')return{ok:true,status:201,json:async()=>({ticket:'short-ticket',ice_servers:[{urls:['stun:example.test:3478']}]})};
    if(url==='/api/sfu/bridges/')return await new Promise(resolve=>{releaseCreate=()=>resolve({ok:true,status:202,json:async()=>({id:'bridge-1'})});});
    if(url==='/api/sfu/bridges/bridge-1/stop')return{ok:true,status:204};
    if(url==='/api/sfu/bridges/bridge-1' && options.method==='DELETE')return{ok:true,status:204};
    if(url==='/api/sfu/bridges/bridge-1')return{ok:true,status:200,json:async()=>({state:'stopped'})};
    throw new Error(`unexpected fetch ${url}`);
  };
  const context={document:{documentElement:{dataset:{mode:'nvr'}},getElementById:element,createElement:()=>new Element()},window:{addEventListener(){}},location:{protocol:'https:',host:'nvr.test'},navigator:{},sessionStorage:{getItem:()=> 'encoded-credentials',setItem() {}},fetch,WebSocket:Socket,RTCPeerConnection:Peer,MediaStream:class{},AbortController,setTimeout,clearTimeout,Map,Set,Promise,Error,encodeURIComponent};
  vm.runInNewContext(fs.readFileSync(path.join(__dirname,'../../../internal/sfumeetui/web/app.js'),'utf8'),context);
  element('join-form').listeners.submit({preventDefault(){}});
  const waitFor=async predicate=>{for(let i=0;i<100;i++){if(predicate())return;await new Promise(resolve=>setTimeout(resolve,5));}assert.fail('operation timed out');};
  await waitFor(()=>!element('share').disabled);
  assert.equal(peerConfig.iceServers[0].urls[0],'stun:example.test:3478');
  assert.equal(socket.url,'wss://nvr.test/api/sfu/ws?ticket=short-ticket');
  assert.equal(calls[0].options.headers.Authorization,'Basic encoded-credentials');
  socket.snapshot([{ID:'video-publication',ParticipantID:'remote',Source:'camera',Kind:'video',Codec:'video/H264',RTPReady:true}]);
  await waitFor(()=>element('bridge-list').children.length>0);
  element('bridge-list').children[0].children[1].listeners.click();
  await waitFor(()=>typeof releaseCreate==='function');
  element('leave').listeners.click();
  assert.equal(element('bridge-list').children.length,0,'leaving must clear stale bridge tracks');
  releaseCreate();
  await waitFor(()=>calls.some(c=>c.url==='/api/sfu/bridges/bridge-1' && c.options.method==='DELETE'));
  assert.ok(!calls.some(c=>c.url.includes('/whep')),'closed room must not start a player');
});
