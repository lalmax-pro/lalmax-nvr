<script lang="ts">
  import type { Recording } from '$lib/api';
  import { formatDuration, formatFileSize } from '$lib/format';

  interface Props {
    recordings: Recording[];
    selectedRecording?: Recording;
    selectedHour?: number;
    onSelect: (recording: Recording) => void;
    onHourSelect?: (hour: number) => void;
    onSeek?: (recording: Recording, offsetSec: number) => void;
  }

  let { recordings, selectedRecording, selectedHour = -1, onSelect, onHourSelect, onSeek }: Props = $props();

  const HOURS = 24;
  
  // Zoom state: startHour and endHour define visible range
  let viewStart = $state(0);
  let viewEnd = $state(24);
  let zoomLevel = $state(1); // 1 = full day, 2 = 12h, 4 = 6h, etc.
  
  // Update view when selectedHour changes
  $effect(() => {
    if (selectedHour >= 0) {
      viewStart = selectedHour;
      viewEnd = selectedHour + 1;
      zoomLevel = 24;
    } else {
      viewStart = 0;
      viewEnd = 24;
      zoomLevel = 1;
    }
  });

  function getHourLabels(): string[] {
    const labels: string[] = [];
    const range = viewEnd - viewStart;
    let step: number;
    
    if (range <= 1) step = 5 / 60; // 5 min intervals
    else if (range <= 2) step = 10 / 60; // 10 min intervals  
    else if (range <= 6) step = 0.5; // 30 min intervals
    else if (range <= 12) step = 1; // 1 hour intervals
    else step = 2; // 2 hour intervals
    
    for (let h = viewStart; h <= viewEnd; h += step) {
      const hour = Math.floor(h);
      const min = Math.round((h - hour) * 60);
      labels.push(`${String(hour).padStart(2, '0')}:${String(min).padStart(2, '0')}`);
    }
    return labels;
  }

  function getBlockStyle(recording: Recording): string {
    const start = new Date(recording.started_at);
    const end = new Date(recording.ended_at);
    const dayStart = new Date(start);
    dayStart.setHours(0, 0, 0, 0);

    const startMs = start.getTime() - dayStart.getTime();
    const durationMs = end.getTime() - start.getTime();
    const dayMs = 24 * 60 * 60 * 1000;

    const startHour = startMs / (60 * 60 * 1000);
    const endHour = startHour + durationMs / (60 * 60 * 1000);
    
    // Calculate position relative to view range
    const viewRange = viewEnd - viewStart;
    const left = ((startHour - viewStart) / viewRange) * 100;
    const width = ((endHour - startHour) / viewRange) * 100;

    return `left: ${left}%; width: ${Math.max(width, 0.5)}%;`;
  }

  function getBlockColor(recording: Recording): string {
    switch (recording.format) {
      case 'h264':
      case 'h265':
        return 'bg-blue-500 hover:bg-blue-400';
      case 'mjpeg':
        return 'bg-green-500 hover:bg-green-400';
      case 'timelapse':
        return 'bg-purple-500 hover:bg-purple-400';
      default:
        return 'bg-gray-500 hover:bg-gray-400';
    }
  }

  function formatTime(dateStr: string): string {
    const d = new Date(dateStr);
    return d.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', hour12: false });
  }

  function formatHourMinute(hour: number): string {
    const h = Math.floor(hour);
    const min = Math.round((hour - h) * 60);
    return `${String(h).padStart(2, '0')}:${String(min).padStart(2, '0')}`;
  }

  let hoveredRecording = $state<Recording | null>(null);
  let tooltipX = $state(0);
  let tooltipY = $state(0);

  function handleMouseMove(e: MouseEvent, recording: Recording) {
    hoveredRecording = recording;
    tooltipX = e.clientX;
    tooltipY = e.clientY - 60;
  }

  function handleMouseLeave() {
    hoveredRecording = null;
  }

  let timelineEl: HTMLDivElement | undefined = $state();
  let isDragging = $state(false);
  let dragStartX = $state(0);
  let dragStartScrollLeft = $state(0);
  let dragMoved = $state(false);

  function handleWheel(e: WheelEvent) {
    e.preventDefault();
    
    if (e.ctrlKey || e.metaKey) {
      // Ctrl+wheel = zoom
      const rect = timelineEl?.getBoundingClientRect();
      if (!rect) return;
      
      // Mouse position as percentage of timeline width
      const mouseXPercent = (e.clientX - rect.left) / rect.width;
      const mouseHour = viewStart + mouseXPercent * (viewEnd - viewStart);
      
      // Zoom factor
      const zoomFactor = e.deltaY > 0 ? 0.8 : 1.25;
      const newRange = Math.max(0.5, Math.min(24, (viewEnd - viewStart) * zoomFactor));
      
      // Keep mouse position stable
      viewStart = Math.max(0, Math.min(24 - newRange, mouseHour - mouseXPercent * newRange));
      viewEnd = viewStart + newRange;
      zoomLevel = 24 / newRange;
      
      // Notify parent of hour selection change
      if (onHourSelect && newRange <= 1.5) {
        onHourSelect(Math.floor(viewStart));
      } else if (onHourSelect && newRange >= 24) {
        onHourSelect(-1);
      }
    } else {
      // Plain wheel = horizontal scroll (pan)
      const range = viewEnd - viewStart;
      const panAmount = (e.deltaY > 0 ? 1 : -1) * range * 0.1;
      
      const newStart = Math.max(0, Math.min(24 - range, viewStart + panAmount));
      viewStart = newStart;
      viewEnd = newStart + range;
    }
  }

  function handleMouseDown(e: MouseEvent) {
    if (e.button !== 0) return;
    isDragging = true;
    dragMoved = false;
    dragStartX = e.clientX;
    if (timelineEl) {
      dragStartScrollLeft = timelineEl.scrollLeft;
    }
  }

  function handleMouseMoveGlobal(e: MouseEvent) {
    if (!isDragging || !timelineEl) return;
    
    const dx = e.clientX - dragStartX;
    if (Math.abs(dx) > 3) dragMoved = true;
    const rect = timelineEl.getBoundingClientRect();
    const range = viewEnd - viewStart;
    const hourPerPixel = range / rect.width;
    const hourDelta = -dx * hourPerPixel;
    
    const newStart = Math.max(0, Math.min(24 - range, viewStart + hourDelta));
    viewStart = newStart;
    viewEnd = newStart + range;
    dragStartX = e.clientX;
  }

  function handleMouseUp() {
    isDragging = false;
  }

  function hourFromClientX(clientX: number): number {
    const rect = timelineEl?.getBoundingClientRect();
    if (!rect || rect.width <= 0) return viewStart;
    const pct = Math.min(1, Math.max(0, (clientX - rect.left) / rect.width));
    return viewStart + pct * (viewEnd - viewStart);
  }

  function snapSeekAtHour(hour: number) {
    if (!onSeek || recordings.length === 0) return;
    const dayStart = new Date(recordings[0].started_at);
    dayStart.setHours(0, 0, 0, 0);
    const t = dayStart.getTime() + hour * 60 * 60 * 1000;
    let best = recordings[0];
    let bestDist = Infinity;
    let offsetSec = 0;
    for (const rec of recordings) {
      const start = new Date(rec.started_at).getTime();
      const end = new Date(rec.ended_at).getTime();
      if (t >= start && t <= end) {
        onSeek(rec, Math.max(0, (t - start) / 1000));
        return;
      }
      const distStart = Math.abs(t - start);
      const distEnd = Math.abs(t - end);
      if (distStart < bestDist) {
        bestDist = distStart;
        best = rec;
        offsetSec = 0;
      }
      if (distEnd < bestDist) {
        bestDist = distEnd;
        best = rec;
        offsetSec = Math.max(0, (end - start) / 1000);
      }
    }
    onSeek(best, offsetSec);
  }

  function handleTimelineClick(e: MouseEvent) {
    if (dragMoved) return;
    if ((e.target as HTMLElement).closest('button')) return;
    snapSeekAtHour(hourFromClientX(e.clientX));
  }

  function resetZoom() {
    viewStart = 0;
    viewEnd = 24;
    zoomLevel = 1;
    if (onHourSelect) onHourSelect(-1);
  }

  function zoomToHour(hour: number) {
    viewStart = hour;
    viewEnd = hour + 1;
    zoomLevel = 24;
    if (onHourSelect) onHourSelect(hour);
  }

  // Check if recording is visible in current view
  function isRecordingVisible(recording: Recording): boolean {
    const start = new Date(recording.started_at);
    const end = new Date(recording.ended_at);
    const dayStart = new Date(start);
    dayStart.setHours(0, 0, 0, 0);
    
    const startHour = (start.getTime() - dayStart.getTime()) / (60 * 60 * 1000);
    const endHour = startHour + (end.getTime() - start.getTime()) / (60 * 60 * 1000);
    
    return endHour > viewStart && startHour < viewEnd;
  }

  const visibleRecordings = $derived(recordings.filter(isRecordingVisible));
</script>

<div class="timeline-container">
  <!-- Hour markers -->
  <div class="flex justify-between px-1 mb-1 select-none">
    {#each getHourLabels() as label}
      <span class="text-xs th-text-tertiary font-mono">{label}</span>
    {/each}
  </div>

  <!-- Timeline bar -->
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <div 
    bind:this={timelineEl}
    class="relative h-12 th-bg-tertiary rounded-lg cursor-grab select-none"
    class:cursor-grabbing={isDragging}
    style="overflow: hidden;"
    onwheel={handleWheel}
    onmousedown={handleMouseDown}
    onclick={handleTimelineClick}
    tabindex="0"
    role="slider"
    aria-label="Timeline"
    aria-valuemin={0}
    aria-valuemax={100}
    aria-valuenow={0}
  >
    {#each visibleRecordings as recording (recording.id)}
      <button
        class="absolute top-0 h-full rounded-sm transition-opacity {getBlockColor(recording)}"
        class:ring-2={selectedRecording?.id === recording.id}
        class:ring-white={selectedRecording?.id === recording.id}
        class:ring-offset-1={selectedRecording?.id === recording.id}
        class:opacity-70={selectedRecording?.id !== recording.id}
        style={getBlockStyle(recording)}
        onclick={(e) => {
          e.stopPropagation();
          if (onSeek) {
            const start = new Date(recording.started_at).getTime();
            const dayStart = new Date(recording.started_at);
            dayStart.setHours(0, 0, 0, 0);
            const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
            const ratio = Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width));
            const dur = (new Date(recording.ended_at).getTime() - start) / 1000;
            onSeek(recording, ratio * dur);
          } else {
            onSelect(recording);
          }
        }}
        onmousemove={(e) => handleMouseMove(e, recording)}
        onmouseleave={handleMouseLeave}
        aria-label="Recording {formatTime(recording.started_at)} - {formatTime(recording.ended_at)}"
      ></button>
    {/each}

    <!-- Empty state -->
    {#if visibleRecordings.length === 0}
      <div class="absolute inset-0 flex items-center justify-center">
        <span class="text-sm th-text-muted">
          {recordings.length === 0 ? 'No recordings for this day' : 'No recordings in this time range'}
        </span>
      </div>
    {/if}

    <!-- Zoom controls -->
    <div class="absolute top-1 right-1 flex gap-1 z-10">
      {#if zoomLevel > 1}
        <button 
          onclick={(e) => { e.stopPropagation(); resetZoom(); }}
          class="btn btn-ghost btn-xs th-bg-primary/80 hover:th-bg-primary"
          title="Reset zoom (show full day)"
        >
          Reset
        </button>
      {/if}
      <button 
        onclick={(e) => { e.stopPropagation(); zoomLevel = Math.min(24, zoomLevel * 2); viewEnd = viewStart + 24/zoomLevel; }}
        class="btn btn-ghost btn-xs th-bg-primary/80 hover:th-bg-primary"
        title="Zoom in (Ctrl+Scroll)"
      >
        +
      </button>
      <button 
        onclick={(e) => { e.stopPropagation(); zoomLevel = Math.max(1, zoomLevel / 2); viewEnd = Math.min(24, viewStart + 24/zoomLevel); }}
        class="btn btn-ghost btn-xs th-bg-primary/80 hover:th-bg-primary"
        title="Zoom out (Ctrl+Scroll)"
      >
        -
      </button>
    </div>
  </div>
  
  <!-- Zoom hint -->
  {#if zoomLevel === 1}
    <div class="text-xs th-text-tertiary mt-1 text-center">
      Ctrl+Scroll to zoom • Scroll to pan • Click hour to focus
    </div>
  {/if}
</div>

<svelte:window onmousemove={handleMouseMoveGlobal} onmouseup={handleMouseUp} />

<!-- Tooltip -->
{#if hoveredRecording}
  <div
    class="fixed z-50 px-3 py-2 rounded-lg shadow-lg th-bg-primary border th-border text-sm pointer-events-none"
    style="left: {tooltipX}px; top: {tooltipY}px; transform: translateX(-50%);"
  >
    <div class="font-medium th-text-primary">
      {formatTime(hoveredRecording.started_at)} - {formatTime(hoveredRecording.ended_at)}
    </div>
    <div class="th-text-secondary text-xs">
      {formatDuration(hoveredRecording.duration)} · {formatFileSize(hoveredRecording.file_size)} · {hoveredRecording.format.toUpperCase()}
    </div>
  </div>
{/if}
