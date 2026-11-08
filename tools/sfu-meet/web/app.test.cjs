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
    play() { this.playCalls=(this.playCalls||0)+1; return Promise.resolve(); }
  }
  const elements=new Map();
  const element=id=>{ if(!elements.has(id))elements.set(id,new Element(id)); return elements.get(id); };
  const commands=[]; let socket, peers=0, peer, remoteVideo;
  class Socket {
    static OPEN=1;
    constructor() { socket=this; this.readyState=1; setImmediate(()=>this.snapshot([])); }
    snapshot(publications) { this.onmessage({data:JSON.stringify({type:'snapshot',snapshot:{Participants:[{ID:'remote',Name:'远端'}],Publications:publications}})}); }
    send(data) { const msg=JSON.parse(data);commands.push(msg);setImmediate(()=>this.onmessage({data:JSON.stringify({type:'result',id:msg.id,sdp:'answer'})})); }
    close() { this.readyState=3; }
  }
  class Peer {
    constructor() { peers++;peer=this;this.iceGatheringState='complete';this.connectionState='connected'; }
    createDataChannel() {} async createOffer() { return {sdp:'offer'}; }
    async setLocalDescription() { this.localDescription={sdp:'offer'}; } async setRemoteDescription() {}
    addTransceiver() { return this.transceiver={stop() {}}; } close() {}
  }
  const context={document:{getElementById:element,createElement:tag=>{const value=new Element();if(tag==='video')remoteVideo=value;return value;}},window:{addEventListener(){}},location:{protocol:'http:',host:'127.0.0.1'},navigator:{},fetch:async()=>({ok:true}),WebSocket:Socket,RTCPeerConnection:Peer,MediaStream:class{},setTimeout,clearTimeout,Map,Set,Promise,Error};
  vm.runInNewContext(fs.readFileSync(path.join(__dirname,'../../../internal/sfumeetui/web/app.js'),'utf8'),context);
  element('join-form').listeners.submit({preventDefault(){}});
  const waitFor=async predicate=>{for(let i=0;i<100;i++){if(predicate())return;await new Promise(resolve=>setTimeout(resolve,5));}assert.fail('operation timed out');};
  await waitFor(()=>!element('share').disabled);
  const publication={ID:'remote-video-id',ParticipantID:'remote',Source:'camera',Kind:'video',Codec:'video/H264',RTPReady:true};
  socket.snapshot([publication]);
  await waitFor(()=>commands.filter(c=>c.type==='subscribe').length===1 && commands.filter(c=>c.type==='offer').length===2);
  peer.ontrack({transceiver:peer.transceiver,track:{kind:'video'}});
  assert.equal(remoteVideo.muted,true,'video-only remote tracks must autoplay muted');
  assert.equal(remoteVideo.playCalls,1,'remote video must resume after being mounted');
  socket.snapshot([publication]);
  assert.equal(remoteVideo.playCalls,1,'an unchanged snapshot must not detach active video');
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
  const calls=[];let socket,releaseCreate,peerConfig,createAttempts=0;
  class Socket {
    static OPEN=1;
    constructor(url){socket=this;this.url=url;this.readyState=1;setImmediate(()=>this.snapshot([]));}
    snapshot(publications,participants=[{ID:'remote'}]){this.onmessage({data:JSON.stringify({type:'snapshot',snapshot:{Participants:participants,Publications:publications}})});}
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
    if(url==='/api/sfu/bridges/'){
      if(++createAttempts===1)throw new TypeError('response lost');
      return await new Promise(resolve=>{releaseCreate=()=>resolve({ok:true,status:202,json:async()=>({id:'bridge-1'})});});
    }
    if(url==='/api/sfu/bridges/bridge-1/stop')return{ok:true,status:204};
    if(url==='/api/sfu/bridges/bridge-1' && options.method==='DELETE')return{ok:true,status:204};
    if(url==='/api/sfu/bridges/bridge-1')return{ok:true,status:200,json:async()=>({state:'stopped'})};
    throw new Error(`unexpected fetch ${url}`);
  };
  const context={document:{documentElement:{dataset:{mode:'nvr'}},getElementById:element,createElement:()=>new Element()},window:{addEventListener(){}},location:{protocol:'https:',host:'nvr.test'},navigator:{},sessionStorage:{getItem:()=> 'encoded-credentials',setItem() {}},fetch,WebSocket:Socket,RTCPeerConnection:Peer,MediaStream:class{},AbortController,crypto:require('node:crypto').webcrypto,setTimeout,clearTimeout,Map,Set,Promise,Error,encodeURIComponent};
  vm.runInNewContext(fs.readFileSync(path.join(__dirname,'../../../internal/sfumeetui/web/app.js'),'utf8'),context);
  element('join-form').listeners.submit({preventDefault(){}});
  const waitFor=async predicate=>{for(let i=0;i<100;i++){if(predicate())return;await new Promise(resolve=>setTimeout(resolve,5));}assert.fail('operation timed out');};
  await waitFor(()=>!element('share').disabled);
  assert.equal(peerConfig.iceServers[0].urls[0],'stun:example.test:3478');
  assert.equal(socket.url,'wss://nvr.test/api/sfu/ws?ticket=short-ticket');
  assert.equal(calls[0].options.headers.Authorization,'Basic encoded-credentials');
  const existingTiles=element('tiles').children;
  socket.snapshot([],[{ID:'remote'},{ID:'bridge_internal'}]);
  assert.equal(element('tiles').children,existingTiles,'internal bridge join must not remount meeting media');
  assert.equal(element('member-count').textContent,'1 人在线');
  socket.snapshot([{ID:'video-publication',ParticipantID:'remote',Source:'camera',Kind:'video',Codec:'video/H264',RTPReady:true}]);
  await waitFor(()=>element('bridge-list').children.length>0);
  element('bridge-list').children[0].children[1].listeners.click();
  await waitFor(()=>typeof releaseCreate==='function');
  const creates=calls.filter(c=>c.url==='/api/sfu/bridges/');
  assert.equal(creates.length,2);
  assert.match(creates[0].options.headers['Idempotency-Key'],/^[0-9a-f]{32}$/);
  assert.equal(creates[0].options.headers['Idempotency-Key'],creates[1].options.headers['Idempotency-Key']);
  assert.equal(creates[0].options.body,creates[1].options.body);
  element('leave').listeners.click();
  assert.equal(element('bridge-list').children.length,0,'leaving must clear stale bridge tracks');
  releaseCreate();
  await waitFor(()=>calls.some(c=>c.url==='/api/sfu/bridges/bridge-1' && c.options.method==='DELETE'));
  assert.ok(!calls.some(c=>c.url.includes('/whep')),'closed room must not start a player');
});

for (const kind of ['audio','video']) test(`switching ${kind} preserves publication and cleans up failed or late capture`, async () => {
  class Element {
    constructor(){this.children=[];this.listeners={};this.disabled=false;this.hidden=true;this.checked=false;this.value='';this.textContent='';this.classList={toggle(){}};}
    addEventListener(type,fn){this.listeners[type]=fn;} setAttribute(){} querySelector(){return new Element();}
    replaceChildren(...children){this.children=children;} append(...children){this.children.push(...children);} remove(){} play(){return Promise.resolve();}
  }
  class Stream {
    constructor(tracks=[]){this.tracks=[...tracks];} getTracks(){return [...this.tracks];}
    getAudioTracks(){return this.tracks.filter(t=>t.kind==='audio');} getVideoTracks(){return this.tracks.filter(t=>t.kind==='video');}
    removeTrack(track){this.tracks=this.tracks.filter(t=>t!==track);} addTrack(track){this.tracks.push(track);}
  }
  const elements=new Map(), element=id=>{if(!elements.has(id))elements.set(id,new Element());return elements.get(id);};
  const track=id=>({id,kind,enabled:true,stopped:false,stop(){this.stopped=true;}});
  const original=track('old'), replacement=track('new'), rejected=track('rejected'), late=track('late');
  let capture=0, resolveLate, releaseSwap, sender;const calls=[];
  const inputKind=kind==='video'?'videoinput':'audioinput', selectID=kind==='video'?'camera-device':'microphone-device';
  const mediaDevices={addEventListener(){},async enumerateDevices(){return [{kind:inputKind,deviceId:'cam-0',label:'Device 0'},{kind:inputKind,deviceId:'cam-1',label:'Device 1'},{kind:inputKind,deviceId:'cam-2',label:'Device 2'}];},
    async getUserMedia(options){calls.push(options);capture++;if(capture===1)return new Stream([original]);if(capture===2)return new Stream([replacement]);if(capture===3)return new Stream([rejected]);if(capture===4)throw new Error('device unavailable');return await new Promise(resolve=>{resolveLate=()=>resolve(new Stream([late]));});}};
  const commands=[];
  class Socket {
    static OPEN=1;
    constructor(){this.readyState=1;setImmediate(()=>this.onmessage({data:JSON.stringify({type:'snapshot',snapshot:{Participants:[],Publications:[]}})}));}
    send(data){const msg=JSON.parse(data);commands.push(msg.type);setImmediate(()=>this.onmessage({data:JSON.stringify(msg.type==='publish'?{type:'result',id:msg.id,publication:{ID:'publication'}}:{type:'result',id:msg.id,sdp:'answer'})}));}
    close(){this.readyState=3;}
  }
  class Peer {
    constructor(){this.iceGatheringState='complete';this.connectionState='connected';this.transceivers=[];}
    addTrack(track){sender={track,async replaceTrack(next){if(this.rejectNext){this.rejectNext=false;throw new Error('replace failed');}if(next===replacement)await new Promise(resolve=>{releaseSwap=resolve;});this.track=next;}};this.transceivers.push({sender});return sender;}
    getTransceivers(){return this.transceivers;} async createOffer(){return{sdp:'offer'};} async setLocalDescription(){this.localDescription={sdp:'offer'};}
    async setRemoteDescription(){} close(){}
  }
  const context={document:{getElementById:element,createElement:()=>new Element()},window:{addEventListener(){}},location:{protocol:'http:',host:'127.0.0.1'},navigator:{mediaDevices},fetch:async()=>({ok:true}),WebSocket:Socket,RTCPeerConnection:Peer,MediaStream:Stream,setTimeout,clearTimeout,Map,Set,Promise,Error};
  vm.runInNewContext(fs.readFileSync(path.join(__dirname,'../../../internal/sfumeetui/web/app.js'),'utf8'),context);
  const waitFor=async predicate=>{for(let i=0;i<100;i++){if(predicate())return;await new Promise(resolve=>setTimeout(resolve,5));}assert.fail('operation timed out');};
  await waitFor(()=>element(selectID).children.length===4);
  element(selectID).value='cam-0';element(kind==='video'?'camera':'microphone').checked=true;
  element('join-form').listeners.submit({preventDefault(){}});
  await waitFor(()=>!element('leave').disabled);
  assert.equal(element(kind==='video'?'microphone-device':'camera-device').disabled,true,'inactive media device selector must be disabled in a call');
  assert.equal(calls[0][kind].deviceId.exact,'cam-0');
  element(kind==='video'?'camera-toggle':'mute').listeners.click();
  element(selectID).value='cam-1';element(selectID).listeners.change();
  await waitFor(()=>typeof releaseSwap==='function');
  assert.equal(replacement.enabled,false,'replacement stays muted while switching');
  element(kind==='video'?'camera-toggle':'mute').listeners.click();releaseSwap();
  await waitFor(()=>sender.track===replacement);
  assert.equal(original.stopped,true);assert.equal(replacement.enabled,true,'latest mute state must survive switching');
  element(kind==='video'?'camera-toggle':'mute').listeners.click();assert.equal(replacement.enabled,false);
  assert.equal(commands.filter(x=>x==='publish').length,1);assert.equal(commands.filter(x=>x==='offer').length,1);
  sender.rejectNext=true;
  element(selectID).value='cam-2';element(selectID).listeners.change();
  await waitFor(()=>!element('error').hidden);
  assert.equal(element(selectID).value,'cam-1');assert.equal(sender.track,replacement);assert.equal(replacement.stopped,false);assert.equal(rejected.stopped,true);
  element('error').hidden=true;element(selectID).value='cam-2';element(selectID).listeners.change();
  await waitFor(()=>!element('error').hidden);
  assert.equal(element(selectID).value,'cam-1');assert.equal(sender.track,replacement);assert.equal(replacement.stopped,false);
  element(selectID).value='cam-2';element(selectID).listeners.change();
  await waitFor(()=>typeof resolveLate==='function');element('leave').listeners.click();resolveLate();
  await waitFor(()=>late.stopped);assert.equal(replacement.stopped,true,'leave must stop the active device');
});

test('NVR room controls create, join and close the selected generation', async () => {
  class Element {
    constructor(id=''){this.id=id;this.children=[];this.listeners={};this.disabled=false;this.hidden=true;this.checked=false;this.value='';this.textContent='';this.classList={toggle(){}};}
    addEventListener(type,fn){this.listeners[type]=fn;} setAttribute(){} querySelector(){return new Element();}
    replaceChildren(...children){this.children=children;} append(...children){this.children.push(...children);} remove(){}
  }
  const elements=new Map(), element=id=>{if(!elements.has(id))elements.set(id,new Element(id));return elements.get(id);};
  let rooms=[{id:'default',generation:'default-gen',participants:0}],ticketBody,deleteURL;const calls=[];
  const fetch=async(url,options={})=>{
    calls.push({url,options});
    if(url==='/api/sfu/rooms/' && options.method==='POST'){const {id}=JSON.parse(options.body);rooms.push({id,generation:'generation-1',participants:0});return{ok:true,status:201,json:async()=>({id})};}
    if(url==='/api/sfu/rooms/')return{ok:true,status:200,json:async()=>rooms};
    if(url.startsWith('/api/sfu/rooms/') && options.method==='DELETE'){deleteURL=url;rooms=rooms.filter(r=>r.id!=='room-1');return{ok:true,status:204};}
    if(url==='/api/sfu/ticket'){ticketBody=JSON.parse(options.body);return{ok:true,status:201,json:async()=>({ticket:'ticket',ice_servers:[]})};}
    throw new Error(`unexpected request ${url}`);
  };
  class Socket {static OPEN=1;constructor(){this.readyState=1;setImmediate(()=>this.onmessage({data:JSON.stringify({type:'snapshot',snapshot:{Participants:[],Publications:[]}})}));}send(data){const msg=JSON.parse(data);setImmediate(()=>this.onmessage({data:JSON.stringify({type:'result',id:msg.id,sdp:'answer'})}));}close(){this.readyState=3;}}
  class Peer {constructor(){this.iceGatheringState='complete';this.connectionState='connected';}createDataChannel(){} async createOffer(){return{sdp:'offer'};}async setLocalDescription(){this.localDescription={sdp:'offer'};}async setRemoteDescription(){}close(){}}
  const context={document:{documentElement:{dataset:{mode:'nvr'}},getElementById:element,createElement:()=>new Element()},window:{addEventListener(){}},location:{protocol:'https:',host:'nvr.test'},navigator:{},sessionStorage:{getItem:()=> 'encoded',setItem(){}},fetch,WebSocket:Socket,RTCPeerConnection:Peer,MediaStream:class{},AbortController,crypto:require('node:crypto').webcrypto,setTimeout,clearTimeout,Map,Set,Promise,Error,encodeURIComponent};
  vm.runInNewContext(fs.readFileSync(path.join(__dirname,'../../../internal/sfumeetui/web/app.js'),'utf8'),context);
  const waitFor=async predicate=>{for(let i=0;i<100;i++){if(predicate())return;await new Promise(resolve=>setTimeout(resolve,5));}assert.fail('operation timed out');};
  element('nvr-room').value='default';element('nvr-room-new').value='room-1';element('nvr-room-create').listeners.click();
  await waitFor(()=>element('nvr-room').value==='room-1' && !element('join').disabled);
  element('join-form').listeners.submit({preventDefault(){}});await waitFor(()=>!element('share').disabled);
  assert.equal(ticketBody.room_id,'room-1');assert.equal(ticketBody.room_generation,'generation-1');assert.equal(element('nvr-room').disabled,true);
  element('leave').listeners.click();assert.equal(element('nvr-room').disabled,false);
  element('nvr-room-close').listeners.click();await waitFor(()=>!!deleteURL && !element('join').disabled);
  assert.equal(deleteURL,'/api/sfu/rooms/room-1?generation=generation-1');assert.equal(element('nvr-room').value,'default');
  assert.equal(calls.filter(c=>c.url==='/api/sfu/rooms/' && c.options.method==='POST').length,1);
});

test('NVR bridge management lists, stops and removes only after terminal state', async () => {
  class Element {
    constructor(){this.children=[];this.listeners={};this.attributes={};this.disabled=false;this.hidden=true;this.checked=false;this.value='';this.textContent='';this.classList={toggle(){}};}
    addEventListener(type,fn){this.listeners[type]=fn;}setAttribute(name,value){this.attributes[name]=value;}
    querySelector(){return new Element();}querySelectorAll(tag){return tag==='button'?this.children.flatMap(row=>row.children.filter(child=>child.type==='button')):[];}
    replaceChildren(...children){this.children=children;}append(...children){this.children.push(...children);}remove(){}
  }
  const elements=new Map(), element=id=>{if(!elements.has(id))elements.set(id,new Element());return elements.get(id);};
  let state='pending', present=true, failList=false;const calls=[];
  const fetch=async(url,options={})=>{
    calls.push({url,method:options.method||'GET'});
    assert.equal(options.headers.Authorization,'Basic encoded');
    if(url==='/api/sfu/bridges/' && !options.method){if(failList)return{ok:false,status:503,json:async()=>({error:'暂不可用'})};return{ok:true,status:200,json:async()=>present?[{id:'bridge/1',publication_id:'pub-1',state}]:[]};}
    if(url==='/api/sfu/bridges/bridge%2F1/stop' && options.method==='POST'){state='stopping';return{ok:true,status:204};}
    if(url==='/api/sfu/bridges/bridge%2F1' && options.method==='DELETE'){assert.equal(state,'stopped');present=false;return{ok:true,status:204};}
    throw new Error(`unexpected request ${url}`);
  };
  const context={document:{documentElement:{dataset:{mode:'nvr'}},getElementById:element,createElement:()=>new Element()},window:{addEventListener(){}},location:{protocol:'https:',host:'nvr.test'},navigator:{},sessionStorage:{getItem:()=> 'encoded',setItem(){}},fetch,WebSocket:class{},RTCPeerConnection:class{},MediaStream:class{},AbortController,setTimeout,clearTimeout,Map,Set,Promise,Error,encodeURIComponent};
  vm.runInNewContext(fs.readFileSync(path.join(__dirname,'../../../internal/sfumeetui/web/app.js'),'utf8'),context);
  const waitFor=async predicate=>{for(let i=0;i<100;i++){if(predicate())return;await new Promise(resolve=>setTimeout(resolve,5));}assert.fail('operation timed out');};
  assert.equal(element('bridge-management').hidden,false);assert.equal(calls.length,0);
  element('bridge-management-refresh').listeners.click();await waitFor(()=>element('bridge-management-list').children.length===1);
  let row=element('bridge-management-list').children[0],button=row.children[1];
  assert.match(row.children[0].children[0].textContent,/bridge\/1/);assert.equal(button.textContent,'停止桥接');
  button.listeners.click();await waitFor(()=>state==='stopping' && !element('bridge-management-refresh').disabled);
  assert.match(element('bridge-management-status').textContent,/稍后刷新/);
  assert.ok(!calls.some(call=>call.method==='DELETE'));
  state='stopped';element('bridge-management-refresh').listeners.click();
  await waitFor(()=>element('bridge-management-list').children[0].children[1].textContent==='删除记录');
  button=element('bridge-management-list').children[0].children[1];button.listeners.click();
  await waitFor(()=>!present && element('bridge-management-list').children.length===0);
  assert.equal(element('bridge-management-status').textContent,'没有可管理的桥接。');
  failList=true;element('bridge-management-refresh').listeners.click();
  await waitFor(()=>element('bridge-management-status').textContent==='暂不可用');
  assert.equal(element('bridge-management-refresh').disabled,false);
});

test('stopping a bridge in management also closes its local player', async () => {
  class Element {
    constructor(){this.children=[];this.listeners={};this.attributes={};this.disabled=false;this.hidden=true;this.checked=false;this.value='';this.textContent='';this.classList={toggle(){}};}
    addEventListener(type,fn){this.listeners[type]=fn;}setAttribute(name,value){this.attributes[name]=value;}querySelector(){return new Element();}
    querySelectorAll(tag){return tag==='button'?this.children.flatMap(row=>row.children.filter(child=>child.type==='button')):[];}
    replaceChildren(...children){this.children=children;}append(...children){this.children.push(...children);}remove(){}play(){return Promise.resolve();}
  }
  const elements=new Map(),element=id=>{if(!elements.has(id))elements.set(id,new Element());return elements.get(id);};
  const peers=[];let socket, bridgeState='pending', whepPending=false;
  class Socket {
    static OPEN=1;
    constructor(){socket=this;this.readyState=1;setImmediate(()=>this.snapshot([]));}
    snapshot(publications){this.onmessage({data:JSON.stringify({type:'snapshot',snapshot:{Participants:[{ID:'remote'}],Publications:publications}})});}
    send(data){const msg=JSON.parse(data);setImmediate(()=>this.onmessage({data:JSON.stringify({type:'result',id:msg.id,sdp:'answer'})}));}
    close(){this.readyState=3;}
  }
  class Peer {
    constructor(){this.iceGatheringState='complete';this.connectionState='connected';peers.push(this);}createDataChannel(){}
    addTransceiver(){return{stop(){}};}async createOffer(){return{sdp:'offer'};}async setLocalDescription(){this.localDescription={sdp:'offer'};}async setRemoteDescription(){}close(){this.closed=true;}
  }
  const fetch=async(url,options={})=>{
    if(url==='/api/sfu/ticket')return{ok:true,status:201,json:async()=>({ticket:'ticket',ice_servers:[]})};
    if(url==='/api/sfu/bridges/' && options.method==='POST')return{ok:true,status:202,json:async()=>({id:'bridge-1'})};
    if(url==='/api/sfu/bridges/' && !options.method)return{ok:true,status:200,json:async()=>[{id:'bridge-1',publication_id:'pub-1',state:bridgeState}]};
    if(url==='/api/sfu/bridges/bridge-1/stop'){bridgeState='stopping';return{ok:true,status:204};}
    if(url==='/api/sfu/bridges/bridge-1/whep'){whepPending=true;return await new Promise((_,reject)=>options.signal.addEventListener('abort',()=>reject(new Error('aborted'))));}
    throw new Error(`unexpected request ${url}`);
  };
  const context={document:{documentElement:{dataset:{mode:'nvr'}},getElementById:element,createElement:()=>new Element()},window:{addEventListener(){}},location:{protocol:'https:',host:'nvr.test'},navigator:{},sessionStorage:{getItem:()=> 'encoded',setItem(){}},fetch,WebSocket:Socket,RTCPeerConnection:Peer,MediaStream:class{},AbortController,crypto:require('node:crypto').webcrypto,setTimeout,clearTimeout,Map,Set,Promise,Error,encodeURIComponent};
  vm.runInNewContext(fs.readFileSync(path.join(__dirname,'../../../internal/sfumeetui/web/app.js'),'utf8'),context);
  const waitFor=async predicate=>{for(let i=0;i<100;i++){if(predicate())return;await new Promise(resolve=>setTimeout(resolve,5));}assert.fail('operation timed out');};
  element('join-form').listeners.submit({preventDefault(){}});await waitFor(()=>!element('share').disabled);
  socket.snapshot([{ID:'pub-1',ParticipantID:'remote',Source:'microphone',Kind:'audio',Codec:'audio/opus',RTPReady:true}]);
  element('bridge-list').children[0].children[1].listeners.click();await waitFor(()=>whepPending);
  const player=peers[1];element('bridge-management-refresh').listeners.click();await waitFor(()=>element('bridge-management-list').children.length===1);
  element('bridge-management-list').children[0].children[1].listeners.click();
  await waitFor(()=>bridgeState==='stopping' && player.closed && !element('bridge-management-refresh').disabled);
  assert.equal(element('bridge-video').hidden,true);
  assert.equal(element('bridge-list').children[0].children[1].textContent,'桥接并播放');
  element('leave').listeners.click();
});

test('NVR reconnects with a new ticket, preserves muted capture and cancels on leave', async () => {
  class Element {
    constructor(){this.children=[];this.listeners={};this.attributes={};this.disabled=false;this.hidden=true;this.checked=false;this.value='';this.textContent='';this.classList={toggle(){}};}
    addEventListener(type,fn){this.listeners[type]=fn;} setAttribute(name,value){this.attributes[name]=value;} querySelector(){return new Element();}
    replaceChildren(...children){this.children=children;} append(...children){this.children.push(...children);} remove(){} play(){return Promise.resolve();}
  }
  class Stream {
    constructor(tracks=[]){this.tracks=[...tracks];}getTracks(){return [...this.tracks];}getAudioTracks(){return this.tracks.filter(t=>t.kind==='audio');}
    getVideoTracks(){return this.tracks.filter(t=>t.kind==='video');}removeTrack(track){this.tracks=this.tracks.filter(t=>t!==track);}addTrack(track){this.tracks.push(track);}
  }
  const elements=new Map(), element=id=>{if(!elements.has(id))elements.set(id,new Element());return elements.get(id);};
  const microphone={kind:'audio',id:'mic',enabled:true,readyState:'live',stop(){this.readyState='ended';}};
  const screen={kind:'video',id:'screen',enabled:true,readyState:'live',stop(){this.readyState='ended';}};
  const generation='0123456789abcdef0123456789abcdef', sockets=[], peers=[], tickets=[], commands=[];
  let captures=0, ticketFailures=0, publishSequence=0;
  class Socket {
    static OPEN=1;
    constructor(url){this.url=url;this.readyState=1;sockets.push(this);setImmediate(()=>this.onmessage({data:JSON.stringify({type:'snapshot',snapshot:{Generation:generation,Participants:[],Publications:[]}})}));}
    send(data){const msg=JSON.parse(data);commands.push(msg);setImmediate(()=>this.onmessage({data:JSON.stringify(msg.type==='publish'?{type:'result',id:msg.id,publication:{ID:`pub-${++publishSequence}`}}:{type:'result',id:msg.id,sdp:'answer'})}));}
    close(){this.readyState=3;}
    disconnect(){this.readyState=3;this.onclose();}
  }
  class Peer {
    constructor(){this.iceGatheringState='complete';this.connectionState='connected';this.transceivers=[];peers.push(this);}
    addTrack(track){const sender={track};this.transceivers.push({sender});return sender;}getTransceivers(){return this.transceivers;}
    async createOffer(){return{sdp:'offer'};}async setLocalDescription(){this.localDescription={sdp:'offer'};}async setRemoteDescription(){}close(){this.closed=true;}
  }
  const fetch=async(url,options)=>{
    if(url!=='/api/sfu/ticket')throw new Error(`unexpected request ${url}`);
    tickets.push(JSON.parse(options.body));
    if(ticketFailures){ticketFailures--;return{ok:false,status:503,json:async()=>({error:'unavailable'})};}
    return{ok:true,status:201,json:async()=>({ticket:`ticket-${tickets.length}`,ice_servers:[]})};
  };
  // Keep the retry timer controllable so leave can cancel it deterministically.
  let retryCallback;
  const schedule=(fn,ms)=>{if(ms===1000||ms===2000||ms===4000){retryCallback=fn;return 'retry';}return setTimeout(fn,ms);};
  const cancel=id=>{if(id==='retry')retryCallback=null;else clearTimeout(id);};
  const context={document:{documentElement:{dataset:{mode:'nvr'}},getElementById:element,createElement:()=>new Element()},window:{addEventListener(){}},location:{protocol:'https:',host:'nvr.test'},navigator:{mediaDevices:{async getUserMedia(){captures++;return new Stream([microphone]);},async getDisplayMedia(){return new Stream([screen]);}}},sessionStorage:{getItem:()=> 'encoded',setItem(){}},fetch,WebSocket:Socket,RTCPeerConnection:Peer,MediaStream:Stream,AbortController,setTimeout:schedule,clearTimeout:cancel,Map,Set,Promise,Error,encodeURIComponent};
  vm.runInNewContext(fs.readFileSync(path.join(__dirname,'../../../internal/sfumeetui/web/app.js'),'utf8'),context);
  const waitFor=async predicate=>{for(let i=0;i<100;i++){if(predicate())return;await new Promise(resolve=>setTimeout(resolve,5));}assert.fail('operation timed out');};
  element('nvr-room').value='default';element('microphone').checked=true;
  element('join-form').listeners.submit({preventDefault(){}});await waitFor(()=>!element('leave').disabled);
  element('mute').listeners.click();assert.equal(microphone.enabled,false);
  await element('share').listeners.click();assert.equal(element('share').textContent,'▣ 停止共享');
  sockets[0].disconnect();assert.equal(peers[0].closed,true);assert.equal(microphone.readyState,'live');
  assert.equal(screen.readyState,'ended');
  assert.equal(element('join').disabled,true);assert.equal(element('leave').disabled,false);
  assert.equal(element('nvr-room').disabled,true);
  const retry=retryCallback;retryCallback=null;retry();
  await waitFor(()=>sockets.length===2 && !element('share').disabled);
  assert.equal(tickets.length,2);assert.equal(tickets[1].room_generation,generation);
  assert.equal(sockets[1].url,'wss://nvr.test/api/sfu/ws?ticket=ticket-2');
  assert.equal(captures,1);assert.equal(peers[1].transceivers[0].sender.track,microphone);
  assert.equal(microphone.enabled,false);assert.equal(element('mute').attributes['aria-pressed'],'true');
  assert.equal(commands.filter(c=>c.type==='publish').length,3);
  assert.match(element('error').textContent,/重新开启屏幕共享/);
  ticketFailures=1;sockets[1].disconnect();assert.equal(typeof retryCallback,'function');
  const failedRetry=retryCallback;retryCallback=null;failedRetry();
  await waitFor(()=>tickets.length===3 && typeof retryCallback==='function');
  assert.equal(microphone.readyState,'live','a failed ticket request must retain capture for the next retry');
  const nextRetry=retryCallback;retryCallback=null;nextRetry();
  await waitFor(()=>sockets.length===3 && !element('share').disabled);
  assert.equal(captures,1);assert.equal(microphone.enabled,false);
  sockets[2].disconnect();assert.equal(typeof retryCallback,'function');
  element('leave').listeners.click();assert.equal(retryCallback,null);assert.equal(microphone.readyState,'ended');
  assert.equal(element('nvr-room').disabled,false);
});

test('reconnect refuses a replacement room generation before publishing', async () => {
  class Element {
    constructor(){this.listeners={};this.disabled=false;this.hidden=true;this.checked=false;this.value='';this.textContent='';this.classList={toggle(){}};}
    addEventListener(type,fn){this.listeners[type]=fn;}setAttribute(){}querySelector(){return new Element();}replaceChildren(){}append(){}remove(){}
  }
  const elements=new Map(),element=id=>{if(!elements.has(id))elements.set(id,new Element());return elements.get(id);};
  const sockets=[],commands=[];let retryCallback,peerCount=0;
  class Socket {
    static OPEN=1;
    constructor(){this.readyState=1;sockets.push(this);const generation=sockets.length===1?'original':'replacement';setImmediate(()=>this.onmessage({data:JSON.stringify({type:'snapshot',snapshot:{Generation:generation,Participants:[],Publications:[]}})}));}
    send(data){const msg=JSON.parse(data);commands.push(msg.type);setImmediate(()=>this.onmessage({data:JSON.stringify({type:'result',id:msg.id,sdp:'answer'})}));}
    close(){this.readyState=3;}disconnect(){this.readyState=3;this.onclose();}
  }
  class Peer {constructor(){peerCount++;this.iceGatheringState='complete';this.connectionState='connected';}createDataChannel(){}async createOffer(){return{sdp:'offer'};}async setLocalDescription(){this.localDescription={sdp:'offer'};}async setRemoteDescription(){}close(){}}
  const schedule=(fn,ms)=>{if(ms===1000){retryCallback=fn;return 'retry';}return setTimeout(fn,ms);};
  const cancel=id=>{if(id==='retry')retryCallback=null;else clearTimeout(id);};
  const context={document:{getElementById:element,createElement:()=>new Element()},window:{addEventListener(){}},location:{protocol:'http:',host:'localhost'},navigator:{},fetch:async()=>({ok:true}),WebSocket:Socket,RTCPeerConnection:Peer,MediaStream:class{},setTimeout:schedule,clearTimeout:cancel,Map,Set,Promise,Error};
  vm.runInNewContext(fs.readFileSync(path.join(__dirname,'../../../internal/sfumeetui/web/app.js'),'utf8'),context);
  const waitFor=async predicate=>{for(let i=0;i<100;i++){if(predicate())return;await new Promise(resolve=>setTimeout(resolve,5));}assert.fail('operation timed out');};
  element('join-form').listeners.submit({preventDefault(){}});await waitFor(()=>!element('share').disabled);
  sockets[0].disconnect();const retry=retryCallback;retryCallback=null;retry();
  await waitFor(()=>!element('error').hidden && element('error').textContent.includes('房间已关闭或重新创建'));
  assert.equal(peerCount,1,'replacement generation must not create a peer connection');
  assert.deepEqual(commands,['offer']);assert.equal(sockets[1].readyState,3);
  assert.equal(element('join').disabled,false);
});

test('failed reconnect stops after three attempts and enables manual join', async () => {
  class Element {
    constructor(){this.listeners={};this.disabled=false;this.hidden=true;this.checked=false;this.value='';this.textContent='';this.classList={toggle(){}};}
    addEventListener(type,fn){this.listeners[type]=fn;}setAttribute(){}querySelector(){return new Element();}replaceChildren(){}append(){}remove(){}
  }
  const elements=new Map(),element=id=>{if(!elements.has(id))elements.set(id,new Element());return elements.get(id);};
  let firstSocket, attempts=0, retryCallback;const delays=[];
  class Socket {
    static OPEN=1;
    constructor(){this.readyState=1;attempts++;if(attempts===1){firstSocket=this;setImmediate(()=>this.onmessage({data:JSON.stringify({type:'snapshot',snapshot:{Generation:'original',Participants:[],Publications:[]}})}));}else setImmediate(()=>{this.onerror();this.onclose();});}
    send(data){const msg=JSON.parse(data);setImmediate(()=>this.onmessage({data:JSON.stringify({type:'result',id:msg.id,sdp:'answer'})}));}
    close(){this.readyState=3;}disconnect(){this.readyState=3;this.onclose();}
  }
  class Peer {constructor(){this.iceGatheringState='complete';this.connectionState='connected';}createDataChannel(){}async createOffer(){return{sdp:'offer'};}async setLocalDescription(){this.localDescription={sdp:'offer'};}async setRemoteDescription(){}close(){}}
  const schedule=(fn,ms)=>{if([1000,2000,4000].includes(ms)){delays.push(ms);retryCallback=fn;return 'retry';}return setTimeout(fn,ms);};
  const cancel=id=>{if(id==='retry')retryCallback=null;else clearTimeout(id);};
  const context={document:{getElementById:element,createElement:()=>new Element()},window:{addEventListener(){}},location:{protocol:'http:',host:'localhost'},navigator:{},fetch:async()=>({ok:true}),WebSocket:Socket,RTCPeerConnection:Peer,MediaStream:class{},setTimeout:schedule,clearTimeout:cancel,Map,Set,Promise,Error};
  vm.runInNewContext(fs.readFileSync(path.join(__dirname,'../../../internal/sfumeetui/web/app.js'),'utf8'),context);
  const waitFor=async predicate=>{for(let i=0;i<100;i++){if(predicate())return;await new Promise(resolve=>setTimeout(resolve,5));}assert.fail('operation timed out');};
  element('join-form').listeners.submit({preventDefault(){}});await waitFor(()=>!element('share').disabled);firstSocket.disconnect();
  for(let attempt=1;attempt<=3;attempt++){
    await waitFor(()=>typeof retryCallback==='function');const retry=retryCallback;retryCallback=null;retry();
    await waitFor(()=>attempts===attempt+1);
  }
  await waitFor(()=>!element('error').hidden && element('error').textContent.includes('请重新加入'));
  assert.deepEqual(delays,[1000,2000,4000]);assert.equal(attempts,4);assert.equal(retryCallback,null);
  assert.equal(element('join').disabled,false);assert.equal(element('leave').disabled,true);
});
