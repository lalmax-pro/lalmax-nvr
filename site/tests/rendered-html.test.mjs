import assert from "node:assert/strict";
import { access, readFile } from "node:fs/promises";
import test from "node:test";

async function render() {
  const workerUrl = new URL("../dist/server/index.js", import.meta.url);
  workerUrl.searchParams.set("test", `${process.pid}-${Date.now()}`);
  const { default: worker } = await import(workerUrl.href);
  return worker.fetch(
    new Request("http://localhost/", { headers: { accept: "text/html" } }),
    { ASSETS: { fetch: async () => new Response("Not found", { status: 404 }) } },
    { waitUntil() {}, passThroughOnException() {} },
  );
}

test("server-renders the lalmax-nvr product site", async () => {
  const response = await render();
  assert.equal(response.status, 200);
  assert.match(response.headers.get("content-type") ?? "", /^text\/html\b/i);
  const html = await response.text();
  assert.match(html, /<title>lalmax-nvr · 开源跨平台网络视频录像机（NVR）<\/title>/i);
  assert.match(html, /让每一路视频/);
  assert.match(html, /统一媒体链路/);
  assert.match(html, /Home Assistant 与集成/);
  assert.match(html, /docker compose up -d/);
  assert.match(html, /\.\/lalmax-nvr -config config\/lalmax-nvr\.yaml/);
  assert.match(html, /https:\/\/github\.com\/lalmax-pro\/lalmax-nvr\/wiki/);
  assert.match(html, /src="\/og\.png"/);
  assert.doesNotMatch(html, /&lt;100ms|零依赖|全协议设备接入|\.\/lalmax-nvr -c config\.yaml/);
  assert.doesNotMatch(html, /codex-preview|Your site is taking shape|Building your site/i);
});

test("ships finished metadata and social assets", async () => {
  const [layout, page] = await Promise.all([
    readFile(new URL("../app/layout.tsx", import.meta.url), "utf8"),
    readFile(new URL("../app/page.tsx", import.meta.url), "utf8"),
  ]);
  assert.match(layout, /openGraph/);
  assert.match(layout, /og\.png/);
  assert.doesNotMatch(layout, /codex-preview|_sites-preview/);
  assert.match(page, /跨平台部署/);
  await access(new URL("../public/og.png", import.meta.url));
  await access(new URL("../public/logo.png", import.meta.url));
});
