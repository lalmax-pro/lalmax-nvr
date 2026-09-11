import { describe, expect, it } from 'vitest';
import { pointInPolygon } from '../ai-zones';

describe('pointInPolygon', () => {
  it('detects points inside a square', () => {
    const sq: [number, number][] = [[0, 0], [1, 0], [1, 1], [0, 1]];
    expect(pointInPolygon(0.5, 0.5, sq)).toBe(true);
    expect(pointInPolygon(1.5, 0.5, sq)).toBe(false);
  });
});
