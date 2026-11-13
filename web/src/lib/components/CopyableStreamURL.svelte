<script lang="ts">
  import { Copy } from 'lucide-svelte';
  import { copyTextWithFallback } from '$lib/clipboard';
  import { showToast } from '$lib/toast';
  import { t } from '$lib/i18n';

  let { protocol, url }: { protocol: string; url: string } = $props();

  async function copyURL() {
    try {
      if (await copyTextWithFallback(url)) {
        showToast(t('streams.copied'), 'success');
      }
    } catch {
      showToast(t('streams.copyFailed'), 'error');
    }
  }
</script>

<button
  type="button"
  class="url-item"
  title={t('streams.copy')}
  aria-label={`${t('streams.copy')} ${protocol}: ${url}`}
  onclick={copyURL}
>
  <span class="url-info">
    <span class="url-protocol">{protocol}</span>
    <code>{url}</code>
  </span>
  <Copy size={14} aria-hidden="true" />
</button>

<style>
  .url-item {
    display: flex;
    width: 100%;
    justify-content: space-between;
    align-items: center;
    gap: 0.75rem;
    min-width: 0;
    padding: 0.75rem;
    border-radius: var(--radius-md);
    border: 1px solid var(--border);
    background: transparent;
    font: inherit;
    text-align: left;
    color: inherit;
    cursor: pointer;
    transition: all 0.15s ease;
  }

  .url-item:hover {
    border-color: var(--color-primary);
    transform: translateY(-1px);
  }

  .url-item:focus-visible {
    outline: 2px solid var(--color-primary);
    outline-offset: 2px;
  }

  .url-info {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    min-width: 0;
  }

  .url-protocol {
    padding: 0.2rem 0.5rem;
    border-radius: var(--radius-sm);
    background: rgba(14, 165, 233, 0.15);
    color: var(--color-primary);
    font-size: 0.7rem;
    font-weight: 700;
    text-transform: uppercase;
    white-space: nowrap;
  }

  .url-info code {
    font-size: 0.8rem;
    color: var(--text-secondary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
