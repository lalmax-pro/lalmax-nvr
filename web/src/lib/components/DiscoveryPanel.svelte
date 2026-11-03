<script lang="ts">
  import { t } from '$lib/i18n';
  import {
    discoverONVIFDevices,
    probeONVIFDevice,
    xiaomiAuth,
    xiaomiDevices,
    xiaomiStatus,
    xiaomiLogout,
    xiaomiCaptcha,
    xiaomiVerify,
    createCamera,
    xiaomiSync,
    checkVendor
  } from '$lib/api';
  import type { DiscoveredDevice, XiaomiDevice, XiaomiAuthResponse, Camera } from '$lib/api';
  import { RefreshCw, WifiOff, Clock, AlertTriangle, Search } from 'lucide-svelte';
  import { showToast } from '$lib/toast';

  interface Props {
    protocol: string;
    cameras: Camera[];
    oncameraadded?: () => void;
    onselectdevice?: (device: DiscoveredDevice) => void;
  }

  let { protocol, cameras, oncameraadded, onselectdevice }: Props = $props();

  // ONVIF state
  let scanning = $state(false);
  let scanDone = $state(false);
  let scanError = $state<{category: string; message: string} | null>(null);
  let discoveredDevices = $state<DiscoveredDevice[]>([]);

  // Manual probe state - show by default since multicast discovery often not supported
  let manualHost = $state('');
  let manualPort = $state(80);
  let probing = $state(false);
  let probedDevice = $state<DiscoveredDevice | null>(null);
  let probeError = $state('');
  let showManualProbe = $state(true);

  // Xiaomi state
  let xiaomiExpanded = $state(false);
  let xiaomiUsername = $state('');
  let xiaomiPassword = $state('');
  let xiaomiRegion = $state('cn');
  let xiaomiRequestVersion = 0;
  let xiaomiLoading = $state(false);
  let xiaomiError = $state('');
  let xiaomiDeviceList = $state<XiaomiDevice[]>([]);
  let xiaomiLoggedIn = $state(false);
  let xiaomiAddingDid = $state<string | null>(null);
  let xiaomiCaptchaImage = $state('');
  let xiaomiCaptchaSessionId = $state('');
  let xiaomiCaptchaCode = $state('');
  let xiaomiVerifySessionId = $state('');
  let xiaomiVerifyTicket = $state('');
  let xiaomiVerifyTarget = $state('');
  let xiaomiVerifyType = $state<'phone' | 'email' | ''>('');
  let syncing = $state(false);

  // TUTK vendor check dialog
  let tutkWarningDevice = $state<XiaomiDevice | null>(null);
  let warningVendor = $state('tutk');

  // Expose scan state for parent
  export function isScanning(): boolean {
    return scanning;
  }

  export async function startDiscovery() {
    if (protocol === 'onvif') {
      await scanONVIF();
    } else if (protocol === 'xiaomi') {
      xiaomiExpanded = true;
      const version = ++xiaomiRequestVersion;
      xiaomiLoading = true;
      xiaomiError = '';
      try {
        const status = await xiaomiStatus();
        if (version !== xiaomiRequestVersion) return;
        xiaomiLoggedIn = status.authenticated;
        xiaomiDeviceList = [];
        if (status.region) xiaomiRegion = status.region;
        if (status.authenticated) {
          const res = await xiaomiDevices();
          if (version !== xiaomiRequestVersion) return;
          xiaomiLoggedIn = res.authenticated;
          xiaomiDeviceList = res.devices || [];
        }
      } catch (e) {
        if (version === xiaomiRequestVersion) xiaomiError = e instanceof Error ? e.message : String(e);
      } finally {
        if (version === xiaomiRequestVersion) xiaomiLoading = false;
      }
    }
  }

  export function dismiss() {
    scanning = false;
    scanDone = false;
    scanError = null;
    discoveredDevices = [];
    xiaomiExpanded = false;
  }

  async function scanONVIF() {
    scanning = true;
    scanError = null;
    discoveredDevices = [];
    scanDone = false;
    try {
      const result = await discoverONVIFDevices(5);
      const existingEndpoints = new Set(
        cameras.filter(c => c.protocol === 'onvif' && c.url).map(c => c.url)
      );
      discoveredDevices = result.devices.filter(d => {
        const ep = d.endpoint || (d.xaddrs.length > 0 ? d.xaddrs[0] : '');
        return !existingEndpoints.has(ep);
      });
      if (result.error) {
        scanError = { category: result.error.category, message: result.error.message };
      }
    } catch (e) {
      scanError = { category: 'NETWORK', message: e instanceof Error ? e.message : String(e) };
    } finally {
      scanning = false;
      scanDone = true;
    }
  }

  async function probeManualDevice() {
    if (!manualHost.trim()) return;
    probing = true;
    probeError = '';
    probedDevice = null;
    try {
      const device = await probeONVIFDevice(manualHost.trim(), manualPort);
      if (device) {
        probedDevice = device;
      } else {
        probeError = t('onvif.probeFailed');
      }
    } catch (e) {
      probeError = e instanceof Error ? e.message : String(e);
    } finally {
      probing = false;
    }
  }

  function configureDiscoveredDevice(device: DiscoveredDevice) {
    onselectdevice?.(device);
    discoveredDevices = discoveredDevices.filter(d => d.uuid !== device.uuid);
    if (probedDevice?.uuid === device.uuid) probedDevice = null;
  }

  // Xiaomi handlers
  async function handleXiaomiLogin() {
    xiaomiLoading = true;
    xiaomiError = '';
    xiaomiCaptchaImage = '';
    xiaomiCaptchaSessionId = '';
    xiaomiVerifySessionId = '';
    xiaomiVerifyTarget = '';
    xiaomiVerifyType = '';
    try {
      const result = await xiaomiAuth(xiaomiUsername, xiaomiPassword, xiaomiRegion);
      await handleAuthResult(result);
    } catch (e: any) {
      xiaomiError = e.message || t('xiaomi.authFailed');
    } finally {
      xiaomiLoading = false;
    }
  }

  async function handleAuthResult(result: XiaomiAuthResponse) {
    if (result.status === 'verification_required') {
      if (result.captcha && result.session_id) {
        xiaomiCaptchaImage = result.captcha.startsWith('data:') ? result.captcha : `data:image/jpeg;base64,${result.captcha}`;
        xiaomiCaptchaSessionId = result.session_id;
        xiaomiCaptchaCode = '';
      } else if (result.verify_phone && result.session_id) {
        xiaomiVerifySessionId = result.session_id;
        xiaomiVerifyTarget = result.verify_phone;
        xiaomiVerifyType = 'phone';
        xiaomiVerifyTicket = '';
      } else if (result.verify_email && result.session_id) {
        xiaomiVerifySessionId = result.session_id;
        xiaomiVerifyTarget = result.verify_email;
        xiaomiVerifyType = 'email';
        xiaomiVerifyTicket = '';
      } else {
        xiaomiError = t('xiaomi.verificationRequired');
      }
      return;
    }
    xiaomiCaptchaImage = '';
    xiaomiCaptchaSessionId = '';
    xiaomiVerifySessionId = '';
    xiaomiVerifyTarget = '';
    xiaomiVerifyType = '';
    xiaomiPassword = '';
    xiaomiLoggedIn = true;
    await refreshXiaomiDevices();
  }

  async function handleXiaomiCaptcha() {
    if (!xiaomiCaptchaSessionId || !xiaomiCaptchaCode.trim()) return;
    xiaomiLoading = true;
    xiaomiError = '';
    try {
      const result = await xiaomiCaptcha(xiaomiCaptchaSessionId, xiaomiCaptchaCode.trim());
      await handleAuthResult(result);
    } catch (e: any) {
      xiaomiError = e.message || t('xiaomi.authFailed');
      xiaomiCaptchaCode = '';
    } finally {
      xiaomiLoading = false;
    }
  }

  async function handleXiaomiVerify() {
    if (!xiaomiVerifySessionId || !xiaomiVerifyTicket.trim()) return;
    xiaomiLoading = true;
    xiaomiError = '';
    try {
      const result = await xiaomiVerify(xiaomiVerifySessionId, xiaomiVerifyTicket.trim());
      await handleAuthResult(result);
    } catch (e: any) {
      xiaomiError = e.message || t('xiaomi.authFailed');
      xiaomiVerifyTicket = '';
    } finally {
      xiaomiLoading = false;
    }
  }

  function isXiaomiDeviceAdded(did: string): boolean {
    return cameras.some(c => c.protocol === 'xiaomi' && c.url === `xiaomi://${did}`);
  }

  async function addXiaomiDevice(device: XiaomiDevice, skipVendorCheck = false) {
    xiaomiAddingDid = device.did;
    try {
      // Check vendor compatibility before adding
      if (!skipVendorCheck) {
        try {
          const result = await checkVendor(device.did);
          if (!result.compatible) {
            tutkWarningDevice = device;
            warningVendor = result.vendor;
            xiaomiAddingDid = null;
            return;
          }
        } catch (e) {
          // Network error — don't block camera creation
          console.warn('Vendor check failed, proceeding anyway:', e);
        }
      }

      await createCamera({
        name: device.name,
        protocol: 'xiaomi',
        encoding: 'h264',
        url: `xiaomi://${device.did}`,
        enabled: true,
      });
      showToast(t('cameras.cameraAdded'), 'success');
      oncameraadded?.();
    } catch (e) { console.warn('Failed to add Xiaomi device:', e); showToast(t('cameras.failedAdd'), 'error'); } finally {
      xiaomiAddingDid = null;
    }
  }

  function handleTutkContinueAnyway() {
    if (tutkWarningDevice) {
      const device = tutkWarningDevice;
      tutkWarningDevice = null;
      addXiaomiDevice(device, true);
    }
  }

  function handleTutkCancel() {
    tutkWarningDevice = null;
  }

  async function refreshXiaomiDevices() {
    const version = ++xiaomiRequestVersion;
    xiaomiLoading = true;
    xiaomiError = '';
    try {
      const res = await xiaomiDevices();
      if (version !== xiaomiRequestVersion) return;
      xiaomiDeviceList = res.devices || [];
      xiaomiLoggedIn = res.authenticated;
    } catch (e) {
      if (version === xiaomiRequestVersion) xiaomiError = e instanceof Error ? e.message : String(e);
    } finally {
      if (version === xiaomiRequestVersion) xiaomiLoading = false;
    }
  }

  async function handleXiaomiLogout() {
    ++xiaomiRequestVersion;
    xiaomiLoading = true;
    xiaomiError = '';
    try {
      await xiaomiLogout();
      xiaomiLoggedIn = false;
      xiaomiDeviceList = [];
      xiaomiUsername = '';
      xiaomiPassword = '';
      xiaomiCaptchaImage = '';
      xiaomiCaptchaSessionId = '';
      xiaomiCaptchaCode = '';
      xiaomiVerifySessionId = '';
      xiaomiVerifyTicket = '';
      xiaomiVerifyTarget = '';
      xiaomiVerifyType = '';
      tutkWarningDevice = null;
    } catch (e) {
      xiaomiError = e instanceof Error ? e.message : String(e);
    } finally {
      xiaomiLoading = false;
    }
  }

  async function handleSyncCloud() {
    syncing = true;
    try {
      const result = await xiaomiSync();
      showToast(t('cameras.syncedCameras').replace('{count}', String(result.synced)), 'success');
      await refreshXiaomiDevices();
    } catch (e: any) {
      showToast(e.message || t('cameras.syncFailed'), 'error');
    } finally {
      syncing = false;
    }
  }

  // Error display config derived from error category
  let scanErrorDisplay = $derived.by(() => {
    if (!scanError) return null;
    const cat = scanError.category;
    switch (cat) {
      case 'TIMEOUT':
        return { color: 'text-amber-400', bg: 'bg-amber-500/10', border: 'border-amber-500/30', icon: Clock };
      case 'NO_DEVICES':
        return { color: 'text-blue-400', bg: 'bg-blue-500/10', border: 'border-blue-500/30', icon: Search };
      case 'NETWORK':
        return { color: 'text-red-400', bg: 'bg-red-500/10', border: 'border-red-500/30', icon: WifiOff };
      default:
        return { color: 'text-red-400', bg: 'bg-red-500/10', border: 'border-red-500/30', icon: AlertTriangle };
    }
  });

  // Determine visibility
  let visible = $derived(protocol === 'onvif' ? (scanning || scanDone) : xiaomiExpanded);

  // Auto-start discovery when mounted or protocol changes
  $effect(() => {
    if (protocol) {
      startDiscovery();
    }
  });
</script>

{#if visible}
  {#if protocol === 'onvif'}
    <!-- ONVIF Discovery Panel -->
    <div class="card p-6 border th-border">
      <div class="flex items-center justify-between mb-4">
        <h3 class="text-lg font-semibold th-text-primary">
          {t('onvif.discover')}
        </h3>
        <button
          type="button"
          class="th-text-muted hover:th-text-primary text-lg leading-none transition-colors"
          onclick={dismiss}
          title={t('common.dismiss')}
        >&times;</button>
      </div>

      {#if scanning}
        <div class="flex items-center gap-3 th-text-secondary py-4">
          <span class="spinner"></span>
          <span>{t('onvif.discovering')}</span>
        </div>
      {:else if scanError && scanErrorDisplay}
        {@const ErrorIcon = scanErrorDisplay.icon}
        <div class="rounded-md border p-4 {scanErrorDisplay.bg} {scanErrorDisplay.border}">
          <div class="flex items-start gap-3">
            <div class="{scanErrorDisplay.color} mt-0.5">
              <ErrorIcon size={18} />
            </div>
            <div class="flex-1 min-w-0">
              <p class="text-sm font-medium {scanErrorDisplay.color}">{t(`discovery.errors.${scanError.category.toLowerCase()}.title`)}</p>
              <p class="text-xs mt-1 th-text-muted">{t(`discovery.errors.${scanError.category.toLowerCase()}.suggestion`)}</p>
            </div>
          </div>
        </div>
      {:else if discoveredDevices.length === 0}
        <p class="th-text-secondary text-sm py-2">{t('onvif.noDevices')}</p>
      {:else}
        <div class="space-y-3">
          {#each discoveredDevices as device (device.uuid)}
            <div class="flex items-center justify-between p-4 rounded-md th-bg-hover border th-border">
              <div class="min-w-0 flex-1 mr-4">
                <div class="font-medium th-text-primary truncate">{device.name || t('onvif.deviceName')}</div>
                <div class="text-sm th-text-secondary truncate">{device.endpoint}</div>
                {#if device.hardware}
                  <div class="text-xs th-text-muted mt-0.5">{device.hardware}</div>
                {/if}
              </div>
              <button
                onclick={() => configureDiscoveredDevice(device)}
                class="btn btn-primary btn-sm shrink-0"
              >
                {t('onvif.configureAndAdd')}
              </button>
            </div>
          {/each}
        </div>
      {/if}

      <!-- Manual Probe Section - Always show since multicast often not supported -->
      {#if showManualProbe}
        <div class="mt-4 pt-4 border-t th-border">
          <div class="flex items-center gap-2 mb-3">
            <Search size={16} class="th-text-secondary" />
            <h4 class="text-sm font-semibold th-text-primary">{t('onvif.manualProbe')}</h4>
          </div>
          <p class="text-xs th-text-muted mb-3">{t('onvif.manualProbeHint')}</p>
          <div class="flex gap-2 items-end">
            <div class="flex-1">
              <label class="input-label text-xs" for="onvif-host">{t('onvif.host')}</label>
              <input
                type="text"
                class="input py-1 text-sm"
                bind:value={manualHost}
                placeholder="192.168.1.100"
              />
            </div>
            <div class="w-24">
              <label class="input-label text-xs" for="onvif-port">{t('onvif.port')}</label>
              <input
                type="number"
                class="input py-1 text-sm"
                bind:value={manualPort}
                placeholder="80"
                min="1"
                max="65535"
              />
            </div>
            <button
              type="button"
              class="btn btn-primary btn-sm shrink-0"
              onclick={probeManualDevice}
              disabled={probing || !manualHost.trim()}
            >
              {#if probing}
                <span class="spinner mr-1"></span>
              {/if}
              {probing ? t('onvif.probing') : t('onvif.manualProbe')}
            </button>
          </div>

          {#if probeError}
            <p class="text-sm mt-2 th-color-danger">{t('onvif.probeFailed')}: {probeError}</p>
          {/if}

          {#if probedDevice}
            <div class="mt-3 flex items-center justify-between p-4 rounded-md th-bg-hover border th-border">
              <div class="min-w-0 flex-1 mr-4">
                <div class="font-medium th-text-primary truncate">{probedDevice.name || t('onvif.deviceName')}</div>
                <div class="text-sm th-text-secondary truncate">{probedDevice.endpoint}</div>
                {#if probedDevice.hardware}
                  <div class="text-xs th-text-muted mt-0.5">{probedDevice.hardware}</div>
                {/if}
              </div>
              <button
                onclick={() => {
                  configureDiscoveredDevice(probedDevice);
                }}
                class="btn btn-primary btn-sm shrink-0"
              >
                {t('onvif.configureAndAdd')}
              </button>
            </div>
          {/if}
        </div>
      {/if}

      {#if !scanning && scanDone}
        <div class="mt-4 flex justify-end">
          <button onclick={scanONVIF} class="btn btn-ghost btn-sm">
            {t('onvif.discover')}
          </button>
        </div>
      {/if}
    </div>
  {:else if protocol === 'xiaomi'}
    <!-- Xiaomi Discovery Panel -->
    <div class="card p-6 border th-border">
      <div class="flex items-center justify-between mb-4">
        <h3 class="text-lg font-semibold th-text-primary">{t('xiaomi.title')}</h3>
        <button
          type="button"
          class="th-text-muted hover:th-text-primary text-lg leading-none transition-colors"
          onclick={dismiss}
          title={t('common.dismiss')}
        >&times;</button>
      </div>

      {#if !xiaomiLoggedIn}
        <!-- Login form -->
        <form onsubmit={(e) => { e.preventDefault(); handleXiaomiLogin(); }} class="space-y-3">
          <p class="text-sm th-text-secondary">{t('xiaomi.signInHint')}</p>
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <div>
              <label class="input-label text-xs" for="xiaomi-account">{t('xiaomi.account')}</label>
              <input id="xiaomi-account" type="text" class="input py-1 text-sm" bind:value={xiaomiUsername} placeholder={t('xiaomi.accountPlaceholder')} required />
            </div>
            <div>
              <label class="input-label text-xs" for="xiaomi-password">{t('xiaomi.password')}</label>
              <input id="xiaomi-password" type="password" class="input py-1 text-sm" bind:value={xiaomiPassword} placeholder="******" required />
            </div>
          </div>
          <div>
            <label class="input-label text-xs" for="xiaomi-region">{t('xiaomi.region')}</label>
            <select id="xiaomi-region" class="input py-1 text-sm" bind:value={xiaomiRegion} disabled={xiaomiLoading}>
              {#each ['cn', 'sg', 'de', 'us', 'ru', 'i2'] as region}
                <option value={region}>{t(`xiaomi.region.${region}`)}</option>
              {/each}
            </select>
          </div>
          <button type="submit" class="btn btn-primary btn-sm" disabled={xiaomiLoading}>
            {#if xiaomiLoading}
              <span class="spinner mr-1"></span>
            {/if}
            {xiaomiLoading ? t('xiaomi.signingIn') : t('xiaomi.signIn')}
          </button>
        </form>

        <!-- Captcha form -->
        {#if xiaomiCaptchaImage}
          <div class="mt-4 p-4 rounded-md border th-border bg-[rgba(0,0,0,0.05)]">
            <p class="text-sm font-medium th-text-primary mb-3">{t('xiaomi.captchaTitle')}</p>
            <div class="flex items-start gap-4">
              <img src={xiaomiCaptchaImage} alt="Captcha" class="border th-border rounded h-12" />
              <div class="flex-1 space-y-2">
                <input
                  type="text"
                  class="input py-1 text-sm"
                  bind:value={xiaomiCaptchaCode}
                  placeholder={t('xiaomi.captchaPlaceholder')}
                  disabled={xiaomiLoading}
                />
                <button
                  type="button"
                  onclick={handleXiaomiCaptcha}
                  class="btn btn-primary btn-sm"
                  disabled={xiaomiLoading || !xiaomiCaptchaCode.trim()}
                >
                  {#if xiaomiLoading}
                    <span class="spinner mr-1"></span>
                  {/if}
                  {t('xiaomi.submitCaptcha')}
                </button>
              </div>
            </div>
          </div>
        {/if}

        <!-- Verify form -->
        {#if xiaomiVerifyType}
          <div class="mt-4 p-4 rounded-md border th-border bg-[rgba(0,0,0,0.05)]">
            <p class="text-sm font-medium th-text-primary mb-1">{t('xiaomi.verifyTitle')}</p>
            <p class="text-xs th-text-secondary mb-3">
              {#if xiaomiVerifyType === 'phone'}
                {t('xiaomi.verifyPhoneHint').replace('{phone}', xiaomiVerifyTarget)}
              {:else}
                {t('xiaomi.verifyEmailHint').replace('{email}', xiaomiVerifyTarget)}
              {/if}
            </p>
            <div class="flex gap-3">
              <input
                type="text"
                class="input py-1 text-sm flex-1"
                bind:value={xiaomiVerifyTicket}
                placeholder={t('xiaomi.verifyCodePlaceholder')}
                disabled={xiaomiLoading}
              />
              <button
                type="button"
                onclick={handleXiaomiVerify}
                class="btn btn-primary btn-sm"
                disabled={xiaomiLoading || !xiaomiVerifyTicket.trim()}
              >
                {#if xiaomiLoading}
                  <span class="spinner mr-1"></span>
                {/if}
                {t('xiaomi.submitVerify')}
              </button>
            </div>
          </div>
        {/if}
      {:else}
        <!-- Device list -->
        <div class="flex items-center justify-between mb-3">
          <span class="text-sm th-text-secondary">{t('xiaomi.devicesFound').replace('{count}', String(xiaomiDeviceList.length))}</span>
          <button class="btn btn-ghost btn-sm" onclick={refreshXiaomiDevices} disabled={xiaomiLoading}>{t('xiaomi.refresh')}</button>
        </div>
        <button onclick={handleSyncCloud} class="btn btn-ghost btn-sm mb-3" disabled={syncing || xiaomiLoading}>
          <RefreshCw size={14} class={syncing ? 'animate-spin' : ''} />
          {t('cameras.syncCloud')}
        </button>
        {#if xiaomiDeviceList.length === 0}
          <p class="th-text-secondary text-sm py-2">{t('xiaomi.noDevices')}</p>
        {:else}
          <div class="space-y-3">
            {#each xiaomiDeviceList as device (device.did)}
              <div class="flex items-center justify-between p-4 rounded-md th-bg-hover border th-border">
                <div class="min-w-0 flex-1 mr-4">
                  <div class="font-medium th-text-primary truncate">{device.name}</div>
                  <div class="text-sm th-text-secondary truncate">{device.model} · {device.localip}</div>
                  <div class="text-xs mt-0.5 {device.isOnline ? 'th-color-success' : 'th-text-muted'}">
                    {device.isOnline ? t('xiaomi.online') : t('xiaomi.offline')}
                  </div>
                </div>
                {#if isXiaomiDeviceAdded(device.did)}
                  <button
                    type="button"
                    class="btn btn-sm shrink-0 opacity-50 cursor-not-allowed"
                    disabled
                  >
                    {t('xiaomi.added')}
                  </button>
                {:else}
                  <button
                    onclick={() => addXiaomiDevice(device)}
                    class="btn btn-primary btn-sm shrink-0"
                    disabled={xiaomiLoading || xiaomiAddingDid === device.did}
                  >
                    {#if xiaomiAddingDid === device.did}
                      <span class="spinner mr-1"></span>
                    {/if}
                    {t('onvif.addCamera')}
                  </button>
                {/if}
              </div>
            {/each}
          </div>
        {/if}
        <div class="mt-4 flex justify-end">
          <button class="btn btn-ghost btn-sm" onclick={handleXiaomiLogout} disabled={xiaomiLoading || syncing || xiaomiAddingDid !== null}>{t('xiaomi.signOut')}</button>
        </div>
      {/if}
      {#if xiaomiError}
        <p class="th-color-danger text-sm" role="alert">{xiaomiError}</p>
      {/if}
    </div>
  {/if}

  <!-- TUTK Vendor Warning Dialog -->
  {#if tutkWarningDevice}
    <div
      class="fixed inset-0 z-50 flex items-center justify-center"
      role="presentation"
      onmousedown={(e) => { if (e.target === e.currentTarget) handleTutkCancel(); }}
    >
      <div class="fixed inset-0 bg-black/60 backdrop-blur-sm" aria-hidden="true"></div>
      <div
        role="dialog"
        tabindex="-1"
        aria-modal="true"
        class="relative card p-6 border th-border max-w-md w-full mx-4"
        onmousedown={(e) => e.stopPropagation()}
      >
        <div class="flex items-start gap-4">
          <div class="flex-shrink-0 mt-0.5">
            <AlertTriangle size={20} class="th-color-danger" />
          </div>
          <div class="flex-1 min-w-0">
            <h3 class="text-lg font-semibold th-text-primary mb-2">
              {t('cameras.tutkInterceptTitle')}
            </h3>
            <p class="text-sm th-text-secondary">
              {t('cameras.unsupportedVendorMessage', { name: tutkWarningDevice.name, vendor: warningVendor })}
            </p>
          </div>
        </div>
        <div class="flex items-center justify-end gap-3 mt-6">
          <button
            class="btn btn-ghost"
            onclick={handleTutkCancel}
          >
            {t('common.cancel')}
          </button>
          <button
            class="btn btn-primary"
            onclick={handleTutkContinueAnyway}
          >
            {t('cameras.continueAnyway')}
          </button>
        </div>
      </div>
    </div>
  {/if}
{/if}
