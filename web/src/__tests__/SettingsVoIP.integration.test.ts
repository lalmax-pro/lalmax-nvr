import { render, fireEvent, cleanup, waitFor } from '@testing-library/svelte';
import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest';
import Settings from '../routes/Settings.svelte';
import VoIP from '../routes/VoIP.svelte';
import { getVoIPSettings, updateVoIPSettings, defaultVoIPConfig } from '$lib/api';
import { i18nState } from '$lib/i18n';

vi.mock('$lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('$lib/api')>();
  const settings = { cleanup: { retention_days: 30, disk_threshold_percent: 90, check_interval: '1h' } };
  return {
    ...actual,
    getSettings: vi.fn().mockResolvedValue(settings),
    getMergeSettings: vi.fn().mockResolvedValue({}),
    getGB28181Settings: vi.fn().mockResolvedValue({ host: '192.168.1.20', media_ip: '192.168.1.20' }),
    getHLSSettings: vi.fn().mockResolvedValue({}),
    getFeatures: vi.fn().mockResolvedValue({ protocols: {} }),
    getStats: vi.fn().mockResolvedValue({}),
    listCameras: vi.fn().mockResolvedValue([]),
    getStreamingSettings: vi.fn().mockResolvedValue({}),
    getAutoDiscoverSettings: vi.fn().mockResolvedValue({}),
    getAiBackendConfig: vi.fn().mockResolvedValue({ enabled: false, backend: 'disabled' }),
    checkConfigChange: vi.fn().mockResolvedValue({ changed: false }),
    getVoIPSettings: vi.fn(),
    getVoIPStatus: vi.fn().mockResolvedValue({enabled:false,endpoints:[],calls:[]}),
    getVoIPCallHistory: vi.fn().mockResolvedValue({items:[],total:0,limit:20,offset:0}),
    updateVoIPSettings: vi.fn().mockResolvedValue({ status: 'updated' }),
    updateSettings: vi.fn().mockResolvedValue({}),
    updateDLNASettings: vi.fn().mockResolvedValue({}),
    updateMergeSettings: vi.fn().mockResolvedValue({}),
    updateStreamingSettings: vi.fn().mockResolvedValue({}),
    updateFeatures: vi.fn().mockResolvedValue({}),
    updateGB28181Settings: vi.fn().mockResolvedValue({}),
    updateHLSSettings: vi.fn().mockResolvedValue({}),
  };
});

beforeEach(() => {
  vi.clearAllMocks();
  i18nState.currentLang = 'en';
  vi.mocked(getVoIPSettings).mockResolvedValue(defaultVoIPConfig());
});
afterEach(cleanup);

describe('VoIP settings page separation', () => {
  it('keeps VoIP configuration out of the general settings page', async () => {
    const view = render(Settings);
    await fireEvent.click(await view.findByRole('tab', { name: 'Advanced', exact: true }));
    expect(view.queryByLabelText('Enable VoIP')).toBeNull();
    expect(getVoIPSettings).not.toHaveBeenCalled();
  });

  it('does not restart VoIP when only another setting changes', async () => {
    const view = render(Settings);
    const retention = await view.findByLabelText('Retention Days');
    await fireEvent.input(retention, { target: { value: '31' } });
    const save = view.getAllByRole('button', { name: 'Save Settings', exact: true }).at(-1) as HTMLButtonElement;
    await waitFor(() => expect(save.disabled).toBe(false));
    await fireEvent.click(save);
    await waitFor(() => expect(save.disabled).toBe(true));
    expect(updateVoIPSettings).not.toHaveBeenCalled();
  });

  it('loads and saves VoIP settings from its dedicated page', async () => {
    const view = render(VoIP);
    const enabled = await view.findByLabelText('Enable VoIP') as HTMLInputElement;
    expect(enabled.checked).toBe(false);
    const save = view.getByRole('button', { name: 'Save', exact: true }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    await fireEvent.click(enabled);
    await fireEvent.input(view.getByLabelText('Advertised media IP'), { target: { value: '192.168.1.20' } });
    await waitFor(() => expect(save.disabled).toBe(false));
    vi.mocked(getVoIPSettings).mockResolvedValue({ ...defaultVoIPConfig(), enabled: true, media_ip: '192.168.1.20' });
    await fireEvent.click(save);
    await waitFor(() => expect(updateVoIPSettings).toHaveBeenCalledWith(expect.objectContaining({ enabled: true, media_ip: '192.168.1.20' })));
    await waitFor(() => expect(save.disabled).toBe(true));
    expect(enabled.checked).toBe(true);
  });
});
