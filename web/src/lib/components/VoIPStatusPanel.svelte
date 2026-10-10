<script lang="ts">
  import { onMount } from 'svelte';
  import VoIPTalkPanel from './VoIPTalkPanel.svelte';
  import VoIPInboundTalkPanel from './VoIPInboundTalkPanel.svelte';
  import VoIPCallHistory from './VoIPCallHistory.svelte';
  import { answerVoIPCall, getVoIPStatus, hangupVoIPCall, rejectVoIPCall } from '$lib/api';
  import type { VoIPStatus } from '$lib/api';
  import { t } from '$lib/i18n';

  let status = $state<VoIPStatus>({ enabled: false, incoming_calls_need_answer: false, upstream_registration: { configured: false, state: 'disabled' }, endpoints: [], calls: [] });
  let error = $state('');
  let ending = $state('');
  let busy = $state(false);
  let pbxExtension = $state('');
  let mediaSecurity = $state<'' | 'rtp' | 'sdes' | 'dtls'>('');
  let stopped = false;
  let controller: AbortController | undefined;

  const pbx = $derived(status.upstream_registration ?? { configured: false, state: 'disabled' });
  const pbxReady = $derived(pbx.configured && pbx.state === 'registered');

  async function refresh() {
    if (busy || stopped) return;
    busy = true;
    controller = new AbortController();
    try {
      const next = await getVoIPStatus(controller.signal);
      if (!stopped) { status = next; error = ''; }
    } catch (e) {
      if (!stopped && !(e instanceof DOMException && e.name === 'AbortError')) error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }

  async function hangup(id: string) {
    ending = id;
    error = '';
    try { await hangupVoIPCall(id); await refresh(); }
    catch (e) { error = e instanceof Error ? e.message : String(e); }
    finally { ending = ''; }
  }

  async function answerOrReject(id: string, action: 'answer' | 'reject') {
    ending = id;
    error = '';
    try {
      if (action === 'answer') await answerVoIPCall(id); else await rejectVoIPCall(id);
      await refresh();
    } catch (e) { error = e instanceof Error ? e.message : String(e); }
    finally { ending = ''; }
  }

  onMount(() => {
    void refresh();
    const timer = setInterval(() => void refresh(), 5000);
    return () => { stopped = true; controller?.abort(); clearInterval(timer); };
  });
</script>

<div class="space-y-7">
  {#if error}
    <p class="rounded-lg border border-red-500/30 px-4 py-3 text-sm th-color-danger" role="alert">{error}</p>
  {/if}

  <section class="overflow-hidden rounded-2xl border th-border th-bg-primary shadow-sm" aria-label={t('settings.voip.pbxDialer')}>
    <div class="border-b th-border px-5 py-5 sm:px-7">
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div>
          <p class="text-xs font-semibold uppercase tracking-[0.12em] th-text-tertiary">{t('settings.voip.webCalling')}</p>
          <h2 class="mt-1 text-xl font-semibold th-text-primary">{t('settings.voip.pbxDialer')}</h2>
          <p class="mt-1 max-w-2xl text-sm th-text-secondary">{t('settings.voip.pbxDialerHint')}</p>
        </div>
        {#if pbx.configured}
          <span class={`inline-flex items-center gap-2 rounded-full px-3 py-1.5 text-xs font-medium ${pbxReady ? 'bg-emerald-500/10 text-emerald-600' : pbx.state === 'failed' ? 'bg-red-500/10 text-red-600' : 'th-bg-secondary th-text-secondary'}`}>
            <span class={`h-1.5 w-1.5 rounded-full ${pbxReady ? 'bg-emerald-500' : pbx.state === 'failed' ? 'bg-red-500' : 'bg-current'}`}></span>
            {t(`settings.voip.pbxRegistrationState.${pbx.state}`)}
          </span>
        {:else}
          <span class="rounded-full th-bg-secondary px-3 py-1.5 text-xs th-text-secondary">{t('settings.voip.pbxRegistrationState.disabled')}</span>
        {/if}
      </div>
      {#if pbx.configured}
        <div class="mt-4 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs th-text-secondary">
          <span>{pbx.username}</span><span aria-hidden="true">·</span><span>{pbx.server}</span><span aria-hidden="true">·</span><span>{pbx.transport?.toUpperCase()}</span>
          {#if pbx.expires_at}<span class="sm:ml-auto">{t('settings.voip.expires')}: {new Date(pbx.expires_at).toLocaleTimeString()}</span>{/if}
        </div>
        {#if pbx.last_error}<p class="mt-2 text-sm th-color-danger" role="alert">{pbx.last_error}</p>{/if}
      {/if}
    </div>

    <div class="grid gap-4 px-5 py-5 sm:px-7 lg:grid-cols-[minmax(0,1fr)_190px_auto] lg:items-end">
      <div>
        <label class="input-label" for="voip-pbx-extension">{t('settings.voip.destination')}</label>
        <div class="relative mt-1">
          <span class="pointer-events-none absolute inset-y-0 left-3 flex items-center text-sm th-text-tertiary">☎</span>
          <input id="voip-pbx-extension" class="input w-full pl-9 text-lg tracking-wide" bind:value={pbxExtension} placeholder={t('settings.voip.extensionPlaceholder')} inputmode="tel" autocomplete="off" />
        </div>
      </div>
      <div>
        <label class="input-label" for="voip-media-security">{t('settings.voip.mediaSecurity')}</label>
        <select id="voip-media-security" class="input mt-1 w-full" bind:value={mediaSecurity}>
          <option value="">{t('settings.voip.autoSecurity')}</option>
          <option value="rtp">RTP</option>
          <option value="sdes">SDES-SRTP</option>
          <option value="dtls">DTLS-SRTP</option>
        </select>
      </div>
      <div class="lg:pb-0.5">
        {#if pbxReady && pbxExtension.trim()}
          <VoIPTalkPanel user={pbxExtension.trim()} security={mediaSecurity} />
        {:else}
          <button class="btn btn-primary w-full whitespace-nowrap" type="button" disabled>
            {t('settings.voip.callTerminal')} <span aria-hidden="true">→</span>
          </button>
        {/if}
      </div>
      {#if !pbxReady}
        <p class="text-xs th-text-secondary lg:col-span-3">{pbx.configured ? t('settings.voip.waitingForPBX') : t('settings.voip.configurePBX')}</p>
      {/if}
    </div>
  </section>

  {#if status.incoming_calls_need_answer}
    <p class="rounded-lg border border-amber-500/30 bg-amber-500/5 px-4 py-3 text-sm th-text-secondary">{t('settings.voip.manualAnswerHint')}</p>
  {/if}

  <section class="space-y-3" aria-label={t('settings.voip.activeCalls')}>
    <div class="flex items-center justify-between gap-3">
      <div>
        <h3 class="font-semibold th-text-primary">{t('settings.voip.activeCalls')}</h3>
        <p class="mt-0.5 text-xs th-text-secondary">{t('settings.voip.activeCallsHint')}</p>
      </div>
      <button type="button" class="btn btn-ghost text-sm" onclick={refresh} disabled={busy}>{t('settings.voip.refresh')}</button>
    </div>
    {#if status.calls.length === 0}
      <div class="rounded-xl border border-dashed th-border px-4 py-8 text-center text-sm th-text-secondary">{t('settings.voip.emptyCalls')}</div>
    {:else}
      <div class="grid gap-3 lg:grid-cols-2">
        {#each status.calls as call (call.call_id)}
          <article class="rounded-xl border th-border th-bg-primary p-4 shadow-sm">
            <div class="flex flex-wrap items-start justify-between gap-3">
              <div class="min-w-0">
                <div class="flex flex-wrap items-center gap-2">
                  <span class={`rounded-full px-2.5 py-1 text-xs font-medium ${call.state === 'established' ? 'bg-emerald-500/10 text-emerald-600' : 'th-bg-secondary th-text-secondary'}`}>{t(`settings.voip.${call.held ? 'held' : call.incoming_pending ? 'ringing' : call.state}`)}</span>
                  <span class="text-xs th-text-tertiary">{call.direction === 'inbound' ? t('settings.voip.inbound') : t('settings.voip.outbound')}</span>
                </div>
                <p class="mt-2 truncate font-medium th-text-primary">{#if call.stream_id}<a class="text-blue-500 hover:underline" aria-label={`${call.from_user} → ${call.to_user}`} href={`#/streams/${encodeURIComponent(call.stream_id)}`}>{call.from_user} <span class="th-text-tertiary">→</span> {call.to_user}</a>{:else}{call.from_user} <span class="th-text-tertiary">→</span> {call.to_user}{/if}</p>
                <p class="mt-1 text-xs th-text-secondary">{call.transport.toUpperCase()} · {call.remote_addr}</p>
              </div>
              <span class="text-sm tabular-nums th-text-secondary">{call.duration_seconds}{t('settings.voip.seconds')}</span>
            </div>
            <div class="mt-3 flex flex-wrap items-center justify-between gap-3 border-t th-border pt-3">
              <div class="text-xs th-text-secondary">
                {#if call.audio_codec || call.video_codec}<span>{call.audio_codec || '—'} / {call.video_codec || '—'}</span>{/if}
                {#if call.browser_ready}<span class="ml-2">{t('settings.voip.talkInUse')}</span>{/if}
                {#if call.failure_reason}<span class="block mt-1 th-color-danger">{call.failure_reason}</span>{/if}
              </div>
              <div class="flex flex-wrap items-center gap-2">
                {#if call.incoming_pending}
                  <button class="btn btn-primary text-sm" type="button" onclick={() => answerOrReject(call.call_id, 'answer')} disabled={ending !== ''}>{t('settings.voip.answer')}</button>
                  <button class="btn btn-ghost text-sm th-color-danger" type="button" onclick={() => answerOrReject(call.call_id, 'reject')} disabled={ending !== ''}>{t('settings.voip.reject')}</button>
                {:else}
                  {#if call.direction === 'inbound' && call.talk_available}<VoIPInboundTalkPanel callId={call.call_id} browserReady={call.browser_ready} dtmfAvailable={call.dtmf_available} />{/if}
                  {#if call.stream_id}<a class="btn btn-ghost text-sm" href={`#/streams/${encodeURIComponent(call.stream_id)}`}>{t('settings.voip.viewStream')}</a>{/if}
                  <button class="btn btn-ghost text-sm th-color-danger" type="button" aria-label={`${t('settings.voip.hangup')} ${call.call_id}`} onclick={() => hangup(call.call_id)} disabled={ending !== '' || call.state === 'ended' || call.state === 'failed'}>{t('settings.voip.hangup')}</button>
                {/if}
              </div>
            </div>
          </article>
        {/each}
      </div>
    {/if}
  </section>

  <section class="space-y-3" aria-label={t('settings.voip.registeredEndpoints')}>
    <div>
      <h3 class="font-semibold th-text-primary">{t('settings.voip.registeredEndpoints')}</h3>
      <p class="mt-0.5 text-xs th-text-secondary">{t('settings.voip.registeredEndpointsHint')}</p>
    </div>
    {#if status.endpoints.length === 0}
      <div class="rounded-xl border border-dashed th-border px-4 py-7 text-sm th-text-secondary">{t('settings.voip.emptyEndpoints')}</div>
    {:else}
      <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {#each status.endpoints as endpoint (endpoint.user)}
          <article class="rounded-xl border th-border th-bg-primary p-4">
            <div class="flex items-center justify-between gap-3">
              <span class="font-medium th-text-primary">{endpoint.user}</span>
              <span class="rounded-full bg-emerald-500/10 px-2.5 py-1 text-xs text-emerald-600">{t('settings.voip.online')}</span>
            </div>
            <p class="mt-2 truncate text-xs th-text-secondary" title={endpoint.user_agent}>{endpoint.user_agent || endpoint.contact}</p>
            <p class="mt-1 text-xs th-text-tertiary">{endpoint.transport.toUpperCase()} · {endpoint.remote_addr} · {t('settings.voip.expires')} {new Date(endpoint.expires_at).toLocaleTimeString()}</p>
            <div class="mt-3 border-t th-border pt-3"><VoIPTalkPanel user={endpoint.user} /></div>
          </article>
        {/each}
      </div>
    {/if}
  </section>

  <VoIPCallHistory />
</div>
