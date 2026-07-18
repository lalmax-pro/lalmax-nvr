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
    ['auth.setup', '完成初始化'],
    ['config.update', '修改配置'],
    ['config.reload', '重载配置'],
    ['camera.create', '添加摄像头'],
    ['camera.update', '修改摄像头'],
    ['camera.start', '启动摄像头'],
    ['camera.stop', '停止摄像头'],
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
