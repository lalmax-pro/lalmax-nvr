<script lang="ts">
  import { t } from '$lib/i18n';
  import { normalizeProtocol, enableCamera, disableCamera, getSnapshotUrl, activateCamera, rediscoverCamera } from '$lib/api';
  import type { Camera, ProtocolInfo } from '$lib/api';
  import type { CameraHealth } from '$lib/api/health';
  import type { PTZCapabilitiesDetailed } from '$lib/api/cameras';
  import { Pencil, Play, Pause, Square, RotateCw, Eye, MoreVertical, Archive, Trash2, Image, Bell, Move, Mic, MicOff, Camera as CameraIcon, ZoomIn, Home, CalendarClock, CircleOff, KeyRound, Search } from 'lucide-svelte';
  import { showToast } from '$lib/toast';

  interface Props {
    camera: Camera;
    protocolsMap: Map<string, ProtocolInfo>;
    health?: CameraHealth;
    ptzCapabilities?: PTZCapabilitiesDetailed;
    onedit: (camera: Camera) => void;
    ondelete: (camera: Camera) => void;
    onpermadelete: (camera: Camera) => void;
    onstart: (camera: Camera) => void;
    onstop: (camera: Camera) => void;
    onrestart: (camera: Camera) => void;
    ontoggle: (camera: Camera) => void;
    onsaveName: (camera: Camera, name: string) => void;
    onpause?: (camera: Camera) => void;
    onresume?: (camera: Camera) => void;
    recordingPaused?: boolean;
    showSnapshot?: boolean;
  }

  let {
    camera,
    protocolsMap,
    health,
    ptzCapabilities,
    onedit,
    ondelete,
    onpermadelete,
    onstart,
    onstop,
    onrestart,
    ontoggle,
    onsaveName,
    onpause,
    onresume,
    recordingPaused = false,
    showSnapshot = true,
  }: Props = $props();

  let menuOpen = $state(false);
  let editingName = $state(false);
  let nameInput = $state('');
  $effect(() => { nameInput = camera.name; });

  let isRecordingPaused = $derived(recordingPaused || camera.recording_paused);
  let isPending = $derived(camera.activation_state === 'pending_activation');
  let activateOpen = $state(false);
  let activateUser = $state('');
  let activatePass = $state('');
  let activating = $state(false);
  let rediscovering = $state(false);

  let variant = $derived(
    !camera.enabled
      ? 'disabled'
      : isRecordingPaused || camera.status === 'paused'
        ? 'paused'
        : camera.status === 'recording'
          ? 'active'
          : 'stopped'
  );

  let isHls = $derived(
    protocolsMap.get(normalizeProtocol(camera.protocol))?.capabilities?.hls ?? false
  );

  let capabilities = $derived(
    protocolsMap.get(normalizeProtocol(camera.protocol))?.capabilities
  );

  let protocolLabel = $derived(
    protocolsMap.get(camera.protocol)?.label ||
    t('cameras.protocol.' + camera.protocol) ||
    camera.protocol
  );

  let encodingLabel = $derived(
    camera.encoding ? (t('cameras.encoding.' + camera.encoding) || camera.encoding) : ''
  );

  function getHealthColor(status?: string): string {
    if (status === 'healthy') return 'bg-emerald-400';
    if (status === 'warning') return 'bg-amber-400';
    if (status === 'error') return 'bg-red-500';
    return 'bg-gray-400';
  }

  function closeMenu() {
    menuOpen = false;
  }

  function toggleMenu(e: MouseEvent) {
    e.stopPropagation();
    menuOpen = !menuOpen;
  }

  async function handleToggle() {
    try {
      if (camera.enabled) {
        await disableCamera(camera.id);
      } else {
        await enableCamera(camera.id);
      }
      ontoggle(camera);
    } catch (e) {
      console.error('Toggle failed:', e);
    }
  }

  async function handleActivate() {
    activating = true;
    try {
      await activateCamera(camera.id, activateUser, activatePass);
      activateOpen = false;
      activatePass = '';
      showToast(t('cameras.activated'), 'success');
      ontoggle(camera);
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('cameras.activateFailed'), 'error');
    } finally {
      activating = false;
    }
  }

  async function handleRediscover() {
    rediscovering = true;
    try {
      const result = await rediscoverCamera(camera.id);
      showToast(result.found ? t('cameras.rediscoverFound') : t('cameras.rediscoverNotFound'), result.found ? 'success' : 'info');
      if (result.found) ontoggle(camera);
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('cameras.rediscoverFailed'), 'error');
    } finally {
      rediscovering = false;
    }
  }

  function startEditName() {
    nameInput = camera.name;
    editingName = true;
  }

  function saveName() {
    const trimmed = nameInput.trim();
    if (trimmed && trimmed !== camera.name) {
      onsaveName(camera, trimmed);
    }
    editingName = false;
  }

  function cancelEditName() {
    editingName = false;
    nameInput = camera.name;
  }

  $effect(() => {
    if (!menuOpen) return;
    const handler = (e: MouseEvent) => { menuOpen = false; };
    window.addEventListener('click', handler);
    return () => window.removeEventListener('click', handler);
  });
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
  class="card camera-card border th-border transition-all {variant === 'disabled' ? 'is-disabled' : ''} {menuOpen ? 'is-menu-open' : ''}"
>
  <!-- Snapshot Preview -->
  {#if showSnapshot && camera.enabled}
    <div class="snapshot-preview">
      <img
        src="{getSnapshotUrl(camera.id)}&_t={Date.now()}"
        alt={camera.name}
        class="snapshot-img"
        loading="lazy"
        onerror={(e) => { (e.target as HTMLImageElement).style.display = 'none'; }}
      />
      <div class="snapshot-overlay">
        <Image size={16} class="text-white/60" />
      </div>
    </div>
  {/if}

  <!-- Content -->
  <div class="p-4">
    <!-- Top: Name + Status -->
    <div class="flex items-start justify-between gap-2 mb-3">
      <div class="min-w-0 flex-1">
        {#if editingName}
          <input
            type="text"
            class="input py-1 px-2 text-sm w-full"
            bind:value={nameInput}
            onkeydown={(e) => {
              if (e.key === 'Enter') saveName();
              if (e.key === 'Escape') cancelEditName();
            }}
            onblur={saveName}
          />
        {:else}
          <button
            class="font-medium th-text-primary hover:underline cursor-pointer flex items-center gap-1.5 text-left"
            onclick={startEditName}
            title={t('cameras.editName')}
          >
            <span class="truncate">{camera.name}</span>
            <Pencil size={12} class="th-text-tertiary shrink-0" />
          </button>
        {/if}
      </div>
      <div class="shrink-0 flex items-center gap-1.5">
        {#if health}
          <div class="relative group" title={health.last_event?.message || health.status}>
            <span class="inline-block h-2.5 w-2.5 rounded-full {getHealthColor(health.status)}"></span>
            <div class="absolute bottom-full left-1/2 -translate-x-1/2 mb-2 hidden group-hover:block z-10">
              <div class="bg-gray-900 text-white text-xs rounded py-1 px-2 whitespace-nowrap shadow-lg">
                <div class="font-medium capitalize">{health.status}</div>
                {#if health.last_event}
                  <div class="text-gray-400">{health.last_event.message}</div>
                {/if}
              </div>
            </div>
          </div>
        {/if}
        {#if camera.error_type === 'tutk_incompatible'}
          <span class="badge badge-error" title={camera.error_detail || ''}>
            {t('cameras.tutkCardBadge')}
          </span>
        {/if}
        {#if isPending}
          <span class="badge badge-warning">{t('cameras.pendingActivation')}</span>
        {:else if variant === 'disabled'}
          <span class="badge badge-neutral">{t('cameras.status.disabled')}</span>
        {:else if isRecordingPaused || camera.status === 'paused'}
          <span class="badge badge-warning">{t('cameras.statusPaused')}</span>
        {:else if camera.status === 'recording'}
          <span class="badge badge-success">{t('cameras.statusRecording')}</span>
        {:else if camera.status === 'error'}
          <span class="badge badge-error">{t('cameras.statusError')}</span>
        {:else if camera.status === 'reconnecting'}
          <span class="badge badge-warning">{t('cameras.statusReconnecting')}</span>
        {:else if camera.status === 'offline'}
          <span class="badge badge-warning">{t('cameras.statusOffline') || 'Offline'}</span>
        {:else}
          <span class="badge badge-neutral">{t('cameras.statusStopped')}</span>
        {/if}
      </div>
    </div>

    <!-- Middle: Protocol + Encoding + URL -->
    <div class="space-y-1.5 mb-3">
      <div class="flex items-center gap-2 flex-wrap">
        <span class="text-xs font-medium th-text-secondary px-2 py-0.5 rounded th-bg-tertiary">{protocolLabel}</span>
        {#if encodingLabel}
          <span class="text-xs th-text-tertiary px-2 py-0.5 rounded th-bg-tertiary">{encodingLabel}</span>
        {/if}
        {#if capabilities?.ptz}
          {#if ptzCapabilities}
            {#if ptzCapabilities.pan_tilt}
              <span class="inline-flex items-center gap-1 text-xs text-blue-600 bg-blue-50 dark:text-blue-400 dark:bg-blue-900/30 px-2 py-0.5 rounded">
                <Move size={10} />
                云台
              </span>
            {/if}
            {#if ptzCapabilities.zoom}
              <span class="inline-flex items-center gap-1 text-xs text-cyan-600 bg-cyan-50 dark:text-cyan-400 dark:bg-cyan-900/30 px-2 py-0.5 rounded">
                <ZoomIn size={10} />
                变焦
              </span>
            {/if}
            {#if ptzCapabilities.presets}
              <span class="inline-flex items-center gap-1 text-xs text-indigo-600 bg-indigo-50 dark:text-indigo-400 dark:bg-indigo-900/30 px-2 py-0.5 rounded">
                预设点
              </span>
            {/if}
            {#if ptzCapabilities.home}
              <span class="inline-flex items-center gap-1 text-xs text-orange-600 bg-orange-50 dark:text-orange-400 dark:bg-orange-900/30 px-2 py-0.5 rounded">
                <Home size={10} />
                归位
              </span>
            {/if}
          {:else}
            <span class="inline-flex items-center gap-1 text-xs text-blue-600 bg-blue-50 dark:text-blue-400 dark:bg-blue-900/30 px-2 py-0.5 rounded">
              <Move size={10} />
              PTZ
            </span>
          {/if}
        {/if}
        {#if camera.audio_enabled}
          <span class="inline-flex items-center gap-1 text-xs text-green-600 bg-green-50 dark:text-green-400 dark:bg-green-900/30 px-2 py-0.5 rounded">
            <Mic size={10} />
            音频
          </span>
        {/if}
        {#if capabilities?.snapshot}
          <span class="inline-flex items-center gap-1 text-xs text-purple-600 bg-purple-50 dark:text-purple-400 dark:bg-purple-900/30 px-2 py-0.5 rounded">
            <CameraIcon size={10} />
            快照
          </span>
        {/if}
        {#if camera.recording_mode === 'scheduled'}
          <span class="inline-flex items-center gap-1 text-xs text-amber-600 bg-amber-50 dark:text-amber-400 dark:bg-amber-900/30 px-2 py-0.5 rounded">
            <CalendarClock size={10} />
            {t('cameras.recordingMode.scheduled')}
          </span>
        {:else if camera.recording_mode === 'event'}
          <span class="inline-flex items-center gap-1 text-xs text-sky-600 bg-sky-50 dark:text-sky-400 dark:bg-sky-900/30 px-2 py-0.5 rounded">
            <Bell size={10} />
            {t('cameras.recordingMode.event')}
          </span>
        {:else if camera.recording_mode === 'adaptive'}
          <span class="inline-flex items-center gap-1 text-xs text-violet-600 bg-violet-50 dark:text-violet-400 dark:bg-violet-900/30 px-2 py-0.5 rounded">
            {t('cameras.recordingMode.adaptive')}
          </span>
        {:else if camera.recording_mode === 'off'}
          <span class="inline-flex items-center gap-1 text-xs th-text-tertiary th-bg-tertiary px-2 py-0.5 rounded">
            <CircleOff size={10} />
            {t('cameras.recordingMode.off')}
          </span>
        {/if}
      </div>
      <p class="text-xs th-text-tertiary truncate font-mono" title={camera.url}>{camera.url}</p>
      
      <!-- Device Info -->
      {#if camera.brand || camera.model || camera.serial_number || camera.location || camera.description || camera.profile_token}
        <div class="mt-2 pt-2 border-t th-border space-y-1">
          {#if camera.brand || camera.model}
            <div class="flex items-center gap-1.5 text-xs th-text-tertiary">
              <span class="font-medium">设备:</span>
              <span>{[camera.brand, camera.model].filter(Boolean).join(' ')}</span>
            </div>
          {/if}
          {#if camera.profile_token}
            <div class="flex items-center gap-1.5 text-xs th-text-tertiary">
              <span class="font-medium">Profile:</span>
              <span class="truncate max-w-[150px]" title={camera.profile_name || camera.profile_token}>{camera.profile_name || camera.profile_token}</span>
            </div>
          {/if}
          {#if camera.serial_number}
            <div class="flex items-center gap-1.5 text-xs th-text-tertiary">
              <span class="font-medium">序列号:</span>
              <span class="font-mono">{camera.serial_number}</span>
            </div>
          {/if}
          {#if camera.location}
            <div class="flex items-center gap-1.5 text-xs th-text-tertiary">
              <span class="font-medium">位置:</span>
              <span>{camera.location}</span>
            </div>
          {/if}
          {#if camera.description}
            <div class="flex items-center gap-1.5 text-xs th-text-tertiary">
              <span class="font-medium">描述:</span>
              <span class="truncate">{camera.description}</span>
            </div>
          {/if}
        </div>
      {/if}
    </div>

    <!-- Bottom: Action bar -->
    <div class="flex items-center justify-between pt-3 border-t th-border">
      <!-- Toggle switch (left) -->
      <button
        type="button"
        class="toggle-switch {camera.enabled ? 'is-on' : ''}"
        onclick={handleToggle}
        onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleToggle(); } }}
        role="switch"
        aria-checked={camera.enabled}
        aria-label={camera.enabled ? t('cameras.action.disable') : t('cameras.action.enable')}
      >
        <span class="toggle-thumb"></span>
      </button>

      <!-- Action buttons (right) -->
      <div class="flex items-center gap-1">
        {#if isPending}
          <button
            class="btn btn-ghost px-2 py-1 text-sm"
            onclick={() => activateOpen = true}
            title={t('cameras.activate')}
          >
            <KeyRound size={14} />
          </button>
        {/if}
        {#if normalizeProtocol(camera.protocol) === 'onvif' && (camera.status === 'error' || camera.status === 'reconnecting' || camera.status === 'offline')}
          <button
            class="btn btn-ghost px-2 py-1 text-sm"
            onclick={handleRediscover}
            disabled={rediscovering}
            title={t('cameras.rediscover')}
          >
            <Search size={14} />
          </button>
        {/if}
        {#if variant !== 'disabled'}
          {#if camera.status === 'recording' || camera.status === 'reconnecting' || camera.status === 'paused' || isRecordingPaused}
            <button
              class="btn btn-ghost px-2 py-1 text-sm"
              onclick={() => onstop(camera)}
              title={t('cameras.stop')}
            >
              <Square size={14} />
            </button>
          {:else}
            <button
              class="btn btn-ghost px-2 py-1 text-sm"
              onclick={() => onstart(camera)}
              title={t('cameras.start')}
            >
              <Play size={14} />
            </button>
          {/if}

          {#if (camera.status === 'recording' || camera.status === 'reconnecting') && !isRecordingPaused}
            <button
              class="btn btn-ghost px-2 py-1 text-sm"
              onclick={() => onpause?.(camera)}
              title={t('cameras.pauseRecording')}
            >
              <Pause size={14} />
            </button>
          {/if}

          {#if isRecordingPaused}
            <button
              class="btn btn-ghost px-2 py-1 text-sm"
              onclick={() => onresume?.(camera)}
              title={t('cameras.resumeRecording')}
            >
              <Play size={14} />
            </button>
          {/if}

          {#if camera.status === 'recording' || camera.status === 'paused' || camera.status === 'error' || camera.status === 'reconnecting' || isRecordingPaused}
            <button
              class="btn btn-ghost px-2 py-1 text-sm"
              onclick={() => onrestart(camera)}
              title={t('cameras.restart')}
            >
              <RotateCw size={14} />
            </button>
          {/if}
        {/if}

        {#if isHls}
          <a
            href="#/live/{camera.id}"
            class="btn btn-ghost px-2 py-1 text-sm"
            title={t('cameras.live')}
          >
            <Eye size={14} />
          </a>
        {/if}

        <!-- More menu -->
        <div class="relative">
          <button
            class="btn btn-ghost px-2 py-1 text-sm"
            onclick={toggleMenu}
            title={t('cameras.moreActions')}
          >
            <MoreVertical size={14} />
          </button>

          {#if menuOpen}
            <div class="dropdown-menu" role="menu" tabindex="-1" onclick={(e) => e.stopPropagation()} onkeydown={(e) => e.stopPropagation()}>
              {#if isHls}
                <a href="#/live/{camera.id}" class="dropdown-item" onclick={closeMenu}>
                  <Eye size={14} />
                  {t('cameras.live')}
                </a>
              {/if}
              <a href="#/events?camera_id={encodeURIComponent(camera.id)}" class="dropdown-item" onclick={closeMenu}>
                <Bell size={14} />
                {t('cameras.events') || '查看事件'}
              </a>
              <a href="#/recordings?camera_id={encodeURIComponent(camera.id)}" class="dropdown-item" onclick={closeMenu}>
                <Image size={14} />
                {t('cameras.recordings') || '查看录像'}
              </a>
              <button class="dropdown-item" onclick={() => { closeMenu(); onedit(camera); }}>
                <Pencil size={14} />
                {t('cameras.edit')}
              </button>
              <button class="dropdown-item dropdown-item--danger" onclick={() => { closeMenu(); ondelete(camera); }}>
                <Archive size={14} />
                {t('cameras.action.archive')}
              </button>
              <button class="dropdown-item dropdown-item--danger" onclick={() => { closeMenu(); onpermadelete(camera); }}>
                <Trash2 size={14} />
                {t('cameras.action.deletePermanent')}
              </button>
            </div>
          {/if}
        </div>
      </div>
    </div>
  </div>
</div>

{#if activateOpen}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" onclick={() => activateOpen = false}>
    <div class="card p-6 w-full max-w-sm border th-border" onclick={(e) => e.stopPropagation()}>
      <h4 class="text-base font-semibold th-text-primary mb-1">{t('cameras.activateTitle')}</h4>
      <p class="text-sm th-text-tertiary mb-4">{t('cameras.activateHint')}</p>
      <label class="input-label" for="activate-user">{t('cameras.username')}</label>
      <input id="activate-user" class="input mb-3" bind:value={activateUser} />
      <label class="input-label" for="activate-pass">{t('cameras.password')}</label>
      <input id="activate-pass" type="password" class="input mb-4" bind:value={activatePass} />
      <div class="flex justify-end gap-2">
        <button type="button" class="btn btn-ghost" onclick={() => activateOpen = false}>{t('common.cancel')}</button>
        <button type="button" class="btn btn-primary" onclick={handleActivate} disabled={activating}>
          {activating ? t('cameras.activating') : t('cameras.activate')}
        </button>
      </div>
    </div>
  </div>
{/if}

<style>
  .camera-card {
    display: flex;
    flex-direction: column;
    position: relative;
    overflow: visible;
  }

  .camera-card.is-menu-open {
    z-index: 100;
  }

  .camera-card.is-disabled {
    opacity: 0.5;
  }

  .camera-card.is-disabled .dropdown-item:not(.dropdown-item--danger),
  .camera-card.is-disabled .btn:not(.toggle-switch) {
    pointer-events: none;
  }

  /* Snapshot preview */
  .snapshot-preview {
    position: relative;
    width: 100%;
    aspect-ratio: 16 / 9;
    overflow: hidden;
    background: var(--bg-secondary);
  }

  .snapshot-img {
    width: 100%;
    height: 100%;
    object-fit: cover;
    transition: transform 0.2s ease;
  }

  .snapshot-preview:hover .snapshot-img {
    transform: scale(1.05);
  }

  .snapshot-overlay {
    position: absolute;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    background: rgba(0, 0, 0, 0.2);
    opacity: 0;
    transition: opacity 0.2s ease;
  }

  .snapshot-preview:hover .snapshot-overlay {
    opacity: 1;
  }

  /* Toggle switch — matches Settings.svelte pattern */
  .toggle-switch {
    position: relative;
    display: inline-flex;
    align-items: center;
    width: 2.75rem;
    height: 1.5rem;
    border-radius: 9999px;
    background-color: var(--bg-tertiary);
    transition: background-color var(--duration-fast) var(--ease-out);
    border: none;
    cursor: pointer;
    padding: 0;
    flex-shrink: 0;
  }

  .toggle-switch.is-on {
    background-color: var(--color-primary);
  }

  .toggle-switch .toggle-thumb {
    display: block;
    width: 1rem;
    height: 1rem;
    border-radius: 9999px;
    background-color: #ffffff;
    transition: transform var(--duration-fast) var(--ease-out);
    transform: translateX(0.25rem);
  }

  .toggle-switch.is-on .toggle-thumb {
    transform: translateX(1.5rem);
  }

  .toggle-switch:focus-visible {
    box-shadow: var(--focus-ring);
    outline: none;
  }

  /* Dropdown menu */
  .dropdown-menu {
    position: absolute;
    right: 0;
    top: 100%;
    margin-top: 0.25rem;
    min-width: 10rem;
    background-color: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    box-shadow: var(--shadow-lg);
    z-index: 9999;
    padding: 0.25rem 0;
    animation: dropdown-enter 0.12s var(--ease-out);
  }

  @keyframes dropdown-enter {
    from {
      opacity: 0;
      transform: translateY(-4px);
    }
    to {
      opacity: 1;
      transform: translateY(0);
    }
  }

  .dropdown-item {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    width: 100%;
    padding: 0.5rem 0.75rem;
    font-size: 0.8125rem;
    color: var(--text-primary);
    background: transparent;
    border: none;
    cursor: pointer;
    text-align: left;
    text-decoration: none;
    transition: background-color var(--duration-fast) var(--ease-out);
  }

  .dropdown-item:hover {
    background-color: var(--bg-hover);
  }

  .dropdown-item--danger {
    color: var(--color-danger);
  }

  .dropdown-item--danger:hover {
    background-color: rgba(239, 68, 68, 0.1);
  }
</style>
