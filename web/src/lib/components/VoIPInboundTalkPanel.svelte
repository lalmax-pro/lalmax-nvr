<script lang="ts">
  import { onMount } from 'svelte';
  import { claimVoIPTalk, detachVoIPTalk } from '$lib/api/settings';
  import { VoIPTalk } from '$lib/voipTalk';
  import VoIPDTMFPad from './VoIPDTMFPad.svelte';
  import { t } from '$lib/i18n';

  let { callId, browserReady = false, dtmfAvailable = false } = $props<{ callId: string; browserReady?: boolean; dtmfAvailable?: boolean }>();
  let audio: HTMLAudioElement;
  let client = $state<VoIPTalk | undefined>(undefined);
  let state = $state('idle');
  let error = $state('');
  let muted = $state(false);
  let blocked = $state(false);
  let claiming = $state(false);
  let alive = true;
  let attempt = 0;

  onMount(() => {
    client = new VoIPTalk(
      audio,
      (value) => {
        state = value;
        if (value === 'idle') muted = false;
      },
      (value) => (error = value),
      () => (blocked = true),
    );
    return () => {
      alive = false;
      attempt++;
      void client?.stop();
    };
  });

  async function join() {
    const currentAttempt = ++attempt;
    claiming = true;
    error = '';
    blocked = false;
    try {
      const { talk_token: token } = await claimVoIPTalk(callId);
      if (!alive || currentAttempt !== attempt) {
        await detachVoIPTalk(callId, token).catch(() => {});
        return;
      }
      await client?.joinInbound(callId, token);
    } catch (cause) {
      if (alive && currentAttempt === attempt) error = cause instanceof Error ? cause.message : String(cause);
    } finally {
      if (currentAttempt === attempt) claiming = false;
    }
  }

  function mute() {
    muted = !muted;
    client?.mute(muted);
  }
</script>

<div class="flex flex-wrap items-center gap-2">
  <audio bind:this={audio} autoplay></audio>
  {#if state === 'idle'}
    <button class="btn btn-primary text-sm" type="button" onclick={join} disabled={claiming || browserReady}>
      {claiming ? t('settings.voip.preparing') : browserReady ? t('settings.voip.talkInUse') : t('settings.voip.joinTalk')}
    </button>
  {:else}
    <span role="status">{t(`settings.voip.${state}`)}</span>
    {#if state === 'established'}
      <button type="button" class="btn btn-ghost text-sm" onclick={mute}>{t(`settings.voip.${muted ? 'unmute' : 'mute'}`)}</button>
    {/if}
    <button type="button" class="btn btn-ghost text-sm" onclick={() => client?.stop()}>{t('settings.voip.leaveTalk')}</button>
  {/if}
  {#if state === 'established' && dtmfAvailable}<VoIPDTMFPad callId={callId} />{/if}
  {#if blocked}
    <button class="btn btn-ghost text-sm" type="button" onclick={() => audio.play().then(() => (blocked = false)).catch(() => {})}>{t('settings.voip.playAudio')}</button>
  {/if}
  {#if error}
    <p role="alert" class="text-sm th-color-danger">{t(`settings.voip.errors.${error}`) === `settings.voip.errors.${error}` ? error : t(`settings.voip.errors.${error}`)}</p>
  {/if}
</div>
