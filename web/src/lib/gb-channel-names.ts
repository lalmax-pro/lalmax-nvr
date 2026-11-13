import { listGB28181Devices } from './api/gb28181';

// Stable IDs are never rewritten when a device renames a channel.
// Only display labels are resolved against the latest GB28181 catalog.
export type ChannelNames = Map<string, string>;
export async function loadGBChannelNames(): Promise<ChannelNames> {
  const { devices } = await listGB28181Devices();
  const names: ChannelNames = new Map();
  for (const device of devices || []) {
    for (const channel of device.channels || []) {
      const name = channel.name?.trim();
      if (!name) continue;
      names.set(`${device.device_id}:${channel.channel_id}`, name);
      names.set(`${device.device_id}_${channel.channel_id}`, name);
    }
  }
  return names;
}

export function gbDisplayName(names: ChannelNames, ids: Array<string | undefined>, fallback: string): string {
  for (const id of ids) {
    if (id && names.has(id)) return names.get(id)!;
  }
  return fallback;
}

/** Resolve the current GB28181 catalog name without changing the camera's stable ID or saved metadata. */
export function cameraDisplayName(
  names: ChannelNames,
  camera: { id: string; stream_id?: string; name?: string; protocol?: string },
): string {
  const fallback = camera.name || camera.id;
  if (camera.protocol?.toLowerCase() !== 'gb28181') return fallback;
  return gbDisplayName(names, [camera.stream_id, camera.id], fallback);
}
