import os
import time
import json
import base64
import logging
import threading
from typing import List, Optional, Dict
from contextlib import asynccontextmanager

import requests
import cv2
import numpy as np
from ultralytics import YOLO
import supervision as sv

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
MOTION_ONLY = os.getenv("MOTION_ONLY", "true").lower() == "true"
MOTION_THRESHOLD = float(os.getenv("MOTION_THRESHOLD", "25"))
MOTION_MIN_AREA = int(os.getenv("MOTION_MIN_AREA", "500"))

# Global state
model = None
active_cameras = {}
# Per-camera trackers for object tracking
trackers: Dict[str, sv.ByteTrack] = {}
ENABLE_TRACKING = os.getenv("ENABLE_TRACKING", "true").lower() == "true"

# Per-camera previous frames for motion detection
prev_frames: Dict[str, np.ndarray] = {}


def detect_motion_regions(camera_id: str, frame: np.ndarray) -> list:
    """Detect motion regions by comparing with previous frame."""
    global prev_frames
    
    gray = cv2.cvtColor(frame, cv2.COLOR_BGR2GRAY)
    gray = cv2.GaussianBlur(gray, (21, 21), 0)
    
    if camera_id not in prev_frames:
        prev_frames[camera_id] = gray
        return []  # No previous frame to compare
    
    prev_gray = prev_frames[camera_id]
    prev_frames[camera_id] = gray
    
    # Frame difference
    frame_delta = cv2.absdiff(prev_gray, gray)
    thresh = cv2.threshold(frame_delta, MOTION_THRESHOLD, 255, cv2.THRESH_BINARY)[1]
    thresh = cv2.dilate(thresh, None, iterations=2)
    
    # Find contours
    contours, _ = cv2.findContours(thresh.copy(), cv2.RETR_EXTERNAL, cv2.CHAIN_APPROX_SIMPLE)
    
    motion_regions = []
    for contour in contours:
        if cv2.contourArea(contour) < MOTION_MIN_AREA:
            continue
        (x, y, w, h) = cv2.boundingRect(contour)
        motion_regions.append((x, y, x + w, y + h))
    
    return motion_regions


def is_in_motion_region(box: tuple, motion_regions: list) -> bool:
    """Check if a detection box overlaps with any motion region."""
    if not motion_regions:
        return True  # If no motion regions, allow all detections
    
    x1, y1, x2, y2 = box
    for mx1, my1, mx2, my2 in motion_regions:
        # Check overlap
        if x1 < mx2 and x2 > mx1 and y1 < my2 and y2 > my1:
            return True
    return False


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
            detection_count = 0
            
            while True:
                ret, frame = cap.read()
                if not ret:
                    logger.warning(f"[{camera_id}] Stream ended, reconnecting...")
                    break
                
                frame_count += 1
                if frame_count % FRAME_SKIP != 0:
                    continue
                
                detection_count += 1
                if detection_count % 10 == 0:
                    logger.info(f"[{camera_id}] Processing frame {frame_count}, detections so far: {detection_count}")
                
                # Detect motion regions if MOTION_ONLY is enabled
                motion_regions = []
                if MOTION_ONLY:
                    motion_regions = detect_motion_regions(camera_id, frame)
                    if not motion_regions:
                        logger.debug(f"[{camera_id}] Frame {frame_count}: No motion detected")
                        continue
                
                # Run YOLO detection
                results = model(frame, conf=CONFIDENCE, iou=NMS_THRESHOLD, verbose=False)
                h, w = frame.shape[:2]

                detections = []
                for result in results:
                    boxes = result.boxes
                    if boxes is not None and len(boxes) > 0:
                        if ENABLE_TRACKING:
                            # Convert to supervision Detections format for tracking
                            sv_detections = sv.Detections.from_ultralytics(result)
                            tracker = get_tracker(camera_id)
                            sv_detections = tracker.update_with_detections(sv_detections)

                            # Extract tracked detections
                            for i in range(len(sv_detections)):
                                x1, y1, x2, y2 = sv_detections.xyxy[i]
                                class_id = sv_detections.class_id[i] if sv_detections.class_id is not None else 0
                                confidence = sv_detections.confidence[i] if sv_detections.confidence is not None else 0.0
                                track_id = sv_detections.tracker_id[i] if sv_detections.tracker_id is not None else None

                                # Check if detection is in motion region
                                if MOTION_ONLY and not is_in_motion_region((x1, y1, x2, y2), motion_regions):
                                    continue

                                detection = {
                                    "label": model.names[int(class_id)],
                                    "confidence": float(confidence),
                                    "box": [
                                        float(x1) / w,
                                        float(y1) / h,
                                        float(x2 - x1) / w,
                                        float(y2 - y1) / h
                                    ],
                                    "track_id": int(track_id) if track_id is not None else None
                                }
                                detections.append(detection)
                        else:
                            # No tracking, just return raw detections
                            for box in boxes:
                                x1, y1, x2, y2 = box.xyxy[0].tolist()

                                # Check if detection is in motion region
                                if MOTION_ONLY and not is_in_motion_region((x1, y1, x2, y2), motion_regions):
                                    continue

                                detection = {
                                    "label": model.names[int(box.cls[0])],
                                    "confidence": float(box.conf[0]),
                                    "box": [
                                        x1 / w,
                                        y1 / h,
                                        (x2 - x1) / w,
                                        (y2 - y1) / h
                                    ]
                                }
                                detections.append(detection)
                
                if detections:
                    logger.info(f"[{camera_id}] Detected {len(detections)} objects in motion regions")
                    # Draw detections on frame and encode
                    annotated_frame = draw_detections(frame.copy(), detections)
                    image_url = encode_frame_base64(annotated_frame)
                    send_webhook(camera_id, detections, frame_count, image_url)
                else:
                    logger.debug(f"[{camera_id}] Frame {frame_count}: No objects in motion regions")
            
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
                response = {
                    "status": "healthy",
                    "model": YOLO_MODEL,
                    "active_cameras": len(active_cameras)
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
