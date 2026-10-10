<script lang="ts">
  import { onMount } from 'svelte';
  import { getVoIPSettings, updateVoIPSettings } from '$lib/api';
  import type { VoIPConfig } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import VoIPSettings from '$lib/components/VoIPSettings.svelte';

  let config = $state<VoIPConfig | null>(null);
  let loading = $state(true);
  let saving = $state(false);
  let error = $state('');
  let originalSnapshot = '';
  let dirty = $derived(!!config && JSON.stringify(config) !== originalSnapshot);

  async function load() {
    loading = true;
    error = '';
    try {
      config = await getVoIPSettings();
      originalSnapshot = JSON.stringify(config);
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.failedLoadSettings');
    } finally {
      loading = false;
    }
  }

  async function save() {
    if (!config || !dirty || saving) return;
    saving = true;
    error = '';
    try {
      await updateVoIPSettings(config);
      config = await getVoIPSettings();
      originalSnapshot = JSON.stringify(config);
      showToast(t('settings.saved'), 'success');
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.failedSaveSettings');
      showToast(error, 'error');
    } finally {
      saving = false;
    }
  }

  onMount(() => { void load(); });
</script>

<div class="p-6 max-w-6xl mx-auto space-y-5">
  <div class="flex flex-wrap items-center justify-between gap-3">
    <div>
      <h1 class="text-2xl font-bold th-text-primary">{t('settings.voip.title')}</h1>
      <p class="text-sm th-text-secondary mt-1">{t('settings.voip.description')}</p>
    </div>
    <button class="btn btn-primary" type="button" onclick={save} disabled={!dirty || saving || loading || config?.supported === false}>
      {saving ? t('common.saving') : t('common.save')}
    </button>
  </div>

  {#if error}
    <div class="card border border-red-500/30 p-4 text-sm th-color-danger" role="alert">{error}</div>
  {/if}

  {#if loading}
    <div class="card border th-border p-8 text-sm th-text-secondary" role="status">{t('common.loading')}</div>
  {:else if config}
    <VoIPSettings bind:config />
  {/if}
</div>
