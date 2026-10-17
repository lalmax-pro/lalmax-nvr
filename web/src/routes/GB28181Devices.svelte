<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { listGB28181Devices, playGB28181Stream, stopGB28181Stream, apiRequest } from '$lib/api';
  import type { GB28181Device } from '$lib/api';
  import { t } from '$lib/i18n';
  import PlaybackURLs from '$lib/components/PlaybackURLs.svelte';
  import { selectPlaybackURLs } from '$lib/playback-urls';
  import { showToast } from '$lib/toast';
  import { Video, RefreshCw, Play, Square, Wifi, WifiOff, ExternalLink, Clock, Info, Link as LinkIcon } from 'lucide-svelte';

  let devices = $state<GB28181Device[]>([]);
  let loading = $state(true);
  let error = $state('');
  let playingStreams = $state<Set<string>>(new Set());
  let expandedDevice = $state<string | null>(null);
  let ws = $state<WebSocket | null>(null);
  let wsConnected = $state(false);
  let urlOpenFor = $state<string | null>(null);
  let urlLoadingFor = $state<string | null>(null);
  let playURLs = $state<{ rtsp: string; webrtc: string } | null>(null);

  async function toggleURLs(deviceId: string, channelId: string, streamId?: string) {
    const key = `${deviceId}:${channelId}`;
    if (urlOpenFor === key) { urlOpenFor = null; playURLs = null; return; }
    urlOpenFor = key;
    playURLs = null;
    urlLoadingFor = key;
    try {
      const res = await apiRequest<{ protocols: { Protocol: string; Available?: boolean; PlayURL?: string }[] }>(
        `/cameras/${encodeURIComponent(streamId || key)}/protocols`
      );
      if (urlOpenFor !== key) return;
      const urls = selectPlaybackURLs(res.protocols);
      if (urls.rtsp || urls.webrtc) playURLs = urls;
      else showToast('当前通道没有可用的播放地址', 'info');
    } catch (e) {
      if (urlOpenFor === key) showToast(e instanceof Error ? e.message : '无法获取播放地址', 'error');
    } finally {
      if (urlLoadingFor === key) urlLoadingFor = null;
    }
  }

  function syncPlayingStreams(deviceList: GB28181Device[]) {
    const newPlaying = new Set<string>();
    for (const device of deviceList) {
      for (const channel of device.channels || []) {
        if (channel.is_playing) {
          newPlaying.add(`${device.device_id}:${channel.channel_id}`);
        }
      }
    }
    playingStreams = newPlaying;
  }

  function connectWebSocket() {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsURL = `${protocol}//${window.location.host}/api/gb28181/ws`;
    
    ws = new WebSocket(wsURL);
    
    ws.onopen = () => {
      wsConnected = true;
      console.log('WebSocket connected');
    };
    
    ws.onmessage = (event) => {
      try {
        const data = JSON.parse(event.data);
        handleWebSocketEvent(data);
      } catch (e) {
        console.error('Failed to parse WebSocket message:', e);
      }
    };
    
    ws.onclose = () => {
      wsConnected = false;
      console.log('WebSocket disconnected');
      // 重连
      setTimeout(connectWebSocket, 3000);
    };
    
    ws.onerror = (error) => {
      console.error('WebSocket error:', error);
    };
  }

  function handleWebSocketEvent(event: { type: string; data: Record<string, unknown> }) {
    switch (event.type) {
      case 'device.online':
      case 'device.offline':
        // 更新设备状态
        updateDeviceStatus(event.data.device_id as string, event.data.is_online as boolean);
        break;
      case 'channel.update':
      case 'channel.delete':
      case 'channel.offline':
        // 刷新设备列表
        loadDevices();
        break;
    }
  }

  function updateDeviceStatus(deviceID: string, isOnline: boolean) {
    devices = devices.map(d => {
      if (d.device_id === deviceID) {
        return { ...d, is_online: isOnline };
      }
      return d;
    });
  }

  async function loadDevices() {
    loading = true;
    error = '';
    try {
      const res = await listGB28181Devices();
      devices = res.devices || [];
      syncPlayingStreams(devices);
    } catch (e) {
      error = e instanceof Error ? e.message : 'Failed to load devices';
      console.error('Failed to load GB28181 devices:', e);
    } finally {
      loading = false;
    }
  }

  async function handlePlay(device: GB28181Device, channelId: string) {
    const streamKey = `${device.device_id}:${channelId}`;
    try {
      const res = await playGB28181Stream({
        device_id: device.device_id,
        channel_id: channelId,
      });
      playingStreams = new Set([...playingStreams, streamKey]);
      showToast(`Stream started: ${res.ssrc}`, 'success');
      // Reload devices to update playing status
      await loadDevices();
    } catch (e) {
      showToast(e instanceof Error ? e.message : 'Failed to start stream', 'error');
    }
  }

  async function handleStop(device: GB28181Device, channelId: string) {
    const streamKey = `${device.device_id}:${channelId}`;
    try {
      await stopGB28181Stream(device.device_id, channelId);
      playingStreams = new Set([...playingStreams].filter(k => k !== streamKey));
      showToast('Stream stopped', 'success');
      // Reload devices to update playing status
      await loadDevices();
    } catch (e) {
      showToast(e instanceof Error ? e.message : 'Failed to stop stream', 'error');
    }
  }

  function goToLive(streamId: string) {
    window.location.hash = `#/live/${encodeURIComponent(streamId)}`;
  }

  function toggleDevice(deviceId: string) {
    expandedDevice = expandedDevice === deviceId ? null : deviceId;
  }

  function formatTime(timeStr: string | undefined): string {
    if (!timeStr) return '-';
    try {
      const date = new Date(timeStr);
      return date.toLocaleString('zh-CN', { 
        month: '2-digit', 
        day: '2-digit', 
        hour: '2-digit', 
        minute: '2-digit',
        second: '2-digit'
      });
    } catch {
      return timeStr;
    }
  }

  onMount(() => {
    loadDevices();
    connectWebSocket();
  });

  onDestroy(() => {
    if (ws) {
      ws.close();
    }
  });
</script>

<div class="min-h-screen th-bg-primary ">
  <main class="mx-auto px-3 sm:px-4 lg:px-6 py-4 sm:py-6" style="max-width: 1200px;">
    <div class="flex items-center justify-between mb-6">
      <div>
        <h1 class="text-2xl font-bold th-text-primary">GB28181 设备</h1>
        <p class="text-sm th-text-secondary mt-1">国标 GB28181 SIP 设备管理</p>
      </div>
      <div class="flex items-center gap-3">
        <div class="ws-status" class:connected={wsConnected}>
          {wsConnected ? '实时更新已连接' : '实时更新已断开'}
        </div>
        <button
          onclick={loadDevices}
          class="btn btn-secondary flex items-center gap-2"
          disabled={loading}
        >
          <RefreshCw class="w-4 h-4 {loading ? 'animate-spin' : ''}" />
          刷新
        </button>
      </div>
    </div>

    {#if error}
      <div class="mb-4 p-4 bg-red-50 border border-red-200 rounded-lg">
        <p class="text-red-600">{error}</p>
      </div>
    {/if}

    {#if loading && devices.length === 0}
      <div class="flex items-center justify-center py-12">
        <RefreshCw class="w-6 h-6 animate-spin th-text-secondary" />
        <span class="ml-2 th-text-secondary">加载中...</span>
      </div>
    {:else if devices.length === 0}
      <div class="flex flex-col items-center justify-center py-12 th-bg-secondary rounded-lg">
        <Video class="w-12 h-12 th-text-tertiary mb-4" />
        <p class="text-lg th-text-secondary">暂无 GB28181 设备</p>
        <p class="text-sm th-text-tertiary mt-1">请在设置中启用 GB28181 并配置 SIP 平台 ID</p>
      </div>
    {:else}
      <div class="grid gap-4">
        {#each devices as device}
          <div class="th-bg-secondary rounded-lg border th-border overflow-hidden">
            <!-- Device Header -->
            <div 
              class="p-4 cursor-pointer hover:th-bg-hover transition-colors"
              onclick={() => toggleDevice(device.device_id)}
              role="button"
              tabindex="0"
              onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggleDevice(device.device_id); } }}
            >
              <div class="flex items-start justify-between">
                <div class="flex items-center gap-3">
                  <div class="p-2 rounded-lg {device.is_online ? 'bg-green-100' : 'bg-gray-100'}">
                    {#if device.is_online}
                      <Wifi class="w-5 h-5 text-green-600" />
                    {:else}
                      <WifiOff class="w-5 h-5 text-gray-400" />
                    {/if}
                  </div>
                  <div>
                    <h3 class="font-semibold th-text-primary">
                      {device.name || device.device_id}
                    </h3>
                    <p class="text-sm th-text-secondary">
                      {device.is_online ? '在线' : '离线'}
                      {#if device.address}
                        · {device.address}
                      {/if}
                    </p>
                  </div>
                </div>
                <div class="flex items-center gap-2">
                  <span class="px-2 py-1 text-xs rounded-full {device.is_online ? 'bg-green-100 text-green-800' : 'bg-gray-100 text-gray-600'}">
                    {device.is_online ? '在线' : '离线'}
                  </span>
                  <span class="px-2 py-1 text-xs rounded-full bg-blue-100 text-blue-800">
                    GB {device.gb_version || '2016'}
                  </span>
                  <Info class="w-4 h-4 th-text-tertiary" />
                </div>
              </div>
            </div>

            <!-- Expanded Device Details -->
            {#if expandedDevice === device.device_id}
              <div class="border-t th-border p-4 bg-gray-50 dark:bg-gray-900/50">
                <!-- Device Info -->
                <div class="grid grid-cols-2 sm:grid-cols-4 gap-4 mb-4">
                  <div>
                    <p class="text-xs th-text-tertiary">设备ID</p>
                    <p class="text-sm font-mono th-text-primary">{device.device_id}</p>
                  </div>
                  <div>
                    <p class="text-xs th-text-tertiary">协议版本</p>
                    <p class="text-sm th-text-primary">GB/T 28181-{device.gb_version || '2016'}</p>
                  </div>
                  {#if device.manufacturer}
                    <div>
                      <p class="text-xs th-text-tertiary">厂商</p>
                      <p class="text-sm th-text-primary">{device.manufacturer}</p>
                    </div>
                  {/if}
                  {#if device.model}
                    <div>
                      <p class="text-xs th-text-tertiary">型号</p>
                      <p class="text-sm th-text-primary">{device.model}</p>
                    </div>
                  {/if}
                  {#if device.firmware}
                    <div>
                      <p class="text-xs th-text-tertiary">固件</p>
                      <p class="text-sm th-text-primary">{device.firmware}</p>
                    </div>
                  {/if}
                  {#if device.last_keepalive_at}
                    <div>
                      <p class="text-xs th-text-tertiary">最后心跳</p>
                      <p class="text-sm th-text-primary flex items-center gap-1">
                        <Clock class="w-3 h-3" />
                        {formatTime(device.last_keepalive_at)}
                      </p>
                    </div>
                  {/if}
                  {#if device.last_register_at}
                    <div>
                      <p class="text-xs th-text-tertiary">最后注册</p>
                      <p class="text-sm th-text-primary flex items-center gap-1">
                        <Clock class="w-3 h-3" />
                        {formatTime(device.last_register_at)}
                      </p>
                    </div>
                  {/if}
                </div>
              </div>
            {/if}

            <!-- Channels -->
            {#if device.channels && device.channels.length > 0}
              <div class="border-t th-border p-4">
                <h4 class="text-sm font-medium th-text-secondary mb-3">通道列表 ({device.channels.length})</h4>
                <div class="grid gap-2">
                  {#each device.channels as channel}
                    {@const streamKey = `${device.device_id}:${channel.channel_id}`}
                    <div class="p-3 bg-white dark:bg-gray-800 rounded-lg border th-border">
                      <div class="flex items-center justify-between gap-2">
                      <div class="flex items-center gap-2">
                        <Video class="w-4 h-4 th-text-tertiary" />
                        <span class="text-sm th-text-primary">{channel.channel_id}</span>
                        {#if channel.is_playing}
                          <span class="px-1.5 py-0.5 text-xs bg-green-100 text-green-800 rounded">播放中</span>
                        {/if}
                      </div>
                      <div class="flex items-center gap-2">
                        <button type="button" class="btn btn-sm btn-secondary flex items-center gap-1" onclick={() => toggleURLs(device.device_id, channel.channel_id, channel.stream_id)} disabled={urlLoadingFor === streamKey} title="显示 RTSP 和 WebRTC 地址" aria-expanded={urlOpenFor === streamKey}><LinkIcon class="w-3 h-3" /> URL</button>
                        {#if channel.is_playing}
                          <button
                            onclick={() => goToLive(channel.stream_id)}
                            class="btn btn-sm btn-secondary flex items-center gap-1"
                          >
                            <ExternalLink class="w-3 h-3" />
                            详情
                          </button>
                          <button
                            onclick={() => handleStop(device, channel.channel_id)}
                            class="btn btn-sm btn-danger flex items-center gap-1"
                          >
                            <Square class="w-3 h-3" />
                            停止
                          </button>
                        {:else}
                          <button
                            onclick={() => handlePlay(device, channel.channel_id)}
                            class="btn btn-sm btn-primary flex items-center gap-1"
                            disabled={!device.is_online}
                          >
                            <Play class="w-3 h-3" />
                            播放
                          </button>
                        {/if}
                      </div>
                      </div>
                      {#if urlOpenFor === streamKey && playURLs}
                        <div class="mt-3 border-t th-border pt-3">
                          <PlaybackURLs idPrefix={streamKey} rtspUrl={playURLs.rtsp} webrtcUrl={playURLs.webrtc} />
                        </div>
                      {/if}
                    </div>
                  {/each}
                </div>
              </div>
            {/if}
          </div>
        {/each}
      </div>
    {/if}
  </main>
</div>

<style>
  .ws-status {
    padding: 4px 8px;
    border-radius: 4px;
    font-size: 12px;
    background-color: #fee2e2;
    color: #dc2626;
  }
  
  .ws-status.connected {
    background-color: #dcfce7;
    color: #16a34a;
  }
</style>
