const enableBtn = document.getElementById("enableBtn");
const statusEl = document.getElementById("status");
const DEVICE_ID_KEY = "hello_webpush_device_id";

function setStatus(message) {
  statusEl.textContent = message;
}

function getOrCreateDeviceId() {
  const existing = localStorage.getItem(DEVICE_ID_KEY);
  if (existing) return existing;
  const value = `${Date.now()}-${crypto.randomUUID()}`;
  localStorage.setItem(DEVICE_ID_KEY, value);
  return value;
}

async function registerServiceWorker() {
  if (!("serviceWorker" in navigator)) {
    throw new Error("Service workers are not supported in this browser.");
  }
  return navigator.serviceWorker.register("/sw.js", { scope: "/" });
}

async function enableNotifications() {
  if (!("Notification" in window) || !("PushManager" in window)) {
    throw new Error("Push notifications are not supported in this browser.");
  }

  const permission = await Notification.requestPermission();
  if (permission !== "granted") {
    throw new Error("Notification permission was not granted.");
  }

  const registration = await registerServiceWorker();
  const existing = await registration.pushManager.getSubscription();
  if (existing) {
    await existing.unsubscribe();
  }

  const keyResp = await fetch("/vapidPublicKey", { headers: { Accept: "application/json" } });
  if (!keyResp.ok) {
    throw new Error(`Failed to get VAPID public key (${keyResp.status}).`);
  }
  const { key } = await keyResp.json();
  if (!key) {
    throw new Error("Server returned empty VAPID key.");
  }

  const subscription = await registration.pushManager.subscribe({
    userVisibleOnly: true,
    applicationServerKey: urlBase64ToUint8Array(key),
  });

  const deviceId = getOrCreateDeviceId();
  const subResp = await fetch("/subscribe", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Accept: "application/json",
    },
    body: JSON.stringify({
      deviceId,
      subscription,
    }),
  });

  const payload = await subResp.json().catch(() => ({}));
  if (!subResp.ok) {
    throw new Error(payload.error || `Subscribe failed (${subResp.status}).`);
  }

  setStatus(`Subscribed. deviceId=${deviceId}`);
}

enableBtn.addEventListener("click", async () => {
  setStatus("Enabling notifications...");
  try {
    await enableNotifications();
  } catch (err) {
    setStatus(`Error: ${err instanceof Error ? err.message : String(err)}`);
  }
});

if ("serviceWorker" in navigator) {
  registerServiceWorker().catch((err) => {
    setStatus(`Service worker registration issue: ${err instanceof Error ? err.message : String(err)}`);
  });
}

function urlBase64ToUint8Array(base64String) {
  const padding = "=".repeat((4 - (base64String.length % 4)) % 4);
  const base64 = (base64String + padding).replace(/-/g, "+").replace(/_/g, "/");
  const rawData = atob(base64);
  const outputArray = new Uint8Array(rawData.length);

  for (let i = 0; i < rawData.length; ++i) {
    outputArray[i] = rawData.charCodeAt(i);
  }
  return outputArray;
}
