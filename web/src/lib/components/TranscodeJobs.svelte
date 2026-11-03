<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { t } from '$lib/i18n';
  import FlvPlayer from '../../components/FlvPlayer.svelte';
  let preview = $state<TranscodeJob | null>(null);
  let previewLease = $state<TranscodeDemandLease | null>(null);
  let previewEpoch = 0;
  let renewalTimer: ReturnType<typeof setInterval> | undefined;
  async function closePreview() {
    previewEpoch++;
    if (renewalTimer) clearInterval(renewalTimer);
    renewalTimer = undefined;
    const lease = previewLease;
    previewLease = null;
    preview = null;
    if (lease) {
      try { await releaseTranscodeDemandLease(lease.token); }
      catch { /* lease expiry or shutdown already releases the media owner */ }
    }
  }
  async function openPreview(job: TranscodeJob) {
    await closePreview();
    const epoch = previewEpoch;
    if (job.run_policy === 'on_demand') {
      const response = await acquireTranscodeDemandLease(job.id, job.generation, 20000);
      if (controller.signal.aborted || epoch !== previewEpoch) {
        await releaseTranscodeDemandLease(response.lease.token).catch(() => undefined);
        return;
      }
      previewLease = response.lease;
      renewalTimer = setInterval(async () => {
        const lease = previewLease;
        if (!lease || controller.signal.aborted) return;
        try {
          const updated = await renewTranscodeDemandLease(lease.token);
          if (previewLease?.token === updated.token) previewLease = updated;
        } catch {
          await closePreview();
          if (!controller.signal.aborted) error = '派生流租约已失效，请重新播放';
        }
      }, 30000);
    }
    preview = job;
  }
  import { listTranscodeJobs, listTranscodeProfiles, createTranscodeProfile, updateTranscodeProfile, deleteTranscodeProfile, probeSavedTranscodeProfile, saveTranscodeJob, transcodeJobAction, derivedRecordingAction, acquireTranscodeDemandLease, renewTranscodeDemandLease, releaseTranscodeDemandLease } from '$lib/api/transcoding';
  import type { TranscodeProfile, TranscodeJob, TranscodeVariant, TranscodeDemandLease } from '$lib/api/transcoding';
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
  let draftPolicy = $state<'manual' | 'always' | 'on_demand'>('manual');
  let profileEditor = $state<TranscodeProfile | null>(null);
  let editingExistingProfile = $state(false);
  let defaultProfileID = $derived(profileBackend === 'gstreamer' ? 'gst_h264_720p' : 'h264_720p');
  const controller = new AbortController();
  async function load() {
    const [p, j] = await Promise.all([listTranscodeProfiles(controller.signal), listTranscodeJobs(controller.signal)]);
    if (!controller.signal.aborted) {
      profiles = p.profiles; jobs = j.jobs; variants = j.variants ?? [];
      if (preview && !variants.some(v => v.job_id === preview!.id && v.state === 'running')) void closePreview();
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
  function editProfile(profile?: TranscodeProfile) {
    editingExistingProfile = Boolean(profile);
    const backend = profile?.backend ?? profileBackend;
    profileEditor = profile
      ? JSON.parse(JSON.stringify(profile)) as TranscodeProfile
      : {
          id: '', name: '', revision: 0, backend,
          video: { codec: 'h264', encoder: backend === 'gstreamer' ? 'x264enc' : 'libx264', max_width: 1280, max_height: 720, fps: 15, bitrate_kbps: 1500, gop_frames: 30 },
          audio: { codec: 'aac', sample_rate: 48000, channels: 1, bitrate_kbps: 64 },
        };
  }
  function updateVideoCodec(codec: string) {
    if (!profileEditor) return;
    if (codec === 'copy') {
      profileEditor.video = { codec: 'copy', encoder: '', max_width: 0, max_height: 0, fps: 0, bitrate_kbps: 0, gop_frames: 0 };
    } else {
      profileEditor.video = { codec: 'h264', encoder: profileEditor.backend === 'gstreamer' ? 'x264enc' : 'libx264', max_width: 1280, max_height: 720, fps: 15, bitrate_kbps: 1500, gop_frames: 30 };
    }
  }
  function updateAudioCodec(codec: string) {
    if (!profileEditor) return;
    profileEditor.audio = codec === 'aac'
      ? { codec: 'aac', sample_rate: 48000, channels: 1, bitrate_kbps: 64 }
      : codec === 'g711a'
      ? { codec: 'g711a', sample_rate: 8000, channels: 1, bitrate_kbps: 64 }
      : { codec, sample_rate: 0, channels: 0, bitrate_kbps: 0 };
  }
  function updateBackend(backend: string) {
    if (!profileEditor || editingExistingProfile) return;
    profileEditor.backend = backend;
    if (profileEditor.video.codec === 'h264') {
      profileEditor.video.encoder = backend === 'gstreamer' ? 'x264enc' : 'libx264';
    }
  }
  async function saveProfileForm() {
    if (!profileEditor) return;
    if (editingExistingProfile) await updateTranscodeProfile(profileEditor, controller.signal);
    else await createTranscodeProfile(profileEditor, controller.signal);
    profileEditor = null;
  }
  async function probe(id: string) {
    const result = await probeSavedTranscodeProfile(id, controller.signal);
    message = result.can_enable && !result.stale ? t('transcode.ready') : t('transcode.notReady');
    if (!result.can_enable) message += ': ' + result.checks.filter(c => c.status !== 'passed').map(c => `${c.name}: ${c.detail}`).join('; ');
  }
  async function draft() {
    const profile = profiles.find(p => p.id === profileID);
    if (!profile) throw new Error(t('transcode.selectProfile'));
    await saveTranscodeJob({ id: jobID, source: {engine_id: 'embedded', stream_id: sourceID, app_name: appName}, profile_id: profile.id, profile_revision: profile.revision, enabled: false, run_policy: draftPolicy, generation: 0 }, controller.signal);
    jobID = ''; sourceID = '';
  }
  onMount(() => { void load().catch(e => { if (!controller.signal.aborted) error = e instanceof Error ? e.message : t('transcode.error'); }).finally(() => loading = false); });
  onDestroy(() => { controller.abort(); void closePreview(); });
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
        <button class="btn btn-secondary" type="button" onclick={() => editProfile()}>新建自定义模板</button>
      </div>
      {#if profileEditor}
        <form class="p-4 border th-border rounded-lg space-y-3" onsubmit={e => { e.preventDefault(); void act(saveProfileForm); }}>
          <h4 class="font-medium">转码模板{editingExistingProfile ? '编辑' : '新建'}</h4>
          <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
            <label class="space-y-1"><span class="input-label">模板 ID</span><input class="input w-full" required disabled={editingExistingProfile} pattern="[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}" bind:value={profileEditor.id} /></label>
            <label class="space-y-1"><span class="input-label">模板名称</span><input class="input w-full" maxlength="128" bind:value={profileEditor.name} /></label>
            <label class="space-y-1"><span class="input-label">编码后端</span><select class="input w-full" value={profileEditor.backend} disabled={editingExistingProfile} onchange={e => updateBackend(e.currentTarget.value)}><option value="ffmpeg">FFmpeg</option><option value="gstreamer">GStreamer</option></select></label>
            <label class="space-y-1"><span class="input-label">视频模式</span><select class="input w-full" value={profileEditor.video.codec} onchange={e => updateVideoCodec(e.currentTarget.value)}><option value="h264">H.264 编码</option><option value="copy">H.264 直接复制</option></select></label>
            {#if profileEditor.video.codec === 'h264'}
              {#if profileEditor.backend === 'ffmpeg'}
                <label class="space-y-1"><span class="input-label">视频编码器</span><select class="input w-full" bind:value={profileEditor.video.encoder}><option value="libx264">libx264（软件）</option><option value="h264_nvenc">NVIDIA NVENC（需实机能力检测）</option></select></label>
              {/if}
              <label class="space-y-1"><span class="input-label">最大宽度</span><input class="input w-full" type="number" min="2" max="3840" step="2" bind:value={profileEditor.video.max_width} /></label>
              <label class="space-y-1"><span class="input-label">最大高度</span><input class="input w-full" type="number" min="2" max="2160" step="2" bind:value={profileEditor.video.max_height} /></label>
              <label class="space-y-1"><span class="input-label">帧率 (fps)</span><input class="input w-full" type="number" min="1" max="60" bind:value={profileEditor.video.fps} /></label>
              <label class="space-y-1"><span class="input-label">视频码率 (kbps)</span><input class="input w-full" type="number" min="64" max="20000" bind:value={profileEditor.video.bitrate_kbps} /></label>
              <label class="space-y-1"><span class="input-label">GOP 帧数</span><input class="input w-full" type="number" min="1" max="600" bind:value={profileEditor.video.gop_frames} /></label>
            {/if}
            <label class="space-y-1"><span class="input-label">音频模式</span><select class="input w-full" value={profileEditor.audio.codec} onchange={e => updateAudioCodec(e.currentTarget.value)}><option value="aac">AAC 编码</option><option value="g711a">G.711 A-law（WebRTC 有声）</option><option value="copy">AAC 复制</option><option value="none">无音频</option></select></label>
            {#if profileEditor.audio.codec === 'aac'}
              <label class="space-y-1"><span class="input-label">采样率</span><select class="input w-full" bind:value={profileEditor.audio.sample_rate}><option value={44100}>44100</option><option value={48000}>48000</option></select></label>
              <label class="space-y-1"><span class="input-label">声道数</span><input class="input w-full" type="number" min="1" max="2" bind:value={profileEditor.audio.channels} /></label>
              <label class="space-y-1"><span class="input-label">音频码率 (kbps)</span><input class="input w-full" type="number" min="32" max="320" bind:value={profileEditor.audio.bitrate_kbps} /></label>
            {/if}
          </div>
          <div class="flex gap-2"><button class="btn btn-primary" type="submit">{editingExistingProfile ? '保存模板' : '创建模板'}</button><button class="btn btn-secondary" type="button" onclick={() => profileEditor = null}>取消</button></div>
          <p class="text-xs th-text-secondary">运行中的任务引用模板时，后端会拒绝不安全的修订。保存后需重新执行能力检测。</p>
        </form>
      {/if}
      <ul class="divide-y th-border">
        {#each profiles as profile}<li class="py-2 flex flex-wrap items-center justify-between gap-2">
          <span class="text-sm th-text-primary">{profile.name || profile.id} · {profile.backend} · v{profile.revision}</span>
          <div class="flex flex-wrap gap-2">
            <button class="btn btn-secondary" type="button" onclick={() => editProfile(profile)}>编辑</button>
            <button class="btn btn-secondary" onclick={() => act(() => probe(profile.id))}>{t('transcode.probe')}</button>
            <button class="btn btn-secondary" type="button" onclick={() => { if (window.confirm('删除模板 ' + profile.id + '？')) void act(() => deleteTranscodeProfile(profile, controller.signal)); }}>删除</button>
          </div>
        </li>{/each}
      </ul>
      <form class="grid grid-cols-1 md:grid-cols-2 gap-3" onsubmit={e => { e.preventDefault(); void act(draft); }}>
        <label class="space-y-1"><span class="input-label">{t('transcode.jobID')}</span><input class="input w-full" required pattern="[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}" bind:value={jobID} /></label>
        <label class="space-y-1"><span class="input-label">{t('transcode.sourceID')}</span><input class="input w-full" required bind:value={sourceID} /></label>
        <label class="space-y-1"><span class="input-label">{t('transcode.appName')}</span><input class="input w-full" required bind:value={appName} /></label>
        <label class="space-y-1"><span class="input-label">{t('transcode.profile')}</span><select class="input w-full" required bind:value={profileID}><option value="">{t('transcode.selectProfile')}</option>{#each profiles as p}<option value={p.id}>{p.name || p.id}</option>{/each}</select></label>
        <label class="space-y-1"><span class="input-label">任务运行策略</span><select class="input w-full" bind:value={draftPolicy}><option value="manual">手动</option><option value="always">常驻自动恢复</option><option value="on_demand">按需（消费者租约）</option></select></label>
        <button type="submit" class="btn btn-primary">{t('transcode.createDraft')}</button>
      </form>
      {#if jobs.length === 0}<p class="text-sm th-text-secondary">{t('transcode.noJobs')}</p>{/if}
      {#if preview}
        <div class="space-y-2">
          <div class="flex justify-between gap-2"><span class="text-sm th-text-primary">{preview.id}</span><button class="btn btn-secondary" onclick={() => void closePreview()}>{t('transcode.closePreview')}</button></div>
          <div class="aspect-video bg-neutral-900 rounded overflow-hidden">
            {#key preview.id}<FlvPlayer allowSnapshotFallback={false} cameraId={preview.source.stream_id} cameraName={preview.id} streamUrl={`/api/streams/${encodeURIComponent(preview.source.stream_id)}/variants/${encodeURIComponent(preview.id)}/stream.flv`} />{/key}
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
            <button class="btn btn-secondary" disabled={busy || (job.run_policy === 'on_demand' ? !job.enabled : runtime?.state !== 'running')} onclick={() => act(() => openPreview(job))}>{t('transcode.preview')}</button>
            <button class="btn btn-secondary" disabled={runtime?.state !== 'running' || busy} onclick={() => act(() => derivedRecordingAction(job, 'start', controller.signal))}>开始派生录像</button>
            <button class="btn btn-secondary" disabled={runtime?.state !== 'running' || busy} onclick={() => act(() => derivedRecordingAction(job, 'stop', controller.signal))}>停止派生录像</button>
            <button class="btn btn-secondary" disabled={!job.enabled || busy || job.run_policy === 'on_demand'} onclick={() => act(() => transcodeJobAction(job, 'start', controller.signal))}>{t('transcode.start')}</button>
            {#if job.run_policy === 'on_demand' && job.stopped_by_user}
              <button class="btn btn-secondary" type="button" disabled={!job.enabled || busy} onclick={() => act(() => transcodeJobAction(job, 'resume', controller.signal))}>恢复按需播放</button>
            {/if}
            <button class="btn btn-secondary" onclick={() => act(() => transcodeJobAction(job, 'stop', controller.signal))}>{t('transcode.stop')}</button>
            <button class="btn btn-secondary" onclick={() => { if (window.confirm(t('transcode.deleteConfirm'))) void act(() => transcodeJobAction(job, 'delete', controller.signal)); }}>{t('transcode.delete')}</button>
          </div>
        </li>{/each}
      </ul>
    </fieldset>
  {/if}
</section>
