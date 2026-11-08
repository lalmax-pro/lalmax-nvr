// Local test-only UDP relay. Each browser socket gets its own upstream socket
// so LiveKit's ICE mux can distinguish separate PeerConnections.
const dgram = require('node:dgram');

module.exports = async function createUDPLossProxy(targetPort) {
  const front = dgram.createSocket('udp4');
  const clients = new Map();
  let impaired = false;
  let closed = false;
  let mediaPackets = 0;
  let dropped = 0;
  let fromClient = 0;
  let fromServer = 0;
  front.on('message', (packet, peer) => {
    if (closed) return;
    fromClient++;
    const key = `${peer.address}:${peer.port}`;
    let upstream = clients.get(key);
    if (!upstream) {
      upstream = dgram.createSocket('udp4');
      upstream.on('message', response => {
        if (closed) return;
        fromServer++;
        // SRTP/SRTCP have version bits 10; leave STUN and DTLS untouched.
        if (impaired && (response[0] & 0xc0) === 0x80) {
          mediaPackets++;
          if (mediaPackets % 5 === 0) { dropped++; return; }
        }
        front.send(response, peer.port, peer.address);
      });
      clients.set(key, upstream);
    }
    upstream.send(packet, targetPort, '127.0.0.1');
  });
  await new Promise((resolve, reject) => { front.once('error', reject); front.bind(0, '127.0.0.1', resolve); });
  return {
    port: front.address().port,
    setImpaired: value => { impaired = value; },
    dropped: () => dropped,
    stats: () => ({port:front.address().port, targetPort, fromClient, fromServer, mediaPackets, dropped, clients:clients.size}),
    close: () => { closed = true; for (const socket of clients.values()) socket.close(); front.close(); },
  };
};
