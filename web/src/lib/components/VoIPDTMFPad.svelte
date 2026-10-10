<script lang="ts">
  import { sendVoIPDTMF } from '$lib/api/settings';
  import { t } from '$lib/i18n';

  let { callId } = $props<{ callId: string }>();
  let sending = $state(false);
  let error = $state('');
  const digits = ['1', '2', '3', '4', '5', '6', '7', '8', '9', '*', '0', '#'];

  async function send(digit: string) {
    if (sending) return;
    sending = true;
    error = '';
    try {
      await sendVoIPDTMF(callId, digit);
    } catch (cause) {
      error = cause instanceof Error ? cause.message : String(cause);
    } finally {
      sending = false;
    }
  }
</script>

<div class="space-y-2">
  <p class="text-xs th-text-secondary">{t('settings.voip.dtmfKeypad')}</p>
  <div class="grid w-fit grid-cols-3 gap-1" aria-label={t('settings.voip.dtmfKeypad')}>
    {#each digits as digit}
      <button
        class="btn btn-ghost h-9 min-w-10 text-sm"
        type="button"
        aria-label={`${t('settings.voip.sendDtmf')} ${digit}`}
        disabled={sending}
        onclick={() => send(digit)}>{digit}</button
      >
    {/each}
  </div>
  {#if error}<p class="text-xs th-color-danger" role="alert">{error}</p>{/if}
</div>
