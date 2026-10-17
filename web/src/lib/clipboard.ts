/**
 * Copy text in secure contexts and in older/non-secure HTTP browsers.
 * Returns false when automatic copying was unavailable and manual selection
 * was offered. Never reports a successful copy in that case.
 */
export async function copyTextWithFallback(text: string): Promise<boolean> {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // Browser may deny permission; use the selection-based fallback.
    }
  }

  if (typeof document !== 'undefined' && typeof document.execCommand === 'function') {
    const textarea = document.createElement('textarea');
    textarea.value = text;
    textarea.readOnly = true;
    textarea.setAttribute('aria-hidden', 'true');
    textarea.style.cssText = 'position:fixed;top:0;left:0;width:1px;height:1px;opacity:0;pointer-events:none';
    const previousFocus = document.activeElement;
    document.body.appendChild(textarea);
    let copied = false;
    try {
      textarea.focus();
      textarea.select();
      textarea.setSelectionRange(0, textarea.value.length);
      copied = document.execCommand('copy');
    } catch {
      // The browser refused the legacy clipboard method.
    } finally {
      textarea.remove();
      if (previousFocus instanceof HTMLElement) previousFocus.focus();
    }
    if (copied) return true;
  }

  // Last resort: let the user select/copy; do not report success prematurely.
  window.prompt('自动复制不可用，请选中地址后按 Ctrl+C（Mac：Cmd+C）：', text);
  return false;
}
