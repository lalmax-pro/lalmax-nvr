import os
import time
import json
import base64
import logging
import threading
from typing import Dict

import requests
import cv2
import numpy as np
from ultralytics import YOLO
import supervision as sv

from motion import MotionDetector
from regions import compute_regions, reduce_detections
from stationary import StationarySuppressor

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger("gocv-yolo")

# Configuration
NVR_URL = os.getenv("NVR_URL", "http://host.docker.internal:9090")
NVR_AUTH = os.getenv("NVR_AUTH", "")
YOLO_MODEL = os.getenv("YOLO_MODEL", "yolov8n.pt")
CONFIDENCE = float(os.getenv("CONFIDENCE", "0.5"))
NMS_THRESHOLD = float(os.getenv("NMS_THRESHOLD", "0.4"))
FRAME_SKIP = int(os.getenv("FRAME_SKIP", "5"))
SYNC_INTERVAL = int(os.getenv("SYNC_INTERVAL", "30"))
RTSP_PORT = int(os.getenv("RTSP_PORT", "15544"))

# Motion gating (frigate-style background-averaging detector)
MOTION_ONLY = os.getenv("MOTION_ONLY", "true").lower() == "true"
MOTION_THRESHOLD = int(os.getenv("MOTION_THRESHOLD", "30"))
MOTION_CONTOUR_AREA = int(os.getenv("MOTION_CONTOUR_AREA", "10"))
MOTION_FRAME_HEIGHT = int(os.getenv("MOTION_FRAME_HEIGHT", "100"))
MOTION_LIGHTNING_THRESHOLD = float(os.getenv("MOTION_LIGHTNING_THRESHOLD", "0.8"))
IMPROVE_CONTRAST = os.getenv("IMPROVE_CONTRAST", "true").lower() == "true"

# Region-based detection: motion boxes are clustered into square crops that
# are sent to the model instead of the resized full frame
REGION_MIN_SIZE = int(os.getenv("REGION_MIN_SIZE", "320"))
MAX_REGIONS = int(os.getenv("MAX_REGIONS", "4"))

# Stationary suppression: objects whose position is unchanged for this many
# processed frames stop being reported until they move again
STATIONARY_THRESHOLD = int(os.getenv("STATIONARY_THRESHOLD", "50"))

# Global state
model = None
active_cameras = {}
# Per-camera trackers for object tracking
trackers: Dict[str, sv.ByteTrack] = {}
ENABLE_TRACKING = os.getenv("ENABLE_TRACKING", "true").lower() == "true"

# Pipeline counters for the health endpoint
stats_lock = threading.Lock()
stats = {
    "frames_processed": 0,
    "frames_no_motion": 0,
    "inferences": 0,
    "detections_suppressed": 0,
}


def bump_stat(key: str, n: int = 1):
    with stats_lock:
        stats[key] += n


def get_tracker(camera_id: str) -> sv.ByteTrack:
    """Get or create a ByteTrack tracker for a specific camera."""
    if camera_id not in trackers:
        trackers[camera_id] = sv.ByteTrack(
            track_activation_threshold=0.25,
            lost_track_buffer=30,
            minimum_matching_threshold=0.8,
            frame_rate=30
        )
    return trackers[camera_id]

def get_auth_headers():
    headers = {"Content-Type": "application/json"}
    if NVR_AUTH:
        headers["Authorization"] = f"Basic {NVR_AUTH}"
    return headers

def fetch_cameras():
    """Fetch camera list from lalmax-nvr API"""
    try:
        resp = requests.get(
            f"{NVR_URL}/api/cameras",
            headers=get_auth_headers(),
            timeout=10
        )
        if resp.status_code == 200:
            return resp.json()
        else:
            logger.error(f"Failed to fetch cameras: HTTP {resp.status_code}")
            return []
    except Exception as e:
        logger.error(f"Failed to fetch cameras: {e}")
        return []

def get_stream_url(camera):
    """Get stream URL for a camera (prefer HTTP-FLV over RTSP)"""
    url = camera.get("url", "")
    camera_id = camera.get("id", "")

    # If URL is already RTSP, use it directly
    if url.startswith("rtsp://"):
        return url

    # For ONVIF cameras, construct HTTP-FLV URL from NVR's lalmax stream
    nvr_host = NVR_URL.replace("http://", "").replace("https://", "").split(":")[0]
    return f"http://{nvr_host}:18080/live/{camera_id}.flv"

def draw_detections(frame, detections):
    """Draw bounding boxes and labels on frame."""
    h, w = frame.shape[:2]
    for det in detections:
        label = det.get("label", "")
        confidence = det.get("confidence", 0)
        box = det.get("box", [])
        track_id = det.get("track_id")

        if len(box) != 4:
            continue

        # Convert normalized coordinates to pixel coordinates
        x, y, bw, bh = box
        x1 = int(x * w)
        y1 = int(y * h)
        x2 = int((x + bw) * w)
        y2 = int((y + bh) * h)

        # Draw box
        color = (0, 255, 0)  # Green
        cv2.rectangle(frame, (x1, y1), (x2, y2), color, 2)

        # Draw label
        text = f"{label} {confidence:.0%}"
        if track_id is not None:
            text += f" #{track_id}"

        # Background for text
        (text_w, text_h), _ = cv2.getTextSize(text, cv2.FONT_HERSHEY_SIMPLEX, 0.6, 1)
        cv2.rectangle(frame, (x1, y1 - text_h - 10), (x1 + text_w, y1), color, -1)
        cv2.putText(frame, text, (x1, y1 - 5), cv2.FONT_HERSHEY_SIMPLEX, 0.6, (0, 0, 0), 1)

    return frame

def encode_frame_base64(frame, quality=80):
    """Encode frame to base64 JPEG data URL for multimodal analysis."""
    try:
        encode_param = [cv2.IMWRITE_JPEG_QUALITY, quality]
        _, buffer = cv2.imencode('.jpg', frame, encode_param)
        b64_data = base64.b64encode(buffer).decode('utf-8')
        return f"data:image/jpeg;base64,{b64_data}"
    except Exception as e:
        logger.warning(f"Failed to encode frame: {e}")
        return ""

def send_webhook(camera_id, detections, frame_num, image_url=""):
    """Send detection results to lalmax-nvr webhook.

    Args:
        camera_id: Camera identifier
        detections: List of detection results
        frame_num: Frame number (PTS)
        image_url: Optional base64 encoded image for multimodal analysis
    """
    payload = {
        "camera_id": camera_id,
        "pts": frame_num,
        "timestamp": int(time.time() * 1000),
        "detections": detections
    }

    # Include image for multimodal LLM analysis
    if image_url:
        payload["image_url"] = image_url

    try:
        resp = requests.post(
            f"{NVR_URL}/api/ai/webhook",
            json=payload,
            headers=get_auth_headers(),
            timeout=5
        )
        if resp.status_code == 200:
            labels = [d["label"] for d in detections]
            logger.info(f"[{camera_id}] Webhook sent: {len(detections)} objects {labels}")
        else:
            logger.warning(f"Webhook returned status {resp.status_code}")
    except Exception as e:
        logger.error(f"Failed to send webhook: {e}")


def detect_in_regions(frame, detection_regions):
    """Run the model on each region crop and map boxes back to frame coordinates.

    Returns a list of dicts with label, class_id, confidence, box_xyxy (pixels).
    """
    detections = []
    for (rx0, ry0, rx1, ry1) in detection_regions:
        crop = frame[ry0:ry1, rx0:rx1]
        if crop.size == 0:
            continue
        bump_stat("inferences")
        results = model(crop, conf=CONFIDENCE, iou=NMS_THRESHOLD, verbose=False)
        for result in results:
            boxes = result.boxes
            if boxes is None:
                continue
            for box in boxes:
                x1, y1, x2, y2 = box.xyxy[0].tolist()
                detections.append({
                    "label": model.names[int(box.cls[0])],
                    "class_id": int(box.cls[0]),
                    "confidence": float(box.conf[0]),
                    "box_xyxy": (rx0 + x1, ry0 + y1, rx0 + x2, ry0 + y2),
                })
    return detections


def track_and_filter(camera_id, merged, suppressor):
    """Run ByteTrack over merged detections and suppress stationary objects.

    Returns (payload_detections, active_boxes) where active_boxes feed the
    next frame's region computation.
    """
    tracker = get_tracker(camera_id)
    sv_detections = sv.Detections(
        xyxy=np.array([d["box_xyxy"] for d in merged], dtype=np.float32),
        confidence=np.array([d["confidence"] for d in merged], dtype=np.float32),
        class_id=np.array([d["class_id"] for d in merged], dtype=int),
    )
    sv_detections = tracker.update_with_detections(sv_detections)

    payload = []
    active_boxes = []
    for i in range(len(sv_detections)):
        x1, y1, x2, y2 = sv_detections.xyxy[i]
        class_id = int(sv_detections.class_id[i]) if sv_detections.class_id is not None else 0
        confidence = float(sv_detections.confidence[i]) if sv_detections.confidence is not None else 0.0
        track_id = sv_detections.tracker_id[i] if sv_detections.tracker_id is not None else None

        box_xyxy = (int(x1), int(y1), int(x2), int(y2))
        state = "active"
        if track_id is not None:
            state = suppressor.update(int(track_id), box_xyxy)
        if state == "stationary":
            bump_stat("detections_suppressed")
            continue
        if state == "active":
            active_boxes.append(box_xyxy)

        payload.append({
            "class_id": class_id,
            "confidence": confidence,
            "box_xyxy": box_xyxy,
            "track_id": int(track_id) if track_id is not None else None,
        })
    return payload, active_boxes


def process_stream(camera_id, camera_name, stream_url):
    """Process a single camera stream"""
    logger.info(f"[{camera_id}] Starting AI detection for: {camera_name} ({stream_url})")

    while True:
        try:
            cap = cv2.VideoCapture(stream_url)
            if not cap.isOpened():
                logger.error(f"[{camera_id}] Failed to open stream, retrying in 5s...")
                time.sleep(5)
                continue

            logger.info(f"[{camera_id}] Connected to stream")
            frame_count = 0
            motion_detector = None  # created once frame size is known
            suppressor = StationarySuppressor(STATIONARY_THRESHOLD)
            # boxes of actively moving tracked objects from the previous
            # processed frame; they get regions even without motion
            object_boxes = []

            while True:
                ret, frame = cap.read()
                if not ret:
                    logger.warning(f"[{camera_id}] Stream ended, reconnecting...")
                    break

                frame_count += 1
                if frame_count % FRAME_SKIP != 0:
                    continue

                bump_stat("frames_processed")
                h, w = frame.shape[:2]

                # motion gating: cluster motion + tracked object boxes into
                # square regions; only those crops go through the model
                if MOTION_ONLY:
                    if motion_detector is None:
                        motion_detector = MotionDetector(
                            (h, w),
                            threshold=MOTION_THRESHOLD,
                            contour_area=MOTION_CONTOUR_AREA,
                            frame_height=MOTION_FRAME_HEIGHT,
                            lightning_threshold=MOTION_LIGHTNING_THRESHOLD,
                            improve_contrast=IMPROVE_CONTRAST,
                        )
                    motion_boxes = motion_detector.detect(frame)
                    candidate_boxes = list(motion_boxes) + object_boxes
                    if not candidate_boxes:
                        bump_stat("frames_no_motion")
                        object_boxes = []
                        suppressor.prune()
                        continue
                    detection_regions = compute_regions((h, w), candidate_boxes, REGION_MIN_SIZE)
                    if len(detection_regions) > MAX_REGIONS:
                        # too many regions: one full-frame pass is cheaper
                        detection_regions = [(0, 0, w, h)]
                else:
                    detection_regions = [(0, 0, w, h)]

                raw_detections = detect_in_regions(frame, detection_regions)
                merged = reduce_detections(raw_detections)

                detections = []
                if merged and ENABLE_TRACKING:
                    payload, object_boxes = track_and_filter(camera_id, merged, suppressor)
                    for p in payload:
                        x1, y1, x2, y2 = p["box_xyxy"]
                        detections.append({
                            "label": model.names[p["class_id"]],
                            "confidence": p["confidence"],
                            "box": [x1 / w, y1 / h, (x2 - x1) / w, (y2 - y1) / h],
                            "track_id": p["track_id"],
                        })
                elif merged:
                    object_boxes = [d["box_xyxy"] for d in merged]
                    for d in merged:
                        x1, y1, x2, y2 = d["box_xyxy"]
                        detections.append({
                            "label": d["label"],
                            "confidence": d["confidence"],
                            "box": [x1 / w, y1 / h, (x2 - x1) / w, (y2 - y1) / h],
                        })
                else:
                    object_boxes = []

                suppressor.prune()

                if detections:
                    logger.info(f"[{camera_id}] Detected {len(detections)} objects in {len(detection_regions)} region(s)")
                    # Draw detections on frame and encode
                    annotated_frame = draw_detections(frame.copy(), detections)
                    image_url = encode_frame_base64(annotated_frame)
                    send_webhook(camera_id, detections, frame_count, image_url)
                else:
                    logger.debug(f"[{camera_id}] Frame {frame_count}: No objects detected")

            cap.release()
            time.sleep(2)

        except Exception as e:
            logger.error(f"[{camera_id}] Error: {e}")
            time.sleep(5)

def sync_cameras():
    """Sync camera list from NVR and start processing for new cameras"""
    global active_cameras

    cameras = fetch_cameras()

    for cam in cameras:
        cam_id = cam.get("id", "")
        if not cam_id:
            continue

        if not cam.get("enabled", False):
            continue

        protocol = cam.get("protocol", "")
        if protocol not in ("onvif", "rtsp"):
            continue

        if cam_id in active_cameras:
            continue

        stream_url = get_stream_url(cam)
        if not stream_url:
            logger.warning(f"No stream URL for camera {cam_id}, skipping")
            continue

        active_cameras[cam_id] = True

        # Start processing in a separate thread
        thread = threading.Thread(
            target=process_stream,
            args=(cam_id, cam.get("name", "Unknown"), stream_url),
            daemon=True
        )
        thread.start()
        logger.info(f"Started processing for camera: {cam_id}")

def camera_sync_loop():
    """Periodically sync camera list"""
    while True:
        try:
            sync_cameras()
        except Exception as e:
            logger.error(f"Camera sync error: {e}")
        time.sleep(SYNC_INTERVAL)

def health_check():
    """Simple health check endpoint"""
    from http.server import HTTPServer, BaseHTTPRequestHandler

    class HealthHandler(BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path == "/health":
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                with stats_lock:
                    pipeline = dict(stats)
                response = {
                    "status": "healthy",
                    "model": YOLO_MODEL,
                    "active_cameras": len(active_cameras),
                    "pipeline": pipeline
                }
                self.wfile.write(json.dumps(response).encode())
            else:
                self.send_response(404)
                self.end_headers()

        def log_message(self, format, *args):
            pass  # Suppress health check logs

    port = int(os.getenv("PORT", "8080"))
    server = HTTPServer(("0.0.0.0", port), HealthHandler)
    logger.info(f"Health server listening on :{port}")
    server.serve_forever()

def main():
    global model

    logger.info(f"NVR URL: {NVR_URL}")
    logger.info(f"YOLO Model: {YOLO_MODEL}")
    logger.info(f"Confidence: {CONFIDENCE}")
    logger.info(f"Frame Skip: {FRAME_SKIP}")
    logger.info(f"Motion gating: {MOTION_ONLY} (threshold={MOTION_THRESHOLD}, region_min={REGION_MIN_SIZE}, stationary_threshold={STATIONARY_THRESHOLD})")

    # Load YOLO model
    logger.info(f"Loading YOLO model: {YOLO_MODEL}")
    model = YOLO(YOLO_MODEL)
    logger.info("YOLO model loaded successfully")

    # Start health check server
    health_thread = threading.Thread(target=health_check, daemon=True)
    health_thread.start()

    # Start camera sync loop
    sync_thread = threading.Thread(target=camera_sync_loop, daemon=True)
    sync_thread.start()

    # Initial sync
    sync_cameras()

    # Keep running
    logger.info("Service started, waiting for cameras...")
    try:
        while True:
            time.sleep(1)
    except KeyboardInterrupt:
        logger.info("Shutting down...")

if __name__ == "__main__":
    main()
