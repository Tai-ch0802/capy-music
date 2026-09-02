import { test, beforeEach } from "node:test";
import assert from "node:assert/strict";
import { generateKeyPairSync } from "node:crypto";
import worker from "../src/index.js";

const pem = generateKeyPairSync("ec", { namedCurve: "P-256" }).privateKey.export({ type: "pkcs8", format: "pem" });

function makeEnv({ allow = true } = {}) {
  return { APPLE_P8: pem, APPLE_KID: "KID1", APPLE_TEAM_ID: "TEAM1", RL: { limit: async () => ({ success: allow }) } };
}

// Node 沒有 Cache API;以最小 stub 模擬 caches.default(match/put)。
let store;
beforeEach(() => {
  store = new Map();
  globalThis.caches = {
    default: {
      match: async (key) => store.get(String(key)),
      put: async (key, resp) => { store.set(String(key), resp); },
    },
  };
});

const ctx = { waitUntil: (p) => p };
const post = (body) => new Request("https://capy.taislife.work/v1/apple/developer-token", {
  method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(body),
});
const GOOD = "0123456789abcdef0123456789abcdef";

test("非 POST 或其他路徑 → 404", async () => {
  const r = await worker.fetch(new Request("https://x/v1/apple/developer-token"), makeEnv(), ctx);
  assert.equal(r.status, 404);
});

test("install_id 格式錯誤 → 400", async () => {
  const r = await worker.fetch(post({ install_id: "nope" }), makeEnv(), ctx);
  assert.equal(r.status, 400);
});

test("rate limited → 429 + Retry-After", async () => {
  const r = await worker.fetch(post({ install_id: GOOD }), makeEnv({ allow: false }), ctx);
  assert.equal(r.status, 429);
  assert.equal(r.headers.get("Retry-After"), "60");
});

test("成功簽發並寫入快取;第二次命中快取", async () => {
  const r1 = await worker.fetch(post({ install_id: GOOD }), makeEnv(), ctx);
  assert.equal(r1.status, 200);
  const j1 = await r1.json();
  assert.match(j1.token, /^eyJ/);
  assert.ok(j1.expires_at > Date.now() / 1000);
  assert.match(r1.headers.get("Cache-Control"), /max-age=\d+/);
  assert.equal(store.size, 1, "應以合成 GET key 寫入快取");

  const r2 = await worker.fetch(post({ install_id: GOOD }), makeEnv(), ctx);
  const j2 = await r2.json();
  assert.equal(j2.token, j1.token, "第二次應命中快取");
});

test("簽章失敗 → 500 結構化 JSON", async () => {
  const env = makeEnv();
  env.APPLE_P8 = "not a pem";
  const r = await worker.fetch(post({ install_id: GOOD }), env, ctx);
  assert.equal(r.status, 500);
  assert.equal((await r.json()).error, "sign failed");
});
