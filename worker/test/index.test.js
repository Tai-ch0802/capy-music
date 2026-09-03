import { test, beforeEach } from "node:test";
import assert from "node:assert/strict";
import { generateKeyPairSync } from "node:crypto";
import worker from "../src/index.js";

const pem = generateKeyPairSync("ec", { namedCurve: "P-256" }).privateKey.export({ type: "pkcs8", format: "pem" });

function makeEnv({ allow = true } = {}) {
  return {
    APPLE_P8: pem, APPLE_KID: "KID1", APPLE_TEAM_ID: "TEAM1", CACHE_VERSION: "1",
    RL: { limit: async () => ({ success: allow }) },
  };
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

test("不同 KID 不共用快取", async () => {
  const env1 = makeEnv();
  const env2 = { ...makeEnv(), APPLE_KID: "KID2" };
  await worker.fetch(post({ install_id: GOOD }), env1, ctx);
  await worker.fetch(post({ install_id: GOOD }), env2, ctx);
  assert.equal(store.size, 2, "不同 KID 應各自快取,不可共用同一把 key");
});

// client(internal/auth/apple/devtoken_source.go)剩餘 < 1h(refreshMargin)就視為快過期、重取。
// Worker 的 Cache-Control max-age 必須比這個門檻更早失效,否則 client 判定該重取時,
// Worker 快取還沒過期,回的是同一顆 client 剛判定「快過期」的舊 token,永遠取不到新的。
test("Worker 快取須比 client 的 1h 重取門檻更早失效", async () => {
  const r = await worker.fetch(post({ install_id: GOOD }), makeEnv(), ctx);
  const { expires_at } = await r.json();
  const maxAge = Number(r.headers.get("Cache-Control").match(/max-age=(\d+)/)[1]);
  const remainingAtCacheExpiry = expires_at - Math.floor(Date.now() / 1000) - maxAge;
  assert.ok(remainingAtCacheExpiry >= 3600, `快取失效時 token 剩餘壽命應 ≥ client 的 1h 門檻,得到 ${remainingAtCacheExpiry}s`);
});

test("簽章失敗 → 500 結構化 JSON", async () => {
  const env = makeEnv();
  env.APPLE_P8 = "not a pem";
  const r = await worker.fetch(post({ install_id: GOOD }), env, ctx);
  assert.equal(r.status, 500);
  assert.equal((await r.json()).error, "sign failed");
});
