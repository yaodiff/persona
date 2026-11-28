export const phoneAppearancePages: string[] = [
  'phone_look_color', 'phone_look_style', 'phone_look_navigation', 'phone_look_layout', 'phone_look_scheme'
];

const coreGroups: Record<string,string[]> = {
  'assistant':['models','assistant','channels','connectors','phone_inbox_sources','extensions','machines'],
  'device':['notifications','about'],
  'hub':['phone_hubs']
};
const advancedGroups: Record<string,string[]> = {
  'assistant':['standing_authorizations','phone_knowledge_selection','phone_meeting_options','phone_agent_options'],
  'device':['appearance','phone_language_options','phone_live_screen','translation','storage','phone_health_reports','phone_native_alarms','phone_xiaoyi','phone'],
  'hub':['hub_update','managers','phone_look_avatar','phone_pairing']
};
export const phoneSettingsSearchCatalog:string[] = [
  'models','assistant','standing_authorizations','channels','connectors','phone_inbox_sources','extensions','phone_agent_options','appearance',
  'phone_language_options','notifications','translation','storage','about','phone_hubs','hub_update','managers','phone_look_avatar','phone_pairing',
  'triage','phone_agent_followup','phone_claude_config','phone_codex_config','phone_agy_config','phone_look_color','phone_look_style','phone_look_scheme',
  'phone_knowledge_selection','phone_meeting_options','phone_live_screen','phone_agent_layout','phone_health_reports','phone_xiaoyi','phone','phone_native_alarms','machines'
];

export function phoneSettingsRows(advanced:boolean,group:string,features:string[],paired:boolean,hasLiveScreen:boolean=false,hasAlarms:boolean=false):string[] {
  const rows=(advanced ? advancedGroups : coreGroups)[group] || [];
  return rows.filter((id:string)=>{
    if (id==='phone_native_alarms') return hasAlarms;
    if (id==='phone_live_screen') return hasLiveScreen;
    if (id==='phone_inbox_sources') return features.includes('inbox_sources');
    if (id==='extensions') return features.includes('extension_manager') || features.includes('plugin_manager');
    if (['phone_hubs','hub_update','managers'].includes(id)) return paired;
    return true;
  });
}

export function phoneAppearancePage(page: string): boolean {
  return page === 'appearance' || page === 'phone_look_avatar' || phoneAppearancePages.includes(page);
}

export function phoneSettingsLink(page: string): string {
  if (page === 'phone_agent_layout') return 'phone_look_layout';
  if (phoneAppearancePage(page) || ['phone_agent_options','phone_language_options','phone_meeting_options','phone_knowledge_selection',
    'machines','phone_settings_advanced','phone_inbox_sources','phone_default_permissions','phone_hubs','about','storage','phone_pairing','phone_live_screen','phone_native_alarms','phone_health_reports','phone_xiaoyi'].includes(page)) return page;
  return '';
}

export function pushPhoneSetting(trail: string[], link: string): string[] {
  const page = phoneSettingsLink(link);
  return !page || trail[trail.length - 1] === page ? trail : trail.concat([page]);
}

export function phoneHubUpdateMessage(state:string,newer:boolean,channel:string):string {
  if (['downloading','waiting_idle','restarting','confirming'].includes(state)) return 'admin_hub_state_'+state;
  return newer ? (channel==='docker' ? 'phone_hub_new_image' : 'phone_hub_update_available') : '';
}
