import { render, cleanup } from '@testing-library/svelte';
import { describe, it, expect, vi, afterEach } from 'vitest';
import CameraCard from '$lib/components/CameraCard.svelte';
import { buildProtocolsMap, DEFAULT_PROTOCOLS } from '$lib/api';
import type { Camera } from '$lib/api';

// Mock API calls that CameraCard invokes
vi.mock('$lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('$lib/api')>();
  return {
    ...actual,
    enableCamera: vi.fn().mockResolvedValue({ id: 'test', enabled: true }),
    disableCamera: vi.fn().mockResolvedValue({ id: 'test', enabled: false }),
  };
});

// Mock lucide-svelte icons
vi.mock('lucide-svelte', () => {
  const icons = ['Pencil', 'Play', 'Pause', 'Square', 'RotateCw', 'Eye', 'MoreVertical', 'Archive', 'Trash2', 'Image', 'Bell', 'Move', 'Mic', 'MicOff', 'Camera', 'ZoomIn', 'Home', 'CalendarClock', 'CircleOff', 'KeyRound', 'Search', 'Copy', 'GitBranch'];
  const mock: Record<string, () => HTMLElement> = {};
  for (const name of icons) {
    mock[name] = () => document.createElement('span');
  }
  return mock;
});

const protocolsMap = buildProtocolsMap(DEFAULT_PROTOCOLS);

function makeCamera(overrides: Partial<Camera> = {}): Camera {
  return {
    id: 'test',
    name: 'Test Camera',
    protocol: 'rtsp',
    url: 'rtsp://192.168.1.100/stream',
    enabled: true,
    ...overrides,
  };
}

const noop = vi.fn();

function defaultProps(camera: Camera) {
  return {
    camera,
    protocolsMap,
    onedit: noop,
    ondelete: noop,
    onpermadelete: noop,
    onstart: noop,
    onstop: noop,
    onrestart: noop,
    ontoggle: noop,
    onsaveName: noop,
  };
}

describe('CameraCard', () => {
  afterEach(() => cleanup());

  it('shows camera name', () => {
    const { getByText } = render(CameraCard, { props: defaultProps(makeCamera()) });
    expect(getByText('Test Camera')).toBeTruthy();
  });

  it('uses the current channel display name without changing the camera identity or saved name', () => {
    const camera = makeCamera({ id: 'device1:channel1', protocol: 'gb28181', name: 'Stored Camera Name' });
    const { getByText, queryByText } = render(CameraCard, {
      props: { ...defaultProps(camera), displayName: 'Current Channel Name' },
    });
    expect(getByText('Current Channel Name')).toBeTruthy();
    expect(queryByText('Stored Camera Name')).toBeNull();
    expect(camera.id).toBe('device1:channel1');
    expect(camera.name).toBe('Stored Camera Name');
  });

  it('shows recording badge for active camera with recording status', () => {
    const camera = makeCamera({ status: 'recording' });
    const { getByText } = render(CameraCard, { props: defaultProps(camera) });
    expect(getByText('Live')).toBeTruthy();
  });

  it('shows disabled badge for disabled camera', () => {
    const camera = makeCamera({ enabled: false });
    const { getByText } = render(CameraCard, { props: defaultProps(camera) });
    expect(getByText('Disabled')).toBeTruthy();
  });
});
