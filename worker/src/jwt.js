// ES256 developer token(spec §4.3):header {alg,kid} / payload {iss,iat,exp}。
// WebCrypto 的 ECDSA sign 回傳 r‖s(IEEE P1363)= JOSE 格式;不要轉 DER。

export function b64url(bytes) {
  let s = "";
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export function pemToPkcs8Der(pem) {
  const body = pem.replace(/-----BEGIN [^-]+-----/, "").replace(/-----END [^-]+-----/, "").replace(/\s+/g, "");
  const bin = atob(body);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

export async function signES256({ pem, kid, teamId, now = Math.floor(Date.now() / 1000), ttl = 12 * 3600 }) {
  const key = await crypto.subtle.importKey("pkcs8", pemToPkcs8Der(pem), { name: "ECDSA", namedCurve: "P-256" }, false, ["sign"]);
  const enc = new TextEncoder();
  const header = b64url(enc.encode(JSON.stringify({ alg: "ES256", kid })));
  const payload = b64url(enc.encode(JSON.stringify({ iss: teamId, iat: now, exp: now + ttl })));
  const input = `${header}.${payload}`;
  const sig = await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, key, enc.encode(input));
  return { token: `${input}.${b64url(new Uint8Array(sig))}`, expiresAt: now + ttl };
}
