/**
 * Chart.js configuration factory functions.
 * Uses dynamic import for tree-shaking — Chart.js is only loaded when charts are rendered.
 */
import { getChartUnit } from './format';
import { getEffectiveTheme } from './preferences';

/** Bar color palette for camera charts */
export const BAR_COLORS = [
  'rgba(139, 92, 246, 0.7)',
  'rgba(56, 189, 248, 0.7)',
  'rgba(16, 185, 129, 0.7)',
  'rgba(245, 158, 11, 0.7)',
  'rgba(239, 68, 68, 0.7)',
  'rgba(168, 85, 247, 0.7)',
  'rgba(34, 197, 94, 0.7)',
  'rgba(251, 146, 60, 0.7)',
];

/** Cached Chart.js module (lazy-loaded once) */
let _chartModule = null;

/**
 * Dynamically import and register Chart.js components.
 * Returns the Chart constructor. Only loads once, then caches.
 */
export async function loadChart() {
  if (_chartModule) return _chartModule;

  const {
    Chart,
    CategoryScale,
    LinearScale,
    BarController,
    BarElement,
    LineController,
    LineElement,
    PointElement,
    Filler,
    Tooltip,
    Legend,
    Title,
  } = await import('chart.js');

  Chart.register(
    CategoryScale,
    LinearScale,
    BarController,
    BarElement,
    LineController,
    LineElement,
    PointElement,
    Filler,
    Tooltip,
    Legend,
    Title,
  );

  _chartModule = Chart;
  return Chart;
}

/** Get theme-aware chart colors */
export function getChartThemeColors() {
  const isDark = getEffectiveTheme() === 'dark';
  return {
    gridColor: isDark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.06)',
    textColor: isDark ? '#a1a1a1' : '#4b5563',
    accentColor: 'rgba(139, 92, 246, 0.8)',
    accentFill: 'rgba(139, 92, 246, 0.1)',
  };
}

/**
 * Create the storage trend line chart.
 * @param {import('chart.js')} Chart - Chart constructor
 * @param {HTMLCanvasElement} canvas
 * @param {{ date: string; total_size: number }[]} trends
 * @returns {import('chart.js').Chart | null}
 */
export function createTrendChart(Chart, canvas, trends) {
  if (!canvas) return null;

  const { gridColor, textColor, accentColor, accentFill } = getChartThemeColors();
  const labels = trends.map((d) => d.date.slice(5));
  const rawSizes = trends.map((d) => d.total_size);
  const chartUnit = getChartUnit(rawSizes);
  const sizes = rawSizes.map((s) => +(s / chartUnit.divisor).toFixed(1));

  return new Chart(canvas, {
    type: 'line',
    data: {
      labels,
      datasets: [
        {
          label: `Storage (${chartUnit.unit})`,
          data: sizes,
          borderColor: accentColor,
          backgroundColor: accentFill,
          fill: true,
          tension: 0.3,
          pointRadius: 4,
          pointBackgroundColor: accentColor,
        },
      ],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        legend: { labels: { color: textColor } },
        tooltip: { mode: 'index', intersect: false },
      },
      scales: {
        x: { grid: { color: gridColor }, ticks: { color: textColor } },
        y: { grid: { color: gridColor }, ticks: { color: textColor }, beginAtZero: true },
      },
    },
  });
}

/**
 * Format a Unix timestamp (seconds) to a short time label (HH:MM).
 * @param {number} ts
 * @returns {string}
 */
function fmtTime(ts) {
  const d = new Date(ts * 1000);
  return d.getHours().toString().padStart(2, '0') + ':' + d.getMinutes().toString().padStart(2, '0');
}

/**
 * Create a system-resource time-series line chart.
 * @param {import('chart.js')} Chart
 * @param {HTMLCanvasElement} canvas
 * @param {{ ts: number; cpu: number; mem: number; net_up: number; net_dn: number }[]} samples
 * @param {'cpu'|'mem'|'net'} metric  which metric to chart
 * @param {string} label  display label
 * @returns {import('chart.js').Chart | null}
 */
export function createSystemMetricChart(Chart, canvas, samples, metric, label) {
  if (!canvas || !samples || samples.length === 0) return null;
  const { gridColor, textColor, accentColor, accentFill } = getChartThemeColors();

  const labels = samples.map((s) => fmtTime(s.ts));
  let data;
  let unit = '';
  if (metric === 'cpu') {
    data = samples.map((s) => +s.cpu.toFixed(1));
    unit = '%';
  } else if (metric === 'mem') {
    data = samples.map((s) => +s.mem.toFixed(1));
    unit = '%';
  } else if (metric === 'goroutines') {
    data = samples.map((s) => s.goroutines ?? 0);
  } else {
    // net: show upload + download as two datasets, in KB/s
    const up = samples.map((s) => +(s.net_up / 1024).toFixed(1));
    const dn = samples.map((s) => +(s.net_dn / 1024).toFixed(1));
    return new Chart(canvas, {
      type: 'line',
      data: {
        labels,
        datasets: [
          {
            label: '↑ KB/s',
            data: up,
            borderColor: 'rgba(56, 189, 248, 0.8)',
            backgroundColor: 'rgba(56, 189, 248, 0.08)',
            fill: true,
            tension: 0.3,
            pointRadius: 0,
            borderWidth: 1.5,
          },
          {
            label: '↓ KB/s',
            data: dn,
            borderColor: 'rgba(16, 185, 129, 0.8)',
            backgroundColor: 'rgba(16, 185, 129, 0.08)',
            fill: true,
            tension: 0.3,
            pointRadius: 0,
            borderWidth: 1.5,
          },
        ],
      },
      options: netChartOptions(gridColor, textColor),
    });
  }

  return new Chart(canvas, {
    type: 'line',
    data: {
      labels,
      datasets: [
        {
          label: `${label} (${unit})`,
          data,
          borderColor: accentColor,
          backgroundColor: accentFill,
          fill: true,
          tension: 0.3,
          pointRadius: 0,
          borderWidth: 1.5,
        },
      ],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        legend: { display: false },
        tooltip: { mode: 'index', intersect: false },
      },
      scales: {
        x: { grid: { color: gridColor }, ticks: { color: textColor, maxTicksLimit: 6, maxRotation: 0 } },
        y: {
          grid: { color: gridColor },
          ticks: { color: textColor, callback: (v) => v + unit },
          beginAtZero: true,
          ...(unit === '%' ? { max: 100 } : {}),
        },
      },
    },
  });
}

function netChartOptions(gridColor, textColor) {
  return {
    responsive: true,
    maintainAspectRatio: false,
    plugins: {
      legend: { labels: { color: textColor, boxWidth: 12, font: { size: 11 } } },
      tooltip: { mode: 'index', intersect: false },
    },
    scales: {
      x: { grid: { color: gridColor }, ticks: { color: textColor, maxTicksLimit: 6, maxRotation: 0 } },
      y: { grid: { color: gridColor }, ticks: { color: textColor, callback: (v) => v + ' KB/s' }, beginAtZero: true },
    },
  };
}

/**
 * Update an existing system-metric chart in place (no destroy/recreate, no flicker).
 * @param {import('chart.js').Chart} chart
 * @param {{ ts: number; cpu: number; mem: number; net_up: number; net_dn: number }[]} samples
 * @param {'cpu'|'mem'|'net'} metric
 */
export function updateSystemMetricChart(chart, samples, metric) {
  if (!chart || !samples || samples.length === 0) return;
  chart.data.labels = samples.map((s) => fmtTime(s.ts));
  if (metric === 'net') {
    chart.data.datasets[0].data = samples.map((s) => +(s.net_up / 1024).toFixed(1));
    chart.data.datasets[1].data = samples.map((s) => +(s.net_dn / 1024).toFixed(1));
  } else if (metric === 'cpu') {
    chart.data.datasets[0].data = samples.map((s) => +s.cpu.toFixed(1));
  } else if (metric === 'goroutines') {
    chart.data.datasets[0].data = samples.map((s) => s.goroutines ?? 0);
  } else {
    chart.data.datasets[0].data = samples.map((s) => +s.mem.toFixed(1));
  }
  chart.update('none'); // 'none' = skip animation for live updates
}

/**
 * Create a stream real-time metric chart (FPS / bitrate / subscribers).
 * @param {import('chart.js')} Chart
 * @param {HTMLCanvasElement} canvas
 * @param {{ ts: number; value: number }[]} samples
 * @param {string} label  y-axis label text
 * @param {string} unit   e.g. 'fps', 'Kbps', ''
 * @param {string} [color]  optional border color override
 * @returns {import('chart.js').Chart | null}
 */
export function createStreamMetricChart(Chart, canvas, samples, label, unit, color) {
  if (!canvas) return null;
  const { gridColor, textColor } = getChartThemeColors();
  const borderColor = color ?? 'rgba(56, 189, 248, 0.85)';
  const fillColor = color ? color.replace(/[\d.]+\)$/, '0.08)') : 'rgba(56, 189, 248, 0.08)';

  return new Chart(canvas, {
    type: 'line',
    data: {
      labels: samples.map((s) => fmtTimeSec(s.ts)),
      datasets: [
        {
          label: `${label}${unit ? ' (' + unit + ')' : ''}`,
          data: samples.map((s) => s.value),
          borderColor: borderColor,
          backgroundColor: fillColor,
          fill: true,
          tension: 0.35,
          pointRadius: 0,
          borderWidth: 1.5,
        },
      ],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      animation: false,
      plugins: {
        legend: { display: false },
        tooltip: {
          mode: 'index',
          intersect: false,
          callbacks: { label: (ctx) => `${ctx.parsed.y} ${unit}` },
        },
      },
      scales: {
        x: {
          grid: { color: gridColor },
          ticks: { color: textColor, maxTicksLimit: 5, maxRotation: 0 },
        },
        y: {
          grid: { color: gridColor },
          ticks: { color: textColor, callback: (v) => v + (unit ? ' ' + unit : '') },
          beginAtZero: true,
        },
      },
    },
  });
}

/**
 * Update a stream metric chart in place without flicker.
 * @param {import('chart.js').Chart} chart
 * @param {{ ts: number; value: number }[]} samples
 */
export function updateStreamMetricChart(chart, samples) {
  if (!chart || !samples || samples.length === 0) return;
  chart.data.labels = samples.map((s) => fmtTimeSec(s.ts));
  chart.data.datasets[0].data = samples.map((s) => s.value);
  chart.update('none');
}

/** Format Unix timestamp (seconds) as HH:MM:SS for stream charts */
function fmtTimeSec(ts) {
  const d = new Date(ts * 1000);
  const hh = d.getHours().toString().padStart(2, '0');
  const mm = d.getMinutes().toString().padStart(2, '0');
  const ss = d.getSeconds().toString().padStart(2, '0');
  return `${hh}:${mm}:${ss}`;
}

/**
 * Create the hourly recording activity bar chart.
 * @param {import('chart.js')} Chart
 * @param {HTMLCanvasElement} canvas
 * @param {{ hour: string; recordings: number; total_size: number }[]} hourly
 * @returns {import('chart.js').Chart | null}
 */
export function createHourlyActivityChart(Chart, canvas, hourly) {
  if (!canvas || !hourly || hourly.length === 0) return null;
  const { gridColor, textColor } = getChartThemeColors();

  const labels = hourly.map((h) => {
    const d = new Date(h.hour);
    return d.getMonth() + 1 + '/' + d.getDate() + ' ' + d.getHours() + 'h';
  });
  const data = hourly.map((h) => h.recordings);

  return new Chart(canvas, {
    type: 'bar',
    data: {
      labels,
      datasets: [
        {
          label: 'Recordings',
          data,
          backgroundColor: 'rgba(139, 92, 246, 0.6)',
          borderRadius: 3,
          borderSkipped: false,
        },
      ],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        legend: { display: false },
        tooltip: { mode: 'index', intersect: false },
      },
      scales: {
        x: { grid: { display: false }, ticks: { color: textColor, maxTicksLimit: 12, maxRotation: 45 } },
        y: { grid: { color: gridColor }, ticks: { color: textColor }, beginAtZero: true },
      },
    },
  });
}

/**
 * Aggregate camera recording counts from trend data.
 * @param {{ date: string; total_size: number; cameras?: Record<string, number> }[]} trends
 * @returns {Record<string, number>}
 */
export function aggregateCameraTotals(trends) {
  const totals = {};
  trends.forEach((d) => {
    if (d.cameras) {
      Object.entries(d.cameras).forEach(([cam, count]) => {
        totals[cam] = (totals[cam] || 0) + count;
      });
    }
  });
  return totals;
}

/**
 * Create the recordings-per-camera bar chart.
 * @param {import('chart.js')} Chart - Chart constructor
 * @param {HTMLCanvasElement} canvas
 * @param {Record<string, number>} cameraTotals
 * @param {string[]} allCameraNames - Full list for color index mapping
 * @param {Set<string>} selectedCameras - Currently selected camera filter
 * @returns {import('chart.js').Chart | null}
 */
export function createCameraChart(Chart, canvas, cameraTotals, allCameraNames, selectedCameras) {
  if (!canvas || Object.keys(cameraTotals).length === 0) return null;

  const { gridColor, textColor } = getChartThemeColors();
  const camLabels = Object.keys(cameraTotals).filter((name) => selectedCameras.has(name));
  const camData = camLabels.map((name) => cameraTotals[name]);
  if (camLabels.length === 0) return null;

  const camBarColors = camLabels.map((name) => {
    const idx = allCameraNames.indexOf(name) % BAR_COLORS.length;
    return BAR_COLORS[idx];
  });

  return new Chart(canvas, {
    type: 'bar',
    data: {
      labels: camLabels,
      datasets: [
        {
          label: 'Recordings',
          data: camData,
          backgroundColor: camBarColors,
          borderRadius: 6,
        },
      ],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        legend: { display: false },
        tooltip: { mode: 'index', intersect: false },
      },
      scales: {
        x: { grid: { display: false }, ticks: { color: textColor } },
        y: { grid: { color: gridColor }, ticks: { color: textColor }, beginAtZero: true },
      },
    },
  });
}

/** Create the API request-rate and server-error chart. */
export function createAPIRequestChart(Chart, canvas, samples, rpsLabel, errorsLabel) {
  if (!canvas || !samples) return null;
  const { gridColor, textColor } = getChartThemeColors();
  return new Chart(canvas, {
    type: 'line',
    data: {
      labels: samples.map((s) => fmtTimeSec(s.timestamp)),
      datasets: [
        {
          label: rpsLabel,
          data: samples.map((s) => s.rps),
          borderColor: 'rgba(56, 189, 248, 0.9)',
          backgroundColor: 'rgba(56, 189, 248, 0.08)',
          fill: true,
          tension: 0.3,
          pointRadius: 0,
          borderWidth: 1.5,
          yAxisID: 'y',
        },
        {
          label: errorsLabel,
          data: samples.map((s) => s.status_5xx),
          borderColor: 'rgba(239, 68, 68, 0.9)',
          backgroundColor: 'rgba(239, 68, 68, 0.08)',
          tension: 0.3,
          pointRadius: 0,
          borderWidth: 1.5,
          yAxisID: 'errors',
        },
      ],
    },
    options: apiChartOptions(gridColor, textColor, 'req/s', 'errors'),
  });
}

/** Create the API P95 latency and 5xx-rate chart. */
export function createAPILatencyChart(Chart, canvas, samples, p95Label, errorRateLabel) {
  if (!canvas || !samples) return null;
  const { gridColor, textColor } = getChartThemeColors();
  return new Chart(canvas, {
    type: 'line',
    data: {
      labels: samples.map((s) => fmtTimeSec(s.timestamp)),
      datasets: [
        {
          label: p95Label,
          data: samples.map((s) => s.p95_ms),
          borderColor: 'rgba(139, 92, 246, 0.9)',
          backgroundColor: 'rgba(139, 92, 246, 0.08)',
          fill: true,
          tension: 0.3,
          pointRadius: 0,
          borderWidth: 1.5,
          yAxisID: 'y',
        },
        {
          label: errorRateLabel,
          data: samples.map((s) => +(s.error_rate * 100).toFixed(2)),
          borderColor: 'rgba(245, 158, 11, 0.9)',
          backgroundColor: 'rgba(245, 158, 11, 0.08)',
          tension: 0.3,
          pointRadius: 0,
          borderWidth: 1.5,
          yAxisID: 'errors',
        },
      ],
    },
    options: apiChartOptions(gridColor, textColor, 'ms', '%'),
  });
}

/** Update either API chart without recreating its canvas. */
export function updateAPIChart(chart, samples, kind) {
  if (!chart || !samples) return;
  chart.data.labels = samples.map((s) => fmtTimeSec(s.timestamp));
  if (kind === 'requests') {
    chart.data.datasets[0].data = samples.map((s) => s.rps);
    chart.data.datasets[1].data = samples.map((s) => s.status_5xx);
  } else {
    chart.data.datasets[0].data = samples.map((s) => s.p95_ms);
    chart.data.datasets[1].data = samples.map((s) => +(s.error_rate * 100).toFixed(2));
  }
  chart.update('none');
}

function apiChartOptions(gridColor, textColor, primaryUnit, secondaryUnit) {
  return {
    responsive: true,
    maintainAspectRatio: false,
    animation: false,
    interaction: { mode: 'index', intersect: false },
    plugins: {
      legend: { labels: { color: textColor, boxWidth: 12, font: { size: 11 } } },
      tooltip: { mode: 'index', intersect: false },
    },
    scales: {
      x: { grid: { color: gridColor }, ticks: { color: textColor, maxTicksLimit: 6, maxRotation: 0 } },
      y: {
        position: 'left',
        beginAtZero: true,
        grid: { color: gridColor },
        ticks: { color: textColor, callback: (v) => `${v} ${primaryUnit}` },
      },
      errors: {
        position: 'right',
        beginAtZero: true,
        grid: { display: false },
        ticks: { color: textColor, callback: (v) => `${v} ${secondaryUnit}` },
      },
    },
  };
}
