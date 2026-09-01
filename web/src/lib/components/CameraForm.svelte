<script lang="ts">
  import { t } from '$lib/i18n';
  import {
    createCamera,
    updateCamera,
    getMergeConfig,
    updateMergeConfig,
    buildProtocolsMap,
    normalizeProtocol,
    testConnection,
    getDeviceCapabilities,
    getONVIFProfiles,
  } from '$lib/api';
  import type {
    Camera,
    CreateCameraRequest,
    UpdateCameraRequest,
    MergeConfig,
    ProtocolInfo,
    XiaomiDevice,
    TestConnectionResult,
    DeviceCapabilitiesInfo,
    DeviceProfile,
  } from '$lib/api';
  import { Eye, EyeOff, PlugZap, RefreshCw } from 'lucide-svelte';
  import { showToast } from '$lib/toast';
  import MergeConfigEditor from '$lib/components/MergeConfigEditor.svelte';
  import TimelapseConfigEditor from '$lib/components/TimelapseConfigEditor.svelte';
  import RecordingScheduleEditor from '$lib/components/RecordingScheduleEditor.svelte';
  import DeviceCapabilities from '$lib/components/DeviceCapabilities.svelte';
  import ImagingPanel from '$lib/components/ImagingPanel.svelte';
  import PresetManager from '$lib/components/PresetManager.svelte';
  import ONVIFEvents from '$lib/components/ONVIFEvents.svelte';
  import DeviceManagement from '$lib/components/DeviceManagement.svelte';

  interface Props {
    editingCamera: Camera | null;
    protocols: ProtocolInfo[];
    protocolsMap: Map<string, ProtocolInfo>;
    xiaomiDeviceList?: XiaomiDevice[];
    onsave: () => void;
    oncancel: () => void;
  }

  let {
    editingCamera,
    protocols,
    protocolsMap,
    xiaomiDeviceList = [],
    onsave,
    oncancel,
  }: Props = $props();

  // Form state
  let formName = $state('');
  let formProtocol = $state('rtsp');
  let formEncoding = $state('h264');
  let formRTSPTransport = $state('tcp');
  let formUrl = $state('');
  let formUsername = $state('');
  let formPassword = $state('');
  let showPassword = $state(false);
  let formEnabled = $state(true);
  let saving = $state(false);
  let formDescription = $state('');
  let formLocation = $state('');
  let formBrand = $state('');
  let formModel = $state('');
  let formSerialNumber = $state('');
  let formRetentionDays = $state(0);
  let formStreamEncoding = $state('');
  let formAudioEnabled = $state(false);
  let formRecordingMode = $state<'continuous' | 'scheduled' | 'off' | 'event' | 'adaptive'>('continuous');
  let formSubStreamURL = $state('');
  let formSubnetHints = $state('');
  let formAdaptiveInterval = $state('30s');
  let validationErrors = $state<Record<string, string>>({});

  // Test connection state
  let testing = $state(false);
  let testResult = $state<TestConnectionResult | null>(null);

  // Merge config
  let mergeConfig = $state<MergeConfig | null>(null);
  let mergeConfigLoading = $state(false);

  // ONVIF capabilities
  let deviceCaps = $state<DeviceCapabilitiesInfo | null>(null);
  let capsLoading = $state(false);

  // ONVIF profiles
  let onvifProfiles = $state<DeviceProfile[]>([]);
  let profilesLoading = $state(false);
  let formProfileToken = $state('');
  let selectedProfile = $state<DeviceProfile | null>(null);

  // Auto-select encoding when protocol changes
  $effect(() => {
    const proto = protocolsMap.get(formProtocol);
    if (!proto) return;
    const encodings = proto.encodings;
    if (!encodings.includes(formEncoding)) {
      if (formProtocol === 'onvif') {
        formEncoding = '';
      } else if (formProtocol === 'http') {
        formEncoding = 'jpeg';
      } else if (encodings.length > 0) {
        formEncoding = encodings[0];
      } else {
        formEncoding = '';
      }
    }
  });

  // Populate form when editingCamera changes
  $effect(() => {
    if (editingCamera) {
      populateForm(editingCamera);
      loadMergeConfig(editingCamera.id);
      loadCapabilities(editingCamera);
      if (normalizeProtocol(editingCamera.protocol) === 'onvif') {
        loadProfiles();
      }
    } else {
      resetFormFields();
      mergeConfig = null;
      mergeConfigLoading = false;
      deviceCaps = null;
      onvifProfiles = [];
      selectedProfile = null;
    }
  });

  function resetFormFields() {
    formName = '';
    formProtocol = 'rtsp';
    formEncoding = 'h264';
    formUrl = '';
    formRTSPTransport = 'tcp';
    formUsername = '';
    formPassword = '';
    showPassword = false;
    formEnabled = true;
    formDescription = '';
    formLocation = '';
    formBrand = '';
    formModel = '';
    formSerialNumber = '';
    formRetentionDays = 0;
    formStreamEncoding = '';
    formAudioEnabled = false;
    formRecordingMode = 'continuous';
    formSubStreamURL = '';
    formSubnetHints = '';
    formAdaptiveInterval = '30s';
    formProfileToken = '';
    selectedProfile = null;
    onvifProfiles = [];
    validationErrors = {};
  }

  function populateForm(camera: Camera) {
    formName = camera.name;
    formProtocol = camera.protocol;
    formEncoding = camera.encoding || '';
    // Handle legacy combined protocols
    if (camera.protocol === 'rtsp_h264') { formProtocol = 'rtsp'; formEncoding = 'h264'; }
    else if (camera.protocol === 'rtsp_h265') { formProtocol = 'rtsp'; formEncoding = 'h265'; }
    else if (camera.protocol === 'rtsp_mjpeg') { formProtocol = 'rtsp'; formEncoding = 'mjpeg'; }
    else if (camera.protocol === 'http_jpeg') { formProtocol = 'http'; formEncoding = 'jpeg'; }
    formUrl = camera.url || '';
    formRTSPTransport = camera.rtsp_transport || 'tcp';
    formUsername = camera.username || '';
    formPassword = '';
    showPassword = false;
    formEnabled = camera.enabled;
    formDescription = camera.description || '';
    formLocation = camera.location || '';
    formBrand = camera.brand || '';
    formModel = camera.model || '';
    formSerialNumber = camera.serial_number || '';
    formRetentionDays = camera.retention_days || 0;
    formStreamEncoding = camera.stream_encoding || '';
    formAudioEnabled = camera.audio_enabled ?? false;
    formRecordingMode = camera.recording_mode ?? 'continuous';
    formSubStreamURL = camera.sub_stream_url || '';
    formSubnetHints = (camera.subnet_hints || []).join(', ');
    formAdaptiveInterval = camera.adaptive?.timelapse_interval || '30s';
    formProfileToken = camera.profile_token || '';
    selectedProfile = null;
    validationErrors = {};
  }

  async function loadMergeConfig(cameraId: string) {
    mergeConfig = null;
    mergeConfigLoading = true;
    try {
      mergeConfig = await getMergeConfig(cameraId);
    } catch (e) { console.warn('Failed to load merge config:', e); mergeConfig = null; } finally {
      mergeConfigLoading = false;
    }
  }

  async function loadCapabilities(cam: Camera) {
    if (normalizeProtocol(cam.protocol) !== 'onvif') {
      deviceCaps = null;
      return;
    }
    capsLoading = true;
    try {
      deviceCaps = await getDeviceCapabilities(cam.id);
    } catch (e) {
      console.warn('Failed to load device capabilities:', e);
      deviceCaps = null;
    } finally {
      capsLoading = false;
    }
  }

  async function loadProfiles() {
    if (formProtocol !== 'onvif' || !formUrl.trim()) {
      onvifProfiles = [];
      return;
    }
    profilesLoading = true;
    try {
      const result = await getONVIFProfiles(editingCamera?.id || '');
      onvifProfiles = result.profiles || [];
      // Auto-select first profile if none selected
      if (!formProfileToken && onvifProfiles.length > 0) {
        formProfileToken = onvifProfiles[0].token;
        selectedProfile = onvifProfiles[0];
      } else if (formProfileToken) {
        selectedProfile = onvifProfiles.find(p => p.token === formProfileToken) || null;
      }
    } catch (e) {
      console.warn('Failed to load ONVIF profiles:', e);
      onvifProfiles = [];
    } finally {
      profilesLoading = false;
    }
  }

  function handleProfileChange() {
    selectedProfile = onvifProfiles.find(p => p.token === formProfileToken) || null;
  }

  function validateField(field: string, value: string) {
    if (field === 'name' && !value.trim()) {
      validationErrors['name'] = t('cameras.nameRequired');
    } else if (field === 'url' && !value.trim()) {
      validationErrors['url'] = t('cameras.urlRequired');
    } else {
      delete validationErrors[field];
    }
  }

  function supportsAudioRecording(): boolean {
    if (formProtocol === 'xiaomi') return true;
    if (formProtocol === 'onvif') return formEncoding !== 'mjpeg';
    if (formProtocol === 'rtsp') return formEncoding === 'h264' || formEncoding === 'h265';
    return false;
  }

  function handleProtocolChange() {
    if (formProtocol === 'onvif') {
      formEncoding = '';
      formStreamEncoding = '';
    }
  }

  function validate(): boolean {
    validationErrors = {};
    if (!formName.trim()) validationErrors['name'] = t('cameras.nameRequired');
    if (!formProtocol) validationErrors['protocol'] = t('cameras.protocolRequired');
    if (!formUrl.trim()) validationErrors['url'] = t('cameras.urlRequired');
    return Object.keys(validationErrors).length === 0;
  }
  async function handleTestConnection() {
    if (!formUrl.trim()) return;
    testing = true;
    testResult = null;
    try {
      testResult = await testConnection({
        protocol: formProtocol,
        url: formUrl,
        username: formUsername || undefined,
        password: formPassword || undefined,
        encoding: formEncoding || undefined,
        rtsp_transport: (formProtocol === 'rtsp' || formProtocol === 'onvif') ? formRTSPTransport : undefined,
        onvif_endpoint: formProtocol === 'onvif' ? formUrl : undefined,
      });
    } catch (e: any) {
      testResult = { success: false, message: e.message || t('cameras.testFailed', { error: '' }), latency_ms: 0 };
    } finally {
      testing = false;
    }
  }

  async function handleSubmit() {
    if (!validate()) return;
    saving = true;

    try {
      if (editingCamera) {
        const data: UpdateCameraRequest = {
          name: formName,
          protocol: formProtocol,
          url: formUrl,
          enabled: formEnabled,
          description: formDescription || undefined,
          location: formLocation || undefined,
          brand: formBrand || undefined,
          model: formModel || undefined,
          serial_number: formSerialNumber || undefined,
          retention_days: formRetentionDays,
          stream_encoding: formProtocol === 'onvif' ? (formStreamEncoding || undefined) : undefined,
          encoding: formEncoding,
          rtsp_transport: (formProtocol === 'rtsp' || formProtocol === 'onvif') ? formRTSPTransport : undefined,
          profile_token: formProtocol === 'onvif' ? (formProfileToken || undefined) : undefined,
          profile_name: formProtocol === 'onvif' ? (selectedProfile?.name || undefined) : undefined,
          audio_enabled: supportsAudioRecording() ? formAudioEnabled : false,
          recording_mode: formRecordingMode,
          sub_stream_url: formSubStreamURL || undefined,
          subnet_hints: formSubnetHints.split(',').map(s => s.trim()).filter(Boolean),
          adaptive: formRecordingMode === 'adaptive' ? { timelapse_interval: formAdaptiveInterval || '30s' } : undefined,
        };
        if (formUsername && formUsername !== editingCamera.username) {
          data.username = formUsername;
        }
        if (formPassword) {
          if (!data.username && formUsername === editingCamera.username) {
            data.username = formUsername;
          }
          data.password = formPassword;
        }

        // Save per-camera merge config if editing
        if (mergeConfig) {
          try {
            await updateMergeConfig(editingCamera.id, mergeConfig);
          } catch (e) { console.warn('Failed to save merge config:', e); }
        }
        await updateCamera(editingCamera.id, data);
        showToast(t('cameras.cameraUpdated'), 'success');
      } else {
        const data: CreateCameraRequest = {
          name: formName,
          protocol: formProtocol,
          url: formUrl,
          enabled: formEnabled,
          description: formDescription || undefined,
          location: formLocation || undefined,
          brand: formBrand || undefined,
          model: formModel || undefined,
          serial_number: formSerialNumber || undefined,
          stream_encoding: formProtocol === 'onvif' ? (formStreamEncoding || undefined) : undefined,
          encoding: formEncoding,
          rtsp_transport: (formProtocol === 'rtsp' || formProtocol === 'onvif') ? formRTSPTransport : undefined,
          profile_token: formProtocol === 'onvif' ? (formProfileToken || undefined) : undefined,
          profile_name: formProtocol === 'onvif' ? (selectedProfile?.name || undefined) : undefined,
          audio_enabled: supportsAudioRecording() ? formAudioEnabled : false,
          recording_mode: formRecordingMode,
          sub_stream_url: formSubStreamURL || undefined,
          subnet_hints: formSubnetHints.split(',').map(s => s.trim()).filter(Boolean),
          adaptive: formRecordingMode === 'adaptive' ? { timelapse_interval: formAdaptiveInterval || '30s' } : undefined,
        };
        if (formUsername) data.username = formUsername;
        if (formPassword) data.password = formPassword;
        await createCamera(data);
        showToast(t('cameras.cameraAdded'), 'success');
      }
      onsave();
    } catch (e) { console.warn('Failed to save camera:', e); showToast(
        editingCamera ? t('cameras.failedUpdate') : t('cameras.failedAdd'),
        'error'
      );
    } finally {
      saving = false;
    }
  }
</script>

<div class="card p-6 border th-border">
  <h3 class="text-lg font-semibold th-text-primary mb-4">
    {editingCamera ? t('cameras.editCamera') : t('cameras.addCamera')}
  </h3>

  <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
    <!-- Name -->
    <div>
      <label for="cam-name" class="input-label">{t('cameras.name')}</label>
      <input id="cam-name" type="text" class="input {validationErrors['name'] ? 'border-red-500' : ''}" bind:value={formName} onblur={() => validateField('name', formName)} oninput={() => { if (validationErrors['name']) delete validationErrors['name']; }} />
      {#if validationErrors['name']}
        <p class="th-color-danger text-xs mt-1">{validationErrors['name']}</p>
      {/if}
    </div>

    <!-- Protocol -->
    <div>
      <label for="cam-protocol" class="input-label">{t('cameras.protocol')}</label>
      <select id="cam-protocol" class="input" bind:value={formProtocol} onchange={handleProtocolChange}>
        {#each protocols.filter(p => p.addable !== false) as proto (proto.id)}
          <option value={proto.id}>{proto.label}</option>
        {/each}
      </select>
      {#if validationErrors['protocol']}
        <p class="th-color-danger text-xs mt-1">{validationErrors['protocol']}</p>
      {/if}
    </div>

    <!-- Encoding -->
    <div>
      <label for="cam-encoding" class="input-label">{t('cameras.tableEncoding')}</label>
      <select id="cam-encoding" class="input" bind:value={formEncoding}>
        {#if formProtocol === 'onvif'}
          <option value="">{t('cameras.autoDetect')}</option>
        {/if}
        {#each (protocolsMap.get(formProtocol)?.encodings || [formEncoding]) as enc}
          <option value={enc}>{t('cameras.encoding.' + enc) || enc.toUpperCase()}</option>
        {/each}
      </select>
    </div>

    {#if formProtocol === 'rtsp' || formProtocol === 'onvif'}
      <div>
        <label for="cam-rtsp-transport" class="input-label">{t('cameras.rtspTransport')}</label>
        <select id="cam-rtsp-transport" class="input" bind:value={formRTSPTransport}>
          <option value="tcp">{t('cameras.transportTcp')}</option>
          <option value="udp">{t('cameras.transportUdp')}</option>
        </select>
      </div>
    {/if}

    <!-- URL -->
    <div class="md:col-span-2">
      <label for="cam-url" class="input-label">
        {t('cameras.url')}
        {#if formProtocol === 'onvif'}
          <span class="text-xs th-text-muted ml-1">({t('cameras.onvifEndpoint')})</span>
        {/if}
      </label>
      <div class="flex gap-2">
        <input id="cam-url" type="text" class="input flex-1 {validationErrors['url'] ? 'border-red-500' : ''}" bind:value={formUrl}
          placeholder={formProtocol === 'xiaomi' ? 'xiaomi://device_id' 
            : formProtocol === 'onvif' ? 'http://192.168.1.100:80/onvif/device_service' 
            : formProtocol === 'rtmp-pull' ? 'rtmp://example.com/live/stream' 
            : formProtocol === 'http-flv-pull' ? 'http://example.com/live/stream.flv' 
            : 'rtsp://...'}
          onblur={() => validateField('url', formUrl)} oninput={() => { if (validationErrors['url']) delete validationErrors['url']; testResult = null; }} />
        {#if formProtocol !== 'xiaomi'}
          <button
            type="button"
            onclick={handleTestConnection}
            disabled={testing || !formUrl.trim()}
            class="btn btn-ghost px-3 py-2 flex items-center gap-1.5 whitespace-nowrap"
            title={t('cameras.testConnection')}
          >
            <PlugZap size={14} />
            {#if testing}
              <span class="spinner mr-1"></span>{t('cameras.testing')}
            {:else}
              {t('cameras.testConnection')}
            {/if}
          </button>
        {/if}
      </div>
      {#if testResult}
        <p class="text-xs mt-1 {testResult.success ? 'th-color-success' : 'th-color-danger'}">
          {testResult.success
            ? t('cameras.testSuccess').replace('{latency}', String(testResult.latency_ms))
            : t('cameras.testFailed').replace('{error}', testResult.message)}
        </p>
      {/if}
      {#if validationErrors['url']}
        <p class="th-color-danger text-xs mt-1">{validationErrors['url']}</p>
      {/if}
    </div>

    <!-- ONVIF Profile Selection -->
    {#if formProtocol === 'onvif' && editingCamera}
      <div class="md:col-span-2">
        <label for="cam-profile" class="input-label">
          ONVIF Profile
          {#if profilesLoading}
            <span class="text-xs th-text-muted ml-1">(加载中...)</span>
          {/if}
        </label>
        <div class="flex gap-2">
          <select 
            id="cam-profile" 
            class="input flex-1" 
            bind:value={formProfileToken} 
            onchange={handleProfileChange}
            disabled={profilesLoading || onvifProfiles.length === 0}
          >
            {#if onvifProfiles.length === 0}
              <option value="">{profilesLoading ? '加载中...' : '无可用profile'}</option>
            {:else}
              {#each onvifProfiles as profile (profile.token)}
                <option value={profile.token}>
                  {profile.name} - {profile.encoding?.toUpperCase() || 'N/A'} ({profile.width}x{profile.height})
                </option>
              {/each}
            {/if}
          </select>
          <button
            type="button"
            onclick={loadProfiles}
            disabled={profilesLoading}
            class="btn btn-ghost px-3 py-2 flex items-center gap-1.5 whitespace-nowrap"
            title="刷新profiles"
          >
            <RefreshCw size={14} class={profilesLoading ? 'animate-spin' : ''} />
          </button>
        </div>
        {#if selectedProfile}
          <div class="mt-2 p-2 rounded th-bg-tertiary text-xs">
            <span class="font-medium">{selectedProfile.name}</span>
            <span class="th-text-muted ml-2">
              {selectedProfile.encoding?.toUpperCase() || 'N/A'} · 
              {selectedProfile.width}x{selectedProfile.height}
            </span>
          </div>
        {/if}
        <p class="text-xs th-text-muted mt-1">
          选择要使用的媒体配置文件。修改后将使用新的RTSP流地址。
        </p>
      </div>
    {/if}

    {#if formProtocol === 'xiaomi'}
      {#if editingCamera?.protocol === 'xiaomi' && xiaomiDeviceList.length > 0}
        {@const matchDid = formUrl.replace('xiaomi://', '')}
        {@const matchedDevice = xiaomiDeviceList.find(d => d.did === matchDid)}
        {#if matchedDevice}
          <div class="p-3 rounded-md th-bg-hover border th-border text-sm">
            <div class="font-medium th-text-primary">{matchedDevice.name}</div>
            <div class="th-text-secondary">{matchedDevice.model} · {matchedDevice.localip}</div>
            <div class="{matchedDevice.isOnline ? 'th-color-success' : 'th-text-muted'}">
              {matchedDevice.isOnline ? t('xiaomi.online') : t('xiaomi.offline')}
            </div>
          </div>
        {/if}
      {/if}
    {/if}

    {#if protocolsMap.get(formProtocol)?.capabilities?.auth}
      <!-- Username -->
      <div>
        <label for="cam-user" class="input-label">{t('cameras.username')}</label>
        <input id="cam-user" type="text" class="input" bind:value={formUsername} placeholder={editingCamera ? (editingCamera.username || t('cameras.notSet')) : ''} />
      </div>

      <!-- Password -->
      <div>
        <label for="cam-pass" class="input-label">{t('cameras.password')}</label>
        <div class="relative">
          <input
            id="cam-pass"
            type={showPassword ? 'text' : 'password'}
            class="input pr-10"
            bind:value={formPassword}
            placeholder={editingCamera ? (editingCamera.has_password ? t('cameras.passwordSet') : t('cameras.notSet')) : ''}
          />
          <button
            type="button"
            class="absolute right-2 top-1/2 -translate-y-1/2 th-text-tertiary hover:th-text-primary transition-colors"
            onclick={() => showPassword = !showPassword}
            aria-label={showPassword ? t('common.hidePassword') : t('common.showPassword')}
          >
            {#if showPassword}
              <EyeOff class="w-4 h-4" />
            {:else}
              <Eye class="w-4 h-4" />
            {/if}
          </button>
        </div>
        {#if editingCamera?.has_password}
          <p class="mt-1 text-xs th-text-secondary">{t('cameras.passwordRetainedHint')}</p>
        {/if}
      </div>
    {:else if protocolsMap.get(formProtocol)}
      <div class="md:col-span-2 text-sm th-text-secondary">
        {t('cameras.authManagedExternally')}
      </div>
    {/if}

    <!-- Enabled -->
    <div class="md:col-span-2 flex items-center gap-2">
      <input id="cam-enabled" type="checkbox" class="accent-[var(--color-accent)]" bind:checked={formEnabled} />
      <label for="cam-enabled" class="th-text-secondary text-sm">{t('cameras.enabledToggle')}</label>
    </div>

    {#if supportsAudioRecording()}
      <div class="md:col-span-2 flex items-start gap-2">
        <input id="cam-audio" type="checkbox" class="accent-[var(--color-accent)] mt-0.5" bind:checked={formAudioEnabled} />
        <div>
          <label for="cam-audio" class="th-text-secondary text-sm">{t('cameras.audioEnabled')}</label>
          <p class="th-text-muted text-xs mt-1">{t('cameras.audioEnabledHint')}</p>
        </div>
      </div>
    {/if}

    <!-- Description -->
    <div class="md:col-span-2">
      <label for="cam-desc" class="input-label">{t('cameras.description')}</label>
      <textarea id="cam-desc" class="input" rows="2" bind:value={formDescription} placeholder={t('cameras.descriptionPlaceholder')}></textarea>
    </div>

    <!-- Location -->
    <div>
      <label for="cam-location" class="input-label">{t('cameras.location')}</label>
      <input id="cam-location" type="text" class="input" bind:value={formLocation} placeholder={t('cameras.locationPlaceholder')} />
    </div>

    <!-- Brand -->
    <div>
      <label for="cam-brand" class="input-label">{t('cameras.brand')}</label>
      <input id="cam-brand" type="text" class="input" bind:value={formBrand} />
    </div>

    <!-- Model -->
    <div>
      <label for="cam-model" class="input-label">{t('cameras.model')}</label>
      <input id="cam-model" type="text" class="input" bind:value={formModel} />
    </div>

    <!-- Serial Number -->
    <div>
      <label for="cam-serial" class="input-label">{t('cameras.serialNumber')}</label>
      <input id="cam-serial" type="text" class="input" bind:value={formSerialNumber} />
    </div>

    <!-- Retention Days -->
    <div>
      <label for="cam-retention" class="input-label">{t('cameras.retentionDays')}</label>
      <input id="cam-retention" type="number" min="0" class="input" bind:value={formRetentionDays} />
      <p class="th-text-muted text-xs mt-1">{t('cameras.retentionDaysHint')}</p>
    </div>
  </div>

  <!-- Recording mode (edit mode only) -->
  {#if editingCamera}
    <div class="mt-6 border th-border rounded-lg p-4">
      <div class="mb-3">
        <span class="th-text-secondary font-medium text-sm">{t('cameras.recordingMode.title')}</span>
        <p class="th-text-muted text-xs mt-1">{t('cameras.recordingMode.hint')}</p>
      </div>
      <div class="flex flex-wrap gap-2">
        {#each (['continuous', 'scheduled', 'event', 'adaptive', 'off'] as const) as mode}
          <button
            type="button"
            class="btn px-3 py-1.5 text-sm {formRecordingMode === mode ? 'btn-primary' : 'btn-ghost'}"
            onclick={() => formRecordingMode = mode}
          >
            {t(`cameras.recordingMode.${mode}`)}
          </button>
        {/each}
      </div>
      {#if formRecordingMode === 'scheduled'}
        <RecordingScheduleEditor cameraId={editingCamera.id} />
      {/if}
      {#if formRecordingMode === 'adaptive'}
        <div class="mt-3">
          <label for="cam-adaptive-interval" class="input-label">{t('cameras.adaptive.interval')}</label>
          <input id="cam-adaptive-interval" type="text" class="input" bind:value={formAdaptiveInterval} placeholder="30s" />
          <p class="th-text-muted text-xs mt-1">{t('cameras.adaptive.intervalHint')}</p>
        </div>
      {/if}
    </div>
  {/if}

  <div class="mt-6 grid grid-cols-1 md:grid-cols-2 gap-4">
    <div class="md:col-span-2">
      <label for="cam-sub-url" class="input-label">{t('cameras.subStreamUrl')}</label>
      <input id="cam-sub-url" type="text" class="input" bind:value={formSubStreamURL} placeholder="rtsp://192.168.1.100:554/stream2" />
      <p class="th-text-muted text-xs mt-1">{t('cameras.subStreamUrlHint')}</p>
    </div>
    {#if formProtocol === 'onvif'}
      <div class="md:col-span-2">
        <label for="cam-subnet-hints" class="input-label">{t('cameras.subnetHints')}</label>
        <input id="cam-subnet-hints" type="text" class="input" bind:value={formSubnetHints} placeholder="192.168.10.0/24" />
        <p class="th-text-muted text-xs mt-1">{t('cameras.subnetHintsHint')}</p>
      </div>
    {/if}
  </div>

  <!-- Merge Config (edit mode only) -->
  {#if editingCamera}
    <MergeConfigEditor
      cameraId={editingCamera.id}
      {mergeConfig}
      {mergeConfigLoading}
      onchange={(config) => mergeConfig = config}
      ondelete={() => mergeConfig = null}
    />
  {/if}

  <!-- Timelapse Config (edit mode only) -->
  {#if editingCamera}
    <TimelapseConfigEditor cameraId={editingCamera.id} />
  {/if}

  <!-- ONVIF Device Settings (edit mode only, ONVIF cameras) -->
  {#if editingCamera && normalizeProtocol(editingCamera.protocol) === 'onvif' && !capsLoading}
    <div class="mt-6 space-y-4">
      <h4 class="text-sm font-semibold th-text-secondary uppercase tracking-wide">ONVIF</h4>

      <!-- Device Capabilities -->
      <DeviceCapabilities cameraId={editingCamera.id} />

      <!-- Imaging Panel (if supported) -->
      {#if deviceCaps?.imaging}
        <details class="border th-border rounded-lg" open>
          <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
            {t('onvif.imaging.title')}
          </summary>
          <div class="px-4 pb-4">
            <ImagingPanel cameraId={editingCamera.id} />
          </div>
        </details>
      {/if}

      <!-- Preset Manager (if PTZ supported) -->
      {#if deviceCaps?.ptz}
        <details class="border th-border rounded-lg" open>
          <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
            {t('onvif.presets.title')}
          </summary>
          <div class="px-4 pb-4">
            <PresetManager cameraId={editingCamera.id} />
          </div>
        </details>
      {:else}
        <div class="px-4 py-3 rounded-lg border th-border th-bg-hover text-sm th-text-muted">
          {t('ptz.notSupported')}
        </div>
      {/if}

      <!-- ONVIF Events (if supported) -->
      {#if deviceCaps?.events}
        <details class="border th-border rounded-lg">
          <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
            {t('onvif.events.title')}
          </summary>
          <div class="px-4 pb-4">
            <ONVIFEvents cameraId={editingCamera.id} maxEvents={50} />
          </div>
        </details>
      {/if}

      <!-- Device Management -->
      <details class="border th-border rounded-lg">
        <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
          {t('onvif.device.title')}
        </summary>
        <div class="px-4 pb-4">
          <DeviceManagement cameraId={editingCamera.id} cameraName={editingCamera.name} />
        </div>
      </details>
    </div>
  {/if}

  <div class="flex items-center gap-3 mt-6">
    <button onclick={handleSubmit} class="btn btn-primary" disabled={saving}>
      {#if saving}
        <span class="spinner mr-2"></span>
      {/if}
      {t('cameras.save')}
    </button>
    <button onclick={oncancel} class="btn btn-ghost">
      {t('cameras.cancel')}
    </button>
  </div>
</div>
