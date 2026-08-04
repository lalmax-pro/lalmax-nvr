"""Background-averaging motion detector.

Ported from Frigate's ImprovedMotionDetector (frigate/motion/improved_motion.py,
MIT License, Copyright (c) 2026 Frigate, Inc.), with PTZ handling, motion masks
and debug image saving removed. Gaussian blur uses cv2 instead of scipy.
"""

import cv2
import numpy as np


class MotionDetector:
    def __init__(
        self,
        frame_shape: tuple[int, int],
        threshold: int = 30,
        contour_area: int = 10,
        frame_height: int = 100,
        frame_alpha: float = 0.01,
        lightning_threshold: float = 0.8,
        improve_contrast: bool = True,
        contrast_frame_history: int = 50,
    ) -> None:
        self.frame_shape = frame_shape  # (height, width)
        self.threshold = threshold
        self.contour_area = contour_area
        self.frame_alpha = frame_alpha
        self.lightning_threshold = lightning_threshold
        self.improve_contrast = improve_contrast

        self.resize_factor = frame_shape[0] / frame_height
        self.motion_frame_size = (
            frame_height,
            frame_height * frame_shape[1] // frame_shape[0],
        )
        self.avg_frame = np.zeros(self.motion_frame_size, np.float32)
        self.motion_frame_count = 0
        self.calibrating = True
        self.contrast_values = np.zeros((contrast_frame_history, 2), np.uint8)
        self.contrast_values[:, 1:2] = 255
        self.contrast_values_index = 0

    def is_calibrating(self) -> bool:
        return self.calibrating

    def detect(self, frame: np.ndarray) -> list[tuple[int, int, int, int]]:
        """Detect motion in a BGR frame.

        Returns motion boxes as (xmin, ymin, xmax, ymax) in full-frame pixels.
        """
        motion_boxes: list[tuple[int, int, int, int]] = []

        gray = cv2.cvtColor(frame, cv2.COLOR_BGR2GRAY)
        resized_frame = cv2.resize(
            gray,
            dsize=(self.motion_frame_size[1], self.motion_frame_size[0]),
            interpolation=cv2.INTER_NEAREST,
        )

        if self.improve_contrast:
            min_value = np.percentile(resized_frame, 4).astype(np.uint8)
            max_value = np.percentile(resized_frame, 96).astype(np.uint8)
            # skip contrast calcs if the image is a single color
            if min_value < max_value:
                # keep a rolling window of contrast values to avoid sudden changes
                self.contrast_values[self.contrast_values_index] = [
                    min_value,
                    max_value,
                ]
                self.contrast_values_index += 1
                if self.contrast_values_index == len(self.contrast_values):
                    self.contrast_values_index = 0

                avg_min, avg_max = np.mean(self.contrast_values, axis=0)
                resized_frame = np.clip(resized_frame, avg_min, avg_max)
                resized_frame = (
                    ((resized_frame - avg_min) / (avg_max - avg_min)) * 255
                ).astype(np.uint8)

        resized_frame = cv2.GaussianBlur(resized_frame, (3, 3), 1)

        # compare to average
        frame_delta = cv2.absdiff(resized_frame, cv2.convertScaleAbs(self.avg_frame))
        thresh = cv2.threshold(frame_delta, self.threshold, 255, cv2.THRESH_BINARY)[1]
        thresh_dilated = cv2.dilate(thresh, None, iterations=1)
        contours, _ = cv2.findContours(
            thresh_dilated, cv2.RETR_EXTERNAL, cv2.CHAIN_APPROX_SIMPLE
        )

        total_contour_area = 0.0
        for c in contours:
            contour_area = cv2.contourArea(c)
            total_contour_area += contour_area
            if contour_area > self.contour_area:
                x, y, w, h = cv2.boundingRect(c)
                motion_boxes.append(
                    (
                        int(x * self.resize_factor),
                        int(y * self.resize_factor),
                        int((x + w) * self.resize_factor),
                        int((y + h) * self.resize_factor),
                    )
                )

        pct_motion = total_contour_area / (
            self.motion_frame_size[0] * self.motion_frame_size[1]
        )

        # once the motion is less than 5% and the number of contours is < 4,
        # assume calibration is done
        if pct_motion < 0.05 and len(motion_boxes) <= 4:
            self.calibrating = False

        # if calibrating or the motion contours cover most of the image
        # (lightning, IR mode switch) recalibrate rather than reporting motion
        if self.calibrating or pct_motion > self.lightning_threshold:
            self.calibrating = True

        if len(motion_boxes) > 0:
            self.motion_frame_count += 1
            if self.motion_frame_count >= 10:
                # only average in the current frame if the difference persists
                cv2.accumulateWeighted(
                    resized_frame,
                    self.avg_frame,
                    0.2 if self.calibrating else self.frame_alpha,
                )
        else:
            # when no motion, just keep averaging the frames together
            cv2.accumulateWeighted(
                resized_frame,
                self.avg_frame,
                0.2 if self.calibrating else self.frame_alpha,
            )
            self.motion_frame_count = 0

        return motion_boxes
