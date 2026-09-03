import { test } from "node:test";
import assert from "node:assert/strict";
import { generateKeyPairSync, verify } from "node:crypto";
import { signES256, pemToPkcs8Der, b64url } from "../src/jwt.js";

const { privateKey, publicKey } = generateKeyPairSync("ec", { namedCurve: "P-256" });
const pem = privateKey.export({ type: "pkcs8", format: "pem" });

test("pemToPkcs8Der 剝殼後與 node 匯出的 DER 相同", () => {
  const der = privateKey.export({ type: "pkcs8", format: "der" });
  assert.deepEqual(Buffer.from(pemToPkcs8Der(pem)), der);
});

test("signES256 產生可驗簽的 ES256 JWT(r‖s,非 DER)", async () => {
  const now = 1_800_000_000;
  const { token, expiresAt } = await signES256({ pem, kid: "KID1", teamId: "TEAM1", now });
  const [h, p, sig] = token.split(".");
  assert.deepEqual(JSON.parse(Buffer.from(h, "base64url")), { alg: "ES256", kid: "KID1" });
  assert.deepEqual(JSON.parse(Buffer.from(p, "base64url")), { iss: "TEAM1", iat: now, exp: now + 12 * 3600 });
  assert.equal(expiresAt, now + 12 * 3600);
  const sigBytes = Buffer.from(sig, "base64url");
  assert.equal(sigBytes.length, 64, "JOSE 簽章為 r‖s 64 bytes");
  const ok = verify("sha256", Buffer.from(`${h}.${p}`), { key: publicKey, dsaEncoding: "ieee-p1363" }, sigBytes);
  assert.ok(ok, "簽章必須能用公鑰驗回");
});

test("b64url 無 padding、URL 安全", () => {
  assert.equal(b64url(new Uint8Array([251, 255, 254])), "-__-");
});
