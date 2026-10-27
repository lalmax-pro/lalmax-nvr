<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { t } from '$lib/i18n';
  import FlvPlayer from '../../components/FlvPlayer.svelte';
  let preview = $state<TranscodeJob | null>(null);
  import { listTranscodeJobs, listTranscodeProfiles, createTranscodeProfile, probeSavedTranscodeProfile, saveTranscodeJob, transcodeJobAction } from '$lib/api/transcoding';
  import type { TranscodeProfile, TranscodeJob, TranscodeVariant } from '$lib/api/transcoding';
  let profiles = $state<TranscodeProfile[]>([]);
  let jobs = $state<TranscodeJob[]>([]);
  let variants = $state<TranscodeVariant[]>([]);
  let loading = $state(true);
  let busy = $state(false);
  let error = $state('');
  let message = $state('');
  let jobID = $state('');
  let sourceID = $state('');
  let appName = $state('live');
  let profileID = $state('');
  let profileBackend = $state<'ffmpeg' | 'gstreamer'>('ffmpeg');
  let defaultProfileID = $derived(profileBackend === 'gstreamer' ? 'gst_h264_720p' : 'h264_720p');
  const controller = new AbortController();
  async function load() {
    const [p, j] = await Promise.all([listTranscodeProfiles(controller.signal), listTranscodeJobs(controller.signal)]);
    if (!controller.signal.aborted) {
      profiles = p.profiles; jobs = j.jobs; variants = j.variants ?? [];
      if (preview && !variants.some(v => v.job_id === preview!.id && v.state === 'running')) preview = null;
    }
  }
  async function act(fn: () => Promise<unknown>) {
    if (busy) return;
    busy = true; error = ''; message = '';
    try { await fn(); if (!controller.signal.aborted) await load(); }
    catch (e) { if (!controller.signal.aborted) error = e instanceof Error ? e.message : t('transcode.error'); }
    finally { busy = false; }
  }
  async function addProfile() {
    await createTranscodeProfile({ id: defaultProfileID, name: 'H.264 720p / AAC', revision: 0, backend: profileBackend, video: { codec: 'h264', encoder: profileBackend === 'gstreamer' ? 'x264enc' : 'libx264', max_width: 1280, max_height: 720, fps: 15, bitrate_kbps: 1500, gop_frames: 30 }, audio: { codec: 'aac', sample_rate: 48000, channels: 1, bitrate_kbps: 64 } }, controller.signal);
  }
  async function probe(id: string) {
    const result = await probeSavedTranscodeProfile(id, controller.signal);
    message = result.can_enable && !result.stale ? t('transcode.ready') : t('transcode.notReady');
    if (!result.can_enable) message += ': ' + result.checks.filter(c => c.status !== 'passed').map(c => `${c.name}: ${c.detail}`).join('; ');
  }
  async function draft() {
    const profile = profiles.find(p => p.id === profileID);
    if (!profile) throw new Error(t('transcode.selectProfile'));
    await saveTranscodeJob({ id: jobID, source: {engine_id: 'embedded', stream_id: sourceID, app_name: appName}, profile_id: profile.id, profile_revision: profile.revision, enabled: false, run_policy: 'manual', generation: 0 }, controller.signal);
    jobID = ''; sourceID = '';
  }
  onMount(() => { void load().catch(e => { if (!controller.signal.aborted) error = e instanceof Error ? e.message : t('transcode.error'); }).finally(() => loading = false); });
  onDestroy(() => controller.abort());
</script>

<section class="card p-6 border th-border space-y-4" aria-label={t('transcode.jobs')}>
  <h3 class="text-lg font-semibold th-text-primary">{t('transcode.jobs')}</h3>
  <p class="text-sm th-text-secondary">{t('transcode.draftHint')}</p>
  {#if error}<p role="alert" class="th-color-danger text-sm">{error}</p>{/if}
  {#if message}<p role="status" class="text-sm th-text-secondary break-words">{message}</p>{/if}
  {#if loading}<p role="status">{t('common.loading')}</p>{:else}
    <fieldset disabled={busy} class="space-y-4">
      <div class="flex flex-wrap gap-2">
        <button class="btn btn-secondary" onclick={() => act(load)}>{t('transcode.refresh')}</button>
        <label class="flex items-center gap-2 text-sm"><span>{t('transcode.backend')}</span><select class="input" bind:value={profileBackend}><option value="ffmpeg">FFmpeg</option><option value="gstreamer">GStreamer</option></select></label>
        {#if !profiles.some(p => p.id === defaultProfileID)}<button class="btn btn-secondary" onclick={() => act(addProfile)}>{t('transcode.addProfile')}</button>{/if}
      </div>
      <ul class="divide-y th-border">
        {#each profiles as profile}<li class="py-2 flex flex-wrap items-center justify-between gap-2">
          <span class="text-sm th-text-primary">{profile.name || profile.id} · {profile.backend} · v{profile.revision}</span>
          <button class="btn btn-secondary" onclick={() => act(() => probe(profile.id))}>{t('transcode.probe')}</button>
        </li>{/each}
      </ul>
      <form class="grid grid-cols-1 md:grid-cols-2 gap-3" onsubmit={e => { e.preventDefault(); void act(draft); }}>
        <label class="space-y-1"><span class="input-label">{t('transcode.jobID')}</span><input class="input w-full" required pattern="[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}" bind:value={jobID} /></label>
        <label class="space-y-1"><span class="input-label">{t('transcode.sourceID')}</span><input class="input w-full" required bind:value={sourceID} /></label>
        <label class="space-y-1"><span class="input-label">{t('transcode.appName')}</span><input class="input w-full" required bind:value={appName} /></label>
        <label class="space-y-1"><span class="input-label">{t('transcode.profile')}</span><select class="input w-full" required bind:value={profileID}><option value="">{t('transcode.selectProfile')}</option>{#each profiles as p}<option value={p.id}>{p.name || p.id}</option>{/each}</select></label>
        <button type="submit" class="btn btn-primary">{t('transcode.createDraft')}</button>
      </form>
      {#if jobs.length === 0}<p class="text-sm th-text-secondary">{t('transcode.noJobs')}</p>{/if}
      {#if preview}
        <div class="space-y-2">
          <div class="flex justify-between gap-2"><span class="text-sm th-text-primary">{preview.id}</span><button class="btn btn-secondary" onclick={() => preview = null}>{t('transcode.closePreview')}</button></div>
          <div class="aspect-video bg-neutral-900 rounded overflow-hidden">
            {#key preview.id}<FlvPlayer cameraId={preview.source.stream_id} cameraName={preview.id} streamUrl={`/api/streams/${encodeURIComponent(preview.source.stream_id)}/variants/${encodeURIComponent(preview.id)}/stream.flv`} />{/key}
          </div>
        </div>
      {/if}
      <ul class="divide-y th-border">
        {#each jobs as job}
          {@const runtime = variants.find(v => v.job_id === job.id)}
          <li class="py-3 space-y-2">
          <p class="text-sm th-text-primary">{job.id} · {job.source.stream_id} → {job.profile_id} · {job.run_policy} · v{job.generation}</p>
          {#if runtime}<p role="status" class="text-xs th-text-secondary">{runtime.state}{runtime.error_code ? ` · ${runtime.error_code}` : ''}{#if runtime.retry_at} · {t('transcode.retryAt')}: <time datetime={runtime.retry_at}>{new Date(runtime.retry_at).toLocaleString()}</time>{/if}</p>{/if}
          <div class="flex flex-wrap gap-2">
            <button class="btn btn-secondary" onclick={() => act(() => saveTranscodeJob({...job, enabled: !job.enabled}, controller.signal))}>{job.enabled ? t('transcode.disable') : t('transcode.enable')}</button>
            <button class="btn btn-secondary" disabled={runtime?.state !== 'running' || busy} onclick={() => preview = job}>{t('transcode.preview')}</button>
            <button class="btn btn-secondary" disabled={!job.enabled || busy} onclick={() => act(() => transcodeJobAction(job, 'start', controller.signal))}>{t('transcode.start')}</button>
            <button class="btn btn-secondary" onclick={() => act(() => transcodeJobAction(job, 'stop', controller.signal))}>{t('transcode.stop')}</button>
            <button class="btn btn-secondary" onclick={() => { if (window.confirm(t('transcode.deleteConfirm'))) void act(() => transcodeJobAction(job, 'delete', controller.signal)); }}>{t('transcode.delete')}</button>
          </div>
        </li>{/each}
      </ul>
    </fieldset>
  {/if}
</section>
