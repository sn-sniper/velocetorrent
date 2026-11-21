const DEFAULT_ENDPOINT = 'http://127.0.0.1:7878';

chrome.runtime.onInstalled.addListener(() => {
  chrome.contextMenus.create({
    id: 'velocetorrent-add-link',
    title: 'Queue in VeloceTorrent',
    contexts: ['link'],
  });
});

chrome.contextMenus.onClicked.addListener(async (info) => {
  if (info.menuItemId !== 'velocetorrent-add-link' || !info.linkUrl) {
    return;
  }
  const { vtEndpoint, vtToken, vtOutputDir } = await chrome.storage.local.get([
    'vtEndpoint',
    'vtToken',
    'vtOutputDir',
  ]);
  const endpoint = vtEndpoint || DEFAULT_ENDPOINT;
  const headers = { 'Content-Type': 'application/json' };
  if (vtToken) {
    headers['Authorization'] = `Bearer ${vtToken}`;
    headers['X-VT-Token'] = vtToken;
  }

  await fetch(`${endpoint}/api/torrents`, {
    method: 'POST',
    headers,
    body: JSON.stringify({
      source: info.linkUrl,
      outputDir: vtOutputDir || '',
      sequential: false,
    }),
  });
});
