<script lang="ts">
  import { t } from '$lib/i18n';
  import type { RecordingScheduleRange } from '$lib/api';
  import { showToast } from '$lib/toast';
  import { Plus, Trash2, Copy } from 'lucide-svelte';

  interface Props {
    ranges?: RecordingScheduleRange[];
    onchange?: (ranges: RecordingScheduleRange[]) => void;
    showSave?: boolean;
    onsave?: (ranges: RecordingScheduleRange[]) => void | Promise<void>;
    saving?: boolean;
    loading?: boolean;
  }

  let {
    ranges = [],
    onchange,
    showSave = false,
    onsave,
    saving = false,
    loading = false,
  }: Props = $props();

  const dayLabels = $derived([0, 1, 2, 3, 4, 5, 6].map((d) => t(`cameras.weekday.${d}`)));

  function emit(next: RecordingScheduleRange[]) {
    onchange?.(next);
  }

  function addRange(day: number) {
    emit([...ranges, { day_of_week: day, start_time: '08:00', end_time: '18:00' }]);
  }

  function removeRange(index: number) {
    emit(ranges.filter((_, i) => i !== index));
  }

  function setStart(index: number, value: string) {
    emit(ranges.map((r, i) => (i === index ? { ...r, start_time: value } : r)));
  }

  function setEnd(index: number, value: string) {
    emit(ranges.map((r, i) => (i === index ? { ...r, end_time: value } : r)));
  }

  function copyToAll(index: number) {
    const src = ranges[index];
    if (!src) return;
    const additions: RecordingScheduleRange[] = [];
    for (let d = 0; d < 7; d++) {
      const exists = ranges.some(
        (r) => r.day_of_week === d && r.start_time === src.start_time && r.end_time === src.end_time,
      );
      if (!exists) additions.push({ day_of_week: d, start_time: src.start_time, end_time: src.end_time });
    }
    emit([...ranges, ...additions]);
  }

  function copyToWeekdays(index: number) {
    const src = ranges[index];
    if (!src) return;
    const additions: RecordingScheduleRange[] = [];
    for (let d = 1; d <= 5; d++) {
      const exists = ranges.some(
        (r) => r.day_of_week === d && r.start_time === src.start_time && r.end_time === src.end_time,
      );
      if (!exists) additions.push({ day_of_week: d, start_time: src.start_time, end_time: src.end_time });
    }
    emit([...ranges, ...additions]);
  }

  function validate(): boolean {
    for (const r of ranges) {
      if (r.start_time === r.end_time) {
        showToast(t('cameras.recordingSchedule.invalidRange'), 'error');
        return false;
      }
    }
    return true;
  }

  async function save() {
    if (saving || !onsave) return;
    if (!validate()) return;
    await onsave(ranges);
  }
</script>

<div class="mt-4 border th-border rounded-lg p-4">
  <div class="flex items-center justify-between mb-3">
    <span class="th-text-secondary font-medium text-sm">{t('cameras.recordingSchedule.title')}</span>
    {#if showSave}
      <button class="btn btn-primary text-xs px-3 py-1.5" onclick={save} disabled={saving || loading}>
        {saving ? t('common.saving') : t('cameras.recordingSchedule.save')}
      </button>
    {/if}
  </div>

  {#if loading}
    <div class="th-text-muted text-sm py-3">{t('common.loading')}</div>
  {:else}
    <p class="th-text-muted text-xs mb-3">{t('cameras.recordingSchedule.hint')}</p>
    <div class="space-y-2">
      {#each dayLabels as dayName, dayIndex}
        <div class="flex items-start gap-3">
          <div class="w-10 pt-2 text-sm font-medium th-text-secondary shrink-0">{dayName}</div>
          <div class="flex-1 space-y-1.5">
            {#each ranges as r, i}
              {#if r.day_of_week === dayIndex}
                <div class="flex items-center gap-2 flex-wrap">
                  <input type="time" class="input w-28" value={r.start_time}
                    oninput={(e) => setStart(i, (e.target as HTMLInputElement).value)} />
                  <span class="th-text-muted">{t('cameras.recordingSchedule.to')}</span>
                  <input type="time" class="input w-28" value={r.end_time}
                    oninput={(e) => setEnd(i, (e.target as HTMLInputElement).value)} />
                  <button class="btn btn-ghost p-1.5" title={t('cameras.recordingSchedule.copyAll')} onclick={() => copyToAll(i)}>
                    <Copy size={14} />
                  </button>
                  <button class="btn btn-ghost px-2 py-1 text-xs" title={t('cameras.recordingSchedule.copyWeekdays')} onclick={() => copyToWeekdays(i)}>
                    {t('cameras.recordingSchedule.weekdaysShort')}
                  </button>
                  <button class="btn btn-ghost p-1.5" title={t('common.delete')} onclick={() => removeRange(i)}>
                    <Trash2 size={14} />
                  </button>
                </div>
              {/if}
            {/each}
          </div>
          <button class="btn btn-ghost text-xs px-2 py-1 flex items-center gap-1 shrink-0" onclick={() => addRange(dayIndex)}>
            <Plus size={12} /> {t('cameras.recordingSchedule.add')}
          </button>
        </div>
      {/each}
    </div>
  {/if}
</div>
