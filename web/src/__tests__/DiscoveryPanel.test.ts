import { cleanup, fireEvent, render, waitFor } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import DiscoveryPanel from '$lib/components/DiscoveryPanel.svelte';

const api = vi.hoisted(() => ({
  status: vi.fn(), accounts: vi.fn(), devices: vi.fn(), auth: vi.fn(), logout: vi.fn(),
}));
vi.mock('$lib/api', async importOriginal => ({
  ...await importOriginal<typeof import('$lib/api')>(),
  xiaomiStatus: api.status, xiaomiDevices: api.devices,
  xiaomiAccounts: api.accounts,
  xiaomiAuth: api.auth, xiaomiLogout: api.logout,
}));
vi.mock('$lib/i18n', () => ({ t: (key: string) => key }));

beforeEach(() => {
  vi.resetAllMocks();
  api.status.mockResolvedValue({ authenticated: true, region: 'sg' });
  api.accounts.mockImplementation(async () => ({ accounts: [{ id: 'default', ...await api.status() }] }));
  api.devices.mockResolvedValue({ authenticated: true, devices: [] });
  api.auth.mockResolvedValue({ status: 'ok' });
  api.logout.mockResolvedValue({ status: 'ok' });
});
afterEach(cleanup);

function fixture() {
  return render(DiscoveryPanel, { protocol: 'xiaomi', cameras: [] });
}

describe('Xiaomi account discovery', () => {
  it('keeps an authenticated empty account signed in', async () => {
    const ui = fixture();
    await waitFor(() => expect(api.devices).toHaveBeenCalledOnce());
    expect(await ui.findByText('xiaomi.noDevices')).toBeTruthy();
    expect(ui.getByRole('button', { name: 'xiaomi.signOut' })).toBeTruthy();
    expect(ui.queryByRole('button', { name: 'xiaomi.signIn' })).toBeNull();
  });

  it('keeps account controls available when device refresh fails', async () => {
    const ui = fixture();
    const refresh = await ui.findByRole('button', { name: 'xiaomi.refresh' });
    await waitFor(() => expect((refresh as HTMLButtonElement).disabled).toBe(false));
    api.devices.mockRejectedValueOnce(new Error('cloud unavailable'));
    await fireEvent.click(refresh);
    expect(await ui.findByRole('alert')).toHaveProperty('textContent', 'cloud unavailable');
    expect(ui.getByRole('button', { name: 'xiaomi.signOut' })).toBeTruthy();
  });

  it('only clears the UI after the backend confirms logout', async () => {
    const ui = fixture();
    const logout = await ui.findByRole('button', { name: 'xiaomi.signOut' });
    await waitFor(() => expect((logout as HTMLButtonElement).disabled).toBe(false));
    let finish!: (result: { status: string }) => void;
    api.logout.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
    await fireEvent.click(logout);
    expect(api.logout).toHaveBeenCalledOnce();
    expect(ui.queryByRole('button', { name: 'xiaomi.signIn' })).toBeNull();
    finish({ status: 'ok' });
    expect(await ui.findByRole('button', { name: 'xiaomi.signIn' })).toBeTruthy();
    expect(ui.queryByText('xiaomi.noDevices')).toBeNull();
  });

  it('preserves account controls and exposes a failed logout', async () => {
    const ui = fixture();
    const logout = await ui.findByRole('button', { name: 'xiaomi.signOut' });
    await waitFor(() => expect((logout as HTMLButtonElement).disabled).toBe(false));
    api.logout.mockRejectedValueOnce(new Error('could not save configuration'));
    await fireEvent.click(logout);
    expect(await ui.findByRole('alert')).toHaveProperty('textContent', 'could not save configuration');
    expect(ui.getByRole('button', { name: 'xiaomi.signOut' })).toBeTruthy();
  });

  it.each(['de', 'i2'])('passes region %s when signing in', async regionCode => {
    api.status.mockResolvedValue({ authenticated: false, region: '' });
    const ui = fixture();
    const region = await ui.findByRole('combobox', { name: 'xiaomi.region' });
    await waitFor(() => expect((region as HTMLSelectElement).disabled).toBe(false));
    expect(api.devices).not.toHaveBeenCalled();
    await fireEvent.input(ui.getByLabelText('xiaomi.account'), { target: { value: 'account' } });
    await fireEvent.input(ui.getByLabelText('xiaomi.password'), { target: { value: 'password' } });
    await fireEvent.change(region, { target: { value: regionCode } });
    await fireEvent.submit(ui.container.querySelector('form')!);
    await waitFor(() => expect(api.auth).toHaveBeenCalledWith('account', 'password', regionCode, undefined, 'default'));
    expect(await ui.findByRole('button', { name: 'xiaomi.signOut' })).toBeTruthy();
  });
});
