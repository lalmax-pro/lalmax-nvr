<script lang="ts">
 import type { VoIPConfig } from '$lib/api';
 import { t } from '$lib/i18n';
 import VoIPStatusPanel from './VoIPStatusPanel.svelte';
 let { config = $bindable() }: { config: VoIPConfig } = $props();
</script>
<section class="card p-8 border th-border" aria-labelledby="voip-title">
 <h3 id="voip-title" class="text-lg font-semibold th-text-primary mb-1">{t('settings.voip.title')}</h3>
 <p class="text-sm th-text-secondary mb-5">{t('settings.voip.description')}</p>
 {#if config.supported === false}<p class="text-sm th-color-warning mb-4">{t('settings.voip.unsupported')}</p>{/if}
<label class="flex items-center gap-3 th-text-primary text-sm" for="voip-enabled"><input id="voip-enabled" type="checkbox" class="h-4 w-4 accent-blue-600" bind:checked={config.enabled} disabled={config.supported === false} />{t('settings.voip.enabled')}</label>
<label class="mt-4 flex items-center gap-3 th-text-primary text-sm" for="voip-manual_answer"><input id="voip-manual_answer" type="checkbox" class="h-4 w-4 accent-blue-600" bind:checked={config.manual_answer} />{t('settings.voip.manualAnswer')}</label>
{#if config.manual_answer}<div class="mt-3 max-w-xs"><label for="voip-ring_timeout_ms" class="input-label">{t('settings.voip.ringTimeout')}</label><input id="voip-ring_timeout_ms" type="number" class="input" bind:value={config.ring_timeout_ms} min={1000} max={300000} /></div>{/if}
<div class="grid grid-cols-1 md:grid-cols-3 gap-6 mt-5">
<div><label for="voip-sip_listen_addr" class="input-label">{t('settings.voip.sip_listen_addr')}</label><input id="voip-sip_listen_addr" type="text" class="input" bind:value={config.sip_listen_addr} placeholder="0.0.0.0:5070" /></div>
<div><label for="voip-sip_tcp_listen_addr" class="input-label">{t('settings.voip.sip_tcp_listen_addr')}</label><input id="voip-sip_tcp_listen_addr" type="text" class="input" bind:value={config.sip_tcp_listen_addr} placeholder="0.0.0.0:5070" /></div>
<div><label for="voip-realm" class="input-label">{t('settings.voip.realm')}</label><input id="voip-realm" type="text" class="input" bind:value={config.realm} placeholder="lalmax-nvr" /></div>
<div><label for="voip-sip_ip" class="input-label">{t('settings.voip.sip_ip')}</label><input id="voip-sip_ip" type="text" class="input" bind:value={config.sip_ip} placeholder="192.168.1.20" /></div>
<div><label for="voip-media_ip" class="input-label">{t('settings.voip.media_ip')}</label><input id="voip-media_ip" type="text" class="input" bind:value={config.media_ip} placeholder="192.168.1.20" /></div>
<div><label for="voip-media_port_min" class="input-label">{t('settings.voip.media_port_min')}</label><input id="voip-media_port_min" type="number" class="input" bind:value={config.media_port_min} placeholder="" min={1024} max={65535} /></div>
<div><label for="voip-media_port_max" class="input-label">{t('settings.voip.media_port_max')}</label><input id="voip-media_port_max" type="number" class="input" bind:value={config.media_port_max} placeholder="" min={1024} max={65535} /></div>
<div><label for="voip-rtp_timeout_ms" class="input-label">{t('settings.voip.rtp_timeout_ms')}</label><input id="voip-rtp_timeout_ms" type="number" class="input" bind:value={config.rtp_timeout_ms} placeholder="" min={1} /></div>
<div><label for="voip-ack_timeout_ms" class="input-label">{t('settings.voip.ack_timeout_ms')}</label><input id="voip-ack_timeout_ms" type="number" class="input" bind:value={config.ack_timeout_ms} placeholder="" min={1} /></div>
</div><p class="text-xs th-text-tertiary mt-3">{t('settings.voip.ipHint')}</p><div class="mt-6"><label class="flex items-center gap-3 th-text-primary text-sm" for="voip-auth_enable"><input id="voip-auth_enable" type="checkbox" class="h-4 w-4 accent-blue-600" bind:checked={config.auth_enable} />{t('settings.voip.auth_enable')}</label>
</div>
 <div class="mt-4 flex items-center justify-between gap-3"><h4 class="font-medium th-text-primary">{t('settings.voip.users')}</h4><button type="button" class="btn btn-ghost text-sm" onclick={() => config.users = [...config.users, { username: '', password: '' }]}>{t('settings.voip.addUser')}</button></div>
 <p class="text-xs th-text-tertiary mt-1 mb-3">{t('settings.voip.usersHint')}</p>
 <div class="space-y-3">{#each config.users as user, i}
 <div class="grid grid-cols-1 md:grid-cols-[1fr_1fr_auto] gap-3 items-end">
 <div><label for={`voip-user-${i}`} class="input-label">{t('settings.voip.username')}</label><input id={`voip-user-${i}`} class="input" bind:value={user.username} autocomplete="off" /></div>
 <div><label for={`voip-password-${i}`} class="input-label">{t('settings.voip.password')}</label><input id={`voip-password-${i}`} class="input" type="password" bind:value={user.password} autocomplete="new-password" placeholder={t(user.has_password ? 'settings.voip.keepPassword' : 'settings.voip.newPassword')} /></div>
 <button type="button" class="btn btn-ghost th-color-danger" aria-label={`${t('settings.voip.removeUser')} ${user.username}`} onclick={() => config.users = config.users.filter((_, index) => index !== i)}>{t('settings.voip.removeUser')}</button>
 <label class="flex items-center gap-2 text-sm th-text-primary"><input type="checkbox" bind:checked={user.record_calls} />{t('settings.voip.recordCalls')}</label>
 </div>{/each}</div>
 <details class="mt-6 border-t th-border pt-4"><summary class="cursor-pointer font-medium th-text-primary">{t('settings.voip.advanced')}</summary><div class="flex flex-wrap gap-6 mt-4">
<label class="flex items-center gap-3 th-text-primary text-sm" for="voip-srtp_enable"><input id="voip-srtp_enable" type="checkbox" class="h-4 w-4 accent-blue-600" bind:checked={config.srtp_enable} />{t('settings.voip.srtp_enable')}</label>
<label class="flex items-center gap-3 th-text-primary text-sm" for="voip-srtp_mandatory"><input id="voip-srtp_mandatory" type="checkbox" class="h-4 w-4 accent-blue-600" bind:checked={config.srtp_mandatory} />{t('settings.voip.srtp_mandatory')}</label>
<label class="flex items-center gap-3 th-text-primary text-sm" for="voip-bundle_enable"><input id="voip-bundle_enable" type="checkbox" class="h-4 w-4 accent-blue-600" bind:checked={config.bundle_enable} />{t('settings.voip.bundle_enable')}</label>
</div><div class="grid grid-cols-1 md:grid-cols-2 gap-5 mt-5">
<div><label for="voip-sip_tls_listen_addr" class="input-label">{t('settings.voip.sip_tls_listen_addr')}</label><input id="voip-sip_tls_listen_addr" type="text" class="input" bind:value={config.sip_tls_listen_addr} placeholder="" /></div>
<div><label for="voip-sip_ws_listen_addr" class="input-label">{t('settings.voip.sip_ws_listen_addr')}</label><input id="voip-sip_ws_listen_addr" type="text" class="input" bind:value={config.sip_ws_listen_addr} placeholder="" /></div>
<div><label for="voip-sip_wss_listen_addr" class="input-label">{t('settings.voip.sip_wss_listen_addr')}</label><input id="voip-sip_wss_listen_addr" type="text" class="input" bind:value={config.sip_wss_listen_addr} placeholder="" /></div>
<div><label for="voip-sip_dtls_listen_addr" class="input-label">{t('settings.voip.sip_dtls_listen_addr')}</label><input id="voip-sip_dtls_listen_addr" type="text" class="input" bind:value={config.sip_dtls_listen_addr} placeholder="" /></div>
<div><label for="voip-sip_tls_cert_file" class="input-label">{t('settings.voip.sip_tls_cert_file')}</label><input id="voip-sip_tls_cert_file" type="text" class="input" bind:value={config.sip_tls_cert_file} placeholder="" /></div>
<div><label for="voip-sip_tls_key_file" class="input-label">{t('settings.voip.sip_tls_key_file')}</label><input id="voip-sip_tls_key_file" type="text" class="input" bind:value={config.sip_tls_key_file} placeholder="" /></div>
</div><p class="text-xs th-text-tertiary mt-3">{t('settings.voip.tlsHint')}</p></details>
 <details class="mt-6 border-t th-border pt-4"><summary class="cursor-pointer font-medium th-text-primary">{t('settings.voip.pbx')}</summary>
 <p class="text-xs th-text-tertiary mt-2">{t('settings.voip.pbxHint')}</p>
 <div class="grid grid-cols-1 md:grid-cols-2 gap-5 mt-4">
 <div><label for="voip-pbx_server" class="input-label">{t('settings.voip.pbxServer')}</label><input id="voip-pbx_server" type="text" class="input" bind:value={config.pbx_server} placeholder="pbx.example.com:5060" /></div>
 <div><label for="voip-pbx_transport" class="input-label">{t('settings.voip.pbxTransport')}</label><select id="voip-pbx_transport" class="input" bind:value={config.pbx_transport}><option value="udp">UDP</option><option value="tcp">TCP</option><option value="tls">TLS</option></select></div>
 <div><label for="voip-pbx_domain" class="input-label">{t('settings.voip.pbxDomain')}</label><input id="voip-pbx_domain" type="text" class="input" bind:value={config.pbx_domain} placeholder={t('settings.voip.pbxDomainPlaceholder')} /></div>
 <div><label for="voip-pbx_username" class="input-label">{t('settings.voip.pbxUsername')}</label><input id="voip-pbx_username" type="text" class="input" bind:value={config.pbx_username} autocomplete="username" /></div>
 <div><label for="voip-pbx_password" class="input-label">{t('settings.voip.pbxPassword')}</label><input id="voip-pbx_password" type="password" class="input" bind:value={config.pbx_password} autocomplete="new-password" placeholder={t(config.pbx_has_password ? 'settings.voip.keepPassword' : 'settings.voip.newPassword')} /></div>
 <div><label for="voip-pbx_register_expires" class="input-label">{t('settings.voip.pbxExpires')}</label><input id="voip-pbx_register_expires" type="number" class="input" bind:value={config.pbx_register_expires} min={60} max={86400} /></div>
 {#if config.pbx_transport==='tls'}
 <div><label for="voip-pbx_tls_server_name" class="input-label">{t('settings.voip.pbxTLSServerName')}</label><input id="voip-pbx_tls_server_name" type="text" class="input" bind:value={config.pbx_tls_server_name} placeholder={t('settings.voip.pbxTLSServerNameHint')} /></div>
 <div><label for="voip-pbx_tls_ca_file" class="input-label">{t('settings.voip.pbxTLSCAFile')}</label><input id="voip-pbx_tls_ca_file" type="text" class="input" bind:value={config.pbx_tls_ca_file} placeholder={t('settings.voip.pbxTLSCAFileHint')} /></div>
 {/if}
 </div></details>
 <VoIPStatusPanel />
</section>
