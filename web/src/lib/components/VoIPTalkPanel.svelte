<script lang="ts">
  import { onMount } from 'svelte';
  import { VoIPTalk } from '$lib/voipTalk';
  import VoIPDTMFPad from './VoIPDTMFPad.svelte';
  import type { VoIPMediaSecurity } from '$lib/api/settings';
  import { t } from '$lib/i18n';

  let { user, security = '' }: { user: string; security?: VoIPMediaSecurity } = $props();
  let audio: HTMLAudioElement;
  let client = $state<VoIPTalk | undefined>(undefined);
  let state = $state('idle');
  let error = $state('');
  let muted = $state(false);
  let blocked = $state(false);
  let dtmfAvailable = $state(false);

  onMount(() => {
    client = new VoIPTalk(audio, next => {
      state = next;
      if (next === 'idle') { muted = false; dtmfAvailable = false; }
    }, message => { error = message; }, () => { blocked = true; }, available => { dtmfAvailable = available; });
    return () => { void client?.stop(); };
  });

  function call() { error = ''; blocked = false; void client?.start(user, security); }
  function mute() { muted = !muted; client?.mute(muted); }
</script>

<audio bind:this={audio} autoplay></audio>
{#if state === 'idle'}
  <button class="btn btn-primary min-h-10 w-full justify-center whitespace-nowrap px-4" type="button" onclick={call}>
    <span aria-hidden="true" class="mr-1.5">☎</span>{t('settings.voip.callTerminal')} <span class="ml-1 font-semibold">{user}</span>
  </button>
{:else}
  <div class="rounded-lg border th-border th-bg-secondary p-3" aria-label={t('settings.voip.callControls')}>
    <div class="flex flex-wrap items-center justify-between gap-2">
      <div class="flex items-center gap-2 text-sm font-medium th-text-primary" role="status">
        <span class={`h-2 w-2 rounded-full ${state === 'established' ? 'bg-emerald-500' : 'animate-pulse bg-amber-500'}`}></span>
        {t(`settings.voip.${state}`)} <span class="th-text-secondary">· {user}</span>
      </div>
      <div class="flex items-center gap-2">
        {#if state === 'established'}
          <button type="button" class="btn btn-ghost text-sm" onclick={mute}>{t(`settings.voip.${muted ? 'unmute' : 'mute'}`)}</button>
        {/if}
        <button type="button" class="btn btn-ghost text-sm th-color-danger" onclick={() => client?.stop()}>{t('settings.voip.hangup')}</button>
      </div>
    </div>
    {#if state === 'established' && dtmfAvailable}
      <div class="mt-3 border-t th-border pt-3"><VoIPDTMFPad callId={client?.activeCallId || ''} /></div>
    {/if}
  </div>
{/if}

{#if blocked}
  <button class="mt-2 text-xs underline th-text-secondary" type="button" onclick={() => audio.play().then(() => { blocked = false; })}>{t('settings.voip.playAudio')}</button>
{/if}
{#if error}
  <p role="alert" class="mt-2 text-xs th-color-danger">{t(`settings.voip.errors.${error}`) === `settings.voip.errors.${error}` ? error : t(`settings.voip.errors.${error}`)}</p>
{/if}
