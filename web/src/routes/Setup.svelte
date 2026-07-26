<script lang="ts">
  import { browseSetupDirectories, setupApi, storeCredentials } from '$lib/api';
  import type { SetupDirectoryEntry } from '$lib/api';
  import { setProtocolPreference } from '$lib/preferences';
  import ThemeToggle from '../components/ThemeToggle.svelte';
  import LanguageSwitcher from '../components/LanguageSwitcher.svelte';
  import { t, i18nState, setLang } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { ArrowUp, Check, Eye, EyeOff, FolderOpen, X } from 'lucide-svelte';

  let username = $state('admin');
  let dataDir = $state('');
  let password = $state('');
  let confirmPassword = $state('');
  let showPassword = $state(false);
  let showConfirmPassword = $state(false);
  let language = $state(i18nState.currentLang);

  // Keep form dropdown in sync with the header LanguageSwitcher
  $effect(() => {
    language = i18nState.currentLang;
  });

  function handleLanguageChange(e: Event) {
    const val = (e.target as HTMLSelectElement).value;
    language = val;
    setLang(val);
  }
  let error = $state('');
  let loading = $state(false);
  let restartRequired = $state(false);
  let configuredDataDir = $state('');
  let folderPickerOpen = $state(false);
  let folderPickerLoading = $state(false);
  let folderPickerError = $state('');
  let folderPickerPath = $state('');
  let folderPickerParent = $state('');
  let folderEntries = $state<SetupDirectoryEntry[]>([]);

  let errors = $state({ dataDir: '', username: '', password: '', confirmPassword: '' });

  // Browser capability detection
  let capabilities = $state({
    llhls: true,
    webrtc: false,
    flv: false,
    hls: false,
  });
  let bestProtocol = $state('webrtc');

  $effect(() => {
    // LL-HLS: hls.js bundled — always available
    capabilities.llhls = true;

    // WebRTC: RTCPeerConnection available
    capabilities.webrtc = typeof RTCPeerConnection !== 'undefined';

    // FLV: ReadableStream + MediaSource available
    capabilities.flv = typeof ReadableStream !== 'undefined' && typeof MediaSource !== 'undefined';

    // HLS: native HLS support (Safari)
    try {
      const video = document.createElement('video');
      capabilities.hls = video.canPlayType('application/vnd.apple.mpegurl') !== '';
    } catch {
      capabilities.hls = false;
    }

    // Auto-select best available: WebRTC > LL-HLS > FLV > HLS
    if (capabilities.webrtc) {
      bestProtocol = 'webrtc';
    } else if (capabilities.llhls) {
      bestProtocol = 'llhls';
    } else if (capabilities.flv) {
      bestProtocol = 'flv';
    } else {
      bestProtocol = 'hls';
    }
  });

  function validateUsername() {
    if (!username.trim()) {
      errors.username = t('setup.errors.usernameRequired');
    } else {
      errors.username = '';
    }
  }

  function validateDataDir() {
    if (!dataDir.trim()) {
      errors.dataDir = t('setup.errors.dataDirRequired');
    } else {
      errors.dataDir = '';
    }
  }

  function validatePassword() {
    if (!password) {
      errors.password = t('setup.errors.passwordRequired');
    } else if (password.length < 8) {
      errors.password = t('setup.errors.passwordMinLength');
    } else {
      errors.password = '';
    }
    // Re-validate confirm if it has content
    if (confirmPassword) validateConfirmPassword();
  }

  function validateConfirmPassword() {
    if (!confirmPassword) {
      errors.confirmPassword = t('setup.errors.confirmRequired');
    } else if (password !== confirmPassword) {
      errors.confirmPassword = t('setup.errors.passwordMismatch');
    } else {
      errors.confirmPassword = '';
    }
  }

  function onDataDirInput() { if (errors.dataDir) errors.dataDir = ''; }
  function onUsernameInput() { if (errors.username) errors.username = ''; }
  function onPasswordInput() { if (errors.password) errors.password = ''; }
  function onConfirmInput() { if (errors.confirmPassword) errors.confirmPassword = ''; }

  async function browseFolder(path?: string) {
    folderPickerLoading = true;
    folderPickerError = '';
    try {
      const result = await browseSetupDirectories(path);
      folderPickerPath = result.path;
      folderPickerParent = result.parent;
      folderEntries = result.entries || [];
    } catch (e) {
      folderPickerError = e instanceof Error ? e.message : t('setup.folderPickerFailed');
    } finally {
      folderPickerLoading = false;
    }
  }

  async function openFolderPicker() {
    folderPickerOpen = true;
    await browseFolder(dataDir.trim() || undefined);
  }

  function chooseFolder(path: string) {
    dataDir = path;
    validateDataDir();
    folderPickerOpen = false;
  }

  async function handleSubmit() {
    validateDataDir();
    validateUsername();
    validatePassword();
    validateConfirmPassword();
    if (errors.dataDir || errors.username || errors.password || errors.confirmPassword) return;

    error = '';
    loading = true;

    try {
      setLang(language);
      const res = await setupApi(username, password, language, dataDir.trim());

      // Decode token to get credentials and store them
      const decoded = atob(res.token);
      const [user, pass] = decoded.split(':');
      storeCredentials(user, pass);

      if (res.restart_required) {
        configuredDataDir = res.data_dir || dataDir.trim();
        restartRequired = true;
        return;
      }

      // Store detected protocol preference
      setProtocolPreference(bestProtocol);

      // Show completion toast
      const protocolLabel = bestProtocol === 'llhls' ? 'LL-HLS'
        : bestProtocol === 'webrtc' ? 'WebRTC'
        : bestProtocol === 'flv' ? 'HTTP-FLV' : 'HLS';
      showToast(t('setup.complete', { protocol: protocolLabel }), 'success');

      // Redirect to recordings
      window.location.hash = '#/recordings';
    } catch (e) {
      error = e instanceof Error ? e.message : t('setup.errors.failed');
    } finally {
      loading = false;
    }
  }

  function handleKeydown(e: KeyboardEvent) {
    if (e.key === 'Enter') handleSubmit();
  }
</script>

<div class="min-h-screen flex items-center justify-center th-bg-primary px-4" data-lang={i18nState.currentLang}>
  <div class="fixed top-4 right-4 flex items-center gap-2 z-50">
    <ThemeToggle />
    <LanguageSwitcher />
  </div>

  <div class="card w-full max-w-lg p-10 border th-border shadow-2xl">
    <div class="text-center mb-8">
      <div class="setup-logo-wrap" aria-hidden="true">
        <img class="setup-logo" src="/lalmax-nvr-logo.png" alt="lalmax-nvr" />
      </div>
      <h1 class="text-3xl font-bold bg-gradient-to-r from-violet-400 to-blue-400 bg-clip-text text-transparent mb-3">{t('setup.title')}</h1>
      <p class="th-text-tertiary text-sm">{t('setup.subtitle')}</p>
    </div>

    {#if error}
      <div class="mb-6 p-3 bg-[rgba(239,68,68,0.3)] border th-border-danger rounded-lg th-color-danger text-sm">
        {error}
      </div>
    {/if}

    {#if restartRequired}
      <div class="space-y-5 text-center">
        <div class="p-4 rounded-lg border border-green-500/30 bg-green-500/10 th-text-secondary text-sm">
          <p class="font-semibold th-text-primary mb-2">{t('setup.restartTitle')}</p>
          <p>{t('setup.restartMessage')}</p>
          <p class="mt-3 break-all font-mono text-xs th-text-primary">{configuredDataDir}</p>
        </div>
        <p class="text-xs th-text-tertiary">{t('setup.restartHint')}</p>
        <button type="button" class="btn btn-primary w-full" onclick={() => { window.location.hash = '#/login'; }}>
          {t('setup.backToLogin')}
        </button>
      </div>
    {:else}
    <form onsubmit={(e) => { e.preventDefault(); handleSubmit(); }} class="space-y-5">
      <!-- Server data directory -->
      <div>
        <label for="setup-data-dir" class="input-label">{t('setup.dataDir')}</label>
        <div class="flex gap-2">
          <input
            id="setup-data-dir"
            type="text"
            class="input flex-1 {errors.dataDir ? 'border-red-500' : ''}"
            bind:value={dataDir}
            placeholder={t('setup.dataDirPlaceholder')}
            disabled={loading}
            onkeydown={handleKeydown}
            onblur={validateDataDir}
            oninput={onDataDirInput}
            autocomplete="off"
          />
          <button type="button" class="btn btn-secondary shrink-0" disabled={loading} onclick={openFolderPicker}>
            <FolderOpen size={16} class="mr-1" />
            {t('setup.chooseDataDir')}
          </button>
        </div>
        <p class="text-xs th-text-tertiary mt-1">{t('setup.dataDirHint')}</p>
        {#if errors.dataDir}
          <p class="th-color-danger text-xs mt-1">{errors.dataDir}</p>
        {/if}
      </div>

      <!-- Username -->
      <div>
        <label for="setup-username" class="input-label">{t('setup.username')}</label>
        <input
          id="setup-username"
          type="text"
          class="input {errors.username ? 'border-red-500' : ''}"
          bind:value={username}
          placeholder="admin"
          disabled={loading}
          onkeydown={handleKeydown}
          onblur={validateUsername}
          oninput={onUsernameInput}
          autocomplete="username"
        />
        {#if errors.username}
          <p class="th-color-danger text-xs mt-1">{errors.username}</p>
        {/if}
      </div>

      <!-- Password -->
      <div>
        <label for="setup-password" class="input-label">{t('setup.password')}</label>
        <div class="relative">
          <input
            id="setup-password"
            type={showPassword ? 'text' : 'password'}
            class="input pr-10 {errors.password ? 'border-red-500' : ''}"
            bind:value={password}
            placeholder={t('setup.passwordPlaceholder')}
            disabled={loading}
            onkeydown={handleKeydown}
            onblur={validatePassword}
            oninput={onPasswordInput}
            autocomplete="new-password"
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
        {#if errors.password}
          <p class="th-color-danger text-xs mt-1">{errors.password}</p>
        {/if}
      </div>

      <!-- Confirm Password -->
      <div>
        <label for="setup-confirm" class="input-label">{t('setup.confirmPassword')}</label>
        <div class="relative">
          <input
            id="setup-confirm"
            type={showConfirmPassword ? 'text' : 'password'}
            class="input pr-10 {errors.confirmPassword ? 'border-red-500' : ''}"
            bind:value={confirmPassword}
            placeholder={t('setup.confirmPasswordPlaceholder')}
            disabled={loading}
            onkeydown={handleKeydown}
            onblur={validateConfirmPassword}
            oninput={onConfirmInput}
            autocomplete="new-password"
          />
          <button
            type="button"
            class="absolute right-2 top-1/2 -translate-y-1/2 th-text-tertiary hover:th-text-primary transition-colors"
            onclick={() => showConfirmPassword = !showConfirmPassword}
            aria-label={showConfirmPassword ? t('common.hidePassword') : t('common.showPassword')}
          >
            {#if showConfirmPassword}
              <EyeOff class="w-4 h-4" />
            {:else}
              <Eye class="w-4 h-4" />
            {/if}
          </button>
        </div>
        {#if errors.confirmPassword}
          <p class="th-color-danger text-xs mt-1">{errors.confirmPassword}</p>
        {/if}
      </div>

      <!-- Browser Capabilities -->
      <div class="border th-border rounded-lg p-4 space-y-3">
        <h3 class="text-sm font-semibold th-text-primary">{t('setup.capabilities')}</h3>
        <div class="space-y-2">
          <div class="flex items-center gap-2 text-sm">
            <span class="w-2.5 h-2.5 rounded-full {capabilities.llhls ? 'bg-green-500' : 'bg-gray-500'}"></span>
            <span class="th-text-secondary">LL-HLS</span>
            <span class="th-text-tertiary text-xs ml-auto">
              {#if capabilities.llhls}
                <span class="text-green-500">{t('setup.supported')}</span>
              {:else}
                {t('setup.notSupported')}
              {/if}
            </span>
          </div>
          <div class="flex items-center gap-2 text-sm">
            <span class="w-2.5 h-2.5 rounded-full {capabilities.webrtc ? 'bg-green-500' : 'bg-gray-500'}"></span>
            <span class="th-text-secondary">WebRTC</span>
            <span class="th-text-tertiary text-xs ml-auto">
              {#if capabilities.webrtc}
                <span class="text-green-500">{t('setup.supported')}</span>
              {:else}
                {t('setup.notSupported')}
              {/if}
            </span>
          </div>
          <div class="flex items-center gap-2 text-sm">
            <span class="w-2.5 h-2.5 rounded-full {capabilities.flv ? 'bg-green-500' : 'bg-gray-500'}"></span>
            <span class="th-text-secondary">HTTP-FLV</span>
            <span class="th-text-tertiary text-xs ml-auto">
              {#if capabilities.flv}
                <span class="text-green-500">{t('setup.supported')}</span>
              {:else}
                {t('setup.notSupported')}
              {/if}
            </span>
          </div>
          <div class="flex items-center gap-2 text-sm">
            <span class="w-2.5 h-2.5 rounded-full {capabilities.hls ? 'bg-green-500' : 'bg-gray-500'}"></span>
            <span class="th-text-secondary">HLS</span>
            <span class="th-text-tertiary text-xs ml-auto">
              {#if capabilities.hls}
                <span class="text-green-500">{t('setup.supported')}</span>
              {:else}
                {t('setup.notSupported')}
              {/if}
            </span>
          </div>
        </div>
        <p class="text-xs th-text-tertiary">{t('setup.bestProtocol', { protocol: bestProtocol === 'llhls' ? 'LL-HLS' : bestProtocol === 'webrtc' ? 'WebRTC' : bestProtocol === 'flv' ? 'HTTP-FLV' : 'HLS' })}</p>
      </div>

      <!-- Optional: Language -->
      <div>
        <label for="setup-language" class="input-label">{t('setup.language')}</label>
        <select
          id="setup-language"
          class="input"
          value={language}
          onchange={handleLanguageChange}
          disabled={loading}
        >
          <option value="en">{t('lang.en')}</option>
          <option value="zh">{t('lang.zh')}</option>
        </select>
      </div>

      <!-- Submit -->
      <button type="submit" class="btn btn-primary w-full" disabled={loading}>
        {#if loading}
          <span class="spinner mr-2"></span>
          {t('setup.submitting')}
        {:else}
          {t('setup.submit')}
        {/if}
      </button>
    </form>
    {/if}

    <div class="mt-6 text-center text-sm th-text-tertiary">
      <p class="border-t th-border pt-4">{t('setup.secureNote')}</p>
    </div>
  </div>
</div>

{#if folderPickerOpen}
  <div class="fixed inset-0 z-[60] flex items-center justify-center bg-black/60 p-4" role="presentation" onclick={() => { if (!folderPickerLoading) folderPickerOpen = false; }}>
    <!-- svelte-ignore a11y_click_events_have_key_events a11y_interactive_supports_focus -->
    <div class="card w-full max-w-xl border th-border p-5 shadow-2xl" role="dialog" aria-modal="true" aria-labelledby="folder-picker-title" tabindex="-1" onclick={(e) => e.stopPropagation()}>
      <div class="flex items-center justify-between mb-4">
        <h2 id="folder-picker-title" class="text-lg font-semibold th-text-primary">{t('setup.folderPickerTitle')}</h2>
        <button type="button" class="btn btn-ghost btn-sm" onclick={() => folderPickerOpen = false} aria-label={t('common.close')}>
          <X size={18} />
        </button>
      </div>

      <div class="flex items-center gap-2 mb-3">
        <button type="button" class="btn btn-secondary btn-sm" disabled={!folderPickerParent || folderPickerLoading} onclick={() => browseFolder(folderPickerParent)}>
          <ArrowUp size={15} class="mr-1" />
          {t('setup.folderPickerUp')}
        </button>
        <div class="flex-1 min-w-0 rounded-lg border th-border px-3 py-2 text-xs font-mono th-text-secondary truncate" title={folderPickerPath}>
          {folderPickerPath || t('setup.folderPickerLoading')}
        </div>
        <button type="button" class="btn btn-primary btn-sm shrink-0" disabled={!folderPickerPath || folderPickerLoading} onclick={() => chooseFolder(folderPickerPath)}>
          <Check size={15} class="mr-1" />
          {t('setup.folderPickerChoose')}
        </button>
      </div>

      {#if folderPickerError}
        <div class="mb-3 rounded-lg border th-border-danger p-3 text-sm th-color-danger">{folderPickerError}</div>
      {/if}

      <div class="h-64 overflow-y-auto rounded-lg border th-border">
        {#if folderPickerLoading}
          <div class="p-8 text-center th-text-tertiary">{t('setup.folderPickerLoading')}</div>
        {:else if folderEntries.length === 0}
          <div class="p-8 text-center th-text-tertiary">{t('setup.folderPickerEmpty')}</div>
        {:else}
          {#each folderEntries as entry (entry.path)}
            <button type="button" class="flex w-full items-center gap-3 border-b th-border px-4 py-3 text-left last:border-b-0 hover:th-bg-hover" onclick={() => browseFolder(entry.path)}>
              <FolderOpen size={17} class="th-text-tertiary shrink-0" />
              <span class="truncate th-text-primary">{entry.name}</span>
            </button>
          {/each}
        {/if}
      </div>

      <p class="mt-3 text-xs th-text-tertiary">{t('setup.folderPickerHint')}</p>
    </div>
  </div>
{/if}

<style>
  .setup-logo-wrap {
    width: 100%;
    height: 7rem;
    overflow: hidden;
    border-radius: 0.75rem;
    background: #ffffff;
    margin-bottom: 1rem;
  }

  .setup-logo {
    display: block;
    width: 100%;
    height: 100%;
    object-fit: cover;
    object-position: center;
  }
</style>
