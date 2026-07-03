<script lang="ts">
  import { onMount } from 'svelte';
  import { isAuthenticated, healthCheck } from '$lib/api';
  import { t } from '$lib/i18n';
  import { WifiOff } from 'lucide-svelte';
  import Login from './routes/Login.svelte';
  import Recordings from './routes/Recordings.svelte';
  import RecordingDetail from './routes/RecordingDetail.svelte';
  import Events from './routes/Events.svelte';
  import Stats from './routes/Stats.svelte';
  import Settings from './routes/Settings.svelte';
  import LiveView from './routes/LiveView.svelte';
  import Dashboard from './routes/Dashboard.svelte';
  import Setup from './routes/Setup.svelte';
  import Streams from './routes/Streams.svelte';
  import StreamDetail from './routes/StreamDetail.svelte';

  import Devices from './routes/Devices.svelte';
  import DeviceGroups from './routes/DeviceGroups.svelte';
  import GB28181Channels from './routes/GB28181Channels.svelte';
  import Users from './routes/Users.svelte';
  import AIDetection from './routes/AIDetection.svelte';
  import Header from './components/Header';

  // Network status
  let isOffline = $state(false);
  let showOfflineBanner = $state(false);
  let showOnlineBanner = $state(false);
  let onlineBannerTimer: ReturnType<typeof setTimeout> | null = null;

  function handleOffline() {
    isOffline = true;
    showOfflineBanner = true;
    showOnlineBanner = false;
    if (onlineBannerTimer) clearTimeout(onlineBannerTimer);
  }

  function handleOnline() {
    isOffline = false;
    showOfflineBanner = false;
    showOnlineBanner = true;
    if (onlineBannerTimer) clearTimeout(onlineBannerTimer);
    onlineBannerTimer = setTimeout(() => { showOnlineBanner = false; }, 3000);
  }

  async function checkSetupRequired() {
    if (isAuthenticated()) return;
    try {
      const health = await healthCheck();
      if (health.setup_required && currentRoute === 'login') {
        window.location.hash = '#/setup';
      }
    } catch {
      // Health check failed — ignore, user stays on login page
    }
  }


  // Parse hash-based routes (hoisted — function declarations are available before this line)
  function parseRoute(hash: string) {
    const raw = hash.slice(1); // Remove #
    // Separate the query string so it doesn't leak into the path segments
    // (e.g. "#/events?camera_id=X" must still match the "events" route).
    const qIdx = raw.indexOf('?');
    const path = qIdx >= 0 ? raw.slice(0, qIdx) : raw;
    const query = new URLSearchParams(qIdx >= 0 ? raw.slice(qIdx + 1) : '');

    if (!path || path === '/') {
      return isAuthenticated() ? { route: 'dashboard', params: {} } : { route: 'login', params: {} };
    }

    const segments = path.split('/').filter(Boolean);

    if (segments[0] === 'login') {
      return { route: 'login', params: {} };
    }

    if (segments[0] === 'setup') {
      return { route: 'setup', params: {} };
    }

    // All routes below require authentication
    if (!isAuthenticated()) {
      return { route: 'login', params: {} };
    }

    if (segments[0] === 'recordings') {
      if (segments[1]) {
        return { route: 'recording-detail', params: { id: segments[1] } };
      }
      return { route: 'recordings', params: { cameraId: query.get('camera_id') || '' } };
    }

    if (segments[0] === 'events') {
      return { route: 'events', params: { cameraId: query.get('camera_id') || '' } };
    }

    if (segments[0] === 'cameras') {
      return { route: 'devices', params: {} };
    }

    if (segments[0] === 'live') {
      if (segments[1]) {
        return { route: 'live', params: { id: segments[1] } };
      }
      return { route: 'devices', params: {} };
    }

    if (segments[0] === 'surveillance') {
      return { route: 'dashboard', params: {} };
    }

    if (segments[0] === 'devices') {
      return { route: 'devices', params: {} };
    }

    if (segments[0] === 'streams') {
      const streamPath = path.replace(/^\/?streams\/?/, '');
      if (streamPath) {
        let streamId = streamPath;
        try {
          streamId = decodeURIComponent(streamId);
        } catch {
          // keep raw segment when decoding fails
        }
        return { route: 'stream-detail', params: { id: streamId } };
      }
      return { route: 'streams', params: {} };
    }
    if (segments[0] === 'stats') {
      return { route: 'stats', params: {} };
    }

    if (segments[0] === 'settings') {
      return { route: 'settings', params: {} };
    }

    if (segments[0] === 'dashboard') {
      const tab = segments[1] === 'health' ? 'health' : 'dashboard';
      return { route: 'dashboard', params: { tab } };
    }

    if (segments[0] === 'device-groups') {
      return { route: 'device-groups', params: {} };
    }

    if (segments[0] === 'gb-channels') {
      return { route: 'gb-channels', params: {} };
    }


    if (segments[0] === 'users') {
      return { route: 'users', params: {} };
    }

    if (segments[0] === 'ai' || segments[0] === 'ai-detection') {
      return { route: 'ai-detection', params: {} };
    }

    // Default to login for unknown routes
    return { route: 'login', params: {} };
  }

  // Current route — initialize from hash synchronously to prevent
  // Login component from redirecting to recordings before onMount runs
  // Redirect legacy #/health and #/status routes
  if (typeof window !== 'undefined' && (window.location.hash === '#/health' || window.location.hash.startsWith('#/status'))) {
    window.location.replace('#/dashboard');
  }

  const initialRoute = typeof window !== 'undefined' ? parseRoute(window.location.hash) : { route: 'login', params: {} };
  let currentRoute = $state(initialRoute.route);
  let params: Record<string, string> = $state(initialRoute.params);


  function updateRoute() {
    const hash = window.location.hash;
    // Redirect legacy #/health and #/status routes
    if (hash === '#/health' || hash.startsWith('#/status')) {
      window.location.replace('#/dashboard');
      return;
    }
    const { route, params: routeParams } = parseRoute(hash);
    currentRoute = route;
    params = routeParams;

    // When auth guard redirects to login, sync the hash so that
    // post-login hash change actually triggers hashchange.
    // Without this, if hash was already #/recordings when auth expired,
    // setting hash to #/recordings after login won't fire hashchange.
    if (route === 'login' && hash !== '#/login' && hash !== '' && hash !== '#') {
      window.location.hash = '#/login';
      return;
    }
  }

  // Listen for hash changes + network status
  onMount(() => {
    updateRoute();
    checkSetupRequired();
    window.addEventListener('hashchange', updateRoute);

    // Network detection
    isOffline = !navigator.onLine;
    if (isOffline) showOfflineBanner = true;
    window.addEventListener('offline', handleOffline);
    window.addEventListener('online', handleOnline);

    return () => {
      window.removeEventListener('hashchange', updateRoute);
      window.removeEventListener('offline', handleOffline);
      window.removeEventListener('online', handleOnline);
      if (onlineBannerTimer) clearTimeout(onlineBannerTimer);
    };
  });
</script>

<!-- Offline banner -->
{#if showOfflineBanner}
  <div class="offline-banner" role="alert" aria-live="assertive">
    <WifiOff size={16} />
    <span>{t('network.offline')}</span>
  </div>
{/if}

<!-- Online restored banner -->
{#if showOnlineBanner}
  <div class="online-banner" role="status" aria-live="polite">
    <span>{t('network.online')}</span>
  </div>
{/if}

{#if currentRoute === 'login'}
    <Login />
  {:else if currentRoute === 'setup'}
    <Setup />
  {:else}
    <Header showBack={currentRoute === 'recording-detail' || currentRoute === 'live' || currentRoute === 'stream-detail'} />
    <main class="main-content">
      {#if currentRoute === 'recordings'}
        <Recordings initialCameraId={params.cameraId} />
      {:else if currentRoute === 'recording-detail'}
        <RecordingDetail recordingId={params.id} />
      {:else if currentRoute === 'events'}
        <Events initialCameraId={params.cameraId} />
      {:else if currentRoute === 'live'}
        <LiveView cameraId={params.id} />
      {:else if currentRoute === 'devices'}
        <Devices />
      {:else if currentRoute === 'streams'}
        <Streams />
      {:else if currentRoute === 'stream-detail'}
        <StreamDetail streamId={params.id} />
      {:else if currentRoute === 'stats'}
        <Stats />
      {:else if currentRoute === 'settings'}
        <Settings />
      {:else if currentRoute === 'dashboard'}
        <Dashboard initialTab={params.tab || 'dashboard'} />
      {:else if currentRoute === 'device-groups'}
        <DeviceGroups />
      {:else if currentRoute === 'gb-channels'}
        <GB28181Channels />
      {:else if currentRoute === 'users'}
        <Users />
      {:else if currentRoute === 'ai-detection'}
        <AIDetection />
      {/if}
    </main>
  {/if}

<style>
  .main-content {
    margin-left: 240px;
    margin-top: 56px;
    min-height: calc(100vh - 56px);
    transition: margin-left var(--duration-normal) var(--ease-out);
  }

  @media (max-width: 767px) {
    .main-content {
      margin-left: 0;
    }
  }

  .offline-banner {
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    z-index: 1800;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 0.5rem;
    padding: 0.625rem 1rem;
    background: var(--color-danger);
    color: #ffffff;
    font-size: 0.875rem;
    font-weight: 500;
    animation: slide-down 0.25s var(--ease-out);
    box-shadow: 0 2px 8px rgba(0, 0, 0, 0.3);
  }

  .online-banner {
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    z-index: 1800;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 0.5rem;
    padding: 0.625rem 1rem;
    background: var(--color-success);
    color: #ffffff;
    font-size: 0.875rem;
    font-weight: 500;
    animation: slide-down 0.25s var(--ease-out);
    box-shadow: 0 2px 8px rgba(0, 0, 0, 0.2);
  }

  @keyframes slide-down {
    from {
      transform: translateY(-100%);
      opacity: 0;
    }
    to {
      transform: translateY(0);
      opacity: 1;
    }
  }
</style>
