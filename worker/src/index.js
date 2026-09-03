import { signES256 } from "./jwt.js";

// Cache API 對非 GET 的 put() 會 error → 一律用合成 GET key(不可拿進來的 POST request 當 key)。
// key 併入 KID(金鑰輪替時不同 .p8 不該共用舊快取)與 CACHE_VERSION(手動 bust 用,見 wrangler.jsonc)。
const cacheKey = (env) =>
  `https://capy-token.internal/v1/apple/developer-token?kid=${encodeURIComponent(env.APPLE_KID)}&v=${env.CACHE_VERSION ?? "1"}`;
const INSTALL_ID_RE = /^[0-9a-f]{32}$/;
// 到期前 65 分鐘讓快取失效——client(devtoken_source.go)剩餘 < 1h 就視為快過期並重取,
// Worker 的快取要比那個門檻更早失效,不然 client 判定該重取時拿到的還是同一顆舊 token。
const REFRESH_MARGIN = 3900;

function json(body, status = 200, headers = {}) {
  return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json", ...headers } });
}

export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    if (request.method !== "POST" || url.pathname !== "/v1/apple/developer-token") {
      return json({ error: "not found" }, 404);
    }
    let body;
    try {
      body = await request.json(); // 有界:只有一個短欄位
    } catch {
      return json({ error: "invalid json" }, 400);
    }
    const installID = typeof body?.install_id === "string" ? body.install_id : "";
    if (!INSTALL_ID_RE.test(installID)) return json({ error: "install_id 需為 32 位十六進位" }, 400);

    const { success } = await env.RL.limit({ key: installID });
    if (!success) return json({ error: "rate limited" }, 429, { "Retry-After": "60" });

    const cache = caches.default;
    const key = cacheKey(env);
    const hit = await cache.match(key);
    if (hit) return hit;

    let signed;
    try {
      signed = await signES256({ pem: env.APPLE_P8, kid: env.APPLE_KID, teamId: env.APPLE_TEAM_ID });
    } catch (e) {
      console.error(JSON.stringify({ message: "sign failed", error: e instanceof Error ? e.message : String(e) }));
      return json({ error: "sign failed" }, 500);
    }
    const ttl = Math.max(60, signed.expiresAt - Math.floor(Date.now() / 1000) - REFRESH_MARGIN);
    const resp = json({ token: signed.token, expires_at: signed.expiresAt }, 200, { "Cache-Control": `max-age=${ttl}` });
    ctx.waitUntil(cache.put(key, resp.clone()));
    console.log(JSON.stringify({ message: "signed developer token", install_id: installID, ttl }));
    return resp;
  },
};
