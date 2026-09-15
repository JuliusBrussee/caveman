// Reflects the master on/off state on the toolbar icon badge.
function setBadge(on) {
  chrome.action.setBadgeText({ text: on ? "ON" : "" });
  chrome.action.setBadgeBackgroundColor({ color: "#C75B39" });
}

function syncBadge() {
  chrome.storage.sync.get({ enabled: true }, (s) => setBadge(s.enabled));
}

chrome.runtime.onInstalled.addListener(syncBadge);
chrome.runtime.onStartup.addListener(syncBadge);
chrome.storage.onChanged.addListener((changes, area) => {
  if (area === "sync" && changes.enabled) setBadge(changes.enabled.newValue);
});
