"""Region clustering for motion-guided detection.

Ported from Frigate (frigate/util/object.py and frigate/util/image.py,
MIT License, Copyright (c) 2026 Frigate, Inc.). Motion boxes and tracked
object boxes are clustered into square regions that are cropped and sent
to the detector, instead of resizing the full frame.
"""

import math

import cv2
import numpy as np


def area(box) -> int:
    return (box[2] - box[0]) * (box[3] - box[1])


def intersection_over_union(box_a, box_b) -> float:
    x_a = max(box_a[0], box_b[0])
    y_a = max(box_a[1], box_b[1])
    x_b = min(box_a[2], box_b[2])
    y_b = min(box_a[3], box_b[3])
    inter = max(0, x_b - x_a) * max(0, y_b - y_a)
    if inter == 0:
        return 0.0
    return inter / float(area(box_a) + area(box_b) - inter)


def box_inside(b1, b2) -> bool:
    # check if b2 is inside b1
    return b2[0] >= b1[0] and b2[1] >= b1[1] and b2[2] <= b1[2] and b2[3] <= b1[3]


def average_boxes(boxes) -> list[float]:
    n = len(boxes)
    return [
        sum(box[0] for box in boxes) / n,
        sum(box[1] for box in boxes) / n,
        sum(box[2] for box in boxes) / n,
        sum(box[3] for box in boxes) / n,
    ]


def calculate_region(frame_shape, xmin, ymin, xmax, ymax, model_size, multiplier=2):
    # size is the longest edge and divisible by 4
    size = int((max(xmax - xmin, ymax - ymin) * multiplier) // 4 * 4)
    # dont go any smaller than the model_size
    if size < model_size:
        size = model_size

    # x_offset is midpoint of bounding box minus half the size
    x_offset = int((xmax - xmin) / 2.0 + xmin - size / 2.0)
    if x_offset < 0:
        x_offset = 0
    elif x_offset > (frame_shape[1] - size):
        x_offset = max(0, (frame_shape[1] - size))

    y_offset = int((ymax - ymin) / 2.0 + ymin - size / 2.0)
    if y_offset < 0:
        y_offset = 0
    elif y_offset > (frame_shape[0] - size):
        y_offset = max(0, (frame_shape[0] - size))

    return (x_offset, y_offset, x_offset + size, y_offset + size)


def get_cluster_boundary(box, min_region):
    # compute the max region size for the current box (box is 10% of region)
    box_width = box[2] - box[0]
    box_height = box[3] - box[1]
    max_region_area = abs(box_width * box_height) / 0.1
    max_region_size = max(min_region, int(math.sqrt(max_region_area)))

    centroid = (box_width / 2 + box[0], box_height / 2 + box[1])

    max_x_dist = int(max_region_size - box_width / 2 * 1.1)
    max_y_dist = int(max_region_size - box_height / 2 * 1.1)

    return [
        int(centroid[0] - max_x_dist),
        int(centroid[1] - max_y_dist),
        int(centroid[0] + max_x_dist),
        int(centroid[1] + max_y_dist),
    ]


def get_cluster_region(frame_shape, min_region, cluster, boxes):
    min_x = frame_shape[1]
    min_y = frame_shape[0]
    max_x = 0
    max_y = 0
    for b in cluster:
        min_x = min(boxes[b][0], min_x)
        min_y = min(boxes[b][1], min_y)
        max_x = max(boxes[b][2], max_x)
        max_y = max(boxes[b][3], max_y)
    return calculate_region(
        frame_shape, min_x, min_y, max_x, max_y, min_region, multiplier=1.35
    )


def get_cluster_candidates(frame_shape, min_region, boxes):
    # create clusters of boxes: a box joins a cluster when it fits inside the
    # cluster's max region boundary and would still be a reasonable size
    # relative to the resulting region
    cluster_candidates = []
    used_boxes = set()
    for current_index, b in enumerate(boxes):
        if current_index in used_boxes:
            continue
        cluster = [current_index]
        used_boxes.add(current_index)
        cluster_boundary = get_cluster_boundary(b, min_region)
        for compare_index, compare_box in enumerate(boxes):
            if compare_index in used_boxes:
                continue

            if not box_inside(cluster_boundary, compare_box):
                continue

            potential_cluster = cluster + [compare_index]
            cluster_region = get_cluster_region(
                frame_shape, min_region, potential_cluster, boxes
            )
            # if region could be smaller and either box would be too small
            # for the resulting region, dont cluster
            should_cluster = True
            if (cluster_region[2] - cluster_region[0]) > min_region:
                for idx in potential_cluster:
                    box = boxes[idx]
                    # boxes should be more than 5% of the area of the region
                    if area(box) / area(cluster_region) < 0.05:
                        should_cluster = False
                        break

            if should_cluster:
                cluster.append(compare_index)
                used_boxes.add(compare_index)
        cluster_candidates.append(cluster)

    unique = {tuple(sorted(c)) for c in cluster_candidates}
    return [list(tup) for tup in unique]


def compute_regions(frame_shape, boxes, min_region):
    """Cluster motion/object boxes into square detection regions.

    frame_shape is (height, width); boxes are (xmin, ymin, xmax, ymax).
    Returns a list of square regions clamped to the frame.
    """
    if not boxes:
        return []

    clusters = get_cluster_candidates(frame_shape, min_region, boxes)
    regions = [
        get_cluster_region(frame_shape, min_region, cluster, boxes)
        for cluster in clusters
    ]

    clamped = []
    for x0, y0, x1, y1 in regions:
        clamped.append(
            (
                max(0, x0),
                max(0, y0),
                min(frame_shape[1], x1),
                min(frame_shape[0], y1),
            )
        )
    # drop duplicates from clamping
    return list(dict.fromkeys(clamped))


def reduce_detections(detections, nms_threshold=0.45):
    """Apply per-label NMS across detections merged from multiple regions.

    detections: list of dicts with keys label, confidence, box_xyxy (pixels).
    """
    groups: dict[str, list[dict]] = {}
    for det in detections:
        groups.setdefault(det["label"], []).append(det)

    selected = []
    for group in groups.values():
        boxes = [
            (
                d["box_xyxy"][0],
                d["box_xyxy"][1],
                d["box_xyxy"][2] - d["box_xyxy"][0],
                d["box_xyxy"][3] - d["box_xyxy"][1],
            )
            for d in group
        ]
        confidences = [d["confidence"] for d in group]
        indices = cv2.dnn.NMSBoxes(boxes, confidences, 0.1, nms_threshold)
        for index in indices:
            index = index if isinstance(index, (int, np.integer)) else index[0]
            selected.append(group[index])

    return selected
