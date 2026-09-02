import { signES256 } from "./jwt.js";

// Cache API 對非 GET 的 put() 會 error → 一律用合成 GET key(不可拿進來的 POST request 當 key)。
const CACHE_KEY = "https://capy-token.internal/v1/apple/developer-token";
const INSTALL_ID_RE = /^[0-9a-f]{32}$/;
const REFRESH_MARGIN = 300; // 到期前 5 分鐘讓快取失效

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
    const hit = await cache.match(CACHE_KEY);
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
    ctx.waitUntil(cache.put(CACHE_KEY, resp.clone()));
    console.log(JSON.stringify({ message: "signed developer token", install_id: installID, ttl }));
    return resp;
  },
};
