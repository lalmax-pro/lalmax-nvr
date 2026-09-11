export interface AiZone {
  id: string;
  name: string;
  camera_id: string;
  points: [number, number][];
}

const KEY = 'nvr_ai_zones';

export function loadZones(cameraId?: string): AiZone[] {
  try {
    const raw = localStorage.getItem(KEY);
    const all = raw ? JSON.parse(raw) as AiZone[] : [];
    return cameraId ? all.filter(z => z.camera_id === cameraId) : all;
  } catch {
    return [];
  }
}

export function saveZones(zones: AiZone[]) {
  localStorage.setItem(KEY, JSON.stringify(zones));
}

export function pointInPolygon(x: number, y: number, pts: [number, number][]): boolean {
  let inside = false;
  for (let i = 0, j = pts.length - 1; i < pts.length; j = i++) {
    const [xi, yi] = pts[i];
    const [xj, yj] = pts[j];
    const intersect = ((yi > y) !== (yj > y)) && (x < (xj - xi) * (y - yi) / ((yj - yi) || 1e-9) + xi);
    if (intersect) inside = !inside;
  }
  return inside;
}
