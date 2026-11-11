import { afterEach, describe, expect, it, vi } from 'vitest';
import { PtzHold } from '$lib/ptzHold';
const request = { mode: 'continuous' as const, pan: -0.5, tilt: 0, zoom: 0 };
afterEach(() => vi.useRealTimers());
describe('PTZ gesture ordering', () => {
  it('waits for an in-flight move before sending stop and deduplicates release', async () => {
    let resolve!: () => void;
    const move = vi.fn(() => new Promise<void>(r => { resolve = r; }));
    const stop = vi.fn().mockResolvedValue({});
    const controller = new PtzHold(move, stop, vi.fn());
    expect(controller.start(request)).toBe(true);
    await Promise.resolve();
    const stopped = controller.stop();
    expect(controller.stop()).toBe(stopped);
    expect(controller.start(request)).toBe(false);
    expect(stop).not.toHaveBeenCalled();
    resolve();
    await stopped;
    expect(stop).toHaveBeenCalledOnce();
    await controller.dispose();
  });
  it('renews held movement and clears the renewal when released', async () => {
    vi.useFakeTimers();
    const move = vi.fn().mockResolvedValue({}), stop = vi.fn().mockResolvedValue({});
    const controller = new PtzHold(move, stop, vi.fn(), 500);
    controller.start(request);
    await vi.advanceTimersByTimeAsync(1500);
    expect(move).toHaveBeenCalledTimes(4);
    await controller.stop();
    await vi.advanceTimersByTimeAsync(2000);
    expect(move).toHaveBeenCalledTimes(4);
    expect(stop).toHaveBeenCalledOnce();
  });
  it('attempts stop when the move request fails', async () => {
    const error = vi.fn(), stop = vi.fn().mockResolvedValue({});
    const controller = new PtzHold(vi.fn().mockRejectedValue(new Error('lost ACK')), stop, error);
    controller.start(request);
    await vi.waitFor(() => expect(stop).toHaveBeenCalledOnce());
    expect(error).toHaveBeenCalledOnce();
    await controller.dispose();
  });
  it('stops a pending move after disposal and never starts another gesture', async () => {
    let resolve!: () => void;
    const move = vi.fn(() => new Promise<void>(r => { resolve = r; }));
    const stop = vi.fn().mockResolvedValue({});
    const controller = new PtzHold(move, stop, vi.fn());
    controller.start(request);
    await Promise.resolve();
    const disposed = controller.dispose();
    resolve();
    await disposed;
    expect(stop).toHaveBeenCalledOnce();
    expect(controller.start(request)).toBe(false);
  });
});
