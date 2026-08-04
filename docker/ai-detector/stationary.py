"""Stationary object suppression.

Ported from Frigate's position-history tracking (frigate/track/norfair_tracker.py
update_position and frigate/track/stationary_classifier.py thresholds, MIT
License, Copyright (c) 2026 Frigate, Inc.), without the crop-similarity
classifier. An object whose bounding box stays within its historical average
position for enough processed frames is marked stationary and suppressed from
webhook reports until it moves again.
"""

import time

from regions import average_boxes, intersection_over_union

# IOU thresholds from frigate's StationaryThresholds defaults
KNOWN_ACTIVE_IOU = 0.2
STATIONARY_CHECK_IOU = 0.6
ACTIVE_CHECK_IOU = 0.9
MAX_STATIONARY_HISTORY = 10

# forget tracks not seen for this many seconds
TRACK_EXPIRY_SECONDS = 60


class StationarySuppressor:
    """Per-camera tracker of which track IDs are stationary.

    States returned by update():
      "active"            object is moving, report it
      "became_stationary" first frame classified stationary, report once
      "stationary"        still stationary, suppress
    """

    def __init__(self, threshold_frames: int = 50) -> None:
        self.threshold_frames = threshold_frames
        self.history: dict[int, list[tuple[int, int, int, int]]] = {}
        self.motionless_count: dict[int, int] = {}
        self.stationary: dict[int, bool] = {}
        self.last_seen: dict[int, float] = {}

    def update(self, track_id: int, box: tuple[int, int, int, int]) -> str:
        now = time.monotonic()
        self.last_seen[track_id] = now

        if track_id not in self.history:
            self.history[track_id] = [box]
            self.motionless_count[track_id] = 0
            self.stationary[track_id] = False
            return "active"

        history = self.history[track_id]
        history.append(box)
        if len(history) > MAX_STATIONARY_HISTORY:
            self.history[track_id] = history = history[-MAX_STATIONARY_HISTORY:]

        avg_box = average_boxes(history)
        avg_iou = intersection_over_union(box, avg_box)
        is_stationary = self.stationary[track_id]

        # minimal or zero overlap with recent history: object is clearly active
        if avg_iou < KNOWN_ACTIVE_IOU:
            return self._mark_active(track_id, box)

        threshold = STATIONARY_CHECK_IOU if is_stationary else ACTIVE_CHECK_IOU
        if avg_iou < threshold:
            return self._mark_active(track_id, box)

        # position is unchanged
        self.motionless_count[track_id] += 1
        if is_stationary:
            return "stationary"

        if self.motionless_count[track_id] >= self.threshold_frames:
            self.stationary[track_id] = True
            return "became_stationary"

        return "active"

    def _mark_active(self, track_id: int, box) -> str:
        self.history[track_id] = [box]
        self.motionless_count[track_id] = 0
        self.stationary[track_id] = False
        return "active"

    def prune(self) -> None:
        """Drop state for tracks that have not been seen recently."""
        cutoff = time.monotonic() - TRACK_EXPIRY_SECONDS
        for track_id in [t for t, ts in self.last_seen.items() if ts < cutoff]:
            self.history.pop(track_id, None)
            self.motionless_count.pop(track_id, None)
            self.stationary.pop(track_id, None)
            self.last_seen.pop(track_id, None)
