import { describe, expect, it, vi } from 'vitest';
import { cameraDisplayName, gbDisplayName, loadGBChannelNames } from '../gb-channel-names';

vi.mock('../api/gb28181', () => ({
  listGB28181Devices: vi.fn(async () => ({
    devices: [
      { device_id: 'deviceA', channels: [{ channel_id: 'stream0', name: '东门新名称' }] },
      { device_id: 'deviceB', channels: [{ channel_id: 'stream0', name: '西门新名称' }] },
    ],
  })),
}));

describe('GB28181 display name resolution', () => {
  it('keeps same channel ID on different devices separate', async () => {
    const names = await loadGBChannelNames();
    expect(names.get('deviceA:stream0')).toBe('东门新名称');
    expect(names.get('deviceB:stream0')).toBe('西门新名称');
    expect(names.get('deviceA_stream0')).toBe('东门新名称');
    expect(gbDisplayName(names, ['deviceB:stream0'], '旧名称')).toBe('西门新名称');
  });
  it('uses catalog label for GB28181, but does not change camera identity', async () => {
    const names = await loadGBChannelNames();
    const camera = { id: 'deviceA:stream0', name: '旧数据库名称', protocol: 'gb28181' };
    expect(cameraDisplayName(names, camera)).toBe('东门新名称');
    expect(camera).toEqual({ id: 'deviceA:stream0', name: '旧数据库名称', protocol: 'gb28181' });
  });
  it('does not relabel unrelated protocols and falls back when absent', async () => {
    const names = await loadGBChannelNames();
    expect(cameraDisplayName(names, { id: 'deviceA:stream0', protocol: 'onvif', name: '我的 ONVIF' })).toBe('我的 ONVIF');
    expect(cameraDisplayName(names, { id: 'missing', protocol: 'gb28181', name: '本地名称' })).toBe('本地名称');
  });
});
