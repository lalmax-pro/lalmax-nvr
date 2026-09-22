import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { subscribeNvrEvents } from '$lib/api/events';

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  url: string;
  readyState = 1;
  onerror: ((ev: Event) => void) | null = null;
  private listeners = new Map<string, Array<(ev: MessageEvent) => void>>();

  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, fn: (ev: MessageEvent) => void) {
    const list = this.listeners.get(type) ?? [];
    list.push(fn);
    this.listeners.set(type, list);
  }

  close() {
    this.readyState = 2;
  }

  emit(type: string, data: string) {
    for (const fn of this.listeners.get(type) ?? []) {
      fn({ data } as MessageEvent);
    }
  }
}

describe('subscribeNvrEvents', () => {
  const OriginalES = globalThis.EventSource;

  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal('EventSource', FakeEventSource);
    sessionStorage.setItem('nvr_auth', btoa('admin:pass'));
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    globalThis.EventSource = OriginalES;
    sessionStorage.clear();
    vi.useRealTimers();
  });

  it('delivers nvr events', () => {
    const seen: string[] = [];
    const stop = subscribeNvrEvents({ source: 'health' }, (ev) => {
      seen.push(ev.camera_id);
    });
    expect(FakeEventSource.instances).toHaveLength(1);
    expect(FakeEventSource.instances[0].url).toContain('/api/events/stream');
    expect(FakeEventSource.instances[0].url).toContain('source=health');
    FakeEventSource.instances[0].emit('nvr', JSON.stringify({ camera_id: 'cam1', source: 'health' }));
    expect(seen).toEqual(['cam1']);
    stop();
  });

  it('debounces rapid events', () => {
    vi.useFakeTimers();
    const seen: string[] = [];
    const stop = subscribeNvrEvents({}, (ev) => seen.push(ev.camera_id), { debounceMs: 200 });
    const es = FakeEventSource.instances[0];
    es.emit('nvr', JSON.stringify({ camera_id: 'a' }));
    es.emit('nvr', JSON.stringify({ camera_id: 'b' }));
    expect(seen).toEqual([]);
    vi.advanceTimersByTime(200);
    expect(seen).toEqual(['b']);
    stop();
  });

  it('stop() closes the EventSource', () => {
    const stop = subscribeNvrEvents({}, () => {});
    const es = FakeEventSource.instances[0];
    stop();
    expect(es.readyState).toBe(2);
  });
});
