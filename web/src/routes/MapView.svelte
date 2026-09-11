<script lang="ts">
  import { onMount } from 'svelte';
  import { listCameras, updateCamera } from '$lib/api';
  import { getAuthToken } from '$lib/api/client';
  import type { Camera } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';

  const TILE = 256;
  const MIN_Z = 2;
  const MAX_Z = 18;

  let cameras = $state<Camera[]>([]);
  let loadError = $state('');
  let selected = $state<Camera | null>(null);
  let placing = $state(false);
  let zoom = $state(4);
  let centerLat = $state(35);
  let centerLng = $state(104);
  let width = $state(0);
  let height = $state(0);
  let dragging = false;
  let moved = false;
  let dragStart = { x: 0, y: 0, lat: 0, lng: 0 };
  const token = getAuthToken() || '';

  onMount(() => {
    void listCameras(undefined, true)
      .then((list) => {
        cameras = list || [];
        const placed = cameras.filter((c) => c.latitude && c.longitude);
        if (placed.length === 1) {
          centerLat = placed[0].latitude!;
          centerLng = placed[0].longitude!;
          zoom = 12;
        }
      })
      .catch((e) => {
        loadError = e instanceof Error ? e.message : String(e);
      });
  });

  function lngToX(lng: number, z: number) {
    return ((lng + 180) / 360) * 2 ** z * TILE;
  }
  function latToY(lat: number, z: number) {
    const clamped = Math.max(-85.05112878, Math.min(85.05112878, lat));
    const s = Math.sin((clamped * Math.PI) / 180);
    return (0.5 - Math.log((1 + s) / (1 - s)) / (4 * Math.PI)) * 2 ** z * TILE;
  }
  function xToLng(x: number, z: number) {
    return (x / (2 ** z * TILE)) * 360 - 180;
  }
  function yToLat(y: number, z: number) {
    const n = Math.PI * (1 - 2 * (y / (2 ** z * TILE)));
    return (180 / Math.PI) * Math.atan(Math.sinh(n));
  }

  let origin = $derived({
    x: lngToX(centerLng, zoom) - Math.max(width, 1) / 2,
    y: latToY(centerLat, zoom) - Math.max(height, 1) / 2,
  });

  let tiles = $derived.by(() => {
    const w = Math.max(width, 1);
    const h = Math.max(height, 1);
    if (w < 8 || h < 8) return [];
    const n = 2 ** zoom;
    const minTx = Math.floor(origin.x / TILE) - 1;
    const maxTx = Math.floor((origin.x + w) / TILE) + 1;
    const minTy = Math.max(0, Math.floor(origin.y / TILE) - 1);
    const maxTy = Math.min(n - 1, Math.floor((origin.y + h) / TILE) + 1);
    const q = token ? `?token=${encodeURIComponent(token)}` : '';
    const out: { key: string; left: number; top: number; src: string }[] = [];
    for (let x = minTx; x <= maxTx; x++) {
      for (let y = minTy; y <= maxTy; y++) {
        const xx = ((x % n) + n) % n;
        out.push({
          key: `${zoom}-${xx}-${y}`,
          left: x * TILE - origin.x,
          top: y * TILE - origin.y,
          src: `/api/map/tiles/${zoom}/${xx}/${y}.png${q}`,
        });
      }
    }
    return out;
  });

  function pinStyle(cam: Camera) {
    return `left:${lngToX(cam.longitude || 0, zoom) - origin.x}px;top:${latToY(cam.latitude || 0, zoom) - origin.y}px;`;
  }

  function onWheel(e: WheelEvent) {
    e.preventDefault();
    const el = e.currentTarget as HTMLElement;
    const next = Math.min(MAX_Z, Math.max(MIN_Z, zoom + (e.deltaY > 0 ? -1 : 1)));
    if (next === zoom) return;
    const rect = el.getBoundingClientRect();
    const mx = e.clientX - rect.left;
    const my = e.clientY - rect.top;
    const lat = yToLat(origin.y + my, zoom);
    const lng = xToLng(origin.x + mx, zoom);
    zoom = next;
    centerLng = xToLng(lngToX(lng, zoom) - mx + width / 2, zoom);
    centerLat = yToLat(latToY(lat, zoom) - my + height / 2, zoom);
  }

  function onPointerDown(e: PointerEvent) {
    if ((e.target as HTMLElement).closest('a,button')) return;
    dragging = true;
    moved = false;
    dragStart = { x: e.clientX, y: e.clientY, lat: centerLat, lng: centerLng };
    (e.currentTarget as HTMLElement).setPointerCapture?.(e.pointerId);
  }

  function onPointerMove(e: PointerEvent) {
    if (!dragging) return;
    const dx = e.clientX - dragStart.x;
    const dy = e.clientY - dragStart.y;
    if (Math.abs(dx) + Math.abs(dy) > 4) moved = true;
    centerLng = xToLng(lngToX(dragStart.lng, zoom) - dx, zoom);
    centerLat = yToLat(latToY(dragStart.lat, zoom) - dy, zoom);
  }

  function onPointerUp() {
    dragging = false;
  }

  async function handleMapClick(e: MouseEvent) {
    if (moved || !placing || !selected) return;
    const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
    const lng = xToLng(origin.x + e.clientX - rect.left, zoom);
    const lat = yToLat(origin.y + e.clientY - rect.top, zoom);
    try {
      await updateCamera(selected.id, { longitude: lng, latitude: lat });
      cameras = cameras.map((c) => (c.id === selected!.id ? { ...c, longitude: lng, latitude: lat } : c));
      placing = false;
      showToast(t('map.saved'), 'success');
    } catch (err) {
      showToast(err instanceof Error ? err.message : t('map.saveFailed'), 'error');
    }
  }

  function focusCamera(cam: Camera) {
    selected = cam;
    placing = true;
    if (cam.latitude && cam.longitude) {
      centerLat = cam.latitude;
      centerLng = cam.longitude;
      if (zoom < 10) zoom = 12;
    }
  }
</script>

<div class="map-page">
  <aside class="map-list">
    <h3>{t('map.title')}</h3>
    {#if loadError}
      <p class="hint">{loadError}</p>
    {/if}
    {#each cameras as cam (cam.id)}
      <button class="cam-row" class:active={selected?.id === cam.id} onclick={() => focusCamera(cam)}>
        <span>{cam.name}</span>
        <small>{cam.latitude && cam.longitude ? `${cam.latitude.toFixed(4)}, ${cam.longitude.toFixed(4)}` : t('map.unplaced')}</small>
      </button>
    {:else}
      {#if !loadError}
        <p class="hint">{t('map.empty')}</p>
      {/if}
    {/each}
    {#if placing && selected}
      <p class="hint">{t('map.placeHint')}</p>
    {/if}
    <p class="hint">{t('map.hint')}</p>
  </aside>
  <section
    class="map-canvas"
    bind:clientWidth={width}
    bind:clientHeight={height}
    onwheel={onWheel}
    onpointerdown={onPointerDown}
    onpointermove={onPointerMove}
    onpointerup={onPointerUp}
    onclick={handleMapClick}
    role="presentation"
  >
    {#each tiles as tile (tile.key)}
      <img class="tile" src={tile.src} alt="" style="transform: translate({tile.left}px, {tile.top}px);" draggable="false" />
    {/each}
    {#each cameras.filter((c) => c.latitude && c.longitude) as cam (cam.id)}
      <a class="pin" class:active={selected?.id === cam.id} style={pinStyle(cam)} href="#/live/{cam.id}" title={cam.name}>{cam.name.slice(0, 1)}</a>
    {/each}
    <div class="zoom-ctrl">
      <button type="button" onclick={() => { zoom = Math.min(MAX_Z, zoom + 1); }}>+</button>
      <button type="button" onclick={() => { zoom = Math.max(MIN_Z, zoom - 1); }}>−</button>
    </div>
    <div class="attrib">{t('map.attribution')}</div>
  </section>
</div>

<style>
  .map-page {
    display: grid;
    grid-template-columns: 240px minmax(0, 1fr);
    width: 100%;
    height: calc(100vh - 56px);
    min-height: 480px;
    overflow: hidden;
  }
  .map-list {
    z-index: 1;
    overflow: auto;
    padding: 0.75rem;
    background: var(--bg-elevated);
    border-right: 1px solid var(--border);
  }
  .map-list h3 { margin: 0 0 0.75rem; font-size: 0.9rem; }
  .cam-row {
    display: flex;
    flex-direction: column;
    width: 100%;
    padding: 0.5rem;
    border: none;
    background: none;
    color: var(--text-primary);
    text-align: left;
    cursor: pointer;
  }
  .cam-row.active { background: color-mix(in srgb, var(--color-primary) 12%, transparent); }
  .hint { font-size: 0.75rem; color: var(--text-tertiary); }
  .map-canvas {
    position: relative;
    min-width: 0;
    min-height: 0;
    height: 100%;
    overflow: hidden;
    background: #9ec0d6;
    cursor: grab;
    touch-action: none;
  }
  .map-canvas:active { cursor: grabbing; }
  .tile {
    position: absolute;
    left: 0;
    top: 0;
    width: 256px;
    height: 256px;
    pointer-events: none;
    user-select: none;
  }
  .pin {
    position: absolute;
    z-index: 2;
    display: flex;
    width: 22px;
    height: 22px;
    transform: translate(-50%, -100%) rotate(-45deg);
    align-items: center;
    justify-content: center;
    border-radius: 50% 50% 50% 0;
    background: var(--color-primary);
    color: #fff;
    font-size: 11px;
    text-decoration: none;
  }
  .pin.active { background: var(--color-danger); }
  .zoom-ctrl {
    position: absolute;
    right: 12px;
    bottom: 28px;
    z-index: 3;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .zoom-ctrl button {
    width: 32px;
    height: 32px;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--bg-elevated);
    color: var(--text-primary);
    cursor: pointer;
  }
  .attrib {
    position: absolute;
    left: 8px;
    bottom: 6px;
    z-index: 3;
    padding: 2px 6px;
    border-radius: 4px;
    background: color-mix(in srgb, var(--bg-elevated) 80%, transparent);
    color: var(--text-tertiary);
    font-size: 10px;
  }
</style>
