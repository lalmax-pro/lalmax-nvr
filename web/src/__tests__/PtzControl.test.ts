import { cleanup, fireEvent, render } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import PtzControl from '../components/PtzControl.svelte';
const api = vi.hoisted(() => ({ move: vi.fn(), stop: vi.fn(), presets: vi.fn(), profile: vi.fn() }));
vi.mock('$lib/api', async importOriginal => {
  const original = await importOriginal<typeof import('$lib/api')>();
  return { ...original, ptzMove: api.move, ptzStop: api.stop, getPTZPresets: api.presets, getXiaomiMotorProfile: api.profile };
});
vi.mock('$lib/i18n', () => ({ t: (key: string) => key }));
afterEach(async () => { cleanup(); await new Promise(r => setTimeout(r, 0)); vi.clearAllMocks(); });
function fixture(protocol = 'xiaomi') {
  api.profile.mockResolvedValue({ control: 'unknown' }); api.move.mockResolvedValue({}); api.stop.mockResolvedValue({}); api.presets.mockResolvedValue([]);
  return render(PtzControl, { cameraId: 'cam', enabled: true, protocol });
}
describe('PTZ controls', () => {
  it('shows motor speed choices and omits optical zoom and presets for Xiaomi', async () => {
    const ui = fixture();
    expect(ui.getByRole('combobox', { name: 'ptz.speed' })).toBeTruthy();
    expect(ui.queryByRole('button', { name: 'ptz.zoomIn' })).toBeNull();
    expect(api.presets).not.toHaveBeenCalled();
    await fireEvent.change(ui.getByRole('combobox'), { target: { value: '1' } });
    await fireEvent.pointerDown(ui.getByRole('button', { name: 'ptz.up' }));
    await vi.waitFor(() => expect(api.move).toHaveBeenCalledWith('cam', { mode: 'continuous', pan: 0, tilt: 1, zoom: 0 }));
    await fireEvent.pointerUp(window);
    await vi.waitFor(() => expect(api.stop).toHaveBeenCalledWith('cam'));
  });
  it('shows fixed-step control for recognized models', async () => {
    const ui = fixture();
    api.profile.mockResolvedValue({ control: 'fixed_step' });
    await ui.rerender({ cameraId: 'next', enabled: true, protocol: 'xiaomi' });
    await vi.waitFor(() => expect(ui.getByText('ptz.fixedStep')).toBeTruthy());
    expect(ui.queryByRole('combobox', { name: 'ptz.speed' })).toBeNull();
  });
  it('shows continuous control for a recognized active camera', async () => {
    const ui = fixture();
    api.profile.mockResolvedValue({ control: 'continuous' });
    await ui.rerender({ cameraId: 'next', enabled: true, protocol: 'xiaomi' });
    await vi.waitFor(() => expect(ui.getByText('ptz.continuous')).toBeTruthy());
    expect(ui.getByRole('combobox', { name: 'ptz.speed' })).toBeTruthy();
  });
  it('retains zoom controls for other protocols', () => {
    const ui = fixture('onvif');
    expect(ui.getByRole('button', { name: 'ptz.zoomIn' })).toBeTruthy();
    expect(ui.queryByRole('combobox', { name: 'ptz.speed' })).toBeNull();
  });
  it('stops the old camera after its pending move finishes on a camera switch', async () => {
    const ui = fixture();
    let resolve!: () => void;
    api.move.mockImplementationOnce(() => new Promise<void>(r => { resolve = r; }));
    await fireEvent.pointerDown(ui.getByRole('button', { name: 'ptz.left' }));
    await vi.waitFor(() => expect(api.move).toHaveBeenCalled());
    await ui.rerender({ cameraId: 'new-cam', enabled: true, protocol: 'xiaomi' });
    resolve();
    await vi.waitFor(() => expect(api.stop).toHaveBeenCalledWith('cam'));
    expect(api.stop).not.toHaveBeenCalledWith('new-cam');
  });
});
