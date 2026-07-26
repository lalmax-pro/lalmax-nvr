<script lang="ts">
  import { onMount } from 'svelte';
  import { ClipboardList, RefreshCw, AlertCircle } from 'lucide-svelte';
  import Pagination from '../components/Pagination.svelte';
  import { listOperationLogs } from '$lib/api';
  import type { OperationLog, OperationLogsResponse } from '$lib/api';
  import { formatDate } from '$lib/format';

  let logs = $state<OperationLog[]>([]);
  let total = $state(0);
  let loading = $state(true);
  let error = $state('');
  let usernameFilter = $state('');
  let actionFilter = $state('');
  let statusFilter = $state('');
  let page = $state(0);
  const pageSize = 20;

  const actions = [
    ['auth.login', '用户登录'],
    ['auth.logout', '用户退出'],
    ['auth.setup', '完成初始化'],
    ['config.update', '修改配置'],
    ['config.reload', '重载配置'],
    ['recording.delete', '删除录像'],
    ['recording.batch_delete', '批量删除录像'],
    ['camera.create', '添加摄像头'],
    ['camera.update', '修改摄像头'],
    ['camera.start', '启动摄像头'],
    ['camera.stop', '停止摄像头'],
    ['camera.delete', '归档摄像头'],
    ['camera.permanent_delete', '永久删除摄像头'],
    ['archive.restore', '恢复归档摄像头'],
    ['archive.delete', '永久删除归档组'],
    ['archive.recording.delete', '删除归档录像'],
    ['archive.retention.update', '修改归档保留天数'],
    ['event.delete', '删除事件'],
    ['event.ack', '确认事件'],
    ['onvif.reboot', 'ONVIF 重启设备'],
    ['onvif.network.update', 'ONVIF 修改网络'],
    ['onvif.user.create', 'ONVIF 创建用户'],
    ['onvif.user.update', 'ONVIF 修改用户'],
    ['onvif.user.delete', 'ONVIF 删除用户'],
    ['ptz.move', 'PTZ 移动'],
    ['ptz.stop', 'PTZ 停止'],
    ['ptz.preset.create', 'PTZ 创建预置位'],
    ['ptz.preset.goto', 'PTZ 调用预置位'],
    ['ptz.preset.delete', 'PTZ 删除预置位'],
    ['snapshot.take', '抓拍'],
    ['talk.start', '开始对讲'],
    ['talk.stop', '停止对讲'],
    ['group.create', '创建设备组'],
    ['group.update', '修改设备组'],
    ['group.delete', '删除设备组'],
    ['group.channel.add', '添加通道到组'],
    ['group.channel.remove', '从组移除通道'],
    ['relay.create', '创建中继任务'],
    ['relay.delete', '删除中继任务'],
    ['relay.start', '启动中继任务'],
    ['relay.stop', '停止中继任务'],
    ['gb28181.play', 'GB28181 实时预览'],
    ['gb28181.stop', 'GB28181 停止预览'],
    ['gb28181.playback', 'GB28181 录像回放'],
    ['gb28181.playback.pause', 'GB28181 暂停回放'],
    ['gb28181.playback.resume', 'GB28181 恢复回放'],
    ['gb28181.playback.speed', 'GB28181 调整回放速度'],
    ['gb28181.playback.seek', 'GB28181 回放定位'],
    ['gb28181.platform.create', 'GB28181 添加级联平台'],
    ['gb28181.platform.delete', 'GB28181 删除级联平台'],
    ['gb28181.broadcast.start', 'GB28181 开始广播'],
    ['gb28181.broadcast.stop', 'GB28181 停止广播'],
    ['gb28181.download.start', 'GB28181 开始下载'],
    ['gb28181.download.stop', 'GB28181 停止下载'],
    ['gb28181.download.batch', 'GB28181 批量下载'],
    ['gb28181.ptz.control', 'GB28181 PTZ 控制'],
    ['gb28181.ptz.position', 'GB28181 PTZ 定位'],
    ['gb28181.preset.set', 'GB28181 设置预置位'],
    ['gb28181.preset.call', 'GB28181 调用预置位'],
    ['gb28181.preset.delete', 'GB28181 删除预置位'],
    ['gb28181.cruise.add_point', 'GB28181 添加巡航点'],
    ['gb28181.cruise.delete_point', 'GB28181 删除巡航点'],
    ['gb28181.cruise.start', 'GB28181 开始巡航'],
    ['gb28181.cruise.stop', 'GB28181 停止巡航'],
    ['gb28181.device.reset', 'GB28181 设备复位'],
    ['gb28181.record.control', 'GB28181 录像控制'],
    ['gb28181.home_position', 'GB28181 看守位设置'],
    ['gb28181.region.create', 'GB28181 创建区划'],
    ['gb28181.region.update', 'GB28181 修改区划'],
    ['gb28181.region.delete', 'GB28181 删除区划'],
    ['gb28181.region.sync', 'GB28181 同步区划'],
    ['gb28181.region.add_by_civil_code', 'GB28181 按区划码添加区划'],
    ['gb28181.channel.attach_region', 'GB28181 通道关联区划'],
    ['gb28181.channel.detach_region', 'GB28181 通道解除区划'],
    ['gb28181.group.create', 'GB28181 创建分组'],
    ['gb28181.group.update', 'GB28181 修改分组'],
    ['gb28181.group.delete', 'GB28181 删除分组'],
    ['gb28181.channel.attach_group', 'GB28181 通道关联分组'],
    ['gb28181.channel.detach_group', 'GB28181 通道解除分组'],
    ['user.create', '创建用户'],
    ['user.update', '修改用户'],
    ['user.delete', '删除用户'],
    ['device.online', '设备上线'],
    ['device.offline', '设备下线'],
  ];

  const statuses = [
    ['', '全部结果'],
    ['success', '成功'],
    ['failure', '失败'],
  ];

  function actionLabel(action: string): string {
    return actions.find(([value]) => value === action)?.[1] || action;
  }

  function actorLabel(log: OperationLog): string {
    return log.actor_type === 'system' ? '系统' : (log.username || '未知用户');
  }

  function statusClass(status: string): string {
    return status === 'success' ? 'badge badge-success' : 'badge badge-danger';
  }

  function statusLabel(status: string): string {
    return status === 'success' ? '成功' : '失败';
  }

  async function loadLogs() {
    loading = true;
    error = '';
    try {
      const response: OperationLogsResponse = await listOperationLogs({
        username: usernameFilter || undefined,
        action: actionFilter || undefined,
        status: statusFilter || undefined,
        limit: pageSize,
        offset: page * pageSize,
      });
      logs = response.logs;
      total = response.total;
    } catch (e) {
      error = e instanceof Error ? e.message : '加载操作日志失败';
    } finally {
      loading = false;
    }
  }

  let previousFilters = $state('');
  $effect(() => {
    const current = `${usernameFilter}|${actionFilter}|${statusFilter}`;
    if (current !== previousFilters) {
      previousFilters = current;
      page = 0;
    }
  });

  $effect(() => {
    const requestKey = `${usernameFilter}|${actionFilter}|${statusFilter}|${page}`;
    void requestKey;
    loadLogs();
  });

  let currentPage = $derived(page + 1);
  let totalPages = $derived(Math.max(1, Math.ceil(total / pageSize)));

  function handlePageChange(nextPage: number) {
    page = nextPage - 1;
    window.scrollTo(0, 0);
  }

  onMount(() => {
    const timer = window.setInterval(loadLogs, 30000);
    return () => window.clearInterval(timer);
  });
</script>

<div class="min-h-screen th-bg-primary">
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="mb-6 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <div>
        <h1 class="text-2xl font-semibold th-text-primary">操作日志</h1>
        <p class="text-sm th-text-secondary mt-1">记录用户和系统的重要操作，便于审计与追踪。</p>
      </div>
      <button class="btn btn-secondary btn-sm inline-flex items-center gap-2" onclick={loadLogs} disabled={loading}>
        <RefreshCw size={16} class={loading ? 'animate-spin' : ''} />
        <span>刷新</span>
      </button>
    </div>

    <div class="card p-5 mb-6 border th-border">
      <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
        <div>
          <label for="operation-log-username" class="input-label">操作者</label>
          <input id="operation-log-username" class="input" bind:value={usernameFilter} placeholder="按用户名筛选" />
        </div>
        <div>
          <label for="operation-log-action" class="input-label">操作类型</label>
          <select id="operation-log-action" class="input" bind:value={actionFilter}>
            <option value="">全部操作</option>
            {#each actions as [value, label]}
              <option {value}>{label}</option>
            {/each}
          </select>
        </div>
        <div>
          <label for="operation-log-status" class="input-label">操作结果</label>
          <select id="operation-log-status" class="input" bind:value={statusFilter}>
            {#each statuses as [value, label]}
              <option {value}>{label}</option>
            {/each}
          </select>
        </div>
      </div>
    </div>

    {#if error}
      <div class="card border th-border-danger p-8 text-center mb-6">
        <div class="flex justify-center mb-4 th-color-danger"><AlertCircle size={40} /></div>
        <h3 class="text-lg font-medium th-text-primary mb-2">加载失败</h3>
        <p class="th-text-secondary mb-4">{error}</p>
        <button onclick={loadLogs} class="btn btn-primary btn-sm">重试</button>
      </div>
    {/if}

    <div class="card border th-border">
      {#if loading && logs.length === 0}
        <div class="p-6 space-y-4">
          {#each Array(6) as _}
            <div class="grid grid-cols-5 gap-4"><div class="h-4 th-bg-tertiary rounded animate-pulse"></div><div class="h-4 th-bg-tertiary rounded animate-pulse"></div><div class="h-4 th-bg-tertiary rounded animate-pulse"></div><div class="h-4 th-bg-tertiary rounded animate-pulse"></div><div class="h-4 th-bg-tertiary rounded animate-pulse"></div></div>
          {/each}
        </div>
      {:else if logs.length === 0}
        <div class="p-12 text-center">
          <div class="flex justify-center mb-4 th-text-muted"><ClipboardList size={48} /></div>
          <h3 class="text-lg font-medium th-text-primary mb-2">暂无操作日志</h3>
          <p class="th-text-secondary">用户登录、配置修改或设备状态变化后，日志会显示在这里。</p>
        </div>
      {:else}
        <div class="table-container th-border">
          <table class="table">
            <thead>
              <tr>
                <th>操作时间</th>
                <th>操作者</th>
                <th>操作</th>
                <th>对象</th>
                <th>结果</th>
                <th>说明</th>
                <th>来源 IP</th>
              </tr>
            </thead>
            <tbody>
              {#each logs as log (log.id)}
                <tr class="transition-all duration-200 hover:th-bg-hover">
                  <td class="whitespace-nowrap">{formatDate(log.created_at)}</td>
                  <td><span class="font-medium th-text-primary">{actorLabel(log)}</span></td>
                  <td><span class="badge badge-neutral text-xs">{actionLabel(log.action)}</span></td>
                  <td>{log.resource}{log.resource_id ? ` / ${log.resource_id}` : ''}</td>
                  <td><span class={statusClass(log.status)}>{statusLabel(log.status)}</span></td>
                  <td class="th-text-secondary text-sm max-w-[320px] truncate" title={log.message}>{log.message || '-'}</td>
                  <td class="th-text-secondary text-sm">{log.ip_address || '系统'}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
        <div class="px-4 py-2 border-t th-border">
          <span class="text-sm th-text-muted">显示 {page * pageSize + 1}–{Math.min(page * pageSize + logs.length, total)}，共 {total} 条</span>
        </div>
        {#if totalPages > 1}
          <Pagination {currentPage} {totalPages} onPageChange={handlePageChange} />
        {/if}
      {/if}
    </div>
  </main>
</div>
