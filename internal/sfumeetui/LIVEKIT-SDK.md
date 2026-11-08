The embedded `web/livekit-sdk.js` is the unmodified UMD distribution of
`livekit-client` version 2.23.0 from npm. The package is Apache-2.0 licensed;
see `LIVEKIT-LICENSE` in this directory.

SHA-256: `fb646f2260d84f91238d0a2dc10726c456631d6a26c7cafc43cfee5959b4d31d`

To update it, review the release and dependency licenses, then extract
`package/dist/livekit-client.umd.js` and `package/LICENSE` from the pinned npm
package, update the version and checksum above, and run the Meet tests.

The bundled distribution contains these third-party components:

- `@bufbuild/protobuf@1.10.1`: `third-party-licenses/bufbuild-protobuf-1.10.1.txt`
- `@livekit/mutex@1.1.1`: `third-party-licenses/livekit-mutex-1.1.1.txt`
- `events@3.3.0`: `third-party-licenses/events-3.3.0.txt`
- `jose@6.2.12`: `third-party-licenses/jose-6.2.12.txt`
- `loglevel@1.9.2`: `third-party-licenses/loglevel-1.9.2.txt`
- `machina@7.0.1`: `third-party-licenses/machina-7.0.1.txt`
- `sdp@3.2.2`: `third-party-licenses/sdp-3.2.2.txt`
- `sdp-transform@2.15.0`: `third-party-licenses/sdp-transform-2.15.0.txt`
- `webrtc-adapter@9.0.6`: `third-party-licenses/webrtc-adapter-9.0.6.txt`
