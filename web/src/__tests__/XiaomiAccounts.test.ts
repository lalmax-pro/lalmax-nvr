import { cleanup, fireEvent, render, waitFor } from '@testing-library/svelte';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import DiscoveryPanel from '$lib/components/DiscoveryPanel.svelte';

const api = vi.hoisted(() => ({ accounts: vi.fn(), devices: vi.fn(), vendor: vi.fn(), create: vi.fn() }));
vi.mock('$lib/api', async importOriginal => ({
  ...await importOriginal<typeof import('$lib/api')>(),
  xiaomiAccounts: api.accounts, xiaomiDevices: api.devices,
  checkVendor: api.vendor, createCamera: api.create,
}));
vi.mock('$lib/i18n', () => ({ t: (key: string) => key }));
vi.mock('$lib/toast', () => ({ showToast: vi.fn() }));

beforeEach(() => {
  vi.resetAllMocks();
  api.accounts.mockResolvedValue({ accounts: [
    { id: 'default', region: 'cn', authenticated: true },
    { id: 'home', region: 'sg', authenticated: true },
  ] });
  api.devices.mockImplementation(async (_signal, account) => ({ authenticated: true, devices: account === 'home'
    ? [{ did: '123', name: 'Home camera', model: 'camera', localip: '192.0.2.1', isOnline: true }]
    : [] }));
  api.vendor.mockResolvedValue({ compatible: true });
  api.create.mockResolvedValue({});
});
afterEach(cleanup);

it('lists the chosen account and associates newly added cameras with it', async () => {
  const ui = render(DiscoveryPanel, { protocol: 'xiaomi', cameras: [] });
  const selector = await ui.findByRole('combobox', { name: 'xiaomi.accountSelection' });
  await waitFor(() => expect((selector as HTMLSelectElement).disabled).toBe(false));
  await fireEvent.change(selector, { target: { value: 'home' } });
  expect(await ui.findByText('Home camera')).toBeTruthy();
  expect(api.devices).toHaveBeenCalledWith(undefined, 'home');
  await fireEvent.click(ui.getByRole('button', { name: 'onvif.addCamera' }));
  await waitFor(() => expect(api.create).toHaveBeenCalledWith(expect.objectContaining({ url: 'xiaomi://123?account=home' })));
  expect(api.vendor).toHaveBeenCalledWith('123', 'home');
});
