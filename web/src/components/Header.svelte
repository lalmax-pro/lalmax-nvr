<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { t } from '$lib/i18n';
  import { logout } from '$lib/api';
  import { getEffectiveTheme } from '$lib/preferences';
  import LanguageSwitcher from './LanguageSwitcher.svelte';
  import ThemeToggle from './ThemeToggle.svelte';
  import Toast from './Toast.svelte';
  import {
    ArrowLeft,
    Menu,
    LogOut,
    LayoutDashboard,
    Monitor,
    FolderTree,
    Network,
    Film,
    Bell,
    Radio,
    Link,
    BarChart3,
    Settings,
    ChevronLeft,
    ChevronRight,
    X,
    Users,
    Brain,
    Send,
  } from 'lucide-svelte';

  // Props
  let {
    activeRoute = '',
    showBack = false,
    backLabel = ''
  }: {
    activeRoute?: string;
    showBack?: boolean;
    backLabel?: string;
  } = $props();

  // Sidebar state
  let sidebarCollapsed = $state(false);
  let mobileMenuOpen = $state(false);

  function toggleSidebar() {
    sidebarCollapsed = !sidebarCollapsed;
  }

  function toggleMobileMenu() {
    mobileMenuOpen = !mobileMenuOpen;
  }

  function closeMobileMenu() {
    mobileMenuOpen = false;
  }

  function handleNavClick() {
    closeMobileMenu();
  }

  // Hash change listener to keep activeRoute in sync
  function handleHashChange() {
    const hash = window.location.hash.replace('#', '') || '/dashboard';
    activeRoute = hash;
  }

  onMount(() => {
    // Sync theme
    const effectiveTheme = getEffectiveTheme();
    document.documentElement.setAttribute('data-theme', effectiveTheme);

    // Sync active route from current hash
    handleHashChange();
    window.addEventListener('hashchange', handleHashChange);
  });

  onDestroy(() => {
    window.removeEventListener('hashchange', handleHashChange);
  });

  // Navigation items with icons
  const navItems = [
    { href: '#/dashboard', labelKey: 'nav.dashboard', route: '/dashboard', icon: LayoutDashboard },
    { href: '#/devices', labelKey: 'nav.devices', route: '/devices', icon: Monitor },
    { href: '#/device-groups', labelKey: 'nav.device_groups', route: '/device-groups', icon: FolderTree },
    { href: '#/gb-channels', labelKey: 'nav.gb_channels', route: '/gb-channels', icon: Network },
    { href: '#/recordings', labelKey: 'nav.recordings', route: '/recordings', icon: Film },
    { href: '#/events', labelKey: 'nav.events', route: '/events', icon: Bell },
    { href: '#/streams', labelKey: 'nav.streams', route: '/streams', icon: Radio },
    { href: '#/relay', labelKey: 'nav.relay', route: '/relay', icon: Send },
    { href: '#/ai', labelKey: 'nav.ai_detection', route: '/ai', icon: Brain },
    { href: '#/users', labelKey: 'nav.users', route: '/users', icon: Users },
    { href: '#/stats', labelKey: 'nav.stats', route: '/stats', icon: BarChart3 },
    { href: '#/settings', labelKey: 'nav.settings', route: '/settings', icon: Settings },
  ];

  function isActive(route: string): boolean {
    return activeRoute === route || activeRoute.startsWith(route + '/');
  }

  function goBack() {
    window.history.back();
  }
</script>

<!-- Top Header Bar -->
<header class="top-header">
  <div class="top-header-inner">
    <div class="top-header-left">
      <!-- Mobile menu button -->
      <button class="mobile-menu-btn md:hidden" onclick={toggleMobileMenu}>
        {#if mobileMenuOpen}
          <X size={20} />
        {:else}
          <Menu size={20} />
        {/if}
      </button>
      
      {#if showBack}
        <button class="back-btn" onclick={goBack}>
          <ArrowLeft size={20} />
          <span>{backLabel || t('detail.back')}</span>
        </button>
      {/if}
      <a href="#/dashboard" class="logo">
        <span class="logo-text">lalmax-nvr</span>
      </a>
    </div>
    
    <div class="top-header-right">
      <ThemeToggle />
      <LanguageSwitcher />
      <button class="btn btn-ghost logout-btn" onclick={logout}>
        <LogOut size={18} />
        <span class="logout-text">{t('nav.logout')}</span>
      </button>
    </div>
  </div>
</header>

<!-- Mobile Menu Overlay -->
{#if mobileMenuOpen}
  <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
  <div class="mobile-overlay md:hidden" onclick={closeMobileMenu} role="button" tabindex="-1" aria-label="Close menu"></div>
{/if}

<!-- Sidebar Navigation -->
<aside class="sidebar" class:collapsed={sidebarCollapsed} class:mobile-open={mobileMenuOpen}>
  <!-- Collapse toggle button (desktop only) -->
  <button class="sidebar-toggle hidden md:flex" onclick={toggleSidebar}>
    {#if sidebarCollapsed}
      <ChevronRight size={16} />
    {:else}
      <ChevronLeft size={16} />
    {/if}
  </button>
  
  <nav class="sidebar-nav">
    {#each navItems as item}
      {@const Icon = item.icon}
      <a
        href={item.href}
        class="sidebar-link"
        class:active={isActive(item.route)}
        title={sidebarCollapsed ? t(item.labelKey) : ''}
        onclick={handleNavClick}
      >
        <Icon size={20} class="sidebar-icon" />
        {#if !sidebarCollapsed}
          <span class="sidebar-label">{t(item.labelKey)}</span>
        {/if}
      </a>
    {/each}
  </nav>
</aside>

<!-- Toast container -->
<Toast />

<style>
  /* Top Header */
  .top-header {
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    z-index: 1100;
    height: 56px;
    background: var(--bg-elevated);
    border-bottom: 1px solid var(--border);
    box-shadow: var(--shadow-sm);
  }

  .top-header-inner {
    height: 100%;
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0 1rem;
  }

  .top-header-left {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }

  .top-header-right {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }

  .logo {
    text-decoration: none;
    white-space: nowrap;
  }

  .logo-text {
    font-size: 1.25rem;
    font-weight: 700;
    letter-spacing: -0.025em;
    background: linear-gradient(135deg, #8b5cf6 0%, #a78bfa 40%, #38bdf8 100%);
    -webkit-background-clip: text;
    -webkit-text-fill-color: transparent;
    background-clip: text;
  }

  .back-btn {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    color: var(--text-secondary);
    background: none;
    border: none;
    cursor: pointer;
    font-size: 0.875rem;
    font-weight: 500;
    padding: 0.375rem 0.625rem;
    border-radius: var(--radius-sm);
    transition: all var(--duration-fast) var(--ease-out);
  }

  .back-btn:hover {
    color: var(--text-primary);
    background-color: var(--bg-tertiary);
  }

  .logout-btn {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
  }

  .logout-text {
    display: none;
  }

  @media (min-width: 640px) {
    .logout-text {
      display: inline;
    }
  }

  /* Mobile menu button */
  .mobile-menu-btn {
    display: flex;
    background: none;
    border: none;
    color: var(--text-primary);
    cursor: pointer;
    padding: 0.5rem;
    border-radius: var(--radius-sm);
    transition: background-color var(--duration-fast) var(--ease-out);
  }

  .mobile-menu-btn:hover {
    background-color: var(--bg-tertiary);
  }

  @media (min-width: 768px) {
    .mobile-menu-btn {
      display: none;
    }
  }

  /* Mobile overlay */
  .mobile-overlay {
    position: fixed;
    inset: 0;
    z-index: 1190;
    background: rgba(0, 0, 0, 0.5);
    backdrop-filter: blur(2px);
  }

  /* Sidebar */
  .sidebar {
    position: fixed;
    top: 56px;
    left: 0;
    bottom: 0;
    z-index: 1200;
    width: 240px;
    background: var(--bg-elevated);
    border-right: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    transition: width var(--duration-normal) var(--ease-out);
    overflow: hidden;
  }

  .sidebar.collapsed {
    width: 64px;
  }

  /* Mobile sidebar */
  @media (max-width: 767px) {
    .sidebar {
      transform: translateX(-100%);
      box-shadow: var(--shadow-lg);
    }

    .sidebar.mobile-open {
      transform: translateX(0);
    }
  }

  /* Desktop sidebar */
  @media (min-width: 768px) {
    .sidebar {
      transform: none !important;
    }
  }

  /* Sidebar toggle button */
  .sidebar-toggle {
    position: absolute;
    right: -12px;
    top: 12px;
    width: 24px;
    height: 24px;
    border-radius: 50%;
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    color: var(--text-secondary);
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: center;
    transition: all var(--duration-fast) var(--ease-out);
    z-index: 10;
  }

  .sidebar-toggle:hover {
    background: var(--color-primary);
    color: white;
    border-color: var(--color-primary);
  }

  /* Sidebar navigation */
  .sidebar-nav {
    flex: 1;
    overflow-y: auto;
    padding: 0.5rem;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .sidebar-link {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.75rem;
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    text-decoration: none;
    font-size: 0.875rem;
    font-weight: 500;
    transition: all var(--duration-fast) var(--ease-out);
    white-space: nowrap;
    border: 1px solid transparent;
  }

  .sidebar-link:hover {
    color: var(--text-primary);
    background-color: var(--bg-tertiary);
    border-color: var(--border);
  }

  .sidebar-link.active {
    color: #ffffff;
    background: var(--color-primary);
    border-color: var(--color-primary);
    box-shadow: 0 2px 8px rgba(139, 92, 246, 0.3);
  }

  .sidebar-link.active:hover {
    background: var(--color-primary-hover, var(--color-primary));
  }

  .sidebar.collapsed .sidebar-link {
    justify-content: center;
    padding: 0.75rem;
  }

  .sidebar.collapsed .sidebar-link :global(.sidebar-icon) {
    margin: 0;
  }

  .sidebar-label {
    overflow: hidden;
    text-overflow: ellipsis;
  }

  /* Scrollbar styling */
  .sidebar-nav::-webkit-scrollbar {
    width: 4px;
  }

  .sidebar-nav::-webkit-scrollbar-track {
    background: transparent;
  }

  .sidebar-nav::-webkit-scrollbar-thumb {
    background: var(--border);
    border-radius: 2px;
  }

  .sidebar-nav::-webkit-scrollbar-thumb:hover {
    background: var(--text-muted);
  }
</style>
