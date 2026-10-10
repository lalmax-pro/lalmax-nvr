import { render, fireEvent, cleanup } from '@testing-library/svelte';
import { describe, it, expect, afterEach, vi } from 'vitest';
import VoIPSettings from '$lib/components/VoIPSettings.svelte';
import { defaultVoIPConfig, updateVoIPSettings } from '$lib/api/settings';
import { i18nState } from '$lib/i18n';

afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe('VoIP settings form', () => {
  it('allows enabling inbound calls and editing SIP and media addresses', async () => {
    i18nState.currentLang = 'en';
    const config = defaultVoIPConfig();
    const view = render(VoIPSettings, { props: { config } });
    const enabled = view.getByLabelText('Enable VoIP') as HTMLInputElement;
    expect(enabled.checked).toBe(false);
    await fireEvent.click(enabled);
    expect(enabled.checked).toBe(true);
    const ip = view.getByLabelText('Advertised media IP') as HTMLInputElement;
    await fireEvent.input(ip, { target: { value: '192.168.1.20' } });
    expect(ip.value).toBe('192.168.1.20');
    expect((view.getByLabelText('SIP UDP listen address') as HTMLInputElement).value).toBe('0.0.0.0:5070');
  });
  it('keeps stored credentials hidden and disables enablement for external media', () => {
    i18nState.currentLang = 'en';
    const config = { ...defaultVoIPConfig(), supported: false, users: [{ username: 'door', has_password: true }] };
    const view = render(VoIPSettings, { props: { config } });
    expect((view.getByLabelText('Enable VoIP') as HTMLInputElement).disabled).toBe(true);
    const password = view.getByLabelText('Password') as HTMLInputElement;
    expect(password.type).toBe('password');
    expect(password.value).toBe('');
    expect(password.placeholder).toBe('Leave blank to keep password');
  });
  it('selects the upstream PBX transport and exposes TLS trust settings', async () => {
    i18nState.currentLang = 'en';
    const config = defaultVoIPConfig();
    const view = render(VoIPSettings, { props: { config } });
    const transport = view.getByLabelText('PBX upstream transport') as HTMLSelectElement;
    expect(transport.value).toBe('udp');
    await fireEvent.change(transport, { target: { value: 'tls' } });
    expect(config.pbx_transport).toBe('tls');
    await view.rerender({ config: { ...config } });
    expect(view.getByLabelText('TLS Server Name (optional)')).toBeTruthy();
    expect(view.getByLabelText('TLS CA certificate file (optional)')).toBeTruthy();
  });
  it('sends editable settings without read-only credential metadata', async () => {
    vi.stubGlobal('sessionStorage', { getItem: () => null, setItem: () => {}, removeItem: () => {} });
    const fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ status: 'updated' }) });
    vi.stubGlobal('fetch', fetch);
    const config = { ...defaultVoIPConfig(), enabled: true, supported: true, pbx_transport: 'tls' as const, pbx_tls_ca_file: '/etc/ssl/pbx-ca.pem', users: [{ username: 'door', has_password: true }] };
    await updateVoIPSettings(config);
    const body = JSON.parse(fetch.mock.calls[0][1].body);
    expect(body.enabled).toBe(true);
    expect(body.pbx_transport).toBe('tls');
    expect(body.pbx_tls_ca_file).toBe('/etc/ssl/pbx-ca.pem');
    expect(body.users).toEqual([{ username: 'door' }]);
    expect(body.supported).toBeUndefined();
  });
});
